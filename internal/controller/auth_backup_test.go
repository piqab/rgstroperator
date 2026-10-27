package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	registryv1alpha1 "github.com/piqab/rgstroperator/api/v1alpha1"
)

func secretRef(c corev1.Container, name string) *corev1.SecretKeySelector {
	for _, e := range c.Env {
		if e.Name == name && e.ValueFrom != nil {
			return e.ValueFrom.SecretKeyRef
		}
	}
	return nil
}

func TestDirectoryAuthAndS3Backup(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace(t)
	reg := &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: "reg", Namespace: ns},
		Spec: registryv1alpha1.RegistrySpec{
			Ingress: registryv1alpha1.IngressSpec{Enabled: true, Host: "registry.example.com"},
			Auth: registryv1alpha1.AuthSpec{
				Enabled: true,
				LDAP: &registryv1alpha1.LDAPSpec{
					URL: "ldaps://dc1.corp:636", BaseDN: "DC=corp", BindDN: "CN=svc",
					BindPasswordSecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "dir"}, Key: "bind"},
					UserFilter:            "(sAMAccountName=%s)",
					GroupRolesSpec:        registryv1alpha1.GroupRolesSpec{AdminGroups: []string{"Registry-Admins", "Ops"}},
				},
				OIDC: &registryv1alpha1.OIDCSpec{
					Issuer: "https://sso.example.com/realms/main", ClientID: "rgstr", DisplayName: "Keycloak",
					ClientSecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "dir"}, Key: "oidc"},
				},
			},
			Backup: registryv1alpha1.BackupSpec{Enabled: true, Method: "S3", Schedule: "0 */6 * * *", Keep: 4,
				S3: &registryv1alpha1.S3BackupSpec{Bucket: "backups", Prefix: "/rgstr/prod/", Endpoint: "http://minio:9000",
					SecretRef: corev1.LocalObjectReference{Name: "s3keys"}}},
		},
	}
	if err := k8s.Create(ctx, reg); err != nil {
		t.Fatal(err)
	}
	// Referenced Secrets missing: rgstr is not started.
	defer func() {
		if t.Failed() {
			got := &registryv1alpha1.Registry{}
			get(got, ns, "reg")
			t.Logf("phase %s conditions %+v", got.Status.Phase, got.Status.Conditions)
		}
	}()
	eventually(t, "SecretMissing", func() bool {
		got := &registryv1alpha1.Registry{}
		if !get(got, ns, "reg") {
			return false
		}
		c := apimeta.FindStatusCondition(got.Status.Conditions, CondReady)
		return got.Status.Phase == "Degraded" && c != nil && c.Reason == "SecretMissing"
	})
	for name, data := range map[string]map[string]string{
		"dir":    {"bind": "pw1", "oidc": "cs1"},
		"s3keys": {"AWS_ACCESS_KEY_ID": "ak", "AWS_SECRET_ACCESS_KEY": "sk"},
	} {
		if err := k8s.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, StringData: data}); err != nil {
			t.Fatal(err)
		}
	}
	dep := deployment(t, ns, "reg")
	c := dep.Spec.Template.Spec.Containers[0]
	env := envMap(c)
	want := map[string]string{
		"RGSTR_LDAP_URL":           "ldaps://dc1.corp:636",
		"RGSTR_LDAP_BASE_DN":       "DC=corp",
		"RGSTR_LDAP_USER_FILTER":   "(sAMAccountName=%s)",
		"RGSTR_LDAP_ADMIN_GROUPS":  "Registry-Admins,Ops",
		"RGSTR_OIDC_ISSUER":        "https://sso.example.com/realms/main",
		"RGSTR_OIDC_REDIRECT_URL":  "https://registry.example.com/ui/oidc/callback",
		"RGSTR_OIDC_NAME":          "Keycloak",
		"RGSTR_BACKUP_TARGET":      "s3://backups/rgstr/prod",
		"RGSTR_BACKUP_SCHEDULE":    "0 */6 * * *",
		"RGSTR_BACKUP_KEEP":        "4",
		"RGSTR_BACKUP_S3_ENDPOINT": "http://minio:9000",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if r := secretRef(c, "RGSTR_LDAP_BIND_PASSWORD"); r == nil || r.Name != "dir" || r.Key != "bind" {
		t.Errorf("bind password ref: %+v", r)
	}
	if r := secretRef(c, "AWS_SECRET_ACCESS_KEY"); r == nil || r.Name != "s3keys" {
		t.Errorf("S3 key ref: %+v", r)
	}

	// Changing a referenced Secret restarts rgstr (new checksum).
	before := dep.Spec.Template.Annotations[checksumAnnotation]
	sec := &corev1.Secret{}
	get(sec, ns, "dir")
	sec.Data["bind"] = []byte("pw2")
	if err := k8s.Update(ctx, sec); err != nil {
		t.Fatal(err)
	}
	eventually(t, "checksum changes", func() bool {
		d := deployment(t, ns, "reg")
		return d.Spec.Template.Annotations[checksumAnnotation] != before
	})

	// rgstr reports its last backup at /healthz; a late one is Overdue.
	last := time.Now().Add(-30 * time.Hour).UTC().Truncate(time.Second)
	healthByNS.Store(ns, Health{Version: "v1.8.0-test", LastBackup: last.Format(time.RFC3339)})
	dep = deployment(t, ns, "reg")
	dep.Status.Replicas, dep.Status.ReadyReplicas, dep.Status.UpdatedReplicas = 1, 1, 1
	dep.Status.ObservedGeneration = dep.Generation
	if err := k8s.Status().Update(ctx, dep); err != nil {
		t.Fatal(err)
	}
	eventually(t, "last backup and Overdue", func() bool {
		got := &registryv1alpha1.Registry{}
		if !get(got, ns, "reg") {
			return false
		}
		c := apimeta.FindStatusCondition(got.Status.Conditions, CondBackup)
		return got.Status.LastBackupTime != nil && got.Status.LastBackupTime.Time.Equal(last) &&
			got.Status.NextBackupTime != nil && c != nil && c.Status == metav1.ConditionFalse && c.Reason == "Overdue"
	})
	healthByNS.Store(ns, Health{Version: "v1.8.0-test", LastBackup: time.Now().UTC().Format(time.RFC3339)})
	poke(t, ns, "reg")
	eventually(t, "backup on time", func() bool {
		got := &registryv1alpha1.Registry{}
		if !get(got, ns, "reg") {
			return false
		}
		c := apimeta.FindStatusCondition(got.Status.Conditions, CondBackup)
		return c != nil && c.Status == metav1.ConditionTrue
	})
}

func TestS3BackupNeedsBucketAndSecret(t *testing.T) {
	ns := newNamespace(t)
	err := k8s.Create(context.Background(), &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: "reg", Namespace: ns},
		Spec:       registryv1alpha1.RegistrySpec{Backup: registryv1alpha1.BackupSpec{Enabled: true, Method: "S3"}},
	})
	if err == nil {
		t.Fatal("S3 backup without bucket accepted")
	}
}
