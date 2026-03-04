# RuntimeAdapter PodTemplate Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement full `PodTemplate()` for all 4 runtime adapters using a shared PodBuilder, replacing the current placeholder (busybox container).

**Architecture:** Shared `PodBuilder` + per-adapter `RuntimeSpec`. PodBuilder assembles init container, runtime container, and volumes. Controller injects labels, pod security, channel sidecars. StatefulSet's `volumeClaimTemplates` manages PVCs.

**Tech Stack:** Go 1.25, controller-runtime v0.23.1, K8s API v0.35.2, envtest

**Design doc:** `docs/plans/2026-03-04-podtemplate-design.md`

**Test environment:**
```bash
export KUBEBUILDER_ASSETS=$(setup-envtest use --bin-dir /home/willamhou/codes/k8s4claw/bin/k8s -p path)
```

---

### Task 1: Core Types + Volume Helper Functions

**Files:**
- Create: `internal/runtime/pod_builder.go`
- Create: `internal/runtime/pod_builder_test.go`

**Context:** This task creates the foundational types (`ConfigMergeMode`, `RuntimeSpec`) and volume helper functions that all subsequent tasks depend on. The existing `containerSecurityContext()` in `internal/controller/claw_controller.go:274-289` will be migrated here in Task 7.

**Step 1: Write failing tests for volume helpers**

```go
// internal/runtime/pod_builder_test.go
package runtime

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
)

func TestEmptyDirVolume(t *testing.T) {
	tests := []struct {
		name      string
		volName   string
		sizeLimit *resource.Quantity
		medium    corev1.StorageMedium
	}{
		{
			name:    "basic emptyDir without size limit",
			volName: "tmp",
		},
		{
			name:      "emptyDir with size limit",
			volName:   "wal-data",
			sizeLimit: ptr.To(resource.MustParse("512Mi")),
		},
		{
			name:      "tmpfs emptyDir",
			volName:   "cache",
			sizeLimit: ptr.To(resource.MustParse("1Gi")),
			medium:    corev1.StorageMediumMemory,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vol := emptyDirVolume(tt.volName, tt.sizeLimit, tt.medium)
			if vol.Name != tt.volName {
				t.Errorf("name: got %q, want %q", vol.Name, tt.volName)
			}
			if vol.EmptyDir == nil {
				t.Fatal("expected emptyDir volume source")
			}
			if tt.sizeLimit != nil {
				if vol.EmptyDir.SizeLimit == nil {
					t.Fatal("expected sizeLimit")
				}
				if !vol.EmptyDir.SizeLimit.Equal(*tt.sizeLimit) {
					t.Errorf("sizeLimit: got %v, want %v", vol.EmptyDir.SizeLimit, tt.sizeLimit)
				}
			}
			if vol.EmptyDir.Medium != tt.medium {
				t.Errorf("medium: got %q, want %q", vol.EmptyDir.Medium, tt.medium)
			}
		})
	}
}

func TestConfigMapVolume(t *testing.T) {
	vol := configMapVolume("config-vol", "my-agent-config")
	if vol.Name != "config-vol" {
		t.Errorf("name: got %q, want %q", vol.Name, "config-vol")
	}
	if vol.ConfigMap == nil {
		t.Fatal("expected configMap volume source")
	}
	if vol.ConfigMap.Name != "my-agent-config" {
		t.Errorf("configMap name: got %q, want %q", vol.ConfigMap.Name, "my-agent-config")
	}
	if vol.ConfigMap.Optional == nil || !*vol.ConfigMap.Optional {
		t.Error("expected optional=true")
	}
}

func TestPVCVolume(t *testing.T) {
	vol := pvcVolume("shared-data", "team-shared")
	if vol.Name != "shared-data" {
		t.Errorf("name: got %q, want %q", vol.Name, "shared-data")
	}
	if vol.PersistentVolumeClaim == nil {
		t.Fatal("expected PVC volume source")
	}
	if vol.PersistentVolumeClaim.ClaimName != "team-shared" {
		t.Errorf("claimName: got %q, want %q", vol.PersistentVolumeClaim.ClaimName, "team-shared")
	}
}
```

**Step 2: Run tests to verify they fail**

```bash
cd /home/willamhou/codes/k8s4claw && go test ./internal/runtime/ -run 'TestEmptyDirVolume|TestConfigMapVolume|TestPVCVolume' -v
```

Expected: FAIL — `emptyDirVolume`, `configMapVolume`, `pvcVolume` undefined.

**Step 3: Implement types and helpers**

```go
// internal/runtime/pod_builder.go
package runtime

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
)

// ConfigMergeMode defines how the init container merges runtime configuration.
type ConfigMergeMode string

const (
	ConfigModeOverwrite   ConfigMergeMode = "overwrite"
	ConfigModeDeepMerge   ConfigMergeMode = "deepmerge"
	ConfigModePassthrough ConfigMergeMode = "passthrough"
)

// RuntimeSpec describes the runtime-specific parts of a Pod.
// Each adapter provides a RuntimeSpec; the PodBuilder assembles the full PodTemplateSpec.
type RuntimeSpec struct {
	Image          string
	Command        []string
	Args           []string
	Ports          []corev1.ContainerPort
	Resources      corev1.ResourceRequirements
	ExtraVolumeMounts []corev1.VolumeMount
	ExtraVolumes      []corev1.Volume
	Env            []corev1.EnvVar
	LivenessProbe  *corev1.Probe
	ReadinessProbe *corev1.Probe
	ConfigMode     ConfigMergeMode
	WorkspacePath  string
}

func emptyDirVolume(name string, sizeLimit *resource.Quantity, medium corev1.StorageMedium) corev1.Volume {
	ed := &corev1.EmptyDirVolumeSource{Medium: medium}
	if sizeLimit != nil {
		ed.SizeLimit = sizeLimit
	}
	return corev1.Volume{
		Name:         name,
		VolumeSource: corev1.VolumeSource{EmptyDir: ed},
	}
}

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
```

