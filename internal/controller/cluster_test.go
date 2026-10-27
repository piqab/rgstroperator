package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	registryv1alpha1 "github.com/piqab/rgstroperator/api/v1alpha1"
)

func clusterSpec(replicas int32) registryv1alpha1.RegistrySpec {
	return registryv1alpha1.RegistrySpec{
		Replicas: &replicas,
		Auth:     registryv1alpha1.AuthSpec{Enabled: true},
		Database: &registryv1alpha1.DatabaseSpec{URLSecretRef: corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "pg"}, Key: "url"}},
		Storage: registryv1alpha1.StorageSpec{S3: &registryv1alpha1.S3StorageSpec{
			Bucket: "registry", Endpoint: "http://minio:9000", SecretRef: corev1.LocalObjectReference{Name: "s3"}}},
	}
}

func TestClusterMode(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace(t)
	for name, data := range map[string]map[string]string{
		"pg": {"url": "postgres://u:p@pg/rgstr"},
		"s3": {"AWS_ACCESS_KEY_ID": "ak", "AWS_SECRET_ACCESS_KEY": "sk"},
	} {
		if err := k8s.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, StringData: data}); err != nil {
			t.Fatal(err)
		}
	}
	reg := &registryv1alpha1.Registry{ObjectMeta: metav1.ObjectMeta{Name: "reg", Namespace: ns}, Spec: clusterSpec(3)}
	if err := k8s.Create(ctx, reg); err != nil {
		t.Fatal(err)
	}
	dep := deployment(t, ns, "reg")
	pod := dep.Spec.Template.Spec
	c := pod.Containers[0]
	if *dep.Spec.Replicas != 3 || dep.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType ||
		dep.Spec.Strategy.RollingUpdate.MaxUnavailable.IntValue() != 0 {
		t.Errorf("replicas %d strategy %+v", *dep.Spec.Replicas, dep.Spec.Strategy)
	}
	if c.ReadinessProbe.HTTPGet.Path != "/readyz" || c.LivenessProbe.HTTPGet.Path != "/healthz" {
		t.Errorf("probes %s / %s", c.ReadinessProbe.HTTPGet.Path, c.LivenessProbe.HTTPGet.Path)
	}
	env := envMap(c)
	if env["RGSTR_STORAGE_DRIVER"] != "s3" || env["RGSTR_STORAGE_S3_BUCKET"] != "registry" ||
		env["RGSTR_STORAGE_S3_ENDPOINT"] != "http://minio:9000" {
		t.Errorf("env %v", env)
	}
	if r := secretRef(c, "RGSTR_DATABASE_URL"); r == nil || r.Name != "pg" || r.Key != "url" {
		t.Errorf("database URL ref %+v", r)
	}
	if r := secretRef(c, "RGSTR_STORAGE_S3_SECRET_KEY"); r == nil || r.Name != "s3" || r.Key != "AWS_SECRET_ACCESS_KEY" {
		t.Errorf("S3 key ref %+v", r)
	}
	if pod.Volumes[0].EmptyDir == nil || len(pod.TopologySpreadConstraints) != 2 {
		t.Errorf("volume %+v spread %d", pod.Volumes[0], len(pod.TopologySpreadConstraints))
	}
	if get(&corev1.PersistentVolumeClaim{}, ns, "reg-data") {
		t.Error("a PVC was created for S3 storage")
	}
	pdb := &policyv1.PodDisruptionBudget{}
	eventually(t, "pdb", func() bool { return get(pdb, ns, "reg") })
	if pdb.Spec.MaxUnavailable.IntValue() != 1 {
		t.Errorf("pdb %+v", pdb.Spec)
	}
	eventually(t, "status replicas", func() bool {
		got := &registryv1alpha1.Registry{}
		return get(got, ns, "reg") && got.Status.Replicas == "0/3"
	})

	// Scaled to one replica: no budget (it would block node drains).
	get(reg, ns, "reg")
	one := int32(1)
	reg.Spec.Replicas = &one
	if err := k8s.Update(ctx, reg); err != nil {
		t.Fatal(err)
	}
	eventually(t, "pdb removed", func() bool { return !get(&policyv1.PodDisruptionBudget{}, ns, "reg") })
}

