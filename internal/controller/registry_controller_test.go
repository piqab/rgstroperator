package controller

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	registryv1alpha1 "github.com/piqab/rgstroperator/api/v1alpha1"
)

func envMap(c corev1.Container) map[string]string {
	m := map[string]string{}
	for _, e := range c.Env {
		m[e.Name] = e.Value
	}
	return m
}

func TestDefaultsAndObjects(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace(t)
	reg := &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: "reg", Namespace: ns},
		Spec: registryv1alpha1.RegistrySpec{
			Auth:    registryv1alpha1.AuthSpec{Enabled: true, Admins: []string{"alice", "bob"}, SessionTTL: "8h"},
			Env:     []corev1.EnvVar{{Name: "RGSTR_GC_INTERVAL", Value: "6h"}},
			Pod:     registryv1alpha1.PodSpec{NodeSelector: map[string]string{"disk": "ssd"}},
			Storage: registryv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")},
		},
	}
	if err := k8s.Create(ctx, reg); err != nil {
		t.Fatal(err)
	}
	dep := deployment(t, ns, "reg")
	c := dep.Spec.Template.Spec.Containers[0]

	if c.Image != DefaultImage {
		t.Errorf("image %q", c.Image)
	}
	if dep.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || *dep.Spec.Replicas != 1 {
		t.Errorf("strategy %v replicas %d", dep.Spec.Strategy.Type, *dep.Spec.Replicas)
	}
	if c.ReadinessProbe.HTTPGet.Path != "/healthz" || c.LivenessProbe.HTTPGet.Path != "/healthz" {
		t.Errorf("probes: %+v %+v", c.ReadinessProbe.HTTPGet, c.LivenessProbe.HTTPGet)
	}
	env := envMap(c)
	if env["RGSTR_ADMINS"] != "alice,bob" || env["RGSTR_SESSION_TTL"] != "8h" || env["RGSTR_AUTH_ENABLED"] != "true" ||
		!strings.HasSuffix(env["RGSTR_AUTH_REALM"], "reg."+ns+".svc.cluster.local:5000/v2/auth") {
		t.Errorf("env: %v", env)
	}
	// spec.env comes last, so it overrides the operator's value.
	if last := c.Env[len(c.Env)-1]; last.Name != "RGSTR_GC_INTERVAL" || last.Value != "6h" {
		t.Errorf("spec.env not last: %+v", last)
	}
	if dep.Spec.Template.Spec.NodeSelector["disk"] != "ssd" {
		t.Error("pod nodeSelector not applied")
	}

	// Generated auth Secret, wired through envFrom, kept on deletion.
	sec := &corev1.Secret{}
	if !get(sec, ns, "reg-auth") {
		t.Fatal("auth secret not created")
	}
	if len(sec.Data[keyAuthSecret]) != 64 || len(sec.Data[keyAdminPassword]) != 16 || len(sec.OwnerReferences) != 0 {
		t.Errorf("secret: keys %d/%d owners %d", len(sec.Data[keyAuthSecret]), len(sec.Data[keyAdminPassword]), len(sec.OwnerReferences))
	}
	if c.EnvFrom[0].SecretRef.Name != "reg-auth" || dep.Spec.Template.Annotations[checksumAnnotation] == "" {
		t.Error("secret not wired into the pod")
	}

	pvc := &corev1.PersistentVolumeClaim{}
	if !get(pvc, ns, "reg-data") || len(pvc.OwnerReferences) != 0 {
		t.Errorf("PVC missing or owned (retainOnDelete default)")
	}
	svc := &corev1.Service{}
	if !get(svc, ns, "reg") || svc.Spec.Ports[0].Port != 5000 {
		t.Error("service")
	}

	eventually(t, "status", func() bool {
		got := &registryv1alpha1.Registry{}
		return get(got, ns, "reg") && got.Status.Phase == "Pending" && got.Status.AuthSecretName == "reg-auth" &&
			got.Status.ClaimName == "reg-data" && apimeta.IsStatusConditionTrue(got.Status.Conditions, CondStorage)
	})

	// Changing the generated Secret's password does not get overwritten,
	// and it changes the checksum (pod restart).
	before := dep.Spec.Template.Annotations[checksumAnnotation]
	sec.Data[keyAdminPassword] = []byte("my-own-password")
	if err := k8s.Update(ctx, sec); err != nil {
		t.Fatal(err)
	}
	eventually(t, "checksum change", func() bool {
		d := &appsv1.Deployment{}
		return get(d, ns, "reg") && d.Spec.Template.Annotations[checksumAnnotation] != before
	})
	if get(sec, ns, "reg-auth"); string(sec.Data[keyAdminPassword]) != "my-own-password" {
		t.Error("operator overwrote the admin password")
	}
}

func TestMissingSecretRefBlocksDeployment(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace(t)
	reg := &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: "reg", Namespace: ns},
		Spec: registryv1alpha1.RegistrySpec{Auth: registryv1alpha1.AuthSpec{
			Enabled: true, SecretRef: &corev1.LocalObjectReference{Name: "nope"},
		}},
	}
	if err := k8s.Create(ctx, reg); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Degraded", func() bool {
		got := &registryv1alpha1.Registry{}
		if !get(got, ns, "reg") {
			return false
		}
		c := apimeta.FindStatusCondition(got.Status.Conditions, CondAuth)
		return got.Status.Phase == "Degraded" && c != nil && c.Status == metav1.ConditionFalse
	})
	if get(&appsv1.Deployment{}, ns, "reg") {
		t.Error("deployment created without its secret")
	}
	// Creating the secret recovers.
	if err := k8s.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "nope", Namespace: ns},
		StringData: map[string]string{"RGSTR_AUTH_SECRET": "x"},
	}); err != nil {
		t.Fatal(err)
	}
	dep := deployment(t, ns, "reg")
	if dep.Spec.Template.Spec.Containers[0].EnvFrom[0].SecretRef.Name != "nope" {
		t.Error("user secret not used")
	}
}