**Step 4: Run tests to verify they pass**

```bash
cd /home/willamhou/codes/k8s4claw && go test ./internal/runtime/ -run 'TestEmptyDirVolume|TestConfigMapVolume|TestPVCVolume' -v
```

Expected: PASS

**Step 5: Run go vet**

```bash
cd /home/willamhou/codes/k8s4claw && go vet ./internal/runtime/
```

Expected: clean

**Step 6: Commit**

```bash
git add internal/runtime/pod_builder.go internal/runtime/pod_builder_test.go
git commit -m "feat: add PodBuilder core types and volume helpers"
```

---

### Task 2: buildVolumes Function

**Files:**
- Modify: `internal/runtime/pod_builder.go`
- Modify: `internal/runtime/pod_builder_test.go`

**Context:** `buildVolumes` assembles all Pod volumes from a Claw spec + RuntimeSpec. It reads `claw.Spec.Persistence` to determine conditional volumes. Refer to design doc Section 6.1 for the volume list. Uses helpers from Task 1.

**Step 1: Write failing test**

```go
// Add to internal/runtime/pod_builder_test.go

import (
	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildVolumes_Minimal(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}
	spec := &RuntimeSpec{WorkspacePath: "/workspace"}

	volumes := buildVolumes(claw, spec)

	// Expect 4 always-present volumes: ipc-socket, wal-data, config-vol, tmp
	expected := map[string]bool{
		"ipc-socket": false, "wal-data": false,
		"config-vol": false, "tmp": false,
	}
	for _, v := range volumes {
		if _, ok := expected[v.Name]; ok {
			expected[v.Name] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("missing expected volume %q", name)
		}
	}
}

func TestBuildVolumes_WithPersistence(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Persistence: &clawv1alpha1.PersistenceSpec{
				Session:   &clawv1alpha1.VolumeSpec{Enabled: true, Size: "2Gi", MountPath: "/data/session"},
				Workspace: &clawv1alpha1.VolumeSpec{Enabled: true, Size: "10Gi", MountPath: "/workspace"},
				Cache: &clawv1alpha1.CacheSpec{
					Enabled:   true,
					Size:      "1Gi",
					Medium:    corev1.StorageMediumMemory,
					MountPath: "/cache",
				},
				Shared: []clawv1alpha1.SharedVolumeRef{
					{Name: "datasets", ClaimName: "team-data", MountPath: "/shared"},
				},
			},
		},
	}
	spec := &RuntimeSpec{WorkspacePath: "/workspace"}

	volumes := buildVolumes(claw, spec)

	// Should have: 4 base + cache + shared-datasets = 6
	// (session and workspace are volumeClaimTemplates, not volumes)
	names := map[string]bool{}
	for _, v := range volumes {
		names[v.Name] = true
	}

	for _, expected := range []string{"ipc-socket", "wal-data", "config-vol", "tmp", "cache", "shared-datasets"} {
		if !names[expected] {
			t.Errorf("missing volume %q", expected)
		}
	}

	// Verify cache is tmpfs
	for _, v := range volumes {
		if v.Name == "cache" && v.EmptyDir != nil {
			if v.EmptyDir.Medium != corev1.StorageMediumMemory {
				t.Errorf("cache medium: got %q, want Memory", v.EmptyDir.Medium)
			}
		}
	}
}

func TestBuildVolumes_ExtraVolumes(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeNanoClaw},
	}
	spec := &RuntimeSpec{
		WorkspacePath: "/workspace",
		ExtraVolumes: []corev1.Volume{
			{Name: "bridge-socket", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		},
	}

	volumes := buildVolumes(claw, spec)
	found := false
	for _, v := range volumes {
		if v.Name == "bridge-socket" {
			found = true
		}
	}
	if !found {
		t.Error("missing ExtraVolumes entry 'bridge-socket'")
	}
}
```

**Step 2: Run tests to verify they fail**

```bash
cd /home/willamhou/codes/k8s4claw && go test ./internal/runtime/ -run 'TestBuildVolumes' -v
```

Expected: FAIL — `buildVolumes` undefined.

**Step 3: Implement buildVolumes**

Add to `internal/runtime/pod_builder.go`:

```go
import (
	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func buildVolumes(claw *v1alpha1.Claw, spec *RuntimeSpec) []corev1.Volume {
	walSize := resource.MustParse("512Mi")
	volumes := []corev1.Volume{
		emptyDirVolume("ipc-socket", nil, ""),
		emptyDirVolume("wal-data", &walSize, ""),
		configMapVolume("config-vol", claw.Name+"-config"),
		emptyDirVolume("tmp", nil, ""),
	}

	if p := claw.Spec.Persistence; p != nil {
		if p.Cache != nil && p.Cache.Enabled {
			cacheSize := resource.MustParse(p.Cache.Size)
			volumes = append(volumes,
				emptyDirVolume("cache", &cacheSize, p.Cache.Medium))
		}
		for _, s := range p.Shared {
			volumes = append(volumes, pvcVolume("shared-"+s.Name, s.ClaimName))
		}
	}

	volumes = append(volumes, spec.ExtraVolumes...)
	return volumes
}
```

