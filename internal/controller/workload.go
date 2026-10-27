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
	if s.Auth.Enabled {
		add("RGSTR_AUTH_ENABLED", "true")
		add("RGSTR_AUTH_REALM", realm(reg, s))
		add("RGSTR_TOKEN_TTL", string(s.Auth.TokenTTL))
		add("RGSTR_SESSION_TTL", string(s.Auth.SessionTTL))
		add("RGSTR_ADMIN_USER", s.Auth.AdminUser)
		add("RGSTR_ADMINS", strings.Join(s.Auth.Admins, ","))
		add("RGSTR_READONLY_USERS", strings.Join(s.Auth.ReadOnlyUsers, ","))
	}
	return append(env, s.Env...)
}

// realm is the token endpoint clients are sent to.
func realm(reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec) string {
	if s.Auth.Realm != "" {
		return s.Auth.Realm
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/v2/auth", reg.Name, reg.Namespace, s.Port)
}

func ptr[T any](v T) *T { return &v }
