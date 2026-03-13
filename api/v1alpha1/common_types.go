package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RuntimeType defines the type of Claw runtime.
// +kubebuilder:validation:Enum=openclaw;nanoclaw;zeroclaw;picoclaw;ironclaw;custom
type RuntimeType string

// RuntimeType constants.
const (
	RuntimeOpenClaw RuntimeType = "openclaw"
	RuntimeNanoClaw RuntimeType = "nanoclaw"
	RuntimeZeroClaw RuntimeType = "zeroclaw"
	RuntimePicoClaw RuntimeType = "picoclaw"
	RuntimeIronClaw RuntimeType = "ironclaw"
	RuntimeCustom   RuntimeType = "custom"
)

// ReclaimPolicy defines what happens to PVCs when a Claw is deleted.
// +kubebuilder:validation:Enum=Retain;Archive;Delete
type ReclaimPolicy string

// ReclaimPolicy constants.
const (
	ReclaimRetain  ReclaimPolicy = "Retain"
	ReclaimArchive ReclaimPolicy = "Archive"
	ReclaimDelete  ReclaimPolicy = "Delete"
)

// ChannelMode defines the communication direction.
// +kubebuilder:validation:Enum=inbound;outbound;bidirectional
type ChannelMode string

// ChannelMode constants.
const (
	ChannelModeInbound       ChannelMode = "inbound"
	ChannelModeOutbound      ChannelMode = "outbound"
	ChannelModeBidirectional ChannelMode = "bidirectional"
)

// CredentialSpec defines how credentials are provided to the Claw runtime.
// secretRef and externalSecret are mutually exclusive.
type CredentialSpec struct {
	// SecretRef references a K8s Secret containing all required credentials.
	// Mutually exclusive with ExternalSecret.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`

	// ExternalSecret creates an ExternalSecret CR (requires external-secrets-operator).
	// Mutually exclusive with SecretRef.
	// +optional
	ExternalSecret *ExternalSecretRef `json:"externalSecret,omitempty"`

	// Keys provides fine-grained per-key mappings that override the base Secret.
	// +optional
	Keys []KeyMapping `json:"keys,omitempty"`
}

// ExternalSecretRef references an external secret store.
type ExternalSecretRef struct {
	// Provider is the external secret provider (e.g., vault, aws, gcp).
	Provider string `json:"provider"`

	// Store references a ClusterSecretStore or SecretStore name.
	Store string `json:"store"`

	// Path is the secret path within the provider.
	Path string `json:"path"`

	// RefreshInterval is how often to sync the secret.
	// +optional
	RefreshInterval string `json:"refreshInterval,omitempty"`
}

// KeyMapping maps a single environment variable to a specific Secret key.
type KeyMapping struct {
	// Name is the environment variable name.
	Name string `json:"name"`

	// SecretKeyRef references a key within a K8s Secret.
	SecretKeyRef corev1.SecretKeySelector `json:"secretKeyRef"`
}

// ChannelRef references a ClawChannel with a communication mode.
type ChannelRef struct {
	// Name is the ClawChannel resource name.
	Name string `json:"name"`

	// Mode defines the communication direction.
	// +kubebuilder:default=bidirectional
	Mode ChannelMode `json:"mode,omitempty"`
}

// PersistenceSpec defines all persistence configuration.
type PersistenceSpec struct {
	// ReclaimPolicy controls PVC behavior on Claw deletion.
	// +kubebuilder:default=Retain
	ReclaimPolicy ReclaimPolicy `json:"reclaimPolicy,omitempty"`

	// Session storage for conversation history and context.
	// +optional
	Session *VolumeSpec `json:"session,omitempty"`

	// Output storage for artifacts (papers, code, notebooks).
	// +optional
	Output *OutputVolumeSpec `json:"output,omitempty"`

	// Workspace storage for working files.
	// +optional
	Workspace *VolumeSpec `json:"workspace,omitempty"`

	// Shared volumes mounted from pre-existing PVCs.
	// +optional
	Shared []SharedVolumeRef `json:"shared,omitempty"`

	// Cache for model embeddings and indexes (ephemeral).
	// +optional
	Cache *CacheSpec `json:"cache,omitempty"`
}

// VolumeSpec defines a persistent volume with optional snapshot support.
type VolumeSpec struct {
	// Enabled controls whether this volume is provisioned.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// StorageClass for the PVC. Uses cluster default if empty.
	// +optional
	StorageClass string `json:"storageClass,omitempty"`

	// Size is the initial PVC size (e.g., "2Gi").
	Size string `json:"size"`

	// MaxSize is the auto-expansion ceiling (e.g., "20Gi").
	// +optional
	MaxSize string `json:"maxSize,omitempty"`

	// MountPath inside the container.
	MountPath string `json:"mountPath"`

	// Snapshot configuration for CSI VolumeSnapshot.
	// +optional
	Snapshot *SnapshotSpec `json:"snapshot,omitempty"`
}