Note: Session, output, workspace PVCs are NOT in `buildVolumes` — they are managed via StatefulSet `volumeClaimTemplates` (Task 5).

**Step 4: Run tests**

```bash
cd /home/willamhou/codes/k8s4claw && go test ./internal/runtime/ -run 'TestBuildVolumes' -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add internal/runtime/pod_builder.go internal/runtime/pod_builder_test.go
git commit -m "feat: add buildVolumes for Pod volume assembly"
```

---

### Task 3: buildInitContainer + buildRuntimeContainer

**Files:**
- Modify: `internal/runtime/pod_builder.go`
- Modify: `internal/runtime/pod_builder_test.go`

**Context:** These two functions build the init container (claw-init) and the runtime container. The runtime container gets probes, env, security context from RuntimeSpec. Both use `ContainerSecurityContext()` (public, migrated from controller in Task 7). For now, define it here directly.

**Step 1: Write failing tests**

```go
// Add to internal/runtime/pod_builder_test.go

func TestBuildInitContainer(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Persistence: &clawv1alpha1.PersistenceSpec{
				Workspace: &clawv1alpha1.VolumeSpec{Enabled: true, Size: "10Gi", MountPath: "/workspace"},
			},
		},
	}
	spec := &RuntimeSpec{ConfigMode: ConfigModeDeepMerge, WorkspacePath: "/workspace"}

	c := buildInitContainer(claw, spec)

	if c.Name != "claw-init" {
		t.Errorf("name: got %q, want 'claw-init'", c.Name)
	}
	if c.Image != "ghcr.io/prismer-ai/claw-init:latest" {
		t.Errorf("image: got %q", c.Image)
	}
	// Verify args contain --mode deepmerge
	foundMode := false
	for i, arg := range c.Args {
		if arg == "--mode" && i+1 < len(c.Args) && c.Args[i+1] == "deepmerge" {
			foundMode = true
		}
	}
	if !foundMode {
		t.Errorf("expected --mode deepmerge in args, got %v", c.Args)
	}
	// Verify workspace mount present (because workspace PVC enabled)
	foundWS := false
	for _, m := range c.VolumeMounts {
		if m.Name == "workspace" && m.MountPath == "/workspace" {
			foundWS = true
		}
	}
	if !foundWS {
		t.Error("expected workspace volume mount")
	}
	// Verify security context
	if c.SecurityContext == nil || c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem {
		t.Error("expected readOnlyRootFilesystem=true")
	}
}

func TestBuildInitContainer_NoWorkspace(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeZeroClaw},
	}
	spec := &RuntimeSpec{ConfigMode: ConfigModePassthrough, WorkspacePath: "/workspace"}

	c := buildInitContainer(claw, spec)

	for _, m := range c.VolumeMounts {
		if m.Name == "workspace" {
			t.Error("workspace mount should not be present when no workspace PVC")
		}
	}
}

func TestBuildRuntimeContainer(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Persistence: &clawv1alpha1.PersistenceSpec{
				Session: &clawv1alpha1.VolumeSpec{Enabled: true, Size: "2Gi", MountPath: "/data/session"},
				Cache:   &clawv1alpha1.CacheSpec{Enabled: true, Size: "1Gi", MountPath: "/cache", Medium: corev1.StorageMediumMemory},
			},
		},
	}
	probe := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
		HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: portIntStr(18900)},
	}}
	spec := &RuntimeSpec{
		Image:          "ghcr.io/prismer-ai/k8s4claw-openclaw:latest",
		Command:        []string{"/usr/bin/claw-entrypoint"},
		Ports:          []corev1.ContainerPort{{Name: "gateway", ContainerPort: 18900}},
		Resources:      resources("500m", "1Gi", "2000m", "4Gi"),
		Env:            []corev1.EnvVar{{Name: "OPENCLAW_MODE", Value: "gateway"}},
		LivenessProbe:  probe,
		ReadinessProbe: probe,
		WorkspacePath:  "/workspace",
	}

	c := buildRuntimeContainer(claw, spec)

	if c.Name != "runtime" {
		t.Errorf("name: got %q, want 'runtime'", c.Name)
	}
	if c.Image != spec.Image {
		t.Errorf("image: got %q", c.Image)
	}
	// Verify shared env injected (CLAW_NAME should be first, before OPENCLAW_MODE)
	if len(c.Env) < 4 {
		t.Fatalf("expected at least 4 env vars, got %d", len(c.Env))
	}
	if c.Env[0].Name != "CLAW_NAME" || c.Env[0].Value != "test-agent" {
		t.Errorf("first env: got %s=%s, want CLAW_NAME=test-agent", c.Env[0].Name, c.Env[0].Value)
	}
	// Verify base mounts present
	mountNames := map[string]bool{}
	for _, m := range c.VolumeMounts {
		mountNames[m.Name] = true
	}
	for _, expected := range []string{"ipc-socket", "wal-data", "config-vol", "tmp", "session", "cache"} {
		if !mountNames[expected] {
			t.Errorf("missing mount %q", expected)
		}
	}
	// Verify probes
	if c.LivenessProbe == nil {
		t.Error("expected liveness probe")
	}
	if c.ReadinessProbe == nil {
		t.Error("expected readiness probe")
	}
	// Verify security context
	if c.SecurityContext == nil {
		t.Fatal("expected security context")
	}
}
```

