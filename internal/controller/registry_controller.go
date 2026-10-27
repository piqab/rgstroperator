package controller

import (
	"context"
	"errors"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	registryv1alpha1 "github.com/piqab/rgstroperator/api/v1alpha1"
)

// Condition types.
const (
	CondReady   = "Ready"
	CondStorage = "StorageReady"
	CondAuth    = "AuthReady"
	CondIngress = "IngressReady"
	CondMetrics = "MetricsReady"
)

// resync is how often a Registry is reconciled without any change, to
// repair drift.
const resync = 5 * time.Minute

// RegistryReconciler reconciles a Registry object.
type RegistryReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// ProbeVersion reads the running version; nil: HTTP to /healthz.
	ProbeVersion VersionProber
}

// +kubebuilder:rbac:groups=registry.rgstr.io,resources=registries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=registry.rgstr.io,resources=registries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=registry.rgstr.io,resources=registries/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services;persistentvolumeclaims;secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives the Registry's objects towards its spec. Steps are
// independent: one failing does not stop the others, and its error is
// reported in a condition and returned (so it is retried).
func (r *RegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	reg := &registryv1alpha1.Registry{}
	if err := r.Get(ctx, req.NamespacedName, reg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !reg.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil // owned objects are removed by Kubernetes
	}
	s := withDefaults(reg)
	st := &reg.Status
	var errs []error
	fail := func(cond, reason string, err error) {
		setCondition(reg, cond, metav1.ConditionFalse, reason, err.Error())
		r.Recorder.Event(reg, corev1.EventTypeWarning, reason, err.Error())
		logger.Error(err, reason)
		errs = append(errs, err)
	}

	claim, err := r.reconcileStorage(ctx, reg, s)
	st.ClaimName = claim
	if err != nil {
		fail(CondStorage, "StorageFailed", err)
	} else {
		setCondition(reg, CondStorage, metav1.ConditionTrue, "Bound", "volume "+claim)
	}

	secret, checksum, err := r.reconcileAuthSecret(ctx, reg, s)
	st.AuthSecretName = secret
	switch {
	case !s.Auth.Enabled:
		apimeta.RemoveStatusCondition(&st.Conditions, CondAuth)
	case err != nil:
		fail(CondAuth, "AuthSecretFailed", err)
	default:
		setCondition(reg, CondAuth, metav1.ConditionTrue, "SecretReady", "secret "+secret)
	}

	var dep *appsv1.Deployment
	if len(errs) == 0 { // never start rgstr without its volume or secret
		if dep, err = r.reconcileDeployment(ctx, reg, s, claim, secret, checksum); err != nil {
			fail(CondReady, "DeploymentFailed", err)
		}
	}
	svc, err := r.reconcileService(ctx, reg, s)
	if err != nil {
		fail(CondReady, "ServiceFailed", err)
	} else {
		st.ServiceName, st.ClusterIP = svc.Name, svc.Spec.ClusterIP
	}

	ing, err := r.reconcileIngress(ctx, reg, s)
	switch {
	case err != nil:
		fail(CondIngress, "IngressFailed", err)
	case ing == nil:
		apimeta.RemoveStatusCondition(&st.Conditions, CondIngress)
	case len(ing.Status.LoadBalancer.Ingress) == 0:
		setCondition(reg, CondIngress, metav1.ConditionFalse, "NoAddress", "waiting for the ingress controller to assign an address")
	default:
		setCondition(reg, CondIngress, metav1.ConditionTrue, "Published", externalURL(reg, s))
	}
	st.URL = externalURL(reg, s)

	switch err := r.reconcileServiceMonitor(ctx, reg, s); {
	case err != nil:
		fail(CondMetrics, "ServiceMonitorFailed", err)
	case s.Metrics.Enabled && s.Metrics.ServiceMonitor.Enabled:
		setCondition(reg, CondMetrics, metav1.ConditionTrue, "ServiceMonitor", "scraped at /metrics")
	default:
		clearCondition(reg, CondMetrics)
	}

	r.summarise(reg, s, dep, len(errs) > 0)
	r.observeVersion(ctx, reg, s)
	if err := r.Status().Update(ctx, reg); err != nil {
		errs = append(errs, err)
	}
	return ctrl.Result{RequeueAfter: resync}, errors.Join(errs...)
}

// summarise sets Ready, Phase and ObservedGeneration.
func (r *RegistryReconciler) summarise(reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec, dep *appsv1.Deployment, failed bool) {
	st := &reg.Status
	st.ObservedGeneration = reg.Generation
	st.Ready = dep != nil && *s.Replicas == 1 && dep.Status.ReadyReplicas >= 1 &&
		dep.Status.ObservedGeneration >= dep.Generation && dep.Status.UpdatedReplicas >= 1
	switch {
	case failed:
		st.Phase = "Degraded"
		st.Ready = false
	case *s.Replicas == 0:
		st.Phase = "Stopped"
		setCondition(reg, CondReady, metav1.ConditionFalse, "Stopped", "replicas is 0")
	case st.Ready:
		st.Phase = "Running"
		setCondition(reg, CondReady, metav1.ConditionTrue, "Running", "rgstr is ready")
	default:
		st.Phase = "Pending"
		setCondition(reg, CondReady, metav1.ConditionFalse, "Starting", "waiting for the rgstr pod to become ready")
	}
}

func setCondition(reg *registryv1alpha1.Registry, typ string, status metav1.ConditionStatus, reason, msg string) {
	apimeta.SetStatusCondition(&reg.Status.Conditions, metav1.Condition{
		Type: typ, Status: status, Reason: reason, Message: msg, ObservedGeneration: reg.Generation,
	})
}

// SetupWithManager registers the controller with the manager.
func (r *RegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&registryv1alpha1.Registry{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{}).
		// A user-provided auth Secret changed: reconcile the Registries
		// referencing it (its checksum restarts rgstr).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.registriesForSecret)).
		Complete(r)
}

func (r *RegistryReconciler) registriesForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &registryv1alpha1.RegistryList{}
	if err := r.List(ctx, list, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var out []reconcile.Request
	for _, reg := range list.Items {
		ref := reg.Spec.Auth.SecretRef
		if (ref != nil && ref.Name == obj.GetName()) || reg.Name+authSecretSuffix == obj.GetName() {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&reg)})
		}
	}
	return out
}
