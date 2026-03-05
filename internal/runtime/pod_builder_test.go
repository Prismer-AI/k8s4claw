package runtime

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// Helper: build a minimal Claw with no persistence.
// ---------------------------------------------------------------------------

func minimalClaw() *v1alpha1.Claw {
	return &v1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-claw",
			Namespace: "default",
		},
		Spec: v1alpha1.ClawSpec{
			Runtime: v1alpha1.RuntimeOpenClaw,
		},
	}
}

// minimalSpec returns a RuntimeSpec with the minimum required fields.
func minimalSpec() *RuntimeSpec {
	return &RuntimeSpec{
		Image:   "ghcr.io/prismer-ai/openclaw:latest",
		Command: []string{"/openclaw"},
		Args:    []string{"--port", "18900"},
		Ports: []corev1.ContainerPort{
			{Name: "gateway", ContainerPort: 18900, Protocol: corev1.ProtocolTCP},
		},
		Resources: resources("500m", "512Mi", "2", "2Gi"),
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: portIntStr(18900)},
			},
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: portIntStr(18900)},
			},
		},
		ConfigMode:    ConfigModeOverwrite,
		WorkspacePath: "/workspace",
	}
}

// ---------------------------------------------------------------------------
// TestEmptyDirVolume
// ---------------------------------------------------------------------------