**Step 2: Run tests to verify they fail**

```bash
cd /home/willamhou/codes/k8s4claw && go test ./internal/runtime/ -run 'TestBuildInitContainer|TestBuildRuntimeContainer' -v
```

Expected: FAIL — functions undefined.

**Step 3: Implement**

Add to `internal/runtime/pod_builder.go`:

```go
// ContainerSecurityContext returns the hardened security context for containers.
func ContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		RunAsUser:                ptr.To(int64(1000)),
		RunAsGroup:               ptr.To(int64(1000)),
		RunAsNonRoot:             ptr.To(true),
		ReadOnlyRootFilesystem:   ptr.To(true),
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

func sharedEnvVars(claw *v1alpha1.Claw) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "CLAW_NAME", Value: claw.Name},
		{Name: "CLAW_NAMESPACE", Value: claw.Namespace},
		{Name: "IPC_SOCKET_PATH", Value: "/var/run/claw/bus.sock"},
	}
}

func hasWorkspacePVC(claw *v1alpha1.Claw) bool {
	return claw.Spec.Persistence != nil &&
		claw.Spec.Persistence.Workspace != nil &&
		claw.Spec.Persistence.Workspace.Enabled
}

func initContainerResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
	}
}

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

func buildInitContainer(claw *v1alpha1.Claw, spec *RuntimeSpec) corev1.Container {
	mounts := []corev1.VolumeMount{
		{Name: "config-vol", MountPath: "/etc/claw", ReadOnly: true},
		{Name: "tmp", MountPath: "/tmp"},
	}
	if hasWorkspacePVC(claw) {
		mounts = append(mounts, corev1.VolumeMount{
			Name: "workspace", MountPath: spec.WorkspacePath,
		})
	}

	return corev1.Container{
		Name:    "claw-init",
		Image:   "ghcr.io/prismer-ai/claw-init:latest",
		Command: []string{"/claw-init"},
		Args: []string{
			"--mode", string(spec.ConfigMode),
			"--workspace", spec.WorkspacePath,
			"--runtime", string(claw.Spec.Runtime),
		},
		Env:             sharedEnvVars(claw),
		VolumeMounts:    mounts,
		Resources:       initContainerResources(),
		SecurityContext: ContainerSecurityContext(),
	}
}

func buildRuntimeContainer(claw *v1alpha1.Claw, spec *RuntimeSpec) corev1.Container {
	// Shared env first, then runtime-specific env (last-wins for duplicates).
	env := append(sharedEnvVars(claw), spec.Env...)

	mounts := []corev1.VolumeMount{
		{Name: "ipc-socket", MountPath: "/var/run/claw"},
		{Name: "wal-data", MountPath: "/var/lib/claw/wal"},
		{Name: "config-vol", MountPath: "/etc/claw", ReadOnly: true},
		{Name: "tmp", MountPath: "/tmp"},
	}

	if p := claw.Spec.Persistence; p != nil {
		if p.Cache != nil && p.Cache.Enabled {
			mounts = append(mounts, corev1.VolumeMount{
				Name: "cache", MountPath: p.Cache.MountPath,
			})
		}
		if p.Session != nil && p.Session.Enabled {
			mounts = append(mounts, corev1.VolumeMount{
				Name: "session", MountPath: p.Session.MountPath,
			})
		}
		if p.Output != nil && p.Output.Enabled {
			mounts = append(mounts, corev1.VolumeMount{
				Name: "output", MountPath: p.Output.MountPath,
			})
		}
		if p.Workspace != nil && p.Workspace.Enabled {
			mounts = append(mounts, corev1.VolumeMount{
				Name: "workspace", MountPath: p.Workspace.MountPath,
			})
		}
		for _, s := range p.Shared {
			mounts = append(mounts, corev1.VolumeMount{
				Name: "shared-" + s.Name, MountPath: s.MountPath, ReadOnly: s.ReadOnly,
			})
		}
	}

	mounts = append(mounts, spec.ExtraVolumeMounts...)

	return corev1.Container{
		Name:            "runtime",
		Image:           spec.Image,
		Command:         spec.Command,
		Args:            spec.Args,
		Ports:           spec.Ports,
		Env:             env,
		Resources:       spec.Resources,
		VolumeMounts:    mounts,
		LivenessProbe:   spec.LivenessProbe,
		ReadinessProbe:  spec.ReadinessProbe,
		SecurityContext: ContainerSecurityContext(),
	}
}
```

**Step 4: Run tests**

```bash
cd /home/willamhou/codes/k8s4claw && go test ./internal/runtime/ -run 'TestBuildInitContainer|TestBuildRuntimeContainer' -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add internal/runtime/pod_builder.go internal/runtime/pod_builder_test.go
git commit -m "feat: add buildInitContainer and buildRuntimeContainer"
```

---

### Task 4: BuildPodTemplate + BuildVolumeClaimTemplates

