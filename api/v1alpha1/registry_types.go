package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Duration is a Go duration such as "30m", "1h" or "168h".
// +kubebuilder:validation:Pattern=`^(0|([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+)$`
type Duration string

// RegistrySpec defines the desired state of Registry.
type RegistrySpec struct {
	// Image is the rgstr image. Defaults to the rgstr release this operator
	// was tested with (see the operator's README).
	// +optional
	Image string `json:"image,omitempty"`

	// ImagePullPolicy for the rgstr image. Defaults to IfNotPresent.
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// ImagePullSecrets for pulling the rgstr image from a private registry.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// Replicas is 1 (running) or 0 (stopped). rgstr is a single process on
	// one volume, so more replicas are not supported. Defaults to 1.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	Replicas *int32 `json:"replicas,omitempty"`

	// Port the registry listens on. Defaults to 5000.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`

	// ServiceType of the managed Service. Defaults to ClusterIP.
	// +optional
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	ServiceType corev1.ServiceType `json:"serviceType,omitempty"`

	// Storage configures the volume for registry data (images, users,
	// settings, audit log).
	// +optional
	Storage StorageSpec `json:"storage,omitempty"`

	// Auth configures authentication.
	// +optional
	Auth AuthSpec `json:"auth,omitempty"`

	// GCInterval is how often garbage collection runs (RGSTR_GC_INTERVAL).
	// Defaults to "1h"; "0" disables scheduled runs.
	// +optional
	GCInterval Duration `json:"gcInterval,omitempty"`

	// UploadTTL is the lifetime of unfinished uploads and the GC grace
	// period (RGSTR_UPLOAD_TTL). Defaults to "24h".
	// +optional
	UploadTTL Duration `json:"uploadTTL,omitempty"`

	// PublicRepos is a comma-separated list of glob patterns of
	// repositories anyone may pull (RGSTR_PUBLIC_REPOS).
	// +optional
	PublicRepos string `json:"publicRepos,omitempty"`

	// TrustedProxies are networks whose X-Forwarded-For header is believed
	// (RGSTR_TRUSTED_PROXIES). Unset: loopback and private networks, which
	// covers in-cluster ingress controllers.
	// +optional
	TrustedProxies []string `json:"trustedProxies,omitempty"`

	// Env adds or overrides environment variables of the rgstr container.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Resources sets CPU/memory requests and limits for the rgstr container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Pod customises scheduling and security of the rgstr pod.
	// +optional
	Pod PodSpec `json:"pod,omitempty"`

	// Ingress publishes the registry (docker push/pull and the web admin)
	// outside the cluster.
	// +optional
	Ingress IngressSpec `json:"ingress,omitempty"`

	// Metrics exposes Prometheus metrics.
	// +optional
	Metrics MetricsSpec `json:"metrics,omitempty"`
}

// MetricsSpec configures Prometheus metrics.
type MetricsSpec struct {
	// Enabled serves /metrics on the Service (RGSTR_METRICS_ENABLED). The
	// Ingress never publishes it.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// ServiceMonitor for the Prometheus Operator (needs its CRDs).
	// +optional
	ServiceMonitor ServiceMonitorSpec `json:"serviceMonitor,omitempty"`
}

