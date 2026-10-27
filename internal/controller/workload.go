package controller

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	registryv1alpha1 "github.com/piqab/rgstroperator/api/v1alpha1"
)

// reconcileDeployment creates or updates the rgstr Deployment.
func (r *RegistryReconciler) reconcileDeployment(ctx context.Context, reg *registryv1alpha1.Registry,
	s registryv1alpha1.RegistrySpec, claim, authSecret, checksum string) (*appsv1.Deployment, error) {

	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: reg.Name, Namespace: reg.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, dep, func() error {
		labels := labelsFor(reg)
		if dep.Labels == nil {
			dep.Labels = map[string]string{}
		}
		for k, v := range labels {
			dep.Labels[k] = v
		}
		if dep.Spec.Selector == nil { // immutable once created
			dep.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		}
		dep.Spec.Replicas = s.Replicas
		// One process per volume: the old pod must stop before the new one
		// starts (an RWO volume cannot attach twice; two rgstr processes on
		// one data directory would corrupt it).
		dep.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}

		podLabels := map[string]string{}
		for k, v := range s.Pod.Labels {
			podLabels[k] = v
		}
		for k, v := range labels {
			podLabels[k] = v
		}
		annotations := map[string]string{}
		for k, v := range s.Pod.Annotations {
			annotations[k] = v
		}
		if checksum != "" {
			annotations[checksumAnnotation] = checksum
		}
		dep.Spec.Template.ObjectMeta = metav1.ObjectMeta{Labels: podLabels, Annotations: annotations}
		dep.Spec.Template.Spec = r.podSpec(reg, s, claim, authSecret)
		return controllerutil.SetControllerReference(reg, dep, r.Scheme)
	})
	return dep, err
}

func (r *RegistryReconciler) podSpec(reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec, claim, authSecret string) corev1.PodSpec {
	probe := func(delay, period int32) *corev1.Probe {
		return &corev1.Probe{
			// /healthz answers without credentials; /v2/ is 401 with auth.
			ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
				Path: "/healthz", Port: intstr.FromString("registry"), Scheme: corev1.URISchemeHTTP,
			}},
			InitialDelaySeconds: delay,
			PeriodSeconds:       period,
			TimeoutSeconds:      3,
		}
	}
	c := corev1.Container{
		Name:            "rgstr",
		Image:           s.Image,
		ImagePullPolicy: s.ImagePullPolicy,
		Ports: []corev1.ContainerPort{
			{Name: "registry", ContainerPort: s.Port, Protocol: corev1.ProtocolTCP},
		},
		Env:             r.env(reg, s),
		VolumeMounts:    []corev1.VolumeMount{{Name: "data", MountPath: dataDir}},
		Resources:       s.Resources,
		LivenessProbe:   probe(5, 15),
		ReadinessProbe:  probe(2, 5),
		SecurityContext: s.Pod.ContainerSecurityContext,
	}
	if authSecret != "" {
		c.EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: authSecret},
		}}}
	}
	return corev1.PodSpec{
		Containers:         []corev1.Container{c},
		ImagePullSecrets:   s.ImagePullSecrets,
		NodeSelector:       s.Pod.NodeSelector,
		Tolerations:        s.Pod.Tolerations,
		Affinity:           s.Pod.Affinity,
		SecurityContext:    s.Pod.SecurityContext,
		PriorityClassName:  s.Pod.PriorityClassName,
		ServiceAccountName: s.Pod.ServiceAccountName,
		// rgstr finishes GC and uploads on SIGTERM within 30 s.
		TerminationGracePeriodSeconds: ptr(int64(40)),
		Volumes: []corev1.Volume{{
			Name: "data",
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: claim,
			}},
		}},
	}
}

