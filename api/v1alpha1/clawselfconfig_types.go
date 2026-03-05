package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClawSelfConfigPhase represents the lifecycle phase of a ClawSelfConfig.
type ClawSelfConfigPhase string

const (
	SelfConfigPhasePending ClawSelfConfigPhase = "Pending"
	SelfConfigPhaseApplied ClawSelfConfigPhase = "Applied"
	SelfConfigPhaseFailed  ClawSelfConfigPhase = "Failed"
	SelfConfigPhaseDenied  ClawSelfConfigPhase = "Denied"
)

// ClawSelfConfigSpec defines the desired self-configuration changes.
type ClawSelfConfigSpec struct {
	// ClawRef is the name of the target Claw instance (required, same namespace).
	ClawRef string `json:"clawRef"`

	// AddSkills lists skills to install (max 10).
	// +optional
	AddSkills []string `json:"addSkills,omitempty"`

	// RemoveSkills lists skills to uninstall (max 10).
	// +optional
	RemoveSkills []string `json:"removeSkills,omitempty"`

	// ConfigPatch is a partial config merge applied to the Claw runtime config.
	// +optional
	ConfigPatch map[string]string `json:"configPatch,omitempty"`

	// AddWorkspaceFiles maps file names to content to create in the workspace.
	// +optional
	AddWorkspaceFiles map[string]string `json:"addWorkspaceFiles,omitempty"`

	// RemoveWorkspaceFiles lists workspace files to delete.
	// +optional
	RemoveWorkspaceFiles []string `json:"removeWorkspaceFiles,omitempty"`

	// AddEnvVars lists environment variables to add.
	// +optional
	AddEnvVars []EnvVar `json:"addEnvVars,omitempty"`

	// RemoveEnvVars lists environment variable names to remove.
	// +optional
	RemoveEnvVars []string `json:"removeEnvVars,omitempty"`
}

// EnvVar represents an environment variable.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ClawSelfConfigStatus defines the observed state of ClawSelfConfig.
type ClawSelfConfigStatus struct {
	// Phase is the current phase.
	Phase ClawSelfConfigPhase `json:"phase,omitempty"`

	// Message provides human-readable detail.
	// +optional
	Message string `json:"message,omitempty"`

	// AppliedAt is the timestamp when the config was applied.
	// +optional
	AppliedAt *metav1.Time `json:"appliedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Claw",type=string,JSONPath=`.spec.clawRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// ClawSelfConfig allows AI agents to modify their own Claw configuration.
type ClawSelfConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClawSelfConfigSpec   `json:"spec,omitempty"`
	Status ClawSelfConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClawSelfConfigList contains a list of ClawSelfConfig.
type ClawSelfConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClawSelfConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClawSelfConfig{}, &ClawSelfConfigList{})
}
