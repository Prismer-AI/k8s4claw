package runtime

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// ConfigMergeMode controls how config is merged in the init container.
type ConfigMergeMode string

const (
	ConfigModeOverwrite   ConfigMergeMode = "overwrite"
	ConfigModeDeepMerge   ConfigMergeMode = "deepmerge"
	ConfigModePassthrough ConfigMergeMode = "passthrough"
)

// InitContainerImage is the claw-init container image.
// Set at build time via: -ldflags "-X github.com/Prismer-AI/k8s4claw/internal/runtime.InitContainerImage=ghcr.io/prismer-ai/claw-init:v1.2.3"
var InitContainerImage = "ghcr.io/prismer-ai/claw-init:latest"

// RuntimeSpec defines the runtime-specific pod configuration provided by each adapter.
type RuntimeSpec struct {
	Image             string
	Command           []string
	Args              []string
	Ports             []corev1.ContainerPort
	Resources         corev1.ResourceRequirements
	ExtraVolumeMounts []corev1.VolumeMount
	ExtraVolumes      []corev1.Volume
	Env               []corev1.EnvVar
	LivenessProbe     *corev1.Probe
	ReadinessProbe    *corev1.Probe
	ConfigMode        ConfigMergeMode
	WorkspacePath     string
	SecurityContext   *corev1.SecurityContext
}

// ---------------------------------------------------------------------------
// Volume helpers
// ---------------------------------------------------------------------------

// emptyDirVolume creates an emptyDir Volume with optional size limit and medium.
func emptyDirVolume(name string, sizeLimit *resource.Quantity, medium corev1.StorageMedium) corev1.Volume {
	ed := &corev1.EmptyDirVolumeSource{
		Medium: medium,
	}
	if sizeLimit != nil {
		ed.SizeLimit = sizeLimit
	}
	return corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: ed,
		},
	}
}

// configMapVolume creates a ConfigMap-backed Volume with Optional set to true.
func configMapVolume(name, cmName string) corev1.Volume {
	return corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
				Optional:             ptr.To(true),
			},
		},
	}
}

// pvcVolume creates a PersistentVolumeClaim-backed Volume.
func pvcVolume(name, claimName string) corev1.Volume {
	return corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: claimName,
			},
		},
	}
}

// ---------------------------------------------------------------------------
// buildVolumes
// ---------------------------------------------------------------------------

// buildVolumes assembles the pod-level Volume list.
//
// Always included:
//   - ipc-socket (emptyDir)
//   - wal-data (emptyDir, 512Mi)
//   - config-vol (ConfigMap <claw.Name>-config, optional)
//   - tmp (emptyDir)
//
// Conditionally included:
//   - cache (emptyDir with configured medium when persistence.cache is configured and enabled)
//   - shared-<name> (PVC refs from persistence.shared[])
//
// Session, output, and workspace PVCs are NOT included here; they belong
// in StatefulSet volumeClaimTemplates (see BuildVolumeClaimTemplates).
//
// Finally, spec.ExtraVolumes are appended.
func buildVolumes(claw *v1alpha1.Claw, spec *RuntimeSpec) []corev1.Volume {
	walSize := resource.MustParse("512Mi")
	vols := []corev1.Volume{
		emptyDirVolume("ipc-socket", nil, ""),
		emptyDirVolume("wal-data", &walSize, ""),
		configMapVolume("config-vol", fmt.Sprintf("%s-config", claw.Name)),
		emptyDirVolume("tmp", nil, ""),
	}

	if claw.Spec.Persistence != nil {
		// Cache: ephemeral emptyDir (often tmpfs).
		if claw.Spec.Persistence.Cache != nil && claw.Spec.Persistence.Cache.Enabled {
			cacheSize := resource.MustParse(claw.Spec.Persistence.Cache.Size)
			vols = append(vols, emptyDirVolume("cache", &cacheSize, claw.Spec.Persistence.Cache.Medium))
		}

		// Shared volumes: pre-existing PVC references.
		for _, s := range claw.Spec.Persistence.Shared {
			vols = append(vols, pvcVolume(fmt.Sprintf("shared-%s", s.Name), s.ClaimName))
		}
	}

	// Adapter-specific extra volumes.
	vols = append(vols, spec.ExtraVolumes...)

	return vols
}

// ---------------------------------------------------------------------------
// Container helpers
// ---------------------------------------------------------------------------

