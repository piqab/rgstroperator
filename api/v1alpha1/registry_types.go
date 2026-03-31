package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RegistrySpec defines the desired state of Registry.
type RegistrySpec struct {
	// Image is the rgstr container image. Defaults to ghcr.io/piqab/rgstr:latest.
	// +optional
	Image string `json:"image,omitempty"`

	// Replicas is the number of registry pods. Defaults to 1.
	// +optional
	// +kubebuilder:default=1
	Replicas *int32 `json:"replicas,omitempty"`

	// Port is the port the registry listens on. Defaults to 5000.
	// +optional
	// +kubebuilder:default=5000
	Port int32 `json:"port,omitempty"`

	// ServiceType is the Kubernetes Service type. Defaults to ClusterIP.
	// +optional
	// +kubebuilder:default=ClusterIP
	ServiceType corev1.ServiceType `json:"serviceType,omitempty"`

	// Storage configures the persistent volume for registry data.
	// +optional
	Storage StorageSpec `json:"storage,omitempty"`

	// Auth configures authentication.
	// +optional
	Auth AuthSpec `json:"auth,omitempty"`

	// GCInterval is the garbage collection interval (e.g. "1h"). Defaults to "1h".
	// +optional
	GCInterval string `json:"gcInterval,omitempty"`

	// UploadTTL is the TTL for incomplete uploads (e.g. "24h"). Defaults to "24h".
	// +optional
	UploadTTL string `json:"uploadTTL,omitempty"`

	// PublicRepos is a comma-separated list of glob patterns for public repositories.
	// +optional
	PublicRepos string `json:"publicRepos,omitempty"`

	// Resources sets CPU/memory requests and limits for the registry container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// StorageSpec defines PVC settings for registry blob storage.
type StorageSpec struct {
	// Size is the storage request. Defaults to "10Gi".
	// +optional
	// +kubebuilder:default="10Gi"
	Size resource.Quantity `json:"size,omitempty"`

	// StorageClassName is the storage class to use. If empty, the default class is used.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// AccessModes defaults to [ReadWriteOnce].
	// +optional
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`
}

// AuthSpec configures bearer-token authentication.
type AuthSpec struct {
	// Enabled enables authentication. Defaults to false.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// SecretRef references a Kubernetes Secret that must contain:
	//   RGSTR_AUTH_SECRET — JWT signing secret
	//   RGSTR_USERS       — comma-separated "user:bcrypt-hash" pairs
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`

	// Realm is the URL of the token endpoint returned in WWW-Authenticate.
	// Defaults to "http://<service-name>.<namespace>.svc.cluster.local:<port>/v2/auth".
	// +optional
	Realm string `json:"realm,omitempty"`

	// TokenTTL is the lifetime of issued JWT tokens. Defaults to "1h".
	// +optional
	TokenTTL string `json:"tokenTTL,omitempty"`
}

// RegistryStatus defines the observed state of Registry.
type RegistryStatus struct {
	// Ready is true when the Deployment has the required number of ready replicas.
	Ready bool `json:"ready,omitempty"`

	// Conditions represent the latest available observations of the Registry's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ServiceName is the name of the managed Service.
	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// ClusterIP is the ClusterIP assigned to the managed Service.
	// +optional
	ClusterIP string `json:"clusterIP,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.ready
// +kubebuilder:printcolumn:name="Ready",type="boolean",JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="Service",type="string",JSONPath=".status.serviceName"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Registry is the Schema for the registries API.
type Registry struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RegistrySpec   `json:"spec,omitempty"`
	Status RegistryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RegistryList contains a list of Registry.
type RegistryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Registry `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Registry{}, &RegistryList{})
}
