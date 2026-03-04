package runtime

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// ZeroClawAdapter implements RuntimeAdapter for the ZeroClaw runtime.
type ZeroClawAdapter struct{}

var _ RuntimeAdapter = (*ZeroClawAdapter)(nil)

func (a *ZeroClawAdapter) PodTemplate(claw *v1alpha1.Claw) *corev1.PodTemplateSpec {
	return BuildPodTemplate(claw, a.runtimeSpec(claw))
}

func (a *ZeroClawAdapter) runtimeSpec(claw *v1alpha1.Claw) *RuntimeSpec {
	return &RuntimeSpec{
		Image:          "ghcr.io/prismer-ai/k8s4claw-zeroclaw:latest",
		Command:        []string{"/usr/bin/claw-entrypoint"},
		Ports:          []corev1.ContainerPort{{Name: "gateway", ContainerPort: 3000, Protocol: corev1.ProtocolTCP}},
		Resources:      resources("50m", "32Mi", "200m", "128Mi"),
		ConfigMode:     ConfigModePassthrough,
		WorkspacePath:  "/workspace",
		Env:            []corev1.EnvVar{{Name: "ZEROCLAW_MODE", Value: "gateway"}},
		LivenessProbe:  a.HealthProbe(claw),
		ReadinessProbe: a.ReadinessProbe(claw),
	}
}

func (a *ZeroClawAdapter) HealthProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/health",
				Port: portIntStr(3000),
			},
		},
		InitialDelaySeconds: 3,
		PeriodSeconds:       10,
	}
}

func (a *ZeroClawAdapter) ReadinessProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/ready",
				Port: portIntStr(3000),
			},
		},
		InitialDelaySeconds: 1,
		PeriodSeconds:       5,
	}
}

func (a *ZeroClawAdapter) DefaultConfig() *RuntimeConfig {
	return &RuntimeConfig{
		GatewayPort:   3000,
		WorkspacePath: "/workspace",
		Environment: map[string]string{
			"ZEROCLAW_MODE": "gateway",
		},
	}
}

func (a *ZeroClawAdapter) GracefulShutdownSeconds() int32 {
	return 5
}

func (a *ZeroClawAdapter) Validate(_ context.Context, _ *v1alpha1.ClawSpec) field.ErrorList {
	return nil
}

func (a *ZeroClawAdapter) ValidateUpdate(_ context.Context, _, _ *v1alpha1.ClawSpec) field.ErrorList {
	return nil
}