// sharedEnvVars returns the environment variables injected into every container.
func sharedEnvVars(claw *v1alpha1.Claw) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "CLAW_NAME", Value: claw.Name},
		{Name: "CLAW_NAMESPACE", Value: claw.Namespace},
		{Name: "IPC_SOCKET_PATH", Value: "/var/run/claw/bus.sock"},
	}
}

// hasWorkspacePVC returns true when the Claw has a workspace PVC configured and enabled.
func hasWorkspacePVC(claw *v1alpha1.Claw) bool {
	return claw.Spec.Persistence != nil &&
		claw.Spec.Persistence.Workspace != nil &&
		claw.Spec.Persistence.Workspace.Enabled
}

// initContainerResources returns the resource requirements for the claw-init container.
func initContainerResources() corev1.ResourceRequirements {
	return resources("100m", "128Mi", "500m", "256Mi")
}

// resources builds a ResourceRequirements from human-readable strings.
func resources(cpuReq, memReq, cpuLim, memLim string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpuReq),
			corev1.ResourceMemory: resource.MustParse(memReq),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpuLim),
			corev1.ResourceMemory: resource.MustParse(memLim),
		},
	}
}

// ---------------------------------------------------------------------------
// buildInitContainer
// ---------------------------------------------------------------------------

// buildInitContainer creates the claw-init init container that prepares config
// and workspace before the runtime starts.
func buildInitContainer(claw *v1alpha1.Claw, spec *RuntimeSpec) corev1.Container {
	configMode := string(spec.ConfigMode)
	if configMode == "" {
		configMode = string(ConfigModeOverwrite)
	}

	workspacePath := spec.WorkspacePath
	if workspacePath == "" {
		workspacePath = "/workspace"
	}

	mounts := []corev1.VolumeMount{
		{Name: "config-vol", MountPath: "/etc/claw", ReadOnly: true},
		{Name: "tmp", MountPath: "/tmp"},
	}

	if hasWorkspacePVC(claw) {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      "workspace",
			MountPath: workspacePath,
		})
	}

	return corev1.Container{
		Name:    "claw-init",
		Image:   InitContainerImage,
		Command: []string{"/claw-init"},
		Args: []string{
			"--mode", configMode,
			"--workspace", workspacePath,
			"--runtime", string(claw.Spec.Runtime),
		},
		Env: append(sharedEnvVars(claw),
			corev1.EnvVar{Name: "NPM_CONFIG_IGNORE_SCRIPTS", Value: "true"},
		),
		VolumeMounts:    mounts,
		Resources:       initContainerResources(),
		SecurityContext: ContainerSecurityContext(),
	}
}

// ---------------------------------------------------------------------------
// buildRuntimeContainer
// ---------------------------------------------------------------------------

