package sdk

// RuntimeType mirrors the CRD runtime type for SDK consumers.
type RuntimeType string

const (
	OpenClaw RuntimeType = "openclaw"
	NanoClaw RuntimeType = "nanoclaw"
	ZeroClaw RuntimeType = "zeroclaw"
	Custom   RuntimeType = "custom"
)

// ClawSpec defines the desired state for creating a Claw via the SDK.
type ClawSpec struct {
	// Runtime is the agent runtime type.
	Runtime RuntimeType

	// Config holds runtime-specific configuration.
	Config *RuntimeConfig

	// Namespace to create the Claw in. Defaults to "default".
	Namespace string
}

// RuntimeConfig provides typed runtime configuration.
type RuntimeConfig struct {
	// Environment contains key-value pairs for the runtime.
	Environment map[string]string
}

// ClawInstance represents a running Claw agent.
type ClawInstance struct {
	// Name of the Claw resource.
	Name string

	// Namespace of the Claw resource.
	Namespace string

	// Phase is the current lifecycle phase.
	Phase string
}

// Result represents the output from a Claw agent.
type Result struct {
	// Content is the text response from the agent.
	Content string

	// Metadata contains additional response information.
	Metadata map[string]string
}
