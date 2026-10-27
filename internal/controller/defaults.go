package controller

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	registryv1alpha1 "github.com/piqab/rgstroperator/api/v1alpha1"
)

// DefaultImage is the rgstr release this operator version is tested with.
const DefaultImage = "ghcr.io/piqab/rgstr:1.10.1"

const (
	defaultPort        = int32(5000)
	dataDir            = "/data"
	pvcSuffix          = "-data"
	authSecretSuffix   = "-auth"
	checksumAnnotation = "registry.rgstr.io/auth-checksum"
)

// withDefaults returns the spec with zero values replaced by defaults. The
// object itself is left unchanged.
func withDefaults(reg *registryv1alpha1.Registry) registryv1alpha1.RegistrySpec {
	s := *reg.Spec.DeepCopy()
	if s.Image == "" {
		s.Image = DefaultImage
	}
	if s.ImagePullPolicy == "" {
		s.ImagePullPolicy = corev1.PullIfNotPresent
	}
	if s.Port == 0 {
		s.Port = defaultPort
	}
	if s.Replicas == nil {
		one := int32(1)
		s.Replicas = &one
	} else if *s.Replicas > 1 && s.Database == nil {
		one := int32(1) // validated by the CRD; also guard old objects
		s.Replicas = &one
	}
	if s.ServiceType == "" {
		s.ServiceType = corev1.ServiceTypeClusterIP
	}
	if s.Storage.Size.IsZero() {
		s.Storage.Size = resource.MustParse("10Gi")
	}
	if len(s.Storage.AccessModes) == 0 {
		s.Storage.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	}
	if s.Storage.RetainOnDelete == nil {
		yes := true
		s.Storage.RetainOnDelete = &yes
	}
	if s.GCInterval == "" {
		s.GCInterval = "1h"
	}
	if s.UploadTTL == "" {
		s.UploadTTL = "24h"
	}
	return s
}

func labelsFor(reg *registryv1alpha1.Registry) map[string]string {
	// Also the Deployment's selector, which cannot change: keep as is.
	return map[string]string{
		"app.kubernetes.io/name":       "rgstr",
		"app.kubernetes.io/instance":   reg.Name,
		"app.kubernetes.io/managed-by": "rgstroperator",
	}
}

// cluster reports whether rgstr runs in cluster mode (shared database).
func cluster(s registryv1alpha1.RegistrySpec) bool { return s.Database != nil }

// onS3 reports whether the data is in a bucket instead of a volume.
func onS3(s registryv1alpha1.RegistrySpec) bool { return s.Storage.S3 != nil && s.Database != nil }

func claimName(reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec) string {
	if s.Storage.ExistingClaim != "" {
		return s.Storage.ExistingClaim
	}
	return reg.Name + pvcSuffix
}
