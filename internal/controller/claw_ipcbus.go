package controller

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)

// IPCBusImage is the default claw-ipcbus sidecar image.
var IPCBusImage = "ghcr.io/prismer-ai/claw-ipcbus:latest"

// shouldInjectIPCBus returns true when the IPC Bus sidecar should be injected.
func shouldInjectIPCBus(claw *clawv1alpha1.Claw) bool {
	return len(claw.Spec.Channels) > 0
}

// ipcBusSidecarName returns the name used for the IPC Bus sidecar container.
func ipcBusSidecarName() string { return "ipc-bus" }

// injectIPCBusSidecar adds the IPC Bus sidecar container to the pod template.
func injectIPCBusSidecar(claw *clawv1alpha1.Claw, podTemplate *corev1.PodTemplateSpec, runtimeType string, gatewayPort int) {
	env := []corev1.EnvVar{
		{Name: "CLAW_NAME", Value: claw.Name},
		{Name: "CLAW_NAMESPACE", Value: claw.Namespace},
		{Name: "CLAW_RUNTIME", Value: runtimeType},
		{Name: "CLAW_GATEWAY_PORT", Value: fmt.Sprintf("%d", gatewayPort)},
		{Name: "IPC_SOCKET_PATH", Value: "/var/run/claw/bus.sock"},
		{Name: "WAL_DIR", Value: "/var/run/claw/wal"},
	}

	// NanoClaw uses a UDS socket path instead of a TCP gateway port.
	if runtimeType == "nanoclaw" {
		env = append(env, corev1.EnvVar{
			Name:  "CLAW_RUNTIME_SOCKET",
			Value: "/var/run/claw/runtime.sock",
		})
	}

	sidecar := corev1.Container{
		Name:  ipcBusSidecarName(),
		Image: IPCBusImage,
		Env:   env,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "ipc-socket", MountPath: "/var/run/claw"},
			{Name: "wal-data", MountPath: "/var/run/claw/wal"},
		},
		SecurityContext: clawruntime.ContainerSecurityContext(),
		Lifecycle: &corev1.Lifecycle{
			PreStop: &corev1.LifecycleHandler{
				Exec: &corev1.ExecAction{
					Command: []string{"/claw-ipcbus", "shutdown"},
				},
			},
		},
	}

	// Inject as native sidecar (init container with restartPolicy Always).
	restartAlways := corev1.ContainerRestartPolicyAlways
	sidecar.RestartPolicy = &restartAlways

	// Insert after claw-init (index 0) in InitContainers.
	if len(podTemplate.Spec.InitContainers) > 0 {
		// Insert at index 1 (after the init container at index 0).
		updated := make([]corev1.Container, 0, len(podTemplate.Spec.InitContainers)+1)
		updated = append(updated, podTemplate.Spec.InitContainers[0])
		updated = append(updated, sidecar)
		updated = append(updated, podTemplate.Spec.InitContainers[1:]...)
		podTemplate.Spec.InitContainers = updated
	} else {
		podTemplate.Spec.InitContainers = append(podTemplate.Spec.InitContainers, sidecar)
	}
}

// injectIPCBusIfNeeded checks and injects the IPC Bus sidecar.
func injectIPCBusIfNeeded(claw *clawv1alpha1.Claw, podTemplate *corev1.PodTemplateSpec, runtimeType string, gatewayPort int) {
	if !shouldInjectIPCBus(claw) {
		return
	}

	// Check if already injected (idempotent).
	for i := range podTemplate.Spec.InitContainers {
		if podTemplate.Spec.InitContainers[i].Name == ipcBusSidecarName() {
			return
		}
	}

	injectIPCBusSidecar(claw, podTemplate, runtimeType, gatewayPort)
}