// env maps the spec to rgstr's environment. Keys of the auth Secret come in
// through envFrom; spec.env is appended last and so overrides everything.
func (r *RegistryReconciler) env(reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: "RGSTR_ADDR", Value: fmt.Sprintf(":%d", s.Port)},
		{Name: "RGSTR_STORAGE", Value: dataDir},
		{Name: "RGSTR_GC_INTERVAL", Value: string(s.GCInterval)},
		{Name: "RGSTR_UPLOAD_TTL", Value: string(s.UploadTTL)},
	}
	add := func(name, value string) {
		if value != "" {
			env = append(env, corev1.EnvVar{Name: name, Value: value})
		}
	}
	add("RGSTR_PUBLIC_REPOS", s.PublicRepos)
	add("RGSTR_TRUSTED_PROXIES", strings.Join(s.TrustedProxies, ","))
	if s.Metrics.Enabled {
		add("RGSTR_METRICS_ENABLED", "true")
	}
	if s.Auth.Enabled {
		add("RGSTR_AUTH_ENABLED", "true")
		add("RGSTR_AUTH_REALM", realm(reg, s))
		add("RGSTR_TOKEN_TTL", string(s.Auth.TokenTTL))
		add("RGSTR_SESSION_TTL", string(s.Auth.SessionTTL))
		add("RGSTR_ADMIN_USER", s.Auth.AdminUser)
		add("RGSTR_ADMINS", strings.Join(s.Auth.Admins, ","))
		add("RGSTR_READONLY_USERS", strings.Join(s.Auth.ReadOnlyUsers, ","))
		secretEnv := func(name string, sel *corev1.SecretKeySelector) {
			if sel != nil {
				env = append(env, corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: sel}})
			}
		}
		roles := func(prefix string, g registryv1alpha1.GroupRolesSpec) {
			add(prefix+"ADMIN_GROUPS", strings.Join(g.AdminGroups, ","))
			add(prefix+"READONLY_GROUPS", strings.Join(g.ReadOnlyGroups, ","))
			add(prefix+"ALLOWED_GROUPS", strings.Join(g.AllowedGroups, ","))
			add(prefix+"DEFAULT_ROLE", g.DefaultRole)
		}
		if l := s.Auth.LDAP; l != nil {
			add("RGSTR_LDAP_URL", l.URL)
			add("RGSTR_LDAP_STARTTLS", boolString(l.StartTLS))
			add("RGSTR_LDAP_INSECURE_SKIP_VERIFY", boolString(l.InsecureSkipVerify))
			add("RGSTR_LDAP_BIND_DN", l.BindDN)
			secretEnv("RGSTR_LDAP_BIND_PASSWORD", l.BindPasswordSecretRef)
			add("RGSTR_LDAP_BASE_DN", l.BaseDN)
			add("RGSTR_LDAP_USER_FILTER", l.UserFilter)
			add("RGSTR_LDAP_GROUP_ATTR", l.GroupAttr)
			add("RGSTR_LDAP_GROUP_FILTER", l.GroupFilter)
			add("RGSTR_LDAP_GROUP_BASE_DN", l.GroupBaseDN)
			roles("RGSTR_LDAP_", l.GroupRolesSpec)
		}
		if o := s.Auth.OIDC; o != nil {
			add("RGSTR_OIDC_ISSUER", o.Issuer)
			add("RGSTR_OIDC_CLIENT_ID", o.ClientID)
			secretEnv("RGSTR_OIDC_CLIENT_SECRET", o.ClientSecretRef)
			redirect := o.RedirectURL
			if redirect == "" {
				redirect = externalURL(reg, s) + "/ui/oidc/callback"
			}
			add("RGSTR_OIDC_REDIRECT_URL", redirect)
			add("RGSTR_OIDC_SCOPES", strings.Join(o.Scopes, " "))
			add("RGSTR_OIDC_USERNAME_CLAIM", o.UsernameClaim)
			add("RGSTR_OIDC_GROUPS_CLAIM", o.GroupsClaim)
			add("RGSTR_OIDC_NAME", o.DisplayName)
			roles("RGSTR_OIDC_", o.GroupRolesSpec)
		}
	}
	if backupMethod(s) == methodS3 && s.Backup.S3 != nil {
		b, _ := backupSettings(s)
		s3 := s.Backup.S3
		target := "s3://" + s3.Bucket
		if p := strings.Trim(s3.Prefix, "/"); p != "" {
			target += "/" + p
		}
		add("RGSTR_BACKUP_TARGET", target)
		add("RGSTR_BACKUP_SCHEDULE", b.spec)
		add("RGSTR_BACKUP_KEEP", fmt.Sprint(b.keep))
		add("RGSTR_BACKUP_S3_ENDPOINT", s3.Endpoint)
		add("RGSTR_BACKUP_S3_REGION", s3.Region)
		if s3.PathStyle != nil {
			add("RGSTR_BACKUP_S3_PATH_STYLE", fmt.Sprint(*s3.PathStyle))
		}
		for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
			env = append(env, corev1.EnvVar{Name: k, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: s3.SecretRef, Key: k, Optional: ptr(k == "AWS_SESSION_TOKEN"),
			}}})
		}
	}
	return append(env, s.Env...)
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return ""
}

// referencedSecrets are the Secrets the rgstr environment reads besides
// the auth Secret; their checksum restarts rgstr when they change.
func referencedSecrets(s registryv1alpha1.RegistrySpec) []string {
	var out []string
	if s.Auth.Enabled && s.Auth.LDAP != nil && s.Auth.LDAP.BindPasswordSecretRef != nil {
		out = append(out, s.Auth.LDAP.BindPasswordSecretRef.Name)
	}
	if s.Auth.Enabled && s.Auth.OIDC != nil && s.Auth.OIDC.ClientSecretRef != nil {
		out = append(out, s.Auth.OIDC.ClientSecretRef.Name)
	}
	if backupMethod(s) == methodS3 && s.Backup.S3 != nil {
		out = append(out, s.Backup.S3.SecretRef.Name)
	}
	return out
}

// realm is the token endpoint clients are sent to.
func realm(reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec) string {
	if s.Auth.Realm != "" {
		return s.Auth.Realm
	}
	return externalURL(reg, s) + "/v2/auth"
}

func ptr[T any](v T) *T { return &v }
