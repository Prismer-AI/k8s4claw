package runtime

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// NanoClawAdapter implements RuntimeAdapter for the NanoClaw runtime.
type NanoClawAdapter struct{}

var _ RuntimeAdapter = (*NanoClawAdapter)(nil)

func (a *NanoClawAdapter) PodTemplate(claw *v1alpha1.Claw) *corev1.PodTemplateSpec {
	return BuildPodTemplate(claw, a.runtimeSpec(claw))
}

func (a *NanoClawAdapter) runtimeSpec(claw *v1alpha1.Claw) *RuntimeSpec {
	return &RuntimeSpec{
		Image:          "ghcr.io/prismer-ai/k8s4claw-nanoclaw:latest",
		Command:        []string{"/usr/bin/claw-entrypoint"},
		Ports:          []corev1.ContainerPort{{Name: "gateway", ContainerPort: 19000, Protocol: corev1.ProtocolTCP}},
		Resources:      resources("100m", "256Mi", "500m", "512Mi"),
		ConfigMode:     ConfigModeOverwrite,
		WorkspacePath:  "/workspace",
		Env:            []corev1.EnvVar{{Name: "NANOCLAW_MODE", Value: "container"}},
		LivenessProbe:  a.HealthProbe(claw),
		ReadinessProbe: a.ReadinessProbe(claw),
	}
}

func (a *NanoClawAdapter) HealthProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{
				Command: []string{"/bin/sh", "-c", "test -S /var/run/claw/bus.sock"},
			},
		},
		InitialDelaySeconds: 5,
		PeriodSeconds:       10,
	}
}

func (a *NanoClawAdapter) ReadinessProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{
				Command: []string{"/bin/sh", "-c", "test -S /var/run/claw/bus.sock"},
			},
		},
		InitialDelaySeconds: 3,
		PeriodSeconds:       5,
	}
}

func (a *NanoClawAdapter) DefaultConfig() *RuntimeConfig {
	return &RuntimeConfig{
		GatewayPort:   19000, // NanoClaw bridge adapter port
		WorkspacePath: "/workspace",
		Environment: map[string]string{
			"NANOCLAW_MODE": "container",
		},
	}
}

func (a *NanoClawAdapter) GracefulShutdownSeconds() int32 {
	return 15
}

func (a *NanoClawAdapter) Validate(_ context.Context, _ *v1alpha1.ClawSpec) field.ErrorList {
	return nil
}

func (a *NanoClawAdapter) ValidateUpdate(_ context.Context, _, _ *v1alpha1.ClawSpec) field.ErrorList {
	return nil
}
