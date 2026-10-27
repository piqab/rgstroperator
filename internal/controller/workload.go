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
		if cluster(s) {
			// Processes share state through the database: replace pods one
			// at a time, never dropping below the desired count.
			zero, one := intstr.FromInt32(0), intstr.FromInt32(1)
			dep.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{MaxUnavailable: &zero, MaxSurge: &one}}
		} else {
			// One process per volume: the old pod must stop before the new one
			// starts (an RWO volume cannot attach twice; two rgstr processes on
			// one data directory would corrupt it).
			dep.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
		}

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
	probe := func(path string, delay, period int32) *corev1.Probe {
		return &corev1.Probe{
			// /healthz answers without credentials; /v2/ is 401 with auth.
			ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
				Path: path, Port: intstr.FromString("registry"), Scheme: corev1.URISchemeHTTP,
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
		VolumeMounts:    mounts(s),
		Resources:       s.Resources,
		LivenessProbe:   probe("/healthz", 5, 15),
		ReadinessProbe:  probe(readinessPath(s), 2, 5),
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
		Volumes:                       volumes(s, claim),
		TopologySpreadConstraints:     spread(reg, s),
	}
}

// trivyCacheDir holds Trivy's database when the data is in S3 (with a
// volume it lives on it, under /data/.trivy, and survives restarts).
const trivyCacheDir = "/var/cache/trivy"

func mounts(s registryv1alpha1.RegistrySpec) []corev1.VolumeMount {
	m := []corev1.VolumeMount{{Name: "data", MountPath: dataDir}}
	if s.Scan.Enabled && onS3(s) {
		m = append(m, corev1.VolumeMount{Name: "trivy", MountPath: trivyCacheDir})
	}
	return m
}

func volumes(s registryv1alpha1.RegistrySpec, claim string) []corev1.Volume {
	v := []corev1.Volume{dataVolume(claim)}
	if s.Scan.Enabled && onS3(s) {
		v = append(v, corev1.Volume{Name: "trivy", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
	}
	return v
}

// dataVolume is the PVC, or an empty directory when the data is in S3.
func dataVolume(claim string) corev1.Volume {
	v := corev1.Volume{Name: "data"}
	if claim == "" {
		v.EmptyDir = &corev1.EmptyDirVolumeSource{}
	} else {
		v.PersistentVolumeClaim = &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}
	}
	return v
}

// readinessPath: in cluster mode /readyz also checks the database and the
// storage, so a pod that lost them leaves the Service instead of
// answering with errors.
func readinessPath(s registryv1alpha1.RegistrySpec) string {
	if cluster(s) {
		return "/readyz"
	}
	return "/healthz"
}

// spread places replicas on different nodes (and zones) when possible,
// unless the pod spec sets its own affinity.
func spread(reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec) []corev1.TopologySpreadConstraint {
	if *s.Replicas <= 1 || s.Pod.Affinity != nil {
		return nil
	}
	sel := &metav1.LabelSelector{MatchLabels: labelsFor(reg)}
	return []corev1.TopologySpreadConstraint{
		{MaxSkew: 1, TopologyKey: "kubernetes.io/hostname", WhenUnsatisfiable: corev1.ScheduleAnyway, LabelSelector: sel},
		{MaxSkew: 1, TopologyKey: "topology.kubernetes.io/zone", WhenUnsatisfiable: corev1.ScheduleAnyway, LabelSelector: sel},
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
	if len(s.Proxies) > 0 {
		var list []string
		for _, p := range s.Proxies {
			list = append(list, p.Name+"="+p.URL)
			key := "RGSTR_PROXY_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(p.Name)) + "_"
			if p.Public {
				add(key+"PUBLIC", "true")
			}
			if p.CredentialsSecretRef != nil {
				for _, kv := range [][2]string{{"USERNAME", "username"}, {"PASSWORD", "password"}} {
					env = append(env, corev1.EnvVar{Name: key + kv[0], ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: *p.CredentialsSecretRef, Key: kv[1]}}})
				}
			}
		}
		add("RGSTR_PROXIES", strings.Join(list, ","))
		add("RGSTR_PROXY_TTL", string(s.ProxyTTL))
	}
	if s.Scan.Enabled {
		add("RGSTR_SCAN_ENABLED", "true")
		if s.Scan.OnPush != nil && !*s.Scan.OnPush {
			add("RGSTR_SCAN_ON_PUSH", "false")
		}
		add("RGSTR_SCAN_INTERVAL", string(s.Scan.Interval))
		if s.Scan.Concurrency > 0 {
			add("RGSTR_SCAN_CONCURRENCY", fmt.Sprint(s.Scan.Concurrency))
		}
		if onS3(s) {
			add("RGSTR_SCAN_CACHE_DIR", trivyCacheDir)
		}
	}
	if s.Database != nil {
		env = append(env, corev1.EnvVar{Name: "RGSTR_DATABASE_URL",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: s.Database.URLSecretRef.DeepCopy()}})
	}
	if onS3(s) {
		st := s.Storage.S3
		add("RGSTR_STORAGE_DRIVER", "s3")
		add("RGSTR_STORAGE_S3_BUCKET", st.Bucket)
		add("RGSTR_STORAGE_S3_PREFIX", st.Prefix)
		add("RGSTR_STORAGE_S3_ENDPOINT", st.Endpoint)
		add("RGSTR_STORAGE_S3_REGION", st.Region)
		if st.PathStyle != nil {
			add("RGSTR_STORAGE_S3_PATH_STYLE", fmt.Sprint(*st.PathStyle))
		}
		if st.Redirect {
			add("RGSTR_STORAGE_S3_REDIRECT", "true")
		}
		for k, v := range map[string]string{"RGSTR_STORAGE_S3_ACCESS_KEY": "AWS_ACCESS_KEY_ID",
			"RGSTR_STORAGE_S3_SECRET_KEY": "AWS_SECRET_ACCESS_KEY"} {
			env = append(env, corev1.EnvVar{Name: k, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: st.SecretRef, Key: v}}})
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
	if s.Database != nil {
		out = append(out, s.Database.URLSecretRef.Name)
	}
	if onS3(s) {
		out = append(out, s.Storage.S3.SecretRef.Name)
	}
	for _, p := range s.Proxies {
		if p.CredentialsSecretRef != nil {
			out = append(out, p.CredentialsSecretRef.Name)
		}
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
