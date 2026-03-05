package controller

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)

// ArchiverImage is the default claw-archiver sidecar image.
var ArchiverImage = "ghcr.io/prismer-ai/claw-archiver:latest"

// injectArchiverSidecar adds an archive-sidecar container to the pod template
// when spec.persistence.output.archive.enabled is true.
func injectArchiverSidecar(claw *clawv1alpha1.Claw, podTemplate *corev1.PodTemplateSpec) {
	archive := claw.Spec.Persistence.Output.Archive

	env := []corev1.EnvVar{
		{Name: "ARCHIVE_TYPE", Value: archive.Destination.Type},
		{Name: "ARCHIVE_BUCKET", Value: archive.Destination.Bucket},
		{Name: "ARCHIVE_PREFIX", Value: archive.Destination.Prefix},
		{Name: "ARCHIVE_SCHEDULE", Value: archive.Trigger.Schedule},
	}

	// Credentials from secret.
	env = append(env, corev1.EnvVar{
		Name: "AWS_ACCESS_KEY_ID",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: archive.Destination.SecretRef,
				Key:                  "access-key-id",
				Optional:             ptr.To(true),
			},
		},
	})
	env = append(env, corev1.EnvVar{
		Name: "AWS_SECRET_ACCESS_KEY",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: archive.Destination.SecretRef,
				Key:                  "secret-access-key",
				Optional:             ptr.To(true),
			},
		},
	})

	args := []string{
		"--schedule", archive.Trigger.Schedule,
	}
	if archive.Trigger.Inotify {
		args = append(args, "--inotify")
	}
	if archive.Lifecycle != nil {
		if archive.Lifecycle.LocalRetention != "" {
			args = append(args, "--local-retention", archive.Lifecycle.LocalRetention)
		}
		if archive.Lifecycle.Compress {
			args = append(args, "--compress")
		}
	}

	sidecar := corev1.Container{
		Name:    "archive-sidecar",
		Image:   ArchiverImage,
		Args:    args,
		Env:     env,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "output",
				MountPath: claw.Spec.Persistence.Output.MountPath,
			},
		},
		SecurityContext: clawruntime.ContainerSecurityContext(),
	}

	// Inject as native sidecar (init container with restartPolicy Always).
	restartAlways := corev1.ContainerRestartPolicyAlways
	sidecar.RestartPolicy = &restartAlways
	podTemplate.Spec.InitContainers = append(podTemplate.Spec.InitContainers, sidecar)
}

// shouldInjectArchiver returns true when the archiver sidecar should be injected.
func shouldInjectArchiver(claw *clawv1alpha1.Claw) bool {
	return claw.Spec.Persistence != nil &&
		claw.Spec.Persistence.Output != nil &&
		claw.Spec.Persistence.Output.Archive != nil &&
		claw.Spec.Persistence.Output.Archive.Enabled
}

// archiverSidecarName returns the name used for the archiver sidecar container.
func archiverSidecarName() string {
	return "archive-sidecar"
}

// injectArchiverIfNeeded checks and injects the archiver sidecar.
func injectArchiverIfNeeded(claw *clawv1alpha1.Claw, podTemplate *corev1.PodTemplateSpec) {
	if !shouldInjectArchiver(claw) {
		return
	}

	// Check if already injected.
	for _, c := range podTemplate.Spec.InitContainers {
		if c.Name == archiverSidecarName() {
			return
		}
	}

	injectArchiverSidecar(claw, podTemplate)
}

// formatMountPath returns the mount path for the output volume, used for reference.
func formatMountPath(claw *clawv1alpha1.Claw) string {
	if claw.Spec.Persistence != nil && claw.Spec.Persistence.Output != nil {
		return claw.Spec.Persistence.Output.MountPath
	}
	return fmt.Sprintf("/data/output/%s", claw.Name)
}
