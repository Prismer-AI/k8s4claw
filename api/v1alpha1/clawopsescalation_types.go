package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EscalationPhase represents the lifecycle phase of a ClawOpsEscalation.
// +kubebuilder:validation:Enum=Pending;AutoExecuted;Analyzing;Proposed;AwaitingApproval;Approved;Executed;Rejected;Failed
type EscalationPhase string

// EscalationPhase constants.
const (
	EscalationPhasePending          EscalationPhase = "Pending"
	EscalationPhaseAutoExecuted     EscalationPhase = "AutoExecuted"
	EscalationPhaseAnalyzing        EscalationPhase = "Analyzing"
	EscalationPhaseProposed         EscalationPhase = "Proposed"
	EscalationPhaseAwaitingApproval EscalationPhase = "AwaitingApproval"
	EscalationPhaseApproved         EscalationPhase = "Approved"
	EscalationPhaseExecuted         EscalationPhase = "Executed"
	EscalationPhaseRejected         EscalationPhase = "Rejected"
	EscalationPhaseFailed           EscalationPhase = "Failed"
)

// IsTerminalPhase returns true if the given phase is a terminal state.
func IsTerminalPhase(p EscalationPhase) bool {
	switch p {
	case EscalationPhaseAutoExecuted, EscalationPhaseExecuted,
		EscalationPhaseRejected, EscalationPhaseFailed:
		return true
	default:
		return false
	}
}

// TriggerInfo captures details about the Kubernetes event that initiated an escalation.
type TriggerInfo struct {
	// Type is the trigger event type.
	Type TriggerType `json:"type"`

	// Message is a human-readable description of the event.
	// +optional
	Message string `json:"message,omitempty"`

	// FirstSeen is when the triggering condition was first observed.
	// +optional
	FirstSeen *metav1.Time `json:"firstSeen,omitempty"`

	// Count is the number of times this event has occurred.
	// +optional
	Count int32 `json:"count,omitempty"`
}

// EventRecord captures a single Kubernetes event from the escalation context.
type EventRecord struct {
	// Type is the event type (Normal, Warning).
	Type string `json:"type"`

	// Reason is the short machine-readable reason string.
	Reason string `json:"reason"`

	// Message is the human-readable event message.
	// +optional
	Message string `json:"message,omitempty"`

	// Timestamp is when the event occurred.
	Timestamp metav1.Time `json:"timestamp"`
}

// ClawOpsEscalationSpec defines the desired state of a ClawOpsEscalation.
type ClawOpsEscalationSpec struct {
	// ClawRef references the target Claw instance (same namespace).
	// +kubebuilder:validation:Required
	ClawRef corev1.LocalObjectReference `json:"clawRef"`

	// Trigger describes the Kubernetes event that initiated this escalation.
	Trigger TriggerInfo `json:"trigger"`

	// EventSnapshot is a snapshot of recent Kubernetes events for the target Claw.
	// +optional
	EventSnapshot []EventRecord `json:"eventSnapshot,omitempty"`

	// MetricSnapshot holds a freeform snapshot of relevant metrics at trigger time.
	// +optional
	MetricSnapshot *apiextensionsv1.JSON `json:"metricSnapshot,omitempty"`

	// Severity is the urgency level of this escalation.
	// +kubebuilder:validation:Required
	Severity Severity `json:"severity"`

	// TTLSecondsAfterFinished is how long to keep the resource after reaching a terminal phase.
	// +optional
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`
}

// ClawOpsEscalationStatus defines the observed state of a ClawOpsEscalation.
type ClawOpsEscalationStatus struct {
	// Phase is the current lifecycle phase.
	Phase EscalationPhase `json:"phase,omitempty"`

	// MatchedRule is the name of the escalation rule that matched this trigger.
	// +optional
	MatchedRule string `json:"matchedRule,omitempty"`

	// Analysis is the AI-generated analysis of the incident.
	// +optional
	Analysis string `json:"analysis,omitempty"`

	// ProposedAction describes the remediation action proposed by the operator.
	// +optional
	ProposedAction string `json:"proposedAction,omitempty"`

	// ExecutedAction describes the remediation action that was actually executed.
	// +optional
	ExecutedAction string `json:"executedAction,omitempty"`

	// ExecutedAt is the timestamp when the action was executed.
	// +optional
	ExecutedAt *metav1.Time `json:"executedAt,omitempty"`

	// Result is the outcome of the executed action.
	// +optional
	Result string `json:"result,omitempty"`

	// SignetReceipt is the cryptographic receipt for audit trail.
	// +optional
	SignetReceipt string `json:"signetReceipt,omitempty"`

	// ApprovedBy is the identity that approved the escalation (user or policy).
	// +optional
	ApprovedBy string `json:"approvedBy,omitempty"`

	// ApprovedAt is the timestamp when the escalation was approved.
	// +optional
	ApprovedAt *metav1.Time `json:"approvedAt,omitempty"`

	// RejectionReason explains why the escalation was rejected.
	// +optional
	RejectionReason string `json:"rejectionReason,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Claw",type=string,JSONPath=`.spec.clawRef.name`
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=`.spec.trigger.type`
// +kubebuilder:printcolumn:name="Severity",type=string,JSONPath=`.spec.severity`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ClawOpsEscalation is the Schema for the clawopsescalations API.
type ClawOpsEscalation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClawOpsEscalationSpec   `json:"spec,omitempty"`
	Status ClawOpsEscalationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClawOpsEscalationList contains a list of ClawOpsEscalation.
type ClawOpsEscalationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClawOpsEscalation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClawOpsEscalation{}, &ClawOpsEscalationList{})
}