// OutputVolumeSpec extends VolumeSpec with archival configuration.
type OutputVolumeSpec struct {
	VolumeSpec `json:",inline"`

	// Archive configuration for S3-compatible object storage.
	// +optional
	Archive *ArchiveSpec `json:"archive,omitempty"`
}

// SnapshotSpec configures CSI VolumeSnapshot-based snapshots.
type SnapshotSpec struct {
	// Enabled controls whether snapshots are taken.
	Enabled bool `json:"enabled"`

	// Schedule is a cron expression for snapshot frequency.
	Schedule string `json:"schedule"`

	// Retain is the number of snapshots to keep.
	// +kubebuilder:default=5
	Retain int `json:"retain,omitempty"`

	// VolumeSnapshotClass to use. Required if enabled.
	// +optional
	VolumeSnapshotClass string `json:"volumeSnapshotClass,omitempty"`
}

// ArchiveSpec configures output archival to object storage.
type ArchiveSpec struct {
	// Enabled controls whether archival is active.
	Enabled bool `json:"enabled"`

	// Destination is the object storage target.
	Destination ArchiveDestination `json:"destination"`

	// Trigger controls when archival happens.
	Trigger ArchiveTrigger `json:"trigger"`

	// Lifecycle controls retention policies.
	// +optional
	Lifecycle *ArchiveLifecycle `json:"lifecycle,omitempty"`
}

// ArchiveDestination specifies the target object storage.
type ArchiveDestination struct {
	// Type is the S3-compatible storage backend (s3, minio, gcs-s3-interop, r2).
	Type string `json:"type"`

	// Bucket name.
	Bucket string `json:"bucket"`

	// Prefix is the key prefix (supports Go template: {{.Namespace}}/{{.Name}}/).
	// +optional
	Prefix string `json:"prefix,omitempty"`

	// SecretRef references credentials for the object store.
	SecretRef corev1.LocalObjectReference `json:"secretRef"`
}

// ArchiveTrigger controls when archival is triggered.
type ArchiveTrigger struct {
	// Schedule is a cron expression for periodic archival scans (primary mechanism).
	Schedule string `json:"schedule"`

	// Inotify enables filesystem event-based archival as an optimization.
	// Falls back to periodic scan if inotify is not supported.
	// +kubebuilder:default=true
	Inotify bool `json:"inotify,omitempty"`
}

// ArchiveLifecycle controls retention policies.
type ArchiveLifecycle struct {
	// LocalRetention is how long to keep files locally after archival (e.g., "7d").
	// Files are only deleted locally after confirmed archived.
	// +optional
	LocalRetention string `json:"localRetention,omitempty"`

	// ArchiveRetention is how long to keep files in object storage (e.g., "365d").
	// +optional
	ArchiveRetention string `json:"archiveRetention,omitempty"`

	// Compress enables gzip compression for archived files.
	// +kubebuilder:default=true
	Compress bool `json:"compress,omitempty"`
}

// SharedVolumeRef references a pre-existing PVC for shared data.
type SharedVolumeRef struct {
	// Name is a descriptive name for this shared volume.
	Name string `json:"name"`

	// ClaimName is the PVC name to mount.
	ClaimName string `json:"claimName"`

	// MountPath inside the container.
	MountPath string `json:"mountPath"`

	// ReadOnly mounts the volume as read-only.
	// +kubebuilder:default=false
	ReadOnly bool `json:"readOnly,omitempty"`
}

// CacheSpec defines ephemeral cache storage.
type CacheSpec struct {
	// Enabled controls whether cache volume is created.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// Medium is the emptyDir medium ("" for disk, "Memory" for tmpfs).
	// +kubebuilder:default=Memory
	Medium corev1.StorageMedium `json:"medium,omitempty"`

	// Size limit for the cache.
	Size string `json:"size"`

	// MountPath inside the container.
	MountPath string `json:"mountPath"`
}

// ObservabilitySpec configures monitoring and logging.
type ObservabilitySpec struct {
	// Metrics enables Prometheus metrics endpoint.
	// +kubebuilder:default=true
	Metrics bool `json:"metrics,omitempty"`

	// Logs enables structured logging collection.
	// +kubebuilder:default=true
	Logs bool `json:"logs,omitempty"`

	// Tracing enables OpenTelemetry tracing.
	// +kubebuilder:default=false
	Tracing bool `json:"tracing,omitempty"`
}

// SecuritySpec configures network security policies.
type SecuritySpec struct {
	// NetworkPolicy controls per-instance NetworkPolicy creation.
	// +optional
	NetworkPolicy *NetworkPolicySpec `json:"networkPolicy,omitempty"`
}

// NetworkPolicySpec configures the auto-generated NetworkPolicy.
type NetworkPolicySpec struct {
	// Enabled controls whether a NetworkPolicy is created. Default: true.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// AllowedEgressCIDRs are additional CIDR blocks allowed for egress.
	// +optional
	AllowedEgressCIDRs []string `json:"allowedEgressCIDRs,omitempty"`

	// AllowedIngressNamespaces are namespaces allowed to access this Claw.
	// +optional
	AllowedIngressNamespaces []string `json:"allowedIngressNamespaces,omitempty"`
}

