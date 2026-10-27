package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	registryv1alpha1 "github.com/piqab/rgstroperator/api/v1alpha1"
)

// reconcileService creates or updates the Service in front of rgstr.
func (r *RegistryReconciler) reconcileService(ctx context.Context, reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec) (*corev1.Service, error) {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: reg.Name, Namespace: reg.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if svc.Labels == nil {
			svc.Labels = map[string]string{}
		}
		for k, v := range labelsFor(reg) {
			svc.Labels[k] = v
		}
		svc.Spec.Type = s.ServiceType
		svc.Spec.Selector = labelsFor(reg)
		port := corev1.ServicePort{
			Name: "registry", Port: s.Port, TargetPort: intstr.FromString("registry"), Protocol: corev1.ProtocolTCP,
		}
		// Keep an allocated node port across updates.
		if len(svc.Spec.Ports) > 0 && s.ServiceType != corev1.ServiceTypeClusterIP {
			port.NodePort = svc.Spec.Ports[0].NodePort
		}
		svc.Spec.Ports = []corev1.ServicePort{port}
		return controllerutil.SetControllerReference(reg, svc, r.Scheme)
	})
	return svc, err
}