// ServiceMonitorSpec configures the managed ServiceMonitor.
type ServiceMonitorSpec struct {
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// Interval between scrapes. Defaults to "30s".
	// +optional
	Interval Duration `json:"interval,omitempty"`
	// Labels the Prometheus instance selects ServiceMonitors by.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// IngressSpec configures the managed Ingress.
// +kubebuilder:validation:XValidation:rule="!has(self.enabled) || !self.enabled || (has(self.host) && self.host != ”)",message="host is required when the ingress is enabled"
type IngressSpec struct {
	// Enabled creates an Ingress for Host.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Host clients use, e.g. "registry.example.com". Required when enabled.
	// +optional
	Host string `json:"host,omitempty"`

	// ClassName of the ingress controller, e.g. "nginx". Empty: the default.
	// +optional
	ClassName *string `json:"className,omitempty"`

	// Annotations added to the Ingress; they override the operator's.
	// For the nginx class the operator lifts the request size limit and
	// timeouts so large layers upload.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// TLS for Host. Container runtimes (kubelet pulls) require HTTPS.
	// +optional
	TLS IngressTLSSpec `json:"tls,omitempty"`
}

// IngressTLSSpec configures TLS of the Ingress.
type IngressTLSSpec struct {
	// Enabled defaults to true.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// SecretName holding the certificate. Defaults to "<name>-tls". With
	// an issuer, cert-manager creates it.
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// ClusterIssuer of cert-manager to request the certificate from.
	// +optional
	ClusterIssuer string `json:"clusterIssuer,omitempty"`

	// Issuer of cert-manager (namespaced) to request the certificate from.
	// +optional
	Issuer string `json:"issuer,omitempty"`
}

// StorageSpec defines the persistent volume for registry data.
type StorageSpec struct {
	// Size of the volume. Defaults to "10Gi". Increasing it expands the
	// volume if the storage class allows expansion.
	// +optional
	Size resource.Quantity `json:"size,omitempty"`

	// StorageClassName, e.g. "longhorn". Empty: the default class.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// AccessModes defaults to [ReadWriteOnce].
	// +optional
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`

	// ExistingClaim uses this PVC instead of creating "<name>-data", e.g.
	// a volume restored from a backup or snapshot.
	// +optional
	ExistingClaim string `json:"existingClaim,omitempty"`

	// RetainOnDelete keeps the PVC (and the generated auth Secret) when the
	// Registry is deleted. Defaults to true: deleting the resource never
	// deletes the images.
	// +optional
	RetainOnDelete *bool `json:"retainOnDelete,omitempty"`
}

// AuthSpec configures authentication.
type AuthSpec struct {
	// Enabled turns authentication on (RGSTR_AUTH_ENABLED).
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// SecretRef names a Secret whose keys become rgstr environment
	// variables (RGSTR_AUTH_SECRET, RGSTR_ADMIN_PASSWORD, RGSTR_USERS, …).
	// Unset: the operator creates "<name>-auth" with a random
	// RGSTR_AUTH_SECRET and a temporary RGSTR_ADMIN_PASSWORD.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`

	// Realm is the token endpoint URL clients are sent to. Defaults to
	// https://<ingress host>/v2/auth with an ingress, otherwise the
	// in-cluster Service address.
	// +optional
	Realm string `json:"realm,omitempty"`

	// TokenTTL is the lifetime of docker login tokens (RGSTR_TOKEN_TTL).
	// +optional
	TokenTTL Duration `json:"tokenTTL,omitempty"`

	// SessionTTL is the lifetime of web admin sessions (RGSTR_SESSION_TTL).
	// +optional
	SessionTTL Duration `json:"sessionTTL,omitempty"`

	// AdminUser is the name of the first administrator (RGSTR_ADMIN_USER).
	// Defaults to "admin".
	// +optional
	AdminUser string `json:"adminUser,omitempty"`

	// Admins may delete and administer the registry (RGSTR_ADMINS).
	// +optional
	Admins []string `json:"admins,omitempty"`

	// ReadOnlyUsers may only pull (RGSTR_READONLY_USERS).
	// +optional
	ReadOnlyUsers []string `json:"readOnlyUsers,omitempty"`
}

// PodSpec customises the rgstr pod.
type PodSpec struct {
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
	// SecurityContext of the pod.
	// +optional
	SecurityContext *corev1.PodSecurityContext `json:"securityContext,omitempty"`
	// ContainerSecurityContext of the rgstr container.
	// +optional
	ContainerSecurityContext *corev1.SecurityContext `json:"containerSecurityContext,omitempty"`
	// Annotations and Labels added to the pod.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`
	// ServiceAccountName for the pod. Empty: the namespace default.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
}

// RegistryStatus defines the observed state of Registry.
type RegistryStatus struct {
	// Phase is a one-word summary: Pending, Running, Stopped or Degraded.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Ready is true when the registry pod is ready.
	Ready bool `json:"ready,omitempty"`

	// ObservedGeneration is the generation the status describes.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions: Ready, StorageReady, and more for optional features.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ServiceName is the name of the managed Service.
	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// ClusterIP assigned to the managed Service.
	// +optional
	ClusterIP string `json:"clusterIP,omitempty"`

	// AuthSecretName is the Secret holding RGSTR_AUTH_SECRET and the
	// first administrator's temporary password.
	// +optional
	AuthSecretName string `json:"authSecretName,omitempty"`

	// ClaimName is the PVC holding the registry data.
	// +optional
	ClaimName string `json:"claimName,omitempty"`

	// URL clients use: https://<ingress host>, or the in-cluster address.
	// +optional
	URL string `json:"url,omitempty"`

	// Version of the running rgstr, as reported by its /healthz.
	// +optional
	Version string `json:"version,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=rgstr
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Ready",type="boolean",JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".status.version"
// +kubebuilder:printcolumn:name="URL",type="string",JSONPath=".status.url"
// +kubebuilder:printcolumn:name="Image",type="string",JSONPath=".spec.image",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Registry is an rgstr container registry.
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
