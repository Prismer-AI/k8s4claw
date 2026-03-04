package runtime

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// OpenClawAdapter implements RuntimeAdapter for the OpenClaw runtime.
type OpenClawAdapter struct{}

var _ RuntimeAdapter = (*OpenClawAdapter)(nil)

func (a *OpenClawAdapter) PodTemplate(claw *v1alpha1.Claw) *corev1.PodTemplateSpec {
	return BuildPodTemplate(claw, a.runtimeSpec(claw))
}

func (a *OpenClawAdapter) runtimeSpec(claw *v1alpha1.Claw) *RuntimeSpec {
	return &RuntimeSpec{
		Image:          "ghcr.io/prismer-ai/k8s4claw-openclaw:latest",
		Command:        []string{"/usr/bin/claw-entrypoint"},
		Ports:          []corev1.ContainerPort{{Name: "gateway", ContainerPort: 18900, Protocol: corev1.ProtocolTCP}},
		Resources:      resources("500m", "1Gi", "2000m", "4Gi"),
		ConfigMode:     ConfigModeDeepMerge,
		WorkspacePath:  "/workspace",
		Env:            []corev1.EnvVar{{Name: "OPENCLAW_MODE", Value: "gateway"}},
		LivenessProbe:  a.HealthProbe(claw),
		ReadinessProbe: a.ReadinessProbe(claw),
	}
}

func (a *OpenClawAdapter) HealthProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/health",
				Port: portIntStr(18900),
			},
		},
		InitialDelaySeconds: 10,
		PeriodSeconds:       15,
	}
}

func (a *OpenClawAdapter) ReadinessProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/ready",
				Port: portIntStr(18900),
			},
		},
		InitialDelaySeconds: 5,
		PeriodSeconds:       10,
	}
}

func (a *OpenClawAdapter) DefaultConfig() *RuntimeConfig {
	return &RuntimeConfig{
		GatewayPort:   18900,
		WorkspacePath: "/workspace",
		Environment: map[string]string{
			"OPENCLAW_MODE": "gateway",
		},
	}
}

func (a *OpenClawAdapter) GracefulShutdownSeconds() int32 {
	return 30
}

func (a *OpenClawAdapter) Validate(_ context.Context, _ *v1alpha1.ClawSpec) field.ErrorList {
	// TODO: validate OpenClaw-specific fields
	return nil
}

func (a *OpenClawAdapter) ValidateUpdate(_ context.Context, _, _ *v1alpha1.ClawSpec) field.ErrorList {
	// TODO: validate OpenClaw-specific update constraints
	return nil
}
