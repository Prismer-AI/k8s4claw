package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClawSpec defines the desired state of a Claw agent instance.
type ClawSpec struct {
	// Runtime is the agent runtime type. Immutable after creation.
	// +kubebuilder:validation:Required
	Runtime RuntimeType `json:"runtime"`

	// Config holds runtime-specific configuration.
	// +optional
	Config *apiextensionsv1.JSON `json:"config,omitempty"`

	// Credentials defines how API keys and tokens are provided.
	// +optional
	Credentials *CredentialSpec `json:"credentials,omitempty"`

	// Channels references ClawChannel resources for external communication.
	// +optional
	Channels []ChannelRef `json:"channels,omitempty"`

	// Persistence configures storage volumes.
	// +optional
	Persistence *PersistenceSpec `json:"persistence,omitempty"`

	// Observability configures monitoring.
	// +optional
	Observability *ObservabilitySpec `json:"observability,omitempty"`

	// ServiceAccount overrides the default restricted ServiceAccount.
	// +optional
	ServiceAccount *ServiceAccountRef `json:"serviceAccount,omitempty"`
}

// ServiceAccountRef allows opting into a custom ServiceAccount for the Claw Pod.
type ServiceAccountRef struct {
	// Name of a user-managed ServiceAccount.
	// +optional
	Name string `json:"name,omitempty"`

	// Annotations to apply to the ServiceAccount (e.g., for IRSA or Workload Identity).
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ClawPhase represents the current lifecycle phase.
// +kubebuilder:validation:Enum=Pending;Provisioning;Running;Degraded;Updating;Failed;Terminating
type ClawPhase string

// ClawPhase constants.
const (
	ClawPhasePending      ClawPhase = "Pending"
	ClawPhaseProvisioning ClawPhase = "Provisioning"
	ClawPhaseRunning      ClawPhase = "Running"
	ClawPhaseDegraded     ClawPhase = "Degraded"
	ClawPhaseUpdating     ClawPhase = "Updating"
	ClawPhaseFailed       ClawPhase = "Failed"
	ClawPhaseTerminating  ClawPhase = "Terminating"
)

// ClawStatus defines the observed state of a Claw.
type ClawStatus struct {
	// Phase is the high-level lifecycle phase.
	Phase ClawPhase `json:"phase,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the Claw's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Channels reports per-channel status.
	// +optional
	Channels []ChannelStatus `json:"channels,omitempty"`

	// Persistence reports storage status.
	// +optional
	Persistence *PersistenceStatus `json:"persistence,omitempty"`
}

// ChannelStatus reports the status of a connected channel.
type ChannelStatus struct {
	// Name of the ClawChannel.
	Name string `json:"name"`

	// Status is the connection state.
	Status string `json:"status"`

	// Backpressure is the current pressure state.
	// +optional
	Backpressure string `json:"backpressure,omitempty"`

	// LastError is the most recent error message.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// RetryCount is the current retry attempt number.
	RetryCount int `json:"retryCount,omitempty"`

	// DeadLetterCount is the number of messages in the DLQ for this channel.
	DeadLetterCount int `json:"deadLetterCount,omitempty"`
}

// PersistenceStatus reports storage utilization.
type PersistenceStatus struct {
	// Session volume status.
	// +optional
	Session *VolumeStatus `json:"session,omitempty"`

	// Output volume status.
	// +optional
	Output *VolumeStatus `json:"output,omitempty"`

	// Workspace volume status.
	// +optional
	Workspace *VolumeStatus `json:"workspace,omitempty"`
}

// VolumeStatus reports the status of a single PVC.
type VolumeStatus struct {
	// PVCName is the PersistentVolumeClaim name.
	PVCName string `json:"pvcName,omitempty"`

	// UsagePercent is the current usage as a percentage.
	UsagePercent int `json:"usagePercent,omitempty"`

	// CapacityBytes is the total capacity.
	CapacityBytes int64 `json:"capacityBytes,omitempty"`

	// LastSnapshot is the timestamp of the last successful snapshot.
	// +optional
	LastSnapshot *metav1.Time `json:"lastSnapshot,omitempty"`

	// ArchivedFiles is the total number of archived files (output only).
	ArchivedFiles int `json:"archivedFiles,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Runtime",type=string,JSONPath=`.spec.runtime`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Claw is the Schema for the claws API.
type Claw struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClawSpec   `json:"spec,omitempty"`
	Status ClawStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClawList contains a list of Claw.
type ClawList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Claw `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Claw{}, &ClawList{})
}
