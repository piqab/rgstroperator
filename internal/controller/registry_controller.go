package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	registryv1alpha1 "github.com/piqab/rgstroperator/api/v1alpha1"
)

const (
	defaultImage      = "ghcr.io/piqab/rgstr:latest"
	defaultPort       = int32(5000)
	defaultStorageDir = "/data"
	pvcSuffix         = "-data"
	finalizerName     = "registry.rgstr.io/finalizer"
)

// RegistryReconciler reconciles a Registry object.
type RegistryReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=registry.rgstr.io,resources=registries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=registry.rgstr.io,resources=registries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=registry.rgstr.io,resources=registries/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

func (r *RegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	registry := &registryv1alpha1.Registry{}
	if err := r.Get(ctx, req.NamespacedName, registry); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Apply defaults
	applyDefaults(registry)

	// Reconcile PVC
	if err := r.reconcilePVC(ctx, registry); err != nil {
		logger.Error(err, "failed to reconcile PVC")
		return ctrl.Result{}, err
	}

	// Reconcile Deployment
	if err := r.reconcileDeployment(ctx, registry); err != nil {
		logger.Error(err, "failed to reconcile Deployment")
		return ctrl.Result{}, err
	}

	// Reconcile Service
	if err := r.reconcileService(ctx, registry); err != nil {
		logger.Error(err, "failed to reconcile Service")
		return ctrl.Result{}, err
	}

	// Update status
	if err := r.updateStatus(ctx, registry); err != nil {
		logger.Error(err, "failed to update status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// applyDefaults fills in zero values with sensible defaults.
func applyDefaults(r *registryv1alpha1.Registry) {
	if r.Spec.Image == "" {
		r.Spec.Image = defaultImage
	}
	if r.Spec.Port == 0 {
		r.Spec.Port = defaultPort
	}
	if r.Spec.Replicas == nil {
		one := int32(1)
		r.Spec.Replicas = &one
	}
	if r.Spec.ServiceType == "" {
		r.Spec.ServiceType = corev1.ServiceTypeClusterIP
	}
	if r.Spec.Storage.Size.IsZero() {
		r.Spec.Storage.Size = resource.MustParse("10Gi")
	}
	if len(r.Spec.Storage.AccessModes) == 0 {
		r.Spec.Storage.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	}
	if r.Spec.GCInterval == "" {
		r.Spec.GCInterval = "1h"
	}
	if r.Spec.UploadTTL == "" {
		r.Spec.UploadTTL = "24h"
	}
}

// -------------------------------------------------------------------------
// PVC
// -------------------------------------------------------------------------

func (r *RegistryReconciler) reconcilePVC(ctx context.Context, registry *registryv1alpha1.Registry) error {
	pvc := &corev1.PersistentVolumeClaim{}
	name := registry.Name + pvcSuffix
	err := r.Get(ctx, client.ObjectKey{Name: name, Namespace: registry.Namespace}, pvc)
	if errors.IsNotFound(err) {
		pvc = r.buildPVC(registry, name)
		if err := controllerutil.SetControllerReference(registry, pvc, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, pvc)
	}
	return err
}

func (r *RegistryReconciler) buildPVC(registry *registryv1alpha1.Registry, name string) *corev1.PersistentVolumeClaim {
	storage := registry.Spec.Storage
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: registry.Namespace,
			Labels:    labelsFor(registry),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: storage.AccessModes,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storage.Size,
				},
			},
		},
	}
	if storage.StorageClassName != nil {
		pvc.Spec.StorageClassName = storage.StorageClassName
	}
	return pvc
}

// -------------------------------------------------------------------------
// Deployment
// -------------------------------------------------------------------------

func (r *RegistryReconciler) reconcileDeployment(ctx context.Context, registry *registryv1alpha1.Registry) error {
	dep := &appsv1.Deployment{}
	err := r.Get(ctx, client.ObjectKey{Name: registry.Name, Namespace: registry.Namespace}, dep)
	if errors.IsNotFound(err) {
		dep = r.buildDeployment(registry)
		if err := controllerutil.SetControllerReference(registry, dep, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, dep)
	}
	if err != nil {
		return err
	}

	// Update mutable fields
	dep.Spec.Replicas = registry.Spec.Replicas
	dep.Spec.Template.Spec.Containers[0].Image = registry.Spec.Image
	dep.Spec.Template.Spec.Containers[0].Env = r.buildEnvVars(registry)
	dep.Spec.Template.Spec.Containers[0].Resources = registry.Spec.Resources
	return r.Update(ctx, dep)
}