**Files:**
- Modify: `internal/runtime/pod_builder.go`
- Modify: `internal/runtime/pod_builder_test.go`

**Context:** `BuildPodTemplate` (public) assembles a complete `PodTemplateSpec` from a Claw + RuntimeSpec. `BuildVolumeClaimTemplates` (public) returns PVC templates for the StatefulSet. Both are called from outside the package.

**Step 1: Write failing tests**

```go
// Add to internal/runtime/pod_builder_test.go

func TestBuildPodTemplate(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}
	spec := &RuntimeSpec{
		Image:         "ghcr.io/prismer-ai/k8s4claw-openclaw:latest",
		Command:       []string{"/usr/bin/claw-entrypoint"},
		Ports:         []corev1.ContainerPort{{Name: "gateway", ContainerPort: 18900}},
		Resources:     resources("500m", "1Gi", "2000m", "4Gi"),
		Env:           []corev1.EnvVar{{Name: "OPENCLAW_MODE", Value: "gateway"}},
		ConfigMode:    ConfigModeDeepMerge,
		WorkspacePath: "/workspace",
		LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: portIntStr(18900)},
		}},
	}

	tmpl := BuildPodTemplate(claw, spec)

	// Verify init container
	if len(tmpl.Spec.InitContainers) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(tmpl.Spec.InitContainers))
	}
	if tmpl.Spec.InitContainers[0].Name != "claw-init" {
		t.Errorf("init container name: got %q", tmpl.Spec.InitContainers[0].Name)
	}

	// Verify runtime container
	if len(tmpl.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(tmpl.Spec.Containers))
	}
	if tmpl.Spec.Containers[0].Name != "runtime" {
		t.Errorf("container name: got %q", tmpl.Spec.Containers[0].Name)
	}
	if tmpl.Spec.Containers[0].Image != spec.Image {
		t.Errorf("container image: got %q", tmpl.Spec.Containers[0].Image)
	}

	// Verify volumes exist
	if len(tmpl.Spec.Volumes) < 4 {
		t.Errorf("expected at least 4 volumes, got %d", len(tmpl.Spec.Volumes))
	}
}

func TestBuildVolumeClaimTemplates_NoPersistence(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		Spec: clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeZeroClaw},
	}
	templates := BuildVolumeClaimTemplates(claw)
	if len(templates) != 0 {
		t.Errorf("expected 0 templates, got %d", len(templates))
	}
}

func TestBuildVolumeClaimTemplates_WithPersistence(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Persistence: &clawv1alpha1.PersistenceSpec{
				Session:   &clawv1alpha1.VolumeSpec{Enabled: true, Size: "2Gi", MountPath: "/data/session"},
				Output:    &clawv1alpha1.OutputVolumeSpec{VolumeSpec: clawv1alpha1.VolumeSpec{Enabled: true, Size: "5Gi", MountPath: "/data/output"}},
				Workspace: &clawv1alpha1.VolumeSpec{Enabled: true, Size: "10Gi", MountPath: "/workspace", StorageClass: "fast-ssd"},
			},
		},
	}
	templates := BuildVolumeClaimTemplates(claw)
	if len(templates) != 3 {
		t.Fatalf("expected 3 templates, got %d", len(templates))
	}

	names := map[string]bool{}
	for _, pvc := range templates {
		names[pvc.Name] = true
	}
	for _, expected := range []string{"session", "output", "workspace"} {
		if !names[expected] {
			t.Errorf("missing PVC template %q", expected)
		}
	}

	// Verify workspace has storageClass
	for _, pvc := range templates {
		if pvc.Name == "workspace" {
			if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "fast-ssd" {
				t.Errorf("workspace storageClass: got %v", pvc.Spec.StorageClassName)
			}
		}
	}
}
```

**Step 2: Run tests to verify they fail**

```bash
cd /home/willamhou/codes/k8s4claw && go test ./internal/runtime/ -run 'TestBuildPodTemplate|TestBuildVolumeClaimTemplates' -v
```

Expected: FAIL

**Step 3: Implement**

Add to `internal/runtime/pod_builder.go`:

```go
// BuildPodTemplate assembles a complete PodTemplateSpec from a Claw and RuntimeSpec.
// The caller (controller) is responsible for injecting labels, pod security context,
// terminationGracePeriodSeconds, and channel sidecars.
func BuildPodTemplate(claw *v1alpha1.Claw, spec *RuntimeSpec) *corev1.PodTemplateSpec {
	return &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{buildInitContainer(claw, spec)},
			Containers:     []corev1.Container{buildRuntimeContainer(claw, spec)},
			Volumes:        buildVolumes(claw, spec),
		},
	}
}

// BuildVolumeClaimTemplates returns PVC templates for the StatefulSet.
// Session, output, and workspace PVCs are managed by StatefulSet (auto-created).
func BuildVolumeClaimTemplates(claw *v1alpha1.Claw) []corev1.PersistentVolumeClaim {
	if claw.Spec.Persistence == nil {
		return nil
	}

	var templates []corev1.PersistentVolumeClaim
	p := claw.Spec.Persistence

	if p.Session != nil && p.Session.Enabled {
		templates = append(templates, pvcTemplate("session", p.Session.Size, p.Session.StorageClass))
	}
	if p.Output != nil && p.Output.Enabled {
		templates = append(templates, pvcTemplate("output", p.Output.Size, p.Output.StorageClass))
	}
	if p.Workspace != nil && p.Workspace.Enabled {
		templates = append(templates, pvcTemplate("workspace", p.Workspace.Size, p.Workspace.StorageClass))
	}

	return templates
}

func pvcTemplate(name, size, storageClass string) corev1.PersistentVolumeClaim {
	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
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
		pvc.Spec.StorageClassName = &storageClass
	}
	return pvc
}
```

