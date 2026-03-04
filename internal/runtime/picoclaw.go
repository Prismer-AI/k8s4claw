package runtime

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// PicoClawAdapter implements RuntimeAdapter for the PicoClaw runtime.
// PicoClaw is the ultra-lightweight serverless/edge runtime with minimal footprint.
type PicoClawAdapter struct{}

var _ RuntimeAdapter = (*PicoClawAdapter)(nil)

func (a *PicoClawAdapter) PodTemplate(claw *v1alpha1.Claw) *corev1.PodTemplateSpec {
	return BuildPodTemplate(claw, a.runtimeSpec(claw))
}

func (a *PicoClawAdapter) runtimeSpec(claw *v1alpha1.Claw) *RuntimeSpec {
	return &RuntimeSpec{
		Image:          "ghcr.io/prismer-ai/k8s4claw-picoclaw:latest",
		Command:        []string{"/usr/bin/claw-entrypoint"},
		Ports:          []corev1.ContainerPort{{Name: "gateway", ContainerPort: 8080, Protocol: corev1.ProtocolTCP}},
		Resources:      resources("25m", "16Mi", "100m", "64Mi"),
		ConfigMode:     ConfigModePassthrough,
		WorkspacePath:  "/workspace",
		Env:            []corev1.EnvVar{{Name: "PICOCLAW_MODE", Value: "serverless"}},
		LivenessProbe:  a.HealthProbe(claw),
		ReadinessProbe: a.ReadinessProbe(claw),
	}
}

func (a *PicoClawAdapter) HealthProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{
				Port: portIntStr(8080),
			},
		},
		InitialDelaySeconds: 1,
		PeriodSeconds:       5,
	}
}

func (a *PicoClawAdapter) ReadinessProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{
				Port: portIntStr(8080),
			},
		},
		InitialDelaySeconds: 1,
		PeriodSeconds:       3,
	}
}

func (a *PicoClawAdapter) DefaultConfig() *RuntimeConfig {
	return &RuntimeConfig{
		GatewayPort:   8080,
		WorkspacePath: "/workspace",
		Environment: map[string]string{
			"PICOCLAW_MODE": "serverless",
		},
	}
}

func (a *PicoClawAdapter) GracefulShutdownSeconds() int32 {
	return 2
}

func (a *PicoClawAdapter) Validate(_ context.Context, _ *v1alpha1.ClawSpec) field.ErrorList {
	return nil
}

func (a *PicoClawAdapter) ValidateUpdate(_ context.Context, _, _ *v1alpha1.ClawSpec) field.ErrorList {
	return nil
}