func (r *RegistryReconciler) buildDeployment(registry *registryv1alpha1.Registry) *appsv1.Deployment {
	labels := labelsFor(registry)
	port := registry.Spec.Port

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      registry.Name,
			Namespace: registry.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: registry.Spec.Replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            "rgstr",
							Image:           registry.Spec.Image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Ports: []corev1.ContainerPort{
								{Name: "registry", ContainerPort: port, Protocol: corev1.ProtocolTCP},
							},
							Env: r.buildEnvVars(registry),
							VolumeMounts: []corev1.VolumeMount{
								{Name: "data", MountPath: defaultStorageDir},
							},
							Resources: registry.Spec.Resources,
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path:   "/healthz",
										Port:   intstr.FromInt32(port),
										Scheme: corev1.URISchemeHTTP,
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       15,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path:   "/v2/",
										Port:   intstr.FromInt32(port),
										Scheme: corev1.URISchemeHTTP,
									},
								},
								InitialDelaySeconds: 3,
								PeriodSeconds:       10,
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "data",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: registry.Name + pvcSuffix,
								},
							},
						},
					},
				},
			},
		},
	}
	return dep
}

func (r *RegistryReconciler) buildEnvVars(registry *registryv1alpha1.Registry) []corev1.EnvVar {
	port := registry.Spec.Port
	envs := []corev1.EnvVar{
		{Name: "RGSTR_ADDR", Value: fmt.Sprintf(":%d", port)},
		{Name: "RGSTR_STORAGE", Value: defaultStorageDir},
		{Name: "RGSTR_GC_INTERVAL", Value: registry.Spec.GCInterval},
		{Name: "RGSTR_UPLOAD_TTL", Value: registry.Spec.UploadTTL},
		{Name: "RGSTR_PUBLIC_REPOS", Value: registry.Spec.PublicRepos},
	}

	auth := registry.Spec.Auth
	if auth.Enabled {
		envs = append(envs, corev1.EnvVar{Name: "RGSTR_AUTH_ENABLED", Value: "true"})

		realm := auth.Realm
		if realm == "" {
			realm = fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/v2/auth",
				registry.Name, registry.Namespace, port)
		}
		envs = append(envs,
			corev1.EnvVar{Name: "RGSTR_AUTH_REALM", Value: realm},
			corev1.EnvVar{Name: "RGSTR_AUTH_SERVICE", Value: "rgstr"},
			corev1.EnvVar{Name: "RGSTR_AUTH_ISSUER", Value: "rgstr"},
		)
		if auth.TokenTTL != "" {
			envs = append(envs, corev1.EnvVar{Name: "RGSTR_TOKEN_TTL", Value: auth.TokenTTL})
		}

		if auth.SecretRef != nil {
			secretName := auth.SecretRef.Name
			envs = append(envs,
				corev1.EnvVar{
					Name: "RGSTR_AUTH_SECRET",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
							Key:                  "RGSTR_AUTH_SECRET",
						},
					},
				},
				corev1.EnvVar{
					Name: "RGSTR_USERS",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
							Key:                  "RGSTR_USERS",
						},
					},
				},
			)
		}
	}

	return envs
}

// -------------------------------------------------------------------------
// Service
// -------------------------------------------------------------------------

func (r *RegistryReconciler) reconcileService(ctx context.Context, registry *registryv1alpha1.Registry) error {
	svc := &corev1.Service{}
	err := r.Get(ctx, client.ObjectKey{Name: registry.Name, Namespace: registry.Namespace}, svc)
	if errors.IsNotFound(err) {
		svc = r.buildService(registry)
		if err := controllerutil.SetControllerReference(registry, svc, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, svc)
	}
	if err != nil {
		return err
	}

	// Update mutable fields
	svc.Spec.Type = registry.Spec.ServiceType
	svc.Spec.Ports[0].Port = registry.Spec.Port
	svc.Spec.Ports[0].TargetPort = intstr.FromInt32(registry.Spec.Port)
	return r.Update(ctx, svc)
}

func (r *RegistryReconciler) buildService(registry *registryv1alpha1.Registry) *corev1.Service {
	labels := labelsFor(registry)
	port := registry.Spec.Port
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      registry.Name,
			Namespace: registry.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     registry.Spec.ServiceType,
			Selector: labels,
			Ports: []corev1.ServicePort{
				{
					Name:       "registry",
					Port:       port,
					TargetPort: intstr.FromInt32(port),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}
}

// -------------------------------------------------------------------------
// Status
// -------------------------------------------------------------------------

func (r *RegistryReconciler) updateStatus(ctx context.Context, registry *registryv1alpha1.Registry) error {
	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Name: registry.Name, Namespace: registry.Namespace}, dep); err != nil {
		return client.IgnoreNotFound(err)
	}

	svc := &corev1.Service{}
	if err := r.Get(ctx, client.ObjectKey{Name: registry.Name, Namespace: registry.Namespace}, svc); err != nil {
		return client.IgnoreNotFound(err)
	}

	patch := client.MergeFrom(registry.DeepCopy())
	registry.Status.Ready = dep.Status.ReadyReplicas == *registry.Spec.Replicas
	registry.Status.ServiceName = svc.Name
	registry.Status.ClusterIP = svc.Spec.ClusterIP
	return r.Status().Patch(ctx, registry, patch)
}

// -------------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------------

func labelsFor(registry *registryv1alpha1.Registry) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "rgstr",
		"app.kubernetes.io/instance":   registry.Name,
		"app.kubernetes.io/managed-by": "rgstroperator",
	}
}

// SetupWithManager registers the controller with the manager.
func (r *RegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&registryv1alpha1.Registry{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Complete(r)
}