Add `metav1` import to `pod_builder.go`:
```go
metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
```

**Step 4: Run tests**

```bash
cd /home/willamhou/codes/k8s4claw && go test ./internal/runtime/ -run 'TestBuildPodTemplate|TestBuildVolumeClaimTemplates' -v
```

Expected: PASS

**Step 5: Run full runtime package tests**

```bash
cd /home/willamhou/codes/k8s4claw && go test ./internal/runtime/ -v -count=1
```

Expected: All PASS

**Step 6: Commit**

```bash
git add internal/runtime/pod_builder.go internal/runtime/pod_builder_test.go
git commit -m "feat: add BuildPodTemplate and BuildVolumeClaimTemplates"
```

---

### Task 5: Update All 4 Runtime Adapters

**Files:**
- Modify: `internal/runtime/openclaw.go`
- Modify: `internal/runtime/nanoclaw.go`
- Modify: `internal/runtime/zeroclaw.go`
- Modify: `internal/runtime/picoclaw.go`
- Modify: `internal/runtime/adapter_test.go` (update expectations)

**Context:** Each adapter's `PodTemplate()` currently returns `&corev1.PodTemplateSpec{}`. Replace with a private `runtimeSpec()` method that returns a `RuntimeSpec`, then call `BuildPodTemplate()`. The `HealthProbe()` and `ReadinessProbe()` methods stay unchanged (reused by `runtimeSpec()`).

**Step 1: Write test for OpenClaw PodTemplate output**

```go
// Add to internal/runtime/adapter_test.go

func TestOpenClawAdapter_PodTemplate(t *testing.T) {
	a := &OpenClawAdapter{}
	claw := &v1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-oc", Namespace: "default"},
		Spec:       v1alpha1.ClawSpec{Runtime: v1alpha1.RuntimeOpenClaw},
	}

	tmpl := a.PodTemplate(claw)

	// Should have init container + runtime container
	if len(tmpl.Spec.InitContainers) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(tmpl.Spec.InitContainers))
	}
	if len(tmpl.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(tmpl.Spec.Containers))
	}

	rc := tmpl.Spec.Containers[0]
	if rc.Image != "ghcr.io/prismer-ai/k8s4claw-openclaw:latest" {
		t.Errorf("image: got %q", rc.Image)
	}
	if len(rc.Ports) == 0 || rc.Ports[0].ContainerPort != 18900 {
		t.Error("expected port 18900")
	}
	if rc.LivenessProbe == nil {
		t.Error("expected liveness probe")
	}
}
```

**Step 2: Run test to verify it fails**

```bash
cd /home/willamhou/codes/k8s4claw && go test ./internal/runtime/ -run 'TestOpenClawAdapter_PodTemplate' -v
```

Expected: FAIL — PodTemplate returns empty spec, no containers.

**Step 3: Update all 4 adapters**

For each adapter file, replace the `PodTemplate` method and add `runtimeSpec`. Example for OpenClaw (`internal/runtime/openclaw.go`):

```go
func (a *OpenClawAdapter) PodTemplate(claw *v1alpha1.Claw) *corev1.PodTemplateSpec {
	return BuildPodTemplate(claw, a.runtimeSpec(claw))
}

func (a *OpenClawAdapter) runtimeSpec(claw *v1alpha1.Claw) *RuntimeSpec {
	return &RuntimeSpec{
		Image:          "ghcr.io/prismer-ai/k8s4claw-openclaw:latest",
		Command:        []string{"/usr/bin/claw-entrypoint"},
		Ports:          []corev1.ContainerPort{{Name: "gateway", ContainerPort: 18900}},
		Resources:      resources("500m", "1Gi", "2000m", "4Gi"),
		ConfigMode:     ConfigModeDeepMerge,
		WorkspacePath:  "/workspace",
		Env:            []corev1.EnvVar{{Name: "OPENCLAW_MODE", Value: "gateway"}},
		LivenessProbe:  a.HealthProbe(claw),
		ReadinessProbe: a.ReadinessProbe(claw),
	}
}
```

Repeat pattern for NanoClaw (port 19000, Overwrite, 100m/256Mi/500m/512Mi), ZeroClaw (port 3000, Passthrough, 50m/32Mi/200m/128Mi), PicoClaw (port 8080, Passthrough, 25m/16Mi/100m/64Mi). Use values from design doc Section 8.

**Step 4: Run all runtime tests**

```bash
cd /home/willamhou/codes/k8s4claw && go test ./internal/runtime/ -v -count=1
```

Expected: All PASS. Some existing adapter_test.go tests may need assertion updates if they checked for empty PodTemplate.

**Step 5: Commit**

```bash
git add internal/runtime/openclaw.go internal/runtime/nanoclaw.go internal/runtime/zeroclaw.go internal/runtime/picoclaw.go internal/runtime/adapter_test.go
git commit -m "feat: implement runtimeSpec for all 4 adapters"
```

