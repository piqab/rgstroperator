package controller

import (
	"context"

	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	registryv1alpha1 "github.com/piqab/rgstroperator/api/v1alpha1"
)

// reconcilePDB keeps a cluster of several replicas available during node
// drains and upgrades: at most one pod is evicted at a time. A single
// replica gets no budget (it would block every drain).
func (r *RegistryReconciler) reconcilePDB(ctx context.Context, reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec) error {
	pdb := &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: reg.Name, Namespace: reg.Namespace}}
	if *s.Replicas <= 1 {
		if err := r.Delete(ctx, pdb); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, pdb, func() error {
		pdb.Labels = labelsFor(reg)
		one := intstr.FromInt32(1)
		pdb.Spec.MaxUnavailable = &one
		pdb.Spec.MinAvailable = nil
		pdb.Spec.Selector = &metav1.LabelSelector{MatchLabels: labelsFor(reg)}
		return controllerutil.SetControllerReference(reg, pdb, r.Scheme)
	})
	return err
}
