package runtime

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// RuntimeConfig provides typed default configuration for a runtime.
type RuntimeConfig struct {
	// GatewayPort is the port the runtime gateway listens on.
	GatewayPort int `json:"gatewayPort"`

	// WorkspacePath is the default workspace directory inside the container.
	WorkspacePath string `json:"workspacePath"`

	// Environment contains default environment variables.
	Environment map[string]string `json:"environment"`
}

// RuntimeBuilder constructs K8s resources for a specific runtime.
type RuntimeBuilder interface {
	// PodTemplate returns the Pod template for this runtime.
	PodTemplate(claw *v1alpha1.Claw) *corev1.PodTemplateSpec

	// HealthProbe returns the liveness probe configuration.
	HealthProbe(claw *v1alpha1.Claw) *corev1.Probe

	// ReadinessProbe returns the readiness probe configuration.
	ReadinessProbe(claw *v1alpha1.Claw) *corev1.Probe

	// DefaultConfig returns typed default configuration.
	DefaultConfig() *RuntimeConfig

	// GracefulShutdownSeconds returns the recommended termination grace period.
	GracefulShutdownSeconds() int32
}

// RuntimeValidator validates CRD specs for a specific runtime.
type RuntimeValidator interface {
	// Validate checks the spec for a CREATE operation.
	Validate(ctx context.Context, spec *v1alpha1.ClawSpec) field.ErrorList

	// ValidateUpdate checks the spec for an UPDATE operation.
	ValidateUpdate(ctx context.Context, oldSpec, newSpec *v1alpha1.ClawSpec) field.ErrorList
}

// RuntimeAdapter combines builder and validator for a complete runtime implementation.
type RuntimeAdapter interface {
	RuntimeBuilder
	RuntimeValidator
}

// Registry maps runtime types to their adapters.
// It is NOT safe for concurrent use. All Register calls must complete
// before any concurrent Get calls.
type Registry struct {
	adapters map[v1alpha1.RuntimeType]RuntimeAdapter
}

// NewRegistry creates a new runtime adapter registry.
func NewRegistry() *Registry {
	return &Registry{
		adapters: make(map[v1alpha1.RuntimeType]RuntimeAdapter),
	}
}

// Register adds a runtime adapter to the registry.
func (r *Registry) Register(rt v1alpha1.RuntimeType, adapter RuntimeAdapter) {
	r.adapters[rt] = adapter
}

// Get returns the adapter for the given runtime type.
func (r *Registry) Get(rt v1alpha1.RuntimeType) (RuntimeAdapter, bool) {
	adapter, ok := r.adapters[rt]
	return adapter, ok
}