---

### Task 6: Refactor Controller buildStatefulSet

**Files:**
- Modify: `internal/controller/claw_controller.go`

**Context:** Remove the placeholder container block (lines 241-255) and `buildEnvVars` function (lines 291-309). Remove `containerSecurityContext` (lines 274-289) — it's now `ContainerSecurityContext()` in `internal/runtime/pod_builder.go`. Add `VolumeClaimTemplates` to the StatefulSet spec. The `sort` import will no longer be needed.

**Step 1: Refactor buildStatefulSet**

In `internal/controller/claw_controller.go`:

1. Remove the `if len(podTemplate.Spec.Containers) == 0 { ... }` block (lines 241-255)
2. Add `VolumeClaimTemplates` to StatefulSet spec
3. Remove `containerSecurityContext()` function (lines 274-289)
4. Remove `buildEnvVars()` function (lines 291-309)
5. Clean up unused imports (`sort`)

The refactored `buildStatefulSet` should look like:

```go
func (r *ClawReconciler) buildStatefulSet(claw *clawv1alpha1.Claw, adapter clawruntime.RuntimeAdapter) *appsv1.StatefulSet {
	labels := map[string]string{
		"app.kubernetes.io/name":     "claw",
		"app.kubernetes.io/instance": claw.Name,
		"claw.prismer.ai/runtime":    string(claw.Spec.Runtime),
	}

	replicas := int32(1)
	podTemplate := adapter.PodTemplate(claw)

	if podTemplate.Labels == nil {
		podTemplate.Labels = make(map[string]string)
	}
	for k, v := range labels {
		podTemplate.Labels[k] = v
	}

	podTemplate.Spec.SecurityContext = &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true),
		FSGroup:      ptr.To(int64(1000)),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}

	gracePeriod := int64(adapter.GracefulShutdownSeconds()) + 10
	podTemplate.Spec.TerminationGracePeriodSeconds = &gracePeriod

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claw.Name,
			Namespace: claw.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:             &replicas,
			ServiceName:          claw.Name,
			Selector:             &metav1.LabelSelector{MatchLabels: labels},
			Template:             *podTemplate,
			VolumeClaimTemplates: clawruntime.BuildVolumeClaimTemplates(claw),
		},
	}
}
```

**Step 2: Verify build compiles**

```bash
cd /home/willamhou/codes/k8s4claw && go build ./...
```

Expected: clean

**Step 3: Run go vet**

```bash
cd /home/willamhou/codes/k8s4claw && go vet ./...
```

Expected: clean

**Step 4: Commit**

```bash
git add internal/controller/claw_controller.go
git commit -m "refactor: remove placeholder container, use PodBuilder output"
```

---

### Task 7: Update Envtest Tests

**Files:**
- Modify: `internal/controller/claw_controller_test.go`

**Context:** `TestClawReconciler_StatefulSetCreated` currently expects `busybox:latest` image, no init containers, and env vars from `buildEnvVars`. Update to match new PodBuilder output: `k8s4claw-openclaw:latest` image, 1 init container named `claw-init`, volumes present.

**Step 1: Update TestStatefulSetCreated assertions**

Key changes:
- Image: `busybox:latest` → `ghcr.io/prismer-ai/k8s4claw-openclaw:latest`
- Init container: verify `claw-init` exists
- Volumes: verify `ipc-socket`, `wal-data`, `config-vol`, `tmp` present
- Container env: verify `CLAW_NAME`, `IPC_SOCKET_PATH` present

**Step 2: Run envtest tests**

```bash
cd /home/willamhou/codes/k8s4claw && KUBEBUILDER_ASSETS=$(setup-envtest use --bin-dir bin/k8s -p path) go test ./internal/controller/ -v -count=1 -timeout 120s
```

Expected: All 5 tests PASS

**Step 3: Commit**

```bash
git add internal/controller/claw_controller_test.go
git commit -m "test: update envtest assertions for PodBuilder output"
```

---

### Task 8: Channel Sidecar Injection

**Files:**
- Create: `internal/controller/channel_sidecar.go`
- Create: `internal/controller/channel_sidecar_test.go`

**Context:** `injectChannelSidecars` fetches ClawChannel CRs, resolves sidecar specs, builds containers, and appends them to the PodTemplate. It handles missing channels gracefully (soft failure). `NativeSidecarsEnabled` field must be added to `ClawReconciler`.

**Step 1: Add NativeSidecarsEnabled to ClawReconciler**

In `internal/controller/claw_controller.go`, add field:

```go
type ClawReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	Registry              *clawruntime.Registry
	NativeSidecarsEnabled bool
}
```

**Step 2: Write unit test for buildChannelContainer**

Since `injectChannelSidecars` requires a real K8s client (fetches ClawChannel CRs), test the container-building logic separately:

