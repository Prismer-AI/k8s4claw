package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ChannelType defines the type of communication channel.
// +kubebuilder:validation:Enum=slack;telegram;whatsapp;discord;matrix;webhook;custom
type ChannelType string

// ChannelType constants.
const (
	ChannelTypeSlack    ChannelType = "slack"
	ChannelTypeTelegram ChannelType = "telegram"
	ChannelTypeWhatsApp ChannelType = "whatsapp"
	ChannelTypeDiscord  ChannelType = "discord"
	ChannelTypeMatrix   ChannelType = "matrix"
	ChannelTypeWebhook  ChannelType = "webhook"
	ChannelTypeCustom   ChannelType = "custom"
)

// ClawChannelSpec defines the desired state of a ClawChannel.
type ClawChannelSpec struct {
	// Type is the channel type.
	// +kubebuilder:validation:Required
	Type ChannelType `json:"type"`

	// Mode defines the communication direction.
	// +kubebuilder:default=bidirectional
	Mode ChannelMode `json:"mode,omitempty"`

	// Credentials for the channel service.
	// +optional
	Credentials *CredentialSpec `json:"credentials,omitempty"`

	// Config holds channel-specific configuration.
	// +optional
	Config *apiextensionsv1.JSON `json:"config,omitempty"`

	// Backpressure tuning for this channel.
	// +optional
	Backpressure *BackpressureSpec `json:"backpressure,omitempty"`

	// Resources for built-in sidecar (ignored for type: custom).
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// Sidecar defines a custom channel adapter container (for type: custom).
	// +optional
	Sidecar *SidecarSpec `json:"sidecar,omitempty"`
}

// SidecarSpec defines a custom channel sidecar container.
type SidecarSpec struct {
	// Image is the container image.
	Image string `json:"image"`

	// Resources for the sidecar container.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// Ports exposed by the sidecar.
	// +optional
	Ports []corev1.ContainerPort `json:"ports,omitempty"`

	// Env is additional environment variables.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// LivenessProbe for the sidecar.
	// +optional
	LivenessProbe *corev1.Probe `json:"livenessProbe,omitempty"`

	// ReadinessProbe for the sidecar.
	// +optional
	ReadinessProbe *corev1.Probe `json:"readinessProbe,omitempty"`
}

// ClawChannelStatus defines the observed state of a ClawChannel.
type ClawChannelStatus struct {
	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ReferenceCount is the number of Claw resources referencing this channel.
	ReferenceCount int `json:"referenceCount,omitempty"`

	// ReferencingClaws lists the names of Claw resources referencing this channel.
	// +optional
	ReferencingClaws []string `json:"referencingClaws,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ClawChannel is the Schema for the clawchannels API.
type ClawChannel struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClawChannelSpec   `json:"spec,omitempty"`
	Status ClawChannelStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClawChannelList contains a list of ClawChannel.
type ClawChannelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClawChannel `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClawChannel{}, &ClawChannelList{})
}
