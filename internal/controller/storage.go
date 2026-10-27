package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	registryv1alpha1 "github.com/piqab/rgstroperator/api/v1alpha1"
)

// reconcileStorage makes sure the data volume exists and returns its name.
// With retainOnDelete (the default) the PVC is not owned by the Registry,
// so Kubernetes keeps it — and the images — when the Registry is deleted.
func (r *RegistryReconciler) reconcileStorage(ctx context.Context, reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec) (string, error) {
	name := claimName(reg, s)
	pvc := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, client.ObjectKey{Name: name, Namespace: reg.Namespace}, pvc)

	if s.Storage.ExistingClaim != "" {
		if apierrors.IsNotFound(err) {
			return name, fmt.Errorf("existing claim %q not found", name)
		}
		return name, err // used as is: the operator does not manage it
	}

	if apierrors.IsNotFound(err) {
		pvc = &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: reg.Namespace, Labels: labelsFor(reg)},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      s.Storage.AccessModes,
				StorageClassName: s.Storage.StorageClassName,
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: s.Storage.Size},
				},
			},
		}
		if !*s.Storage.RetainOnDelete {
			if err := controllerutil.SetControllerReference(reg, pvc, r.Scheme); err != nil {
				return name, err
			}
		}
		return name, r.Create(ctx, pvc)
	}
	if err != nil {
		return name, err
	}

	changed := false
	owned := metav1.IsControlledBy(pvc, reg)
	switch {
	case *s.Storage.RetainOnDelete && owned:
		// Created by an older operator (or retainOnDelete was false):
		// release it so deleting the Registry keeps the data.
		pvc.OwnerReferences = removeOwner(pvc.OwnerReferences, reg.UID)
		changed = true
	case !*s.Storage.RetainOnDelete && !owned:
		if err := controllerutil.SetControllerReference(reg, pvc, r.Scheme); err != nil {
			return name, err
		}
		changed = true
	}
	// Volumes can only grow; the storage class must allow expansion.
	if cur := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; s.Storage.Size.Cmp(cur) > 0 {
		if pvc.Spec.Resources.Requests == nil {
			pvc.Spec.Resources.Requests = corev1.ResourceList{}
		}
		pvc.Spec.Resources.Requests[corev1.ResourceStorage] = s.Storage.Size
		changed = true
	}
	if changed {
		if err := r.Update(ctx, pvc); err != nil {
			return name, fmt.Errorf("update PVC %s: %w", name, err)
		}
	}
	return name, nil
}

func removeOwner(refs []metav1.OwnerReference, uid types.UID) []metav1.OwnerReference {
	var out []metav1.OwnerReference
	for _, ref := range refs {
		if ref.UID != uid {
			out = append(out, ref)
		}
	}
	return out
}
