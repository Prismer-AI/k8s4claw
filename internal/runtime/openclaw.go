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
	// TODO: implement OpenClaw pod template with IPC Bus co-process
	return &corev1.PodTemplateSpec{}
}

func (a *OpenClawAdapter) HealthProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/health",
				Port: intstr18900(),
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
				Port: intstr18900(),
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