func TestOldOwnedPVCIsReleased(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace(t)
	no := false
	reg := &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: "reg", Namespace: ns},
		Spec:       registryv1alpha1.RegistrySpec{Storage: registryv1alpha1.StorageSpec{RetainOnDelete: &no}},
	}
	if err := k8s.Create(ctx, reg); err != nil {
		t.Fatal(err)
	}
	pvc := &corev1.PersistentVolumeClaim{}
	eventually(t, "owned PVC", func() bool { return get(pvc, ns, "reg-data") && len(pvc.OwnerReferences) == 1 })

	// Switching to the default (retain) releases it, as for PVCs created
	// by operator versions before retainOnDelete.
	get(reg, ns, "reg")
	reg.Spec.Storage.RetainOnDelete = nil
	if err := k8s.Update(ctx, reg); err != nil {
		t.Fatal(err)
	}
	eventually(t, "released PVC", func() bool { return get(pvc, ns, "reg-data") && len(pvc.OwnerReferences) == 0 })
}

func TestReplicasValidation(t *testing.T) {
	ns := newNamespace(t)
	two := int32(2)
	err := k8s.Create(context.Background(), &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: "reg", Namespace: ns},
		Spec:       registryv1alpha1.RegistrySpec{Replicas: &two},
	})
	if err == nil || !strings.Contains(err.Error(), "replicas") {
		t.Errorf("replicas 2 accepted: %v", err)
	}
	err = k8s.Create(context.Background(), &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: "reg", Namespace: ns},
		Spec:       registryv1alpha1.RegistrySpec{GCInterval: "soon"},
	})
	if err == nil {
		t.Error("invalid duration accepted")
	}
}

func TestStopped(t *testing.T) {
	ns := newNamespace(t)
	zero := int32(0)
	if err := k8s.Create(context.Background(), &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: "reg", Namespace: ns},
		Spec:       registryv1alpha1.RegistrySpec{Replicas: &zero},
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Stopped", func() bool {
		got := &registryv1alpha1.Registry{}
		return get(got, ns, "reg") && got.Status.Phase == "Stopped"
	})
}

func TestIngress(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace(t)
	nginx := "nginx"
	reg := &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: "reg", Namespace: ns},
		Spec: registryv1alpha1.RegistrySpec{
			Auth: registryv1alpha1.AuthSpec{Enabled: true},
			Ingress: registryv1alpha1.IngressSpec{
				Enabled: true, Host: "registry.example.com", ClassName: &nginx,
				Annotations: map[string]string{"nginx.ingress.kubernetes.io/proxy-read-timeout": "1800"},
				TLS:         registryv1alpha1.IngressTLSSpec{ClusterIssuer: "letsencrypt"},
			},
		},
	}
	if err := k8s.Create(ctx, reg); err != nil {
		t.Fatal(err)
	}
	ing := &networkingv1.Ingress{}
	eventually(t, "ingress", func() bool { return get(ing, ns, "reg") })
	a := ing.Annotations
	if a["nginx.ingress.kubernetes.io/proxy-body-size"] != "0" || a["cert-manager.io/cluster-issuer"] != "letsencrypt" ||
		a["nginx.ingress.kubernetes.io/proxy-read-timeout"] != "1800" {
		t.Errorf("annotations: %v", a)
	}
	if len(ing.Spec.TLS) != 1 || ing.Spec.TLS[0].SecretName != "reg-tls" || ing.Spec.Rules[0].Host != "registry.example.com" {
		t.Errorf("tls/rules: %+v %+v", ing.Spec.TLS, ing.Spec.Rules)
	}
	dep := deployment(t, ns, "reg")
	if env := envMap(dep.Spec.Template.Spec.Containers[0]); env["RGSTR_AUTH_REALM"] != "https://registry.example.com/v2/auth" {
		t.Errorf("realm: %q", env["RGSTR_AUTH_REALM"])
	}
	eventually(t, "status url", func() bool {
		got := &registryv1alpha1.Registry{}
		return get(got, ns, "reg") && got.Status.URL == "https://registry.example.com"
	})

	// Disabling the ingress deletes it.
	get(reg, ns, "reg")
	reg.Spec.Ingress.Enabled = false
	if err := k8s.Update(ctx, reg); err != nil {
		t.Fatal(err)
	}
	eventually(t, "ingress deleted", func() bool {
		i := &networkingv1.Ingress{}
		return !get(i, ns, "reg") || !i.DeletionTimestamp.IsZero()
	})
}

func TestIngressHostRequired(t *testing.T) {
	err := k8s.Create(context.Background(), &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: "reg", Namespace: newNamespace(t)},
		Spec:       registryv1alpha1.RegistrySpec{Ingress: registryv1alpha1.IngressSpec{Enabled: true}},
	})
	if err == nil || !strings.Contains(err.Error(), "host is required") {
		t.Errorf("ingress without host: %v", err)
	}
}