func TestClusterValidation(t *testing.T) {
	ns := newNamespace(t)
	two := int32(2)
	cases := map[string]registryv1alpha1.RegistrySpec{
		"replicas without database": {Replicas: &two},
		"s3 without database": {Storage: registryv1alpha1.StorageSpec{S3: &registryv1alpha1.S3StorageSpec{
			Bucket: "b", SecretRef: corev1.LocalObjectReference{Name: "s"}}}},
		"replicas on a ReadWriteOnce volume": func() registryv1alpha1.RegistrySpec {
			s := clusterSpec(2)
			s.Storage = registryv1alpha1.StorageSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}}
			return s
		}(),
		"built-in backups in a cluster": func() registryv1alpha1.RegistrySpec {
			s := clusterSpec(2)
			s.Backup = registryv1alpha1.BackupSpec{Enabled: true}
			return s
		}(),
	}
	for name, spec := range cases {
		err := k8s.Create(context.Background(), &registryv1alpha1.Registry{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "reg-", Namespace: ns}, Spec: spec})
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A ReadWriteMany volume (NFS, CephFS) works for a cluster.
	rwx := clusterSpec(2)
	rwx.Storage = registryv1alpha1.StorageSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}}
	if err := k8s.Create(context.Background(), &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: "rwx", Namespace: ns}, Spec: rwx}); err != nil {
		t.Errorf("ReadWriteMany cluster: %v", err)
	}
}

func TestProxiesAndScan(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace(t)
	if err := k8s.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "hub", Namespace: ns},
		StringData: map[string]string{"username": "me", "password": "pat"}}); err != nil {
		t.Fatal(err)
	}
	reg := &registryv1alpha1.Registry{ObjectMeta: metav1.ObjectMeta{Name: "reg", Namespace: ns},
		Spec: registryv1alpha1.RegistrySpec{
			Proxies: []registryv1alpha1.ProxySpec{
				{Name: "docker-hub", URL: "https://registry-1.docker.io", Public: true,
					CredentialsSecretRef: &corev1.LocalObjectReference{Name: "hub"}},
				{Name: "ghcr", URL: "https://ghcr.io"},
			},
			ProxyTTL: "10m",
			Scan:     registryv1alpha1.ScanSpec{Enabled: true, Interval: "12h", Concurrency: 2},
		}}
	if err := k8s.Create(ctx, reg); err != nil {
		t.Fatal(err)
	}
	c := deployment(t, ns, "reg").Spec.Template.Spec.Containers[0]
	env := envMap(c)
	for k, v := range map[string]string{
		"RGSTR_PROXIES":                 "docker-hub=https://registry-1.docker.io,ghcr=https://ghcr.io",
		"RGSTR_PROXY_DOCKER_HUB_PUBLIC": "true",
		"RGSTR_PROXY_TTL":               "10m",
		"RGSTR_SCAN_ENABLED":            "true",
		"RGSTR_SCAN_INTERVAL":           "12h",
		"RGSTR_SCAN_CONCURRENCY":        "2",
	} {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if r := secretRef(c, "RGSTR_PROXY_DOCKER_HUB_PASSWORD"); r == nil || r.Name != "hub" || r.Key != "password" {
		t.Errorf("upstream password ref: %+v", r)
	}
	if _, ok := env["RGSTR_SCAN_CACHE_DIR"]; ok {
		t.Error("Trivy's database should stay on the data volume")
	}
	bad := &registryv1alpha1.Registry{ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: ns},
		Spec: registryv1alpha1.RegistrySpec{Proxies: []registryv1alpha1.ProxySpec{{Name: "Docker Hub", URL: "registry-1.docker.io"}}}}
	if err := k8s.Create(ctx, bad); err == nil {
		t.Error("invalid proxy accepted")
	}
}