func TestEmptyDirVolume(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		volName    string
		sizeLimit  *resource.Quantity
		medium     corev1.StorageMedium
		wantMedium corev1.StorageMedium
		wantLimit  bool
	}{
		{
			name:       "basic emptyDir without size limit",
			volName:    "tmp",
			sizeLimit:  nil,
			medium:     "",
			wantMedium: "",
			wantLimit:  false,
		},
		{
			name:       "emptyDir with size limit",
			volName:    "wal-data",
			sizeLimit:  ptr.To(resource.MustParse("512Mi")),
			medium:     "",
			wantMedium: "",
			wantLimit:  true,
		},
		{
			name:       "emptyDir with tmpfs medium",
			volName:    "cache",
			sizeLimit:  ptr.To(resource.MustParse("1Gi")),
			medium:     corev1.StorageMediumMemory,
			wantMedium: corev1.StorageMediumMemory,
			wantLimit:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			vol := emptyDirVolume(tt.volName, tt.sizeLimit, tt.medium)

			if vol.Name != tt.volName {
				t.Errorf("Name = %q; want %q", vol.Name, tt.volName)
			}
			if vol.VolumeSource.EmptyDir == nil {
				t.Fatal("EmptyDir is nil")
			}
			if vol.VolumeSource.EmptyDir.Medium != tt.wantMedium {
				t.Errorf("Medium = %q; want %q", vol.VolumeSource.EmptyDir.Medium, tt.wantMedium)
			}
			if tt.wantLimit {
				if vol.VolumeSource.EmptyDir.SizeLimit == nil {
					t.Fatal("SizeLimit is nil; want non-nil")
				}
				if !vol.VolumeSource.EmptyDir.SizeLimit.Equal(*tt.sizeLimit) {
					t.Errorf("SizeLimit = %s; want %s", vol.VolumeSource.EmptyDir.SizeLimit.String(), tt.sizeLimit.String())
				}
			} else if vol.VolumeSource.EmptyDir.SizeLimit != nil {
				t.Errorf("SizeLimit = %s; want nil", vol.VolumeSource.EmptyDir.SizeLimit.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestConfigMapVolume
// ---------------------------------------------------------------------------

func TestConfigMapVolume(t *testing.T) {
	t.Parallel()

	vol := configMapVolume("config-vol", "my-config")

	if vol.Name != "config-vol" {
		t.Errorf("Name = %q; want %q", vol.Name, "config-vol")
	}
	if vol.VolumeSource.ConfigMap == nil {
		t.Fatal("ConfigMap is nil")
	}
	if vol.VolumeSource.ConfigMap.Name != "my-config" {
		t.Errorf("ConfigMap.Name = %q; want %q", vol.VolumeSource.ConfigMap.Name, "my-config")
	}
	if vol.VolumeSource.ConfigMap.Optional == nil || !*vol.VolumeSource.ConfigMap.Optional {
		t.Error("ConfigMap.Optional must be ptr.To(true)")
	}
}

// ---------------------------------------------------------------------------
// TestPVCVolume
// ---------------------------------------------------------------------------

func TestPVCVolume(t *testing.T) {
	t.Parallel()

	vol := pvcVolume("shared-data", "my-pvc")

	if vol.Name != "shared-data" {
		t.Errorf("Name = %q; want %q", vol.Name, "shared-data")
	}
	if vol.VolumeSource.PersistentVolumeClaim == nil {
		t.Fatal("PersistentVolumeClaim is nil")
	}
	if vol.VolumeSource.PersistentVolumeClaim.ClaimName != "my-pvc" {
		t.Errorf("ClaimName = %q; want %q", vol.VolumeSource.PersistentVolumeClaim.ClaimName, "my-pvc")
	}
}

// ---------------------------------------------------------------------------
// TestBuildVolumes_Minimal
// ---------------------------------------------------------------------------

func TestBuildVolumes_Minimal(t *testing.T) {
	t.Parallel()

	claw := minimalClaw()
	spec := minimalSpec()
	vols := buildVolumes(claw, spec)

	// Expect 4 base volumes: ipc-socket, wal-data, config-vol, tmp
	wantNames := map[string]bool{
		"ipc-socket": true,
		"wal-data":   true,
		"config-vol": true,
		"tmp":        true,
	}

	if len(vols) != len(wantNames) {
		t.Fatalf("got %d volumes; want %d", len(vols), len(wantNames))
	}

	gotNames := make(map[string]bool)
	for _, v := range vols {
		gotNames[v.Name] = true
	}
	for name := range wantNames {
		if !gotNames[name] {
			t.Errorf("missing volume %q", name)
		}
	}

	// Verify wal-data has 512Mi size limit.
	for _, v := range vols {
		if v.Name == "wal-data" {
			if v.VolumeSource.EmptyDir == nil || v.VolumeSource.EmptyDir.SizeLimit == nil {
				t.Fatal("wal-data must have SizeLimit")
			}
			want := resource.MustParse("512Mi")
			if !v.VolumeSource.EmptyDir.SizeLimit.Equal(want) {
				t.Errorf("wal-data SizeLimit = %s; want 512Mi", v.VolumeSource.EmptyDir.SizeLimit.String())
			}
		}
	}

	// Verify config-vol references the correct ConfigMap name.
	for _, v := range vols {
		if v.Name == "config-vol" {
			if v.VolumeSource.ConfigMap == nil {
				t.Fatal("config-vol must be a ConfigMap volume")
			}
			wantCMName := claw.Name + "-config"
			if v.VolumeSource.ConfigMap.Name != wantCMName {
				t.Errorf("ConfigMap.Name = %q; want %q", v.VolumeSource.ConfigMap.Name, wantCMName)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TestBuildVolumes_WithPersistence
// ---------------------------------------------------------------------------

func TestBuildVolumes_WithPersistence(t *testing.T) {
	t.Parallel()

	claw := minimalClaw()
	claw.Spec.Persistence = &v1alpha1.PersistenceSpec{
		Cache: &v1alpha1.CacheSpec{
			Enabled:   true,
			Medium:    corev1.StorageMediumMemory,
			Size:      "1Gi",
			MountPath: "/var/cache/claw",
		},
		Shared: []v1alpha1.SharedVolumeRef{
			{Name: "models", ClaimName: "model-data-pvc", MountPath: "/data/models", ReadOnly: true},
			{Name: "datasets", ClaimName: "dataset-pvc", MountPath: "/data/datasets", ReadOnly: true},
		},
		// Session, Output, Workspace should NOT appear in buildVolumes (they go to volumeClaimTemplates).
		Session: &v1alpha1.VolumeSpec{
			Enabled:   true,
			Size:      "2Gi",
			MountPath: "/var/lib/claw/session",
		},
		Output: &v1alpha1.OutputVolumeSpec{
			VolumeSpec: v1alpha1.VolumeSpec{
				Enabled:   true,
				Size:      "5Gi",
				MountPath: "/var/lib/claw/output",
			},
		},
		Workspace: &v1alpha1.VolumeSpec{
			Enabled:   true,
			Size:      "10Gi",
			MountPath: "/workspace",
		},
	}

	spec := minimalSpec()
	vols := buildVolumes(claw, spec)

	// Base (4) + cache (1) + shared (2) = 7
	// Session, Output, Workspace must NOT be included.
	wantNames := map[string]bool{
		"ipc-socket":      true,
		"wal-data":        true,
		"config-vol":      true,
		"tmp":             true,
		"cache":           true,
		"shared-models":   true,
		"shared-datasets": true,
	}

	gotNames := make(map[string]bool)
	for _, v := range vols {
		gotNames[v.Name] = true
	}

	if len(vols) != len(wantNames) {
		t.Fatalf("got %d volumes %v; want %d", len(vols), gotNames, len(wantNames))
	}

	for name := range wantNames {
		if !gotNames[name] {
			t.Errorf("missing volume %q", name)
		}
	}

	// Session, output, workspace must NOT appear.
	for _, forbidden := range []string{"session", "output", "workspace"} {
		if gotNames[forbidden] {
			t.Errorf("volume %q should NOT be in buildVolumes (it belongs in volumeClaimTemplates)", forbidden)
		}
	}

	// Verify cache is tmpfs.
	for _, v := range vols {
		if v.Name == "cache" {
			if v.VolumeSource.EmptyDir == nil {
				t.Fatal("cache must be emptyDir")
			}
			if v.VolumeSource.EmptyDir.Medium != corev1.StorageMediumMemory {
				t.Errorf("cache medium = %q; want Memory", v.VolumeSource.EmptyDir.Medium)
			}
		}
	}

	// Verify shared volumes are PVC refs.
	for _, v := range vols {
		if v.Name == "shared-models" {
			if v.VolumeSource.PersistentVolumeClaim == nil {
				t.Fatal("shared-models must be a PVC volume")
			}
			if v.VolumeSource.PersistentVolumeClaim.ClaimName != "model-data-pvc" {
				t.Errorf("shared-models ClaimName = %q; want %q", v.VolumeSource.PersistentVolumeClaim.ClaimName, "model-data-pvc")
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TestBuildVolumes_ExtraVolumes
// ---------------------------------------------------------------------------

func TestBuildVolumes_ExtraVolumes(t *testing.T) {
	t.Parallel()

	claw := minimalClaw()
	spec := minimalSpec()
	spec.ExtraVolumes = []corev1.Volume{
		{
			Name: "custom-vol",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}

	vols := buildVolumes(claw, spec)

	// Base (4) + extra (1) = 5
	if len(vols) != 5 {
		t.Fatalf("got %d volumes; want 5", len(vols))
	}

	found := false
	for _, v := range vols {
		if v.Name == "custom-vol" {
			found = true
			break
		}
	}
	if !found {
		t.Error("ExtraVolumes not appended")
	}
}

// ---------------------------------------------------------------------------
// TestBuildInitContainer
// ---------------------------------------------------------------------------

func TestBuildInitContainer(t *testing.T) {
	t.Parallel()

	claw := minimalClaw()
	claw.Spec.Persistence = &v1alpha1.PersistenceSpec{
		Workspace: &v1alpha1.VolumeSpec{
			Enabled:   true,
			Size:      "10Gi",
			MountPath: "/workspace",
		},
	}

	spec := minimalSpec()
	spec.ConfigMode = ConfigModeDeepMerge

	ic := buildInitContainer(claw, spec)

	// Verify name and image.
	if ic.Name != "claw-init" {
		t.Errorf("Name = %q; want %q", ic.Name, "claw-init")
	}
	if ic.Image != InitContainerImage {
		t.Errorf("Image = %q; want %q", ic.Image, InitContainerImage)
	}

	// Verify command.
	if len(ic.Command) != 1 || ic.Command[0] != "/claw-init" {
		t.Errorf("Command = %v; want [\"/claw-init\"]", ic.Command)
	}

	// Verify args contain --mode deepmerge.
	wantArgs := []string{"--mode", "deepmerge", "--workspace", "/workspace", "--runtime", "openclaw"}
	if len(ic.Args) != len(wantArgs) {
		t.Fatalf("Args = %v; want %v", ic.Args, wantArgs)
	}
	for i, want := range wantArgs {
		if ic.Args[i] != want {
			t.Errorf("Args[%d] = %q; want %q", i, ic.Args[i], want)
		}
	}

	// Verify env vars.
	envMap := envToMap(ic.Env)
	for _, key := range []string{"CLAW_NAME", "CLAW_NAMESPACE", "IPC_SOCKET_PATH"} {
		if _, ok := envMap[key]; !ok {
			t.Errorf("missing env var %q", key)
		}
	}
	if envMap["CLAW_NAME"] != "test-claw" {
		t.Errorf("CLAW_NAME = %q; want %q", envMap["CLAW_NAME"], "test-claw")
	}

	// Verify volume mounts include config-vol (RO) and tmp.
	mountMap := mountsToMap(ic.VolumeMounts)
	if _, ok := mountMap["config-vol"]; !ok {
		t.Error("missing volume mount config-vol")
	}
	if !mountMap["config-vol"].ReadOnly {
		t.Error("config-vol must be ReadOnly")
	}
	if _, ok := mountMap["tmp"]; !ok {
		t.Error("missing volume mount tmp")
	}

	// Verify workspace mount when workspace PVC is enabled.
	if _, ok := mountMap["workspace"]; !ok {
		t.Error("missing workspace mount when persistence.workspace.enabled=true")
	}

	// Verify resources.
	wantCPUReq := resource.MustParse("100m")
	wantMemReq := resource.MustParse("128Mi")
	wantCPULim := resource.MustParse("500m")
	wantMemLim := resource.MustParse("256Mi")

	if !ic.Resources.Requests.Cpu().Equal(wantCPUReq) {
		t.Errorf("CPU request = %s; want 100m", ic.Resources.Requests.Cpu().String())
	}
	if !ic.Resources.Requests.Memory().Equal(wantMemReq) {
		t.Errorf("Memory request = %s; want 128Mi", ic.Resources.Requests.Memory().String())
	}
	if !ic.Resources.Limits.Cpu().Equal(wantCPULim) {
		t.Errorf("CPU limit = %s; want 500m", ic.Resources.Limits.Cpu().String())
	}
	if !ic.Resources.Limits.Memory().Equal(wantMemLim) {
		t.Errorf("Memory limit = %s; want 256Mi", ic.Resources.Limits.Memory().String())
	}

	// Verify security context.
	if ic.SecurityContext == nil {
		t.Fatal("SecurityContext is nil")
	}
	assertSecurityContext(t, ic.SecurityContext)
}

// ---------------------------------------------------------------------------
// TestBuildInitContainer_DefaultConfigMode
// ---------------------------------------------------------------------------

func TestBuildInitContainer_DefaultConfigMode(t *testing.T) {
	t.Parallel()

	claw := minimalClaw()
	spec := minimalSpec()
	spec.ConfigMode = "" // zero value should default to overwrite

	ic := buildInitContainer(claw, spec)

	// Verify --mode defaults to overwrite.
	for i, arg := range ic.Args {
		if arg == "--mode" && i+1 < len(ic.Args) {
			if ic.Args[i+1] != "overwrite" {
				t.Errorf("default --mode = %q; want %q", ic.Args[i+1], "overwrite")
			}
			return
		}
	}
	t.Error("--mode argument not found in init container args")
}

// ---------------------------------------------------------------------------
// TestBuildInitContainer_NoWorkspace
// ---------------------------------------------------------------------------

func TestBuildInitContainer_NoWorkspace(t *testing.T) {
	t.Parallel()

	claw := minimalClaw() // no persistence
	spec := minimalSpec()

	ic := buildInitContainer(claw, spec)

	mountMap := mountsToMap(ic.VolumeMounts)
	if _, ok := mountMap["workspace"]; ok {
		t.Error("workspace mount should NOT be present when no workspace PVC is configured")
	}
}

// ---------------------------------------------------------------------------
// TestBuildRuntimeContainer
// ---------------------------------------------------------------------------

func TestBuildRuntimeContainer(t *testing.T) {
	t.Parallel()

	claw := minimalClaw()
	claw.Spec.Persistence = &v1alpha1.PersistenceSpec{
		Cache: &v1alpha1.CacheSpec{
			Enabled:   true,
			Medium:    corev1.StorageMediumMemory,
			Size:      "1Gi",
			MountPath: "/var/cache/claw",
		},
		Session: &v1alpha1.VolumeSpec{
			Enabled:   true,
			Size:      "2Gi",
			MountPath: "/var/lib/claw/session",
		},
		Output: &v1alpha1.OutputVolumeSpec{
			VolumeSpec: v1alpha1.VolumeSpec{
				Enabled:   true,
				Size:      "5Gi",
				MountPath: "/var/lib/claw/output",
			},
		},
		Workspace: &v1alpha1.VolumeSpec{
			Enabled:   true,
			Size:      "10Gi",
			MountPath: "/workspace",
		},
		Shared: []v1alpha1.SharedVolumeRef{
			{Name: "models", ClaimName: "model-pvc", MountPath: "/data/models", ReadOnly: true},
		},
	}

	spec := minimalSpec()
	spec.Env = []corev1.EnvVar{
		{Name: "CUSTOM_VAR", Value: "custom-value"},
		// Override a shared env var to verify last-wins.
		{Name: "CLAW_NAME", Value: "overridden"},
	}
	spec.ExtraVolumeMounts = []corev1.VolumeMount{
		{Name: "custom-mount", MountPath: "/custom"},
	}

	c := buildRuntimeContainer(claw, spec)

	// Verify name.
	if c.Name != "runtime" {
		t.Errorf("Name = %q; want %q", c.Name, "runtime")
	}

	// Verify image.
	if c.Image != spec.Image {
		t.Errorf("Image = %q; want %q", c.Image, spec.Image)
	}

	// Verify command and args.
	if len(c.Command) != 1 || c.Command[0] != "/openclaw" {
		t.Errorf("Command = %v; want %v", c.Command, spec.Command)
	}
	if len(c.Args) != 2 || c.Args[0] != "--port" || c.Args[1] != "18900" {
		t.Errorf("Args = %v; want %v", c.Args, spec.Args)
	}

	// Verify env ordering: shared first, then spec.Env (last-wins).
	envMap := envToMap(c.Env)
	if _, ok := envMap["CLAW_NAMESPACE"]; !ok {
		t.Error("missing shared env CLAW_NAMESPACE")
	}
	if _, ok := envMap["IPC_SOCKET_PATH"]; !ok {
		t.Error("missing shared env IPC_SOCKET_PATH")
	}
	if _, ok := envMap["CUSTOM_VAR"]; !ok {
		t.Error("missing spec env CUSTOM_VAR")
	}
	// CLAW_NAME should be overridden by spec.Env (last-wins).
	if envMap["CLAW_NAME"] != "overridden" {
		t.Errorf("CLAW_NAME = %q; want %q (last-wins from spec.Env)", envMap["CLAW_NAME"], "overridden")
	}

	// Verify base volume mounts.
	mountMap := mountsToMap(c.VolumeMounts)
	baseMounts := map[string]string{
		"ipc-socket": "/var/run/claw",
		"wal-data":   "/var/lib/claw/wal",
		"config-vol": "/etc/claw",
		"tmp":        "/tmp",
	}
	for name, path := range baseMounts {
		vm, ok := mountMap[name]
		if !ok {
			t.Errorf("missing base mount %q", name)
			continue
		}
		if vm.MountPath != path {
			t.Errorf("mount %q path = %q; want %q", name, vm.MountPath, path)
		}
	}

	// Verify config-vol is read-only.
	if !mountMap["config-vol"].ReadOnly {
		t.Error("config-vol must be ReadOnly")
	}

	// Verify conditional mounts.
	conditionalMounts := map[string]string{
		"cache":         "/var/cache/claw",
		"session":       "/var/lib/claw/session",
		"output":        "/var/lib/claw/output",
		"workspace":     "/workspace",
		"shared-models": "/data/models",
	}
	for name, path := range conditionalMounts {
		vm, ok := mountMap[name]
		if !ok {
			t.Errorf("missing conditional mount %q", name)
			continue
		}
		if vm.MountPath != path {
			t.Errorf("mount %q path = %q; want %q", name, vm.MountPath, path)
		}
	}

	// Verify shared volume ReadOnly flag.
	if !mountMap["shared-models"].ReadOnly {
		t.Error("shared-models must be ReadOnly")
	}

	// Verify extra volume mounts appended.
	if _, ok := mountMap["custom-mount"]; !ok {
		t.Error("missing extra volume mount custom-mount")
	}

	// Verify probes.
	if c.LivenessProbe == nil {
		t.Error("LivenessProbe is nil")
	}
	if c.ReadinessProbe == nil {
		t.Error("ReadinessProbe is nil")
	}

	// Verify security context.
	if c.SecurityContext == nil {
		t.Fatal("SecurityContext is nil")
	}
	assertSecurityContext(t, c.SecurityContext)

	// Verify ports.
	if len(c.Ports) != 1 || c.Ports[0].ContainerPort != 18900 {
		t.Errorf("Ports = %v; want port 18900", c.Ports)
	}

	// Verify resources.
	if c.Resources.Requests.Cpu().String() != "500m" {
		t.Errorf("CPU request = %s; want 500m", c.Resources.Requests.Cpu().String())
	}
}

// ---------------------------------------------------------------------------
// TestBuildPodTemplate
// ---------------------------------------------------------------------------

func TestBuildPodTemplate(t *testing.T) {
	t.Parallel()

	claw := minimalClaw()
	spec := minimalSpec()

	tmpl := BuildPodTemplate(claw, spec)

	if tmpl == nil {
		t.Fatal("BuildPodTemplate returned nil")
	}

	// Verify 1 init container.
	if len(tmpl.Spec.InitContainers) != 1 {
		t.Fatalf("InitContainers count = %d; want 1", len(tmpl.Spec.InitContainers))
	}
	if tmpl.Spec.InitContainers[0].Name != "claw-init" {
		t.Errorf("init container name = %q; want %q", tmpl.Spec.InitContainers[0].Name, "claw-init")
	}

	// Verify 1 container.
	if len(tmpl.Spec.Containers) != 1 {
		t.Fatalf("Containers count = %d; want 1", len(tmpl.Spec.Containers))
	}
	if tmpl.Spec.Containers[0].Name != "runtime" {
		t.Errorf("container name = %q; want %q", tmpl.Spec.Containers[0].Name, "runtime")
	}

	// Verify at least 4 volumes (base volumes).
	if len(tmpl.Spec.Volumes) < 4 {
		t.Errorf("Volumes count = %d; want >= 4", len(tmpl.Spec.Volumes))
	}

	// Verify no labels set (controller's job).
	if len(tmpl.Labels) != 0 {
		t.Errorf("Labels = %v; want empty (controller sets labels)", tmpl.Labels)
	}

	// Verify no pod security context (controller's job).
	if tmpl.Spec.SecurityContext != nil {
		t.Errorf("SecurityContext = %v; want nil (controller sets pod security context)", tmpl.Spec.SecurityContext)
	}

	// Verify no terminationGracePeriodSeconds (controller's job).
	if tmpl.Spec.TerminationGracePeriodSeconds != nil {
		t.Errorf("TerminationGracePeriodSeconds = %v; want nil (controller sets this)", tmpl.Spec.TerminationGracePeriodSeconds)
	}
}

// ---------------------------------------------------------------------------
// TestBuildVolumeClaimTemplates_NoPersistence
// ---------------------------------------------------------------------------

func TestBuildVolumeClaimTemplates_NoPersistence(t *testing.T) {
	t.Parallel()

	claw := minimalClaw()
	templates := BuildVolumeClaimTemplates(claw)

	if templates != nil {
		t.Errorf("got %d templates; want nil", len(templates))
	}
}

// ---------------------------------------------------------------------------
// TestBuildVolumeClaimTemplates_WithPersistence
// ---------------------------------------------------------------------------

func TestBuildVolumeClaimTemplates_WithPersistence(t *testing.T) {
	t.Parallel()

	claw := minimalClaw()
	claw.Spec.Persistence = &v1alpha1.PersistenceSpec{
		Session: &v1alpha1.VolumeSpec{
			Enabled:      true,
			Size:         "2Gi",
			MountPath:    "/var/lib/claw/session",
			StorageClass: "fast-ssd",
		},
		Output: &v1alpha1.OutputVolumeSpec{
			VolumeSpec: v1alpha1.VolumeSpec{
				Enabled:   true,
				Size:      "5Gi",
				MountPath: "/var/lib/claw/output",
			},
		},
		Workspace: &v1alpha1.VolumeSpec{
			Enabled:      true,
			Size:         "10Gi",
			MountPath:    "/workspace",
			StorageClass: "standard",
		},
	}

	templates := BuildVolumeClaimTemplates(claw)

	if len(templates) != 3 {
		t.Fatalf("got %d templates; want 3", len(templates))
	}

	nameMap := make(map[string]corev1.PersistentVolumeClaim)
	for _, tpl := range templates {
		nameMap[tpl.Name] = tpl
	}

	// Verify session PVC.
	session, ok := nameMap["session"]
	if !ok {
		t.Fatal("missing session PVC template")
	}
	wantSize := resource.MustParse("2Gi")
	gotSize := session.Spec.Resources.Requests[corev1.ResourceStorage]
	if !gotSize.Equal(wantSize) {
		t.Errorf("session size = %s; want 2Gi", gotSize.String())
	}
	if session.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("session access mode = %v; want ReadWriteOnce", session.Spec.AccessModes[0])
	}
	if session.Spec.StorageClassName == nil || *session.Spec.StorageClassName != "fast-ssd" {
		t.Errorf("session storage class = %v; want fast-ssd", session.Spec.StorageClassName)
	}

	// Verify output PVC.
	output, ok := nameMap["output"]
	if !ok {
		t.Fatal("missing output PVC template")
	}
	wantOutputSize := resource.MustParse("5Gi")
	gotOutputSize := output.Spec.Resources.Requests[corev1.ResourceStorage]
	if !gotOutputSize.Equal(wantOutputSize) {
		t.Errorf("output size = %s; want 5Gi", gotOutputSize.String())
	}
	// No storage class specified; should be nil.
	if output.Spec.StorageClassName != nil {
		t.Errorf("output storage class = %v; want nil (use cluster default)", output.Spec.StorageClassName)
	}

	// Verify workspace PVC.
	workspace, ok := nameMap["workspace"]
	if !ok {
		t.Fatal("missing workspace PVC template")
	}
	wantWSSize := resource.MustParse("10Gi")
	gotWSSize := workspace.Spec.Resources.Requests[corev1.ResourceStorage]
	if !gotWSSize.Equal(wantWSSize) {
		t.Errorf("workspace size = %s; want 10Gi", gotWSSize.String())
	}

	// Verify claw.prismer.ai/instance label is set on all templates.
	for _, tpl := range templates {
		got := tpl.Labels["claw.prismer.ai/instance"]
		if got != claw.Name {
			t.Errorf("template %q label claw.prismer.ai/instance = %q; want %q", tpl.Name, got, claw.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// TestBuildVolumeClaimTemplates_PartialPersistence
// ---------------------------------------------------------------------------

func TestBuildVolumeClaimTemplates_PartialPersistence(t *testing.T) {
	t.Parallel()

	claw := minimalClaw()
	claw.Spec.Persistence = &v1alpha1.PersistenceSpec{
		Session: &v1alpha1.VolumeSpec{
			Enabled:   true,
			Size:      "2Gi",
			MountPath: "/var/lib/claw/session",
		},
		// Output not configured
		Workspace: &v1alpha1.VolumeSpec{
			Enabled:   false, // Explicitly disabled
			Size:      "10Gi",
			MountPath: "/workspace",
		},
	}

	templates := BuildVolumeClaimTemplates(claw)

	// Only session (output nil, workspace disabled)
	if len(templates) != 1 {
		t.Fatalf("got %d templates; want 1", len(templates))
	}
	if templates[0].Name != "session" {
		t.Errorf("template name = %q; want %q", templates[0].Name, "session")
	}
}

// ---------------------------------------------------------------------------
// TestContainerSecurityContext
// ---------------------------------------------------------------------------

func TestContainerSecurityContext(t *testing.T) {
	t.Parallel()

	sc := ContainerSecurityContext()
	if sc == nil {
		t.Fatal("ContainerSecurityContext() returned nil")
	}
	assertSecurityContext(t, sc)
}

// ---------------------------------------------------------------------------
// TestResources
// ---------------------------------------------------------------------------

func TestResources(t *testing.T) {
	t.Parallel()

	r := resources("250m", "256Mi", "1", "1Gi")

	wantCPUReq := resource.MustParse("250m")
	wantMemReq := resource.MustParse("256Mi")
	wantCPULim := resource.MustParse("1")
	wantMemLim := resource.MustParse("1Gi")

	if !r.Requests.Cpu().Equal(wantCPUReq) {
		t.Errorf("CPU request = %s; want 250m", r.Requests.Cpu().String())
	}
	if !r.Requests.Memory().Equal(wantMemReq) {
		t.Errorf("Memory request = %s; want 256Mi", r.Requests.Memory().String())
	}
	if !r.Limits.Cpu().Equal(wantCPULim) {
		t.Errorf("CPU limit = %s; want 1", r.Limits.Cpu().String())
	}
	if !r.Limits.Memory().Equal(wantMemLim) {
		t.Errorf("Memory limit = %s; want 1Gi", r.Limits.Memory().String())
	}
}

// ---------------------------------------------------------------------------
// TestSharedEnvVars
// ---------------------------------------------------------------------------

func TestSharedEnvVars(t *testing.T) {
	t.Parallel()

	claw := minimalClaw()
	envs := sharedEnvVars(claw)

	envMap := envToMap(envs)

	if envMap["CLAW_NAME"] != "test-claw" {
		t.Errorf("CLAW_NAME = %q; want %q", envMap["CLAW_NAME"], "test-claw")
	}
	if envMap["CLAW_NAMESPACE"] != "default" {
		t.Errorf("CLAW_NAMESPACE = %q; want %q", envMap["CLAW_NAMESPACE"], "default")
	}
	if _, ok := envMap["IPC_SOCKET_PATH"]; !ok {
		t.Error("missing IPC_SOCKET_PATH")
	}
}

// ---------------------------------------------------------------------------
// TestHasWorkspacePVC
// ---------------------------------------------------------------------------

func TestHasWorkspacePVC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		claw *v1alpha1.Claw
		want bool
	}{
		{
			name: "no persistence",
			claw: minimalClaw(),
			want: false,
		},
		{
			name: "persistence without workspace",
			claw: func() *v1alpha1.Claw {
				c := minimalClaw()
				c.Spec.Persistence = &v1alpha1.PersistenceSpec{}
				return c
			}(),
			want: false,
		},
		{
			name: "workspace disabled",
			claw: func() *v1alpha1.Claw {
				c := minimalClaw()
				c.Spec.Persistence = &v1alpha1.PersistenceSpec{
					Workspace: &v1alpha1.VolumeSpec{Enabled: false, Size: "10Gi", MountPath: "/workspace"},
				}
				return c
			}(),
			want: false,
		},
		{
			name: "workspace enabled",
			claw: func() *v1alpha1.Claw {
				c := minimalClaw()
				c.Spec.Persistence = &v1alpha1.PersistenceSpec{
					Workspace: &v1alpha1.VolumeSpec{Enabled: true, Size: "10Gi", MountPath: "/workspace"},
				}
				return c
			}(),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := hasWorkspacePVC(tt.claw)
			if got != tt.want {
				t.Errorf("hasWorkspacePVC() = %v; want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestInitContainerResources
// ---------------------------------------------------------------------------

func TestInitContainerResources(t *testing.T) {
	t.Parallel()

	r := initContainerResources()

	wantCPUReq := resource.MustParse("100m")
	wantMemReq := resource.MustParse("128Mi")
	wantCPULim := resource.MustParse("500m")
	wantMemLim := resource.MustParse("256Mi")

	if !r.Requests.Cpu().Equal(wantCPUReq) {
		t.Errorf("CPU request = %s; want 100m", r.Requests.Cpu().String())
	}
	if !r.Requests.Memory().Equal(wantMemReq) {
		t.Errorf("Memory request = %s; want 128Mi", r.Requests.Memory().String())
	}
	if !r.Limits.Cpu().Equal(wantCPULim) {
		t.Errorf("CPU limit = %s; want 500m", r.Limits.Cpu().String())
	}
	if !r.Limits.Memory().Equal(wantMemLim) {
		t.Errorf("Memory limit = %s; want 256Mi", r.Limits.Memory().String())
	}
}

// ---------------------------------------------------------------------------
// TestConfigMergeMode constants
// ---------------------------------------------------------------------------

func TestBuildInitContainer_NPMIgnoreScripts(t *testing.T) {
	t.Parallel()

	claw := minimalClaw()
	spec := minimalSpec()
	ic := buildInitContainer(claw, spec)

	envMap := envToMap(ic.Env)
	val, ok := envMap["NPM_CONFIG_IGNORE_SCRIPTS"]
	if !ok {
		t.Fatal("missing NPM_CONFIG_IGNORE_SCRIPTS env var in init container")
	}
	if val != "true" {
		t.Errorf("NPM_CONFIG_IGNORE_SCRIPTS = %q; want %q", val, "true")
	}
}

func TestConfigMergeMode(t *testing.T) {
	t.Parallel()

	if ConfigModeOverwrite != "overwrite" {
		t.Errorf("ConfigModeOverwrite = %q; want %q", ConfigModeOverwrite, "overwrite")
	}
	if ConfigModeDeepMerge != "deepmerge" {
		t.Errorf("ConfigModeDeepMerge = %q; want %q", ConfigModeDeepMerge, "deepmerge")
	}
	if ConfigModePassthrough != "passthrough" {
		t.Errorf("ConfigModePassthrough = %q; want %q", ConfigModePassthrough, "passthrough")
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// envToMap converts a slice of EnvVar to a map for lookup.
// For duplicates, last value wins (matching Kubernetes behavior).
// NOTE: Only captures .Value; .ValueFrom is ignored.
func envToMap(envs []corev1.EnvVar) map[string]string {
	m := make(map[string]string, len(envs))
	for _, e := range envs {
		m[e.Name] = e.Value
	}
	return m
}

// mountsToMap converts a slice of VolumeMount to a map keyed by Name.
func mountsToMap(mounts []corev1.VolumeMount) map[string]corev1.VolumeMount {
	m := make(map[string]corev1.VolumeMount, len(mounts))
	for _, vm := range mounts {
		m[vm.Name] = vm
	}
	return m
}

// assertSecurityContext validates all fields of the container security context.
func assertSecurityContext(t *testing.T, sc *corev1.SecurityContext) {
	t.Helper()

	if sc.RunAsUser == nil || *sc.RunAsUser != 1000 {
		t.Errorf("RunAsUser = %v; want 1000", sc.RunAsUser)
	}
	if sc.RunAsGroup == nil || *sc.RunAsGroup != 1000 {
		t.Errorf("RunAsGroup = %v; want 1000", sc.RunAsGroup)
	}
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Errorf("RunAsNonRoot = %v; want true", sc.RunAsNonRoot)
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Errorf("ReadOnlyRootFilesystem = %v; want true", sc.ReadOnlyRootFilesystem)
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Errorf("AllowPrivilegeEscalation = %v; want false", sc.AllowPrivilegeEscalation)
	}
	if sc.Capabilities == nil {
		t.Fatal("Capabilities is nil")
	}
	if len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("Capabilities.Drop = %v; want [ALL]", sc.Capabilities.Drop)
	}
	if sc.SeccompProfile == nil {
		t.Fatal("SeccompProfile is nil")
	}
	if sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("SeccompProfile.Type = %q; want %q", sc.SeccompProfile.Type, corev1.SeccompProfileTypeRuntimeDefault)
	}
}
