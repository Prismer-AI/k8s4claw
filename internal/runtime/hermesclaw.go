package runtime

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

const (
	hermesGatewayPort      = 8642
	hermesHomePath         = "/opt/data"
	hermesSkillsPath       = "/opt/data/skills"
	hermesDefaultModelName = "hermes-agent"
)

// HermesClawAdapter implements RuntimeAdapter for the Hermes Agent runtime.
type HermesClawAdapter struct{}

var _ RuntimeAdapter = (*HermesClawAdapter)(nil)

func (a *HermesClawAdapter) PodTemplate(claw *v1alpha1.Claw) *corev1.PodTemplateSpec {
	return BuildPodTemplate(claw, a.runtimeSpec(claw))
}

func (a *HermesClawAdapter) runtimeSpec(_ *v1alpha1.Claw) *RuntimeSpec {
	return &RuntimeSpec{
		Image:     "ghcr.io/nousresearch/hermes-agent:latest",
		Args:      []string{"gateway", "run"},
		Ports:     []corev1.ContainerPort{{Name: "gateway", ContainerPort: hermesGatewayPort, Protocol: corev1.ProtocolTCP}},
		Resources: resources("500m", "1Gi", "2000m", "4Gi"),
		ExtraVolumeMounts: []corev1.VolumeMount{
			{
				Name:      "config-vol",
				MountPath: hermesHomePath + "/config.yaml",
				SubPath:   "config.json",
				ReadOnly:  true,
			},
		},
		Env: []corev1.EnvVar{
			{Name: "HERMES_HOME", Value: hermesHomePath},
			{Name: "HOME", Value: hermesHomePath + "/home"},
			{Name: "API_SERVER_ENABLED", Value: "true"},
			{Name: "API_SERVER_HOST", Value: "0.0.0.0"},
			{Name: "API_SERVER_PORT", Value: "8642"},
			{Name: "API_SERVER_MODEL_NAME", Value: hermesDefaultModelName},
		},
		LivenessProbe:   a.HealthProbe(nil),
		ReadinessProbe:  a.ReadinessProbe(nil),
		ConfigMode:      ConfigModeDeepMerge,
		WorkspacePath:   hermesSkillsPath,
		SecurityContext: hermesRuntimeSecurityContext(),
	}
}

func (a *HermesClawAdapter) HealthProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/health",
				Port: portIntStr(hermesGatewayPort),
			},
		},
		InitialDelaySeconds: 20,
		PeriodSeconds:       15,
		TimeoutSeconds:      5,
	}
}

func (a *HermesClawAdapter) ReadinessProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/health",
				Port: portIntStr(hermesGatewayPort),
			},
		},
		InitialDelaySeconds: 10,
		PeriodSeconds:       10,
		TimeoutSeconds:      5,
	}
}

func (a *HermesClawAdapter) DefaultConfig() *RuntimeConfig {
	return &RuntimeConfig{
		GatewayPort:   hermesGatewayPort,
		WorkspacePath: hermesSkillsPath,
		Environment: map[string]string{
			"HERMES_HOME":           hermesHomePath,
			"HOME":                  hermesHomePath + "/home",
			"API_SERVER_ENABLED":    "true",
			"API_SERVER_HOST":       "0.0.0.0",
			"API_SERVER_PORT":       "8642",
			"API_SERVER_MODEL_NAME": hermesDefaultModelName,
		},
	}
}

func (a *HermesClawAdapter) GracefulShutdownSeconds() int32 {
	return 60
}

func (a *HermesClawAdapter) Validate(_ context.Context, spec *v1alpha1.ClawSpec) field.ErrorList {
	var allErrs field.ErrorList

	if !hasCredentials(spec) {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "credentials"),
			"HermesClaw requires credentials (secretRef, externalSecret, or keys)",
		))
	}

	if len(spec.Channels) > 0 {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "channels"),
			"HermesClaw does not support k8s4claw channel sidecars yet; use Hermes native gateway integrations for now",
		))
	}

	if spec.Persistence != nil {
		if p := spec.Persistence.Session; p != nil && p.Enabled && p.MountPath != hermesHomePath {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("spec", "persistence", "session", "mountPath"),
				p.MountPath,
				"HermesClaw session storage must mount at /opt/data",
			))
		}
		if p := spec.Persistence.Workspace; p != nil && p.Enabled && p.MountPath != hermesSkillsPath {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("spec", "persistence", "workspace", "mountPath"),
				p.MountPath,
				"HermesClaw workspace storage must mount at /opt/data/skills",
			))
		}
	}

	return allErrs
}

func (a *HermesClawAdapter) ValidateUpdate(_ context.Context, oldSpec, newSpec *v1alpha1.ClawSpec) field.ErrorList {
	return validatePersistenceUpdate(oldSpec, newSpec)
}

func hermesRuntimeSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		RunAsUser:                ptr.To(int64(10000)),
		RunAsGroup:               ptr.To(int64(10000)),
		RunAsNonRoot:             ptr.To(true),
		ReadOnlyRootFilesystem:   ptr.To(false),
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}
