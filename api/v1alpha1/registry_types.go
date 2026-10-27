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

	// Backup schedules backups of the data volume.
	// +optional
	Backup BackupSpec `json:"backup,omitempty"`
}

// BackupSpec schedules backups of the data volume. Backups are not owned
// by the Registry: deleting it keeps them.
// +kubebuilder:validation:XValidation:rule="!has(self.enabled) || !self.enabled || !has(self.method) || self.method != 'S3' || (has(self.s3) && has(self.s3.bucket) && has(self.s3.secretRef))",message="the S3 method needs s3.bucket and s3.secretRef"
type BackupSpec struct {
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Schedule in cron format (UTC). Defaults to "0 3 * * *".
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// Keep is how many backups to keep. Defaults to 7.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	Keep int32 `json:"keep,omitempty"`

	// Method: VolumeSnapshot (CSI snapshots, made by the operator),
	// Longhorn (a Longhorn RecurringJob) or S3 (rgstr uploads archives to
	// an S3-compatible store itself; no CSI driver needed). Defaults to
	// VolumeSnapshot.
	// +optional
	// +kubebuilder:validation:Enum=VolumeSnapshot;Longhorn;S3
	Method string `json:"method,omitempty"`

	// VolumeSnapshotClassName for the VolumeSnapshot method. Empty: the
	// default class.
	// +optional
	VolumeSnapshotClassName string `json:"volumeSnapshotClassName,omitempty"`

	// Longhorn settings for the Longhorn method.
	// +optional
	Longhorn LonghornBackupSpec `json:"longhorn,omitempty"`

	// S3 settings for the S3 method.
	// +optional
	S3 *S3BackupSpec `json:"s3,omitempty"`
}

// S3BackupSpec says where rgstr uploads its backups.
type S3BackupSpec struct {
	// Bucket, created beforehand.
	// +kubebuilder:validation:MinLength=1
	Bucket string `json:"bucket"`
	// Prefix of the backup objects, e.g. "rgstr/prod".
	// +optional
	Prefix string `json:"prefix,omitempty"`
	// Endpoint of an S3-compatible store, e.g. "https://storage.yandexcloud.net"
	// or "http://minio.minio:9000". Empty: AWS S3 for Region.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// Region. Defaults to "us-east-1".
	// +optional
	Region string `json:"region,omitempty"`
	// PathStyle addresses https://endpoint/bucket/key. Defaults to true
	// with an Endpoint (MinIO, Ceph), false for AWS.
	// +optional
	PathStyle *bool `json:"pathStyle,omitempty"`
	// SecretRef names a Secret with AWS_ACCESS_KEY_ID and
	// AWS_SECRET_ACCESS_KEY (and optionally AWS_SESSION_TOKEN).
	SecretRef corev1.LocalObjectReference `json:"secretRef"`
}

// LonghornBackupSpec configures the Longhorn RecurringJob.
type LonghornBackupSpec struct {
	// Task: "backup" (to the Longhorn backup target, off the node) or
	// "snapshot" (on the volume's own replicas). Defaults to "backup".
	// +optional
	// +kubebuilder:validation:Enum=backup;snapshot
	Task string `json:"task,omitempty"`

	// Namespace Longhorn runs in. Defaults to "longhorn-system".
	// +optional
	Namespace string `json:"namespace,omitempty"`
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
// +kubebuilder:validation:XValidation:rule="!has(self.enabled) || !self.enabled || (has(self.host) && size(self.host) > 0)",message="host is required when the ingress is enabled"
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

	// LDAP lets users sign in with LDAP / Active Directory passwords.
	// +optional
	LDAP *LDAPSpec `json:"ldap,omitempty"`

	// OIDC adds "Sign in with ..." (OpenID Connect) to the web admin.
	// +optional
	OIDC *OIDCSpec `json:"oidc,omitempty"`
}

