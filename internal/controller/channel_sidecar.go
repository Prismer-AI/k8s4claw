package controller

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)

// injectChannelSidecars iterates over the Claw's channel references, fetches
// each ClawChannel CR, validates mode compatibility, builds a sidecar container,
// and appends it to the pod template. Missing or incompatible channels are
// logged as warnings and returned in the skipped list (soft failure).
func (r *ClawReconciler) injectChannelSidecars(ctx context.Context, claw *clawv1alpha1.Claw, podTemplate *corev1.PodTemplateSpec) ([]string, error) {
	logger := log.FromContext(ctx)
	var skipped []string

	for _, ref := range claw.Spec.Channels {
		var channel clawv1alpha1.ClawChannel
		key := types.NamespacedName{Name: ref.Name, Namespace: claw.Namespace}
		if err := r.Get(ctx, key, &channel); err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info("ClawChannel not found, skipping", "channel", ref.Name)
				skipped = append(skipped, ref.Name)
				continue
			}
			return skipped, fmt.Errorf("failed to get ClawChannel %q: %w", ref.Name, err)
		}

		if !isModeCompatible(ref.Mode, channel.Spec.Mode) {
			logger.Info("channel mode incompatible, skipping",
				"channel", ref.Name,
				"requested", ref.Mode,
				"capability", channel.Spec.Mode,
			)
			skipped = append(skipped, ref.Name)
			continue
		}

		container := buildChannelContainer(&channel, ref, r.NativeSidecarsEnabled)

		if r.NativeSidecarsEnabled {
			podTemplate.Spec.InitContainers = append(podTemplate.Spec.InitContainers, container)
		} else {
			podTemplate.Spec.Containers = append(podTemplate.Spec.Containers, container)
		}
	}

	return skipped, nil
}

// buildChannelContainer creates a sidecar container for the given ClawChannel
// and ChannelRef. For built-in channel types the image is derived from the
// type name; for custom channels the SidecarSpec image is used.
func buildChannelContainer(channel *clawv1alpha1.ClawChannel, ref clawv1alpha1.ChannelRef, nativeSidecar bool) corev1.Container {
	var image string
	if channel.Spec.Type == clawv1alpha1.ChannelTypeCustom && channel.Spec.Sidecar != nil {
		image = channel.Spec.Sidecar.Image
	} else {
		image = builtinChannelImage(channel.Spec.Type)
	}

	// Base env vars.
	env := []corev1.EnvVar{
		{Name: "CHANNEL_NAME", Value: ref.Name},
		{Name: "CHANNEL_TYPE", Value: string(channel.Spec.Type)},
		{Name: "CHANNEL_MODE", Value: string(ref.Mode)},
	}

	// Config JSON env var.
	if channel.Spec.Config != nil {
		raw, err := json.Marshal(channel.Spec.Config)
		if err == nil {
			env = append(env, corev1.EnvVar{
				Name:  "CHANNEL_CONFIG",
				Value: string(raw),
			})
		}
	}

	// Custom sidecar extra env vars.
	if channel.Spec.Type == clawv1alpha1.ChannelTypeCustom && channel.Spec.Sidecar != nil {
		env = append(env, channel.Spec.Sidecar.Env...)
	}

	// Volume mounts: always include ipc-socket.
	mounts := []corev1.VolumeMount{
		{Name: "ipc-socket", MountPath: "/var/run/claw"},
	}

	// Resources: channel-level override or defaults.
	res := channelDefaultResources()
	if channel.Spec.Resources != nil {
		res = *channel.Spec.Resources
	}
	if channel.Spec.Type == clawv1alpha1.ChannelTypeCustom && channel.Spec.Sidecar != nil && channel.Spec.Sidecar.Resources != nil {
		res = *channel.Spec.Sidecar.Resources
	}

	container := corev1.Container{
		Name:            "channel-" + ref.Name,
		Image:           image,
		Env:             env,
		VolumeMounts:    mounts,
		Resources:       res,
		SecurityContext: clawruntime.ContainerSecurityContext(),
	}

	// Credential injection via envFrom.
	if channel.Spec.Credentials != nil && channel.Spec.Credentials.SecretRef != nil {
		container.EnvFrom = []corev1.EnvFromSource{
			{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: *channel.Spec.Credentials.SecretRef,
				},
			},
		}
	}

	// Native sidecar restart policy.
	if nativeSidecar {
		container.RestartPolicy = ptr.To(corev1.ContainerRestartPolicyAlways)
	}

	// Custom sidecar: apply ports and probes from SidecarSpec.
	if channel.Spec.Type == clawv1alpha1.ChannelTypeCustom && channel.Spec.Sidecar != nil {
		container.Ports = channel.Spec.Sidecar.Ports
		container.LivenessProbe = channel.Spec.Sidecar.LivenessProbe
		container.ReadinessProbe = channel.Spec.Sidecar.ReadinessProbe
	}

	return container
}

// channelDefaultResources returns the default resource requirements for a
// channel sidecar container.
func channelDefaultResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("50m"),
			corev1.ResourceMemory: resource.MustParse("32Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("200m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
	}
}

// isModeCompatible checks whether a channel's capability mode supports the
// requested mode. A bidirectional channel supports all modes; otherwise the
// capability must exactly match the request.
func isModeCompatible(requested, capability clawv1alpha1.ChannelMode) bool {
	if capability == clawv1alpha1.ChannelModeBidirectional {
		return true
	}
	return capability == requested
}

// builtinChannelImage returns the container image for a built-in channel type.
func builtinChannelImage(channelType clawv1alpha1.ChannelType) string {
	return fmt.Sprintf("ghcr.io/prismer-ai/claw-channel-%s:latest", string(channelType))
}