```go
// internal/controller/channel_sidecar_test.go
package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func TestBuildChannelContainer_Native(t *testing.T) {
	channel := &clawv1alpha1.ClawChannel{
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeSlack,
			Mode: clawv1alpha1.ChannelModeBidirectional,
		},
	}
	ref := clawv1alpha1.ChannelRef{Name: "slack-team", Mode: clawv1alpha1.ChannelModeBidirectional}

	c := buildChannelContainer(channel, ref, true)

	if c.Name != "channel-slack-team" {
		t.Errorf("name: got %q", c.Name)
	}
	if c.RestartPolicy == nil || *c.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Error("expected restartPolicy=Always for native sidecar")
	}
	// Verify ipc-socket mount
	foundIPC := false
	for _, m := range c.VolumeMounts {
		if m.Name == "ipc-socket" {
			foundIPC = true
		}
	}
	if !foundIPC {
		t.Error("missing ipc-socket mount")
	}
}

func TestBuildChannelContainer_Regular(t *testing.T) {
	channel := &clawv1alpha1.ClawChannel{
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeWebhook,
			Mode: clawv1alpha1.ChannelModeInbound,
		},
	}
	ref := clawv1alpha1.ChannelRef{Name: "webhook-in", Mode: clawv1alpha1.ChannelModeInbound}

	c := buildChannelContainer(channel, ref, false)

	if c.RestartPolicy != nil {
		t.Error("expected no restartPolicy for regular sidecar")
	}
}

func TestIsModeCompatible(t *testing.T) {
	tests := []struct {
		requested, capability clawv1alpha1.ChannelMode
		want                 bool
	}{
		{clawv1alpha1.ChannelModeInbound, clawv1alpha1.ChannelModeBidirectional, true},
		{clawv1alpha1.ChannelModeBidirectional, clawv1alpha1.ChannelModeInbound, false},
		{clawv1alpha1.ChannelModeInbound, clawv1alpha1.ChannelModeInbound, true},
	}
	for _, tt := range tests {
		got := isModeCompatible(tt.requested, tt.capability)
		if got != tt.want {
			t.Errorf("isModeCompatible(%s, %s) = %v, want %v",
				tt.requested, tt.capability, got, tt.want)
		}
	}
}
```

**Step 3: Implement channel_sidecar.go**

```go
// internal/controller/channel_sidecar.go
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

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)
```

Implement: `injectChannelSidecars`, `buildChannelContainer`, `channelVolumeMounts`, `channelDefaultResources`, `isModeCompatible`, `builtinChannelImage`.

Key behaviors:
- Fetch each ClawChannel; if NotFound → record as missing, continue
- Validate mode compatibility; if incompatible → record, continue
- Return list of missing/incompatible channel names for condition reporting
- For built-in types: image `ghcr.io/prismer-ai/claw-channel-<type>:latest`
- For custom type: use `channel.Spec.Sidecar`
- Inject credentials via `envFrom` if `channel.Spec.Credentials.SecretRef` set
- Inject config as `CHANNEL_CONFIG` JSON env var

**Step 4: Run tests**

```bash
cd /home/willamhou/codes/k8s4claw && go test ./internal/controller/ -run 'TestBuildChannelContainer|TestIsModeCompatible' -v
```

Expected: PASS

**Step 5: Run go vet**

```bash
cd /home/willamhou/codes/k8s4claw && go vet ./...
```

Expected: clean

**Step 6: Commit**

```bash
git add internal/controller/channel_sidecar.go internal/controller/channel_sidecar_test.go internal/controller/claw_controller.go
git commit -m "feat: add channel sidecar injection with native/regular mode"
```

---

### Task 9: Wire Feature Gate + Update main.go

**Files:**
- Modify: `cmd/operator/main.go`
- Modify: `internal/controller/suite_test.go`

**Context:** Add `--enable-native-sidecars` CLI flag. Pass to `ClawReconciler.NativeSidecarsEnabled`. Update `suite_test.go` to set the field.

**Step 1: Update main.go**

Add flag and pass to reconciler:

```go
var enableNativeSidecars bool
flag.BoolVar(&enableNativeSidecars, "enable-native-sidecars", true,
    "Use native sidecars (K8s 1.28+). Set false for older clusters.")
```

In reconciler construction:

```go
&controller.ClawReconciler{
    Client:                mgr.GetClient(),
    Scheme:                mgr.GetScheme(),
    Registry:              registry,
    NativeSidecarsEnabled: enableNativeSidecars,
}
```

**Step 2: Update suite_test.go**

Set `NativeSidecarsEnabled: true` in the test reconciler setup.

**Step 3: Verify build**

```bash
cd /home/willamhou/codes/k8s4claw && go build ./cmd/operator/
```

Expected: clean

**Step 4: Run all tests**

```bash
cd /home/willamhou/codes/k8s4claw && KUBEBUILDER_ASSETS=$(setup-envtest use --bin-dir bin/k8s -p path) go test ./... -v -count=1 -timeout 120s
```

Expected: All PASS

**Step 5: Commit**

```bash
git add cmd/operator/main.go internal/controller/suite_test.go
git commit -m "feat: add --enable-native-sidecars feature gate flag"
```

---

### Task 10: Full Test Suite + Coverage

**Files:** None (verification only)

**Step 1: Run full test suite with race detection**

```bash
cd /home/willamhou/codes/k8s4claw && KUBEBUILDER_ASSETS=$(setup-envtest use --bin-dir bin/k8s -p path) go test -race -cover ./... -timeout 120s
```

Expected: All PASS, coverage ≥ 80% for `internal/runtime/`

**Step 2: Run go vet on entire project**

```bash
cd /home/willamhou/codes/k8s4claw && go vet ./...
```

Expected: clean

**Step 3: Verify build**

```bash
cd /home/willamhou/codes/k8s4claw && go build ./...
```

Expected: clean
