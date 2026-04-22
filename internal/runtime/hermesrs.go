package runtime

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

const (
	hermesRSGatewayPort = 8080
	hermesRSHomePath    = "/data"
	hermesRSSkillsPath  = "/data/skills"
)

// HermesRSAdapter implements RuntimeAdapter for the hermes-agent-rs runtime.
// This is the Rust implementation of Hermes, featuring managed agents,
// Signet audit trail, and OpenAI-compatible API gateway.
type HermesRSAdapter struct{}

var _ RuntimeAdapter = (*HermesRSAdapter)(nil)

func (a *HermesRSAdapter) PodTemplate(claw *v1alpha1.Claw) *corev1.PodTemplateSpec {
	return BuildPodTemplate(claw, a.runtimeSpec(claw))
}

func (a *HermesRSAdapter) runtimeSpec(_ *v1alpha1.Claw) *RuntimeSpec {
	return &RuntimeSpec{
		Image:   "ghcr.io/prismer-ai/hermes-agent-rs:latest",
		Command: []string{"/usr/local/bin/hermes"},
		Args:    []string{"gateway", "run"},
		Ports:   []corev1.ContainerPort{{Name: "gateway", ContainerPort: hermesRSGatewayPort, Protocol: corev1.ProtocolTCP}},
		Resources: resources("250m", "512Mi", "2000m", "4Gi"),
		Env: []corev1.EnvVar{
			{Name: "HERMES_HOME", Value: hermesRSHomePath},
			{Name: "HERMES_GATEWAY_API_SERVER_BIND_ADDR", Value: "0.0.0.0:8080"},
			{Name: "HERMES_GATEWAY_API_SERVER_MODEL_NAME", Value: "hermes-agent-rs"},
			{Name: "RUST_LOG", Value: "info"},
		},
		LivenessProbe:   a.HealthProbe(nil),
		ReadinessProbe:  a.ReadinessProbe(nil),
		ConfigMode:      ConfigModeDeepMerge,
		WorkspacePath:   hermesRSSkillsPath,
		SecurityContext: hermesRSSecurityContext(),
	}
}

func (a *HermesRSAdapter) HealthProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/health",
				Port: portIntStr(hermesRSGatewayPort),
			},
		},
		InitialDelaySeconds: 5,
		PeriodSeconds:       10,
		TimeoutSeconds:      3,
	}
}

func (a *HermesRSAdapter) ReadinessProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/health",
				Port: portIntStr(hermesRSGatewayPort),
			},
		},
		InitialDelaySeconds: 3,
		PeriodSeconds:       5,
		TimeoutSeconds:      3,
	}
}

func (a *HermesRSAdapter) DefaultConfig() *RuntimeConfig {
	return &RuntimeConfig{
		GatewayPort:   hermesRSGatewayPort,
		WorkspacePath: hermesRSSkillsPath,
		Environment: map[string]string{
			"HERMES_HOME":                            hermesRSHomePath,
			"HERMES_GATEWAY_API_SERVER_BIND_ADDR":    "0.0.0.0:8080",
			"HERMES_GATEWAY_API_SERVER_MODEL_NAME":   "hermes-agent-rs",
			"RUST_LOG":                               "info",
		},
	}
}

func (a *HermesRSAdapter) GracefulShutdownSeconds() int32 {
	return 30
}

func (a *HermesRSAdapter) Validate(_ context.Context, spec *v1alpha1.ClawSpec) field.ErrorList {
	var allErrs field.ErrorList

	if !hasCredentials(spec) {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "credentials"),
			"HermesRS requires credentials (secretRef, externalSecret, or keys) for LLM API access",
		))
	}

	if spec.Persistence != nil {
		if p := spec.Persistence.Session; p != nil && p.Enabled && p.MountPath != hermesRSHomePath {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("spec", "persistence", "session", "mountPath"),
				p.MountPath,
				"HermesRS session storage must mount at /data",
			))
		}
		if p := spec.Persistence.Workspace; p != nil && p.Enabled && p.MountPath != hermesRSSkillsPath {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("spec", "persistence", "workspace", "mountPath"),
				p.MountPath,
				"HermesRS workspace storage must mount at /data/skills",
			))
		}
	}

	return allErrs
}

func (a *HermesRSAdapter) ValidateUpdate(_ context.Context, oldSpec, newSpec *v1alpha1.ClawSpec) field.ErrorList {
	return validatePersistenceUpdate(oldSpec, newSpec)
}

func hermesRSSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		RunAsUser:                ptr.To(int64(10000)),
		RunAsGroup:               ptr.To(int64(10000)),
		RunAsNonRoot:             ptr.To(true),
		ReadOnlyRootFilesystem:   ptr.To(false), // hermes-agent-rs writes to HERMES_HOME
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}