// buildRuntimeContainer creates the main runtime container.
//
// Environment variable ordering: shared env vars first, then spec.Env.
// For duplicate keys Kubernetes uses last-wins semantics, so spec.Env
// can override shared vars.
func buildRuntimeContainer(claw *v1alpha1.Claw, spec *RuntimeSpec) corev1.Container {
	// Build env: shared first, spec.Env appended (last-wins for duplicates).
	env := append(sharedEnvVars(claw), spec.Env...)

	// Base volume mounts (always present).
	mounts := []corev1.VolumeMount{
		{Name: "ipc-socket", MountPath: "/var/run/claw"},
		{Name: "wal-data", MountPath: "/var/lib/claw/wal"},
		{Name: "config-vol", MountPath: "/etc/claw", ReadOnly: true},
		{Name: "tmp", MountPath: "/tmp"},
	}

	// Conditional mounts based on persistence configuration.
	if claw.Spec.Persistence != nil {
		if claw.Spec.Persistence.Cache != nil && claw.Spec.Persistence.Cache.Enabled {
			mounts = append(mounts, corev1.VolumeMount{
				Name:      "cache",
				MountPath: claw.Spec.Persistence.Cache.MountPath,
			})
		}
		if claw.Spec.Persistence.Session != nil && claw.Spec.Persistence.Session.Enabled {
			mounts = append(mounts, corev1.VolumeMount{
				Name:      "session",
				MountPath: claw.Spec.Persistence.Session.MountPath,
			})
		}
		if claw.Spec.Persistence.Output != nil && claw.Spec.Persistence.Output.Enabled {
			mounts = append(mounts, corev1.VolumeMount{
				Name:      "output",
				MountPath: claw.Spec.Persistence.Output.MountPath,
			})
		}
		if hasWorkspacePVC(claw) {
			mounts = append(mounts, corev1.VolumeMount{
				Name:      "workspace",
				MountPath: claw.Spec.Persistence.Workspace.MountPath,
			})
		}
		for _, s := range claw.Spec.Persistence.Shared {
			mounts = append(mounts, corev1.VolumeMount{
				Name:      fmt.Sprintf("shared-%s", s.Name),
				MountPath: s.MountPath,
				ReadOnly:  s.ReadOnly,
			})
		}
	}

	// Adapter-specific extra mounts.
	mounts = append(mounts, spec.ExtraVolumeMounts...)

	securityContext := spec.SecurityContext
	if securityContext == nil {
		securityContext = ContainerSecurityContext()
	}

	return corev1.Container{
		Name:            "runtime",
		Image:           spec.Image,
		Command:         spec.Command,
		Args:            spec.Args,
		Ports:           spec.Ports,
		Env:             env,
		VolumeMounts:    mounts,
		Resources:       spec.Resources,
		LivenessProbe:   spec.LivenessProbe,
		ReadinessProbe:  spec.ReadinessProbe,
		SecurityContext: securityContext,
		Lifecycle: &corev1.Lifecycle{
			PreStop: &corev1.LifecycleHandler{
				Exec: &corev1.ExecAction{
					Command: []string{"sh", "-c", "sleep 2"},
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// ContainerSecurityContext returns the hardened security context applied to
// every container in a Claw pod.
func ContainerSecurityContext() *corev1.SecurityContext {
	return containerSecurityContext(true)
}

func containerSecurityContext(readOnlyRoot bool) *corev1.SecurityContext {
	return &corev1.SecurityContext{
		RunAsUser:                ptr.To(int64(1000)),
		RunAsGroup:               ptr.To(int64(1000)),
		RunAsNonRoot:             ptr.To(true),
		ReadOnlyRootFilesystem:   ptr.To(readOnlyRoot),
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

// BuildPodTemplate assembles a PodTemplateSpec from the Claw and RuntimeSpec.
//
// The returned template contains:
//   - InitContainers: [claw-init]
//   - Containers: [runtime]
//   - Volumes: base + conditional + adapter extras
//
// It does NOT set labels, pod security context, or terminationGracePeriodSeconds.
// Those are the controller's responsibility.
func BuildPodTemplate(claw *v1alpha1.Claw, spec *RuntimeSpec) *corev1.PodTemplateSpec {
	return &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				buildInitContainer(claw, spec),
			},
			Containers: []corev1.Container{
				buildRuntimeContainer(claw, spec),
			},
			Volumes: buildVolumes(claw, spec),
		},
	}
}

// BuildVolumeClaimTemplates returns PVC templates for session, output, and
// workspace when they are configured and enabled. These are intended for use
// in a StatefulSet's volumeClaimTemplates field.
//
// Each template is labeled with "claw.prismer.ai/instance" so that PVCs
// created by the StatefulSet can be discovered during Claw deletion.
func BuildVolumeClaimTemplates(claw *v1alpha1.Claw) []corev1.PersistentVolumeClaim {
	if claw.Spec.Persistence == nil {
		return nil
	}

	labels := map[string]string{
		"claw.prismer.ai/instance": claw.Name,
	}

	var templates []corev1.PersistentVolumeClaim

	if p := claw.Spec.Persistence.Session; p != nil && p.Enabled {
		templates = append(templates, pvcTemplate("session", p.Size, p.StorageClass, labels))
	}
	if p := claw.Spec.Persistence.Output; p != nil && p.Enabled {
		templates = append(templates, pvcTemplate("output", p.Size, p.StorageClass, labels))
	}
	if p := claw.Spec.Persistence.Workspace; p != nil && p.Enabled {
		templates = append(templates, pvcTemplate("workspace", p.Size, p.StorageClass, labels))
	}

	if len(templates) == 0 {
		return nil
	}
	return templates
}

// pvcTemplate creates a PersistentVolumeClaim for use in volumeClaimTemplates.
func pvcTemplate(name, size, storageClass string, labels map[string]string) corev1.PersistentVolumeClaim {
	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(size),
				},
			},
		},
	}
	if storageClass != "" {
		pvc.Spec.StorageClassName = ptr.To(storageClass)
	}
	return pvc
}