// GroupRolesSpec maps directory groups to rgstr roles. Without admin and
// read-only groups, roles of directory users are set in the web admin.
type GroupRolesSpec struct {
	// AdminGroups grant the admin role.
	// +optional
	AdminGroups []string `json:"adminGroups,omitempty"`
	// ReadOnlyGroups grant the read-only role.
	// +optional
	ReadOnlyGroups []string `json:"readOnlyGroups,omitempty"`
	// AllowedGroups, when set, limit sign-in to their members (and to
	// members of the role groups).
	// +optional
	AllowedGroups []string `json:"allowedGroups,omitempty"`
	// DefaultRole of members of no role group. Defaults to "user".
	// +kubebuilder:validation:Enum=admin;user;readonly
	// +optional
	DefaultRole string `json:"defaultRole,omitempty"`
}

// LDAPSpec configures LDAP / Active Directory sign-in (RGSTR_LDAP_*).
type LDAPSpec struct {
	// URL, e.g. "ldaps://dc1.corp.example:636".
	// +kubebuilder:validation:Pattern=`^ldaps?://`
	URL string `json:"url"`
	// StartTLS upgrades an ldap:// connection.
	// +optional
	StartTLS bool `json:"startTLS,omitempty"`
	// InsecureSkipVerify does not check the server certificate (tests only).
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
	// BindDN of the service account that searches users. Empty: anonymous.
	// +optional
	BindDN string `json:"bindDN,omitempty"`
	// BindPasswordSecretRef selects the service account's password.
	// +optional
	BindPasswordSecretRef *corev1.SecretKeySelector `json:"bindPasswordSecretRef,omitempty"`
	// BaseDN to search users in.
	// +kubebuilder:validation:MinLength=1
	BaseDN string `json:"baseDN"`
	// UserFilter; %s is the user name. Defaults to
	// "(&(objectClass=person)(uid=%s))"; for Active Directory use
	// "(&(objectClass=user)(sAMAccountName=%s))".
	// +optional
	UserFilter string `json:"userFilter,omitempty"`
	// GroupAttr of the user entry listing its groups. Defaults to "memberOf".
	// +optional
	GroupAttr string `json:"groupAttr,omitempty"`
	// GroupFilter searches groups for directories without memberOf; %s is
	// the user's DN, e.g. "(&(objectClass=groupOfNames)(member=%s))".
	// +optional
	GroupFilter string `json:"groupFilter,omitempty"`
	// GroupBaseDN to search groups in. Defaults to BaseDN.
	// +optional
	GroupBaseDN string `json:"groupBaseDN,omitempty"`

	GroupRolesSpec `json:",inline"`
}

// OIDCSpec configures OpenID Connect sign-in for the web admin
// (RGSTR_OIDC_*). Users make a CLI secret in the admin for docker login.
type OIDCSpec struct {
	// Issuer URL, e.g. "https://sso.example.com/realms/main".
	// +kubebuilder:validation:Pattern=`^https?://`
	Issuer string `json:"issuer"`
	// ClientID registered at the provider.
	// +kubebuilder:validation:MinLength=1
	ClientID string `json:"clientID"`
	// ClientSecretRef selects the client secret. Unset: a public client
	// (PKCE only).
	// +optional
	ClientSecretRef *corev1.SecretKeySelector `json:"clientSecretRef,omitempty"`
	// RedirectURL registered at the provider. Defaults to
	// "<external URL>/ui/oidc/callback".
	// +optional
	RedirectURL string `json:"redirectURL,omitempty"`
	// Scopes to request. Defaults to [openid, profile, email].
	// +optional
	Scopes []string `json:"scopes,omitempty"`
	// UsernameClaim. Defaults to "preferred_username".
	// +optional
	UsernameClaim string `json:"usernameClaim,omitempty"`
	// GroupsClaim. Defaults to "groups".
	// +optional
	GroupsClaim string `json:"groupsClaim,omitempty"`
	// DisplayName on the sign-in button. Defaults to "SSO".
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	GroupRolesSpec `json:",inline"`
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

	// LastBackup is the newest backup (VolumeSnapshot method) or the
	// RecurringJob (Longhorn method).
	// +optional
	LastBackup string `json:"lastBackup,omitempty"`

	// LastBackupTime is when the newest VolumeSnapshot was taken.
	// +optional
	LastBackupTime *metav1.Time `json:"lastBackupTime,omitempty"`

	// NextBackupTime is when the next backup is due.
	// +optional
	NextBackupTime *metav1.Time `json:"nextBackupTime,omitempty"`
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