// IngressSpec configures external HTTP access.
type IngressSpec struct {
	// Enabled controls whether an Ingress is created.
	Enabled bool `json:"enabled"`

	// Host is the FQDN for the Ingress rule.
	Host string `json:"host"`

	// ClassName is the IngressClass name (e.g., "nginx").
	// +optional
	ClassName string `json:"className,omitempty"`

	// TLS configures TLS termination.
	// +optional
	TLS *IngressTLS `json:"tls,omitempty"`

	// BasicAuth configures optional HTTP Basic Authentication.
	// +optional
	BasicAuth *BasicAuthSpec `json:"basicAuth,omitempty"`

	// Annotations are additional annotations to add to the Ingress.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// IngressTLS configures TLS for an Ingress.
type IngressTLS struct {
	// SecretName is the TLS Secret name.
	SecretName string `json:"secretName"`
}

// BasicAuthSpec configures HTTP Basic Auth for Ingress.
type BasicAuthSpec struct {
	// Enabled controls whether basic auth is active.
	Enabled bool `json:"enabled"`

	// SecretName references a Secret containing htpasswd data.
	SecretName string `json:"secretName"`
}

// AvailabilitySpec configures availability settings.
type AvailabilitySpec struct {
	// PDB configures PodDisruptionBudget.
	// +optional
	PDB *PDBSpec `json:"pdb,omitempty"`
}

// PDBSpec configures a PodDisruptionBudget.
type PDBSpec struct {
	// Enabled controls whether a PDB is created. Default: true.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// MinAvailable is the minimum number of available pods. Default: 1.
	// +kubebuilder:default=1
	MinAvailable int `json:"minAvailable,omitempty"`
}

// BackpressureSpec configures per-channel flow control.
type BackpressureSpec struct {
	// BufferSize is the ring buffer capacity.
	// +kubebuilder:default=1024
	BufferSize int `json:"bufferSize,omitempty"`

	// HighWatermark triggers slow_down signal (0.0-1.0).
	// +kubebuilder:default="0.8"
	HighWatermark string `json:"highWatermark,omitempty"`

	// LowWatermark triggers resume signal (0.0-1.0).
	// +kubebuilder:default="0.3"
	LowWatermark string `json:"lowWatermark,omitempty"`
}

// AutoUpdateSpec configures automatic version updates.
type AutoUpdateSpec struct {
	// Enabled controls whether auto-update is active.
	Enabled bool `json:"enabled"`

	// VersionConstraint is a semver constraint (e.g., "~1.x", "^2.0.0").
	// +optional
	VersionConstraint string `json:"versionConstraint,omitempty"`

	// Schedule is a cron expression for version checks (default: "0 3 * * *").
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// HealthTimeout is how long to wait for Pod readiness after update (default: "10m", range: 2m-30m).
	// +optional
	HealthTimeout string `json:"healthTimeout,omitempty"`

	// MaxRollbacks is the circuit breaker threshold (default: 3).
	// +optional
	MaxRollbacks int `json:"maxRollbacks,omitempty"`
}

// AutoUpdateStatus reports auto-update state.
type AutoUpdateStatus struct {
	// CurrentVersion is the currently running image tag.
	// +optional
	CurrentVersion string `json:"currentVersion,omitempty"`

	// AvailableVersion is the latest version found in the registry.
	// +optional
	AvailableVersion string `json:"availableVersion,omitempty"`

	// LastCheck is when the registry was last queried.
	// +optional
	LastCheck *metav1.Time `json:"lastCheck,omitempty"`

	// LastUpdate is when the last update was applied.
	// +optional
	LastUpdate *metav1.Time `json:"lastUpdate,omitempty"`

	// RollbackCount is the number of consecutive rollbacks.
	// +optional
	RollbackCount int `json:"rollbackCount,omitempty"`

	// FailedVersions are versions that failed health checks and will be skipped.
	// +optional
	FailedVersions []string `json:"failedVersions,omitempty"`

	// CircuitOpen is true when rollbackCount >= maxRollbacks.
	// +optional
	CircuitOpen bool `json:"circuitOpen,omitempty"`

	// VersionHistory records past update attempts.
	// +optional
	VersionHistory []VersionHistoryEntry `json:"versionHistory,omitempty"`
}

// VersionHistoryStatus represents the outcome of a version update.
// +kubebuilder:validation:Enum=Healthy;RolledBack
type VersionHistoryStatus string

const (
	VersionHistoryHealthy    VersionHistoryStatus = "Healthy"
	VersionHistoryRolledBack VersionHistoryStatus = "RolledBack"
)

// VersionHistoryEntry records a single update attempt.
type VersionHistoryEntry struct {
	// Version is the image tag.
	Version string `json:"version"`

	// AppliedAt is when the update was applied.
	AppliedAt metav1.Time `json:"appliedAt"`

	// Status is the outcome of this update attempt.
	Status VersionHistoryStatus `json:"status"`
}
