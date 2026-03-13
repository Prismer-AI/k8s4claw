package runtime

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// IronClawAdapter implements RuntimeAdapter for the IronClaw runtime.
type IronClawAdapter struct{}

var _ RuntimeAdapter = (*IronClawAdapter)(nil)

func (a *IronClawAdapter) PodTemplate(claw *v1alpha1.Claw) *corev1.PodTemplateSpec {
	return BuildPodTemplate(claw, a.runtimeSpec(claw))
}

func (a *IronClawAdapter) runtimeSpec(claw *v1alpha1.Claw) *RuntimeSpec {
	return &RuntimeSpec{
		Image:          "ghcr.io/prismer-ai/k8s4claw-ironclaw:latest",
		Command:        []string{"/usr/bin/claw-entrypoint"},
		Ports:          []corev1.ContainerPort{{Name: "gateway", ContainerPort: 3001, Protocol: corev1.ProtocolTCP}},
		Resources:      resources("500m", "1Gi", "2000m", "4Gi"),
		ConfigMode:     ConfigModeDeepMerge,
		WorkspacePath:  "/workspace",
		Env:            []corev1.EnvVar{{Name: "IRONCLAW_MODE", Value: "gateway"}},
		LivenessProbe:  a.HealthProbe(claw),
		ReadinessProbe: a.ReadinessProbe(claw),
	}
}

func (a *IronClawAdapter) HealthProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/health",
				Port: portIntStr(3001),
			},
		},
		InitialDelaySeconds: 15,
		PeriodSeconds:       15,
	}
}

func (a *IronClawAdapter) ReadinessProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/ready",
				Port: portIntStr(3001),
			},
		},
		InitialDelaySeconds: 10,
		PeriodSeconds:       10,
	}
}

func (a *IronClawAdapter) DefaultConfig() *RuntimeConfig {
	return &RuntimeConfig{
		GatewayPort:   3001,
		WorkspacePath: "/workspace",
		Environment: map[string]string{
			"IRONCLAW_MODE": "gateway",
		},
	}
}

func (a *IronClawAdapter) GracefulShutdownSeconds() int32 {
	return 30
}

func (a *IronClawAdapter) Validate(_ context.Context, _ *v1alpha1.ClawSpec) field.ErrorList {
	return nil
}

func (a *IronClawAdapter) ValidateUpdate(_ context.Context, _, _ *v1alpha1.ClawSpec) field.ErrorList {
	return nil
}
