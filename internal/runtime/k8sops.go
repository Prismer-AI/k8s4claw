package runtime

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// K8sOpsAdapter implements RuntimeAdapter for the k8sops runtime (Companion Claw).
type K8sOpsAdapter struct{}

var _ RuntimeAdapter = (*K8sOpsAdapter)(nil)

func (a *K8sOpsAdapter) PodTemplate(claw *v1alpha1.Claw) *corev1.PodTemplateSpec {
	return BuildPodTemplate(claw, a.runtimeSpec(claw))
}

func (a *K8sOpsAdapter) runtimeSpec(claw *v1alpha1.Claw) *RuntimeSpec {
	return &RuntimeSpec{
		Image:          "ghcr.io/prismer-ai/claw4k8s:latest",
		Ports:          []corev1.ContainerPort{{Name: "gateway", ContainerPort: 18910, Protocol: corev1.ProtocolTCP}},
		Resources:      resources("100m", "256Mi", "500m", "512Mi"),
		ConfigMode:     ConfigModeDeepMerge,
		WorkspacePath:  "/workspace",
		LivenessProbe:  a.HealthProbe(claw),
		ReadinessProbe: a.ReadinessProbe(claw),
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot:             ptr.To(true),
			ReadOnlyRootFilesystem:   ptr.To(true),
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		},
	}
}

func (a *K8sOpsAdapter) HealthProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/healthz",
				Port: portIntStr(18910),
			},
		},
		InitialDelaySeconds: 10,
		PeriodSeconds:       15,
	}
}

func (a *K8sOpsAdapter) ReadinessProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/readyz",
				Port: portIntStr(18910),
			},
		},
		InitialDelaySeconds: 5,
		PeriodSeconds:       10,
	}
}

func (a *K8sOpsAdapter) DefaultConfig() *RuntimeConfig {
	return &RuntimeConfig{
		GatewayPort:   18910,
		WorkspacePath: "/workspace",
		Environment: map[string]string{
			"CLAW4K8S_ROLE":      "companion",
			"CLAW4K8S_WATCH_NS":  "",
			"CLAW4K8S_LLM_MODEL": "claude-sonnet-4-6",
		},
	}
}

func (a *K8sOpsAdapter) GracefulShutdownSeconds() int32 { return 30 }

func (a *K8sOpsAdapter) Validate(_ context.Context, spec *v1alpha1.ClawSpec) field.ErrorList {
	var errs field.ErrorList
	if spec.Security == nil || spec.Security.NetworkPolicy == nil || !spec.Security.NetworkPolicy.Enabled {
		errs = append(errs, field.Required(
			field.NewPath("spec", "security", "networkPolicy", "enabled"),
			"NetworkPolicy is mandatory for k8sops runtime",
		))
	}
	return errs
}

func (a *K8sOpsAdapter) ValidateUpdate(_ context.Context, _, newSpec *v1alpha1.ClawSpec) field.ErrorList {
	return a.Validate(context.Background(), newSpec)
}
