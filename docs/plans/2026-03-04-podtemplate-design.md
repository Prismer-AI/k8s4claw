# RuntimeAdapter PodTemplate Design

**Date:** 2026-03-04
**Status:** Approved
**Prerequisite:** Claw Controller MVP (completed)

## 1. Goal

Implement full `PodTemplate()` for all 4 runtime adapters, replacing the current placeholder (empty `PodTemplateSpec` + busybox container). After this, a Claw CR creates a StatefulSet with a real runtime container, init container, IPC Bus co-process, channel sidecars, and all required volumes.

## 2. Scope

- Runtime container with IPC Bus co-process (embedded in image entrypoint)
- Init container (`claw-init`) for config merge, workspace seed, deps, skills
- Channel sidecar injection with native sidecar / regular sidecar feature gate
- All volumes: ipc-socket, wal-data, config-vol, tmp, cache, PVC (via volumeClaimTemplates), shared
- Per-runtime resource defaults and configuration

**Out of scope:**
- `claw-init` binary implementation (placeholder image for now)
- `ensureConfigMap()` (ConfigMap volume uses `optional: true`)
- `ClawChannelReconciler` (channel sidecar injection is wired but depends on ClawChannel CRs)
- Archive sidecar
- NetworkPolicy, Ingress, PDB

## 3. Architecture: Shared PodBuilder + Adapter RuntimeSpec

### 3.1 Design Decision

**Approach B (Shared PodBuilder)** selected over:
- Approach A (Monolithic per-adapter): ~460 lines, massive duplication
- Approach C (Functional Options): over-engineered for 4 runtimes

PodBuilder assembles common components; each adapter provides only runtime-specific config via `RuntimeSpec`.

### 3.2 Core Types

```go
// internal/runtime/pod_builder.go

type ConfigMergeMode string

const (
    ConfigModeOverwrite   ConfigMergeMode = "overwrite"
    ConfigModeDeepMerge   ConfigMergeMode = "deepmerge"
    ConfigModePassthrough ConfigMergeMode = "passthrough"
)

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
```

### 3.3 Interface Compatibility

`RuntimeBuilder.PodTemplate()` interface is **unchanged**. Each adapter internally calls a private `runtimeSpec()` method and delegates to `BuildPodTemplate()`:

```go
func (a *OpenClawAdapter) PodTemplate(claw *v1alpha1.Claw) *corev1.PodTemplateSpec {
    spec := a.runtimeSpec(claw)
    return BuildPodTemplate(claw, spec)
}

func (a *OpenClawAdapter) runtimeSpec(claw *v1alpha1.Claw) *RuntimeSpec {
    return &RuntimeSpec{
        Image:          "ghcr.io/prismer-ai/k8s4claw-openclaw:latest",
        Command:        []string{"/usr/bin/claw-entrypoint"},
        // ... runtime-specific fields
        LivenessProbe:  a.HealthProbe(claw),
        ReadinessProbe: a.ReadinessProbe(claw),
    }
}
```

`HealthProbe()`/`ReadinessProbe()` remain on the interface for now (backward compat), reused internally by `runtimeSpec()`.

## 4. PodBuilder Assembly

### 4.1 Function Signature

```go
func BuildPodTemplate(claw *v1alpha1.Claw, spec *RuntimeSpec) *corev1.PodTemplateSpec
```

### 4.2 Assembly Order

```
BuildPodTemplate(claw, spec)
  ├─ 1. buildVolumes(claw, spec)           → []corev1.Volume
  ├─ 2. buildInitContainer(claw, spec)     → corev1.Container
  ├─ 3. buildRuntimeContainer(claw, spec)  → corev1.Container
  └─ 4. Assemble PodTemplateSpec
```

### 4.3 Responsibility Split

| Component | Responsibility |
|-----------|---------------|
| **RuntimeSpec** | image, command, ports, resources, env, probes, configMode, extra volumes |
| **PodBuilder** | volumes, init container, runtime container (with probes + env + security), volume mounts |
| **Controller** | labels, pod security context, terminationGracePeriod, channel sidecars |

### 4.4 Environment Variable Priority

K8s container env is **last-wins** for duplicate names. Injection order:

1. Shared env: `CLAW_NAME`, `CLAW_NAMESPACE`, `IPC_SOCKET_PATH` (injected first)
2. `spec.Env`: runtime-specific env (injected second, can override shared)

## 5. Init Container

### 5.1 Phases

| Phase | Description | Overwrite | DeepMerge | Passthrough |
|-------|-------------|-----------|-----------|-------------|
| 1. Config Merge | Merge/replace runtime config | Replace | Deep merge | Skip |
| 2. Workspace Seed | Copy seed files from ConfigMap | Yes | Yes | Yes |
| 3. Dependencies | Runtime dependency install | npm install | npm install | Skip |
| 4. Skills Install | Install declared skills | npm install (IGNORE_SCRIPTS=true) | npm install | Skip |

### 5.2 Per-Runtime Behavior

| Runtime | ConfigMode | Deps | Skills | Estimated Duration |
|---------|-----------|------|--------|-------------------|
| OpenClaw | DeepMerge | npm install | npm install | ~5-15s |
| NanoClaw | Overwrite | npm install | npm install | ~3-10s |
| ZeroClaw | Passthrough | skip | skip | <1s |
| PicoClaw | Passthrough | skip | skip | <1s |

### 5.3 Container Spec

```go
func buildInitContainer(claw *v1alpha1.Claw, spec *RuntimeSpec) corev1.Container {
    args := []string{
        "--mode", string(spec.ConfigMode),
        "--workspace", spec.WorkspacePath,
        "--runtime", string(claw.Spec.Runtime),
    }

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
        Name:            "claw-init",
        Image:           "ghcr.io/prismer-ai/claw-init:latest",
        Command:         []string{"/claw-init"},
        Args:            args,
        Env:             sharedEnvVars(claw),
        VolumeMounts:    mounts,
        Resources:       initContainerResources(), // 100m/128Mi → 500m/256Mi
        SecurityContext: containerSecurityContext(), // readOnlyRootFilesystem=true, /tmp via emptyDir
    }
}
```

### 5.4 Design Decisions

- **Always included**: Even for Passthrough runtimes. claw-init exits <100ms when no work needed. Simplifies PodBuilder logic.
- **Separate image**: `ghcr.io/prismer-ai/claw-init:latest`, not the runtime image. Init logic is shared across runtimes.
- **Security**: `readOnlyRootFilesystem: true` + emptyDir at `/tmp` for temp files.
- **Placeholder for now**: claw-init binary not yet implemented. Use busybox with `echo "init done"` as placeholder.

## 6. Volumes

### 6.1 Volume List

| Volume | Type | Condition | Mount Path |
|--------|------|-----------|------------|
| `ipc-socket` | emptyDir | Always | `/var/run/claw` |
| `wal-data` | emptyDir (512Mi) | Always | `/var/lib/claw/wal` |
| `config-vol` | ConfigMap (optional) | Always | `/etc/claw` |
| `tmp` | emptyDir | Always | `/tmp` |
| `cache` | emptyDir (tmpfs) | persistence.cache configured | user-defined |
| `session` | volumeClaimTemplate | persistence.session configured | user-defined |
| `output` | volumeClaimTemplate | persistence.output configured | user-defined |
| `workspace` | volumeClaimTemplate | persistence.workspace configured | user-defined |
| `shared-<name>` | PVC reference | persistence.shared[] | user-defined (±ReadOnly) |
| adapter extras | varies | adapter provides | adapter-defined |

### 6.2 Mount Matrix

```
                │ Init Container │ Runtime Container │ Channel Sidecar │
────────────────┼────────────────┼───────────────────┼─────────────────┤
ipc-socket      │                │ /var/run/claw     │ /var/run/claw   │
wal-data        │                │ /var/lib/claw/wal │                 │
config-vol (RO) │ /etc/claw      │ /etc/claw         │                 │
tmp             │ /tmp           │ /tmp              │ /tmp            │
cache           │                │ <mountPath>       │                 │
session         │                │ <mountPath>       │                 │
output          │                │ <mountPath>       │ (archive only)  │
workspace       │ <workspacePath>│ <workspacePath>   │                 │
shared-*        │                │ <mountPath> (±RO) │                 │
```

### 6.3 PVC via StatefulSet volumeClaimTemplates

Session, output, and workspace PVCs use StatefulSet's `volumeClaimTemplates` for automatic creation:

```go
func BuildVolumeClaimTemplates(claw *v1alpha1.Claw) []corev1.PersistentVolumeClaim
```

PVC naming: `<template-name>-<statefulset-name>-<ordinal>` (e.g., `session-my-agent-0`)

**Constraints:**
- `volumeClaimTemplates` are immutable after StatefulSet creation (K8s limitation)
- PVC resize requires separate handling (future resize controller)
- `ensureStatefulSet` update path must NOT modify `VolumeClaimTemplates`

Shared volumes remain as `PersistentVolumeClaimVolumeSource` (user pre-creates PVCs).

### 6.4 ConfigMap Optional Strategy

ConfigMap creation (`ensureConfigMap()`) is out of scope. Use `optional: true` to prevent Pod from blocking:

```go
ConfigMap: &corev1.ConfigMapVolumeSource{
    LocalObjectReference: corev1.LocalObjectReference{
        Name: claw.Name + "-config",
    },
    Optional: ptr.To(true),
}
```

### 6.5 Helper Functions

```go
func emptyDirVolume(name string, sizeLimit *resource.Quantity, medium corev1.StorageMedium) corev1.Volume
func configMapVolume(name, cmName string) corev1.Volume  // optional: true
func pvcVolume(name, claimName string) corev1.Volume
func pvcTemplate(name, size, storageClass string) corev1.PersistentVolumeClaim
```

## 7. Channel Sidecar Injection

### 7.1 Feature Gate

Operator-level CLI flag controls native vs regular sidecar mode:

```go
// cmd/operator/main.go
flag.BoolVar(&enableNativeSidecars, "enable-native-sidecars", true,
    "Use native sidecars (K8s 1.28+). Set false for older clusters.")

// Passed to ClawReconciler
type ClawReconciler struct {
    // ...
    NativeSidecarsEnabled bool
}
```

### 7.2 Native vs Regular Sidecar

| | Native Sidecar (K8s 1.28+) | Regular Sidecar |
|---|---|---|
| Placement | `initContainers` with `restartPolicy: Always` | `containers` |
| Start order | Before runtime container | Parallel with runtime |
| Crash recovery | kubelet auto-restarts independently | Depends on Pod restartPolicy |
| Pod termination | Stops after runtime exits | Receives SIGTERM simultaneously |

### 7.3 Injection Flow

```go
// internal/controller/channel_sidecar.go

func (r *ClawReconciler) injectChannelSidecars(ctx context.Context,
    tmpl *corev1.PodTemplateSpec, claw *clawv1alpha1.Claw) ([]string, error)
```

Returns list of missing/incompatible channels for condition reporting.

**Per-channel steps:**
1. Fetch `ClawChannel` CR — if NotFound, skip and record as missing
2. Validate mode compatibility — if incompatible, skip and record
3. Resolve sidecar spec: built-in template or custom `ClawChannel.spec.sidecar`
4. Apply resource override from `ClawChannel.spec.resources`
5. Inject credentials via `envFrom` from `ClawChannel.spec.credentials.secretRef`
6. Inject channel config as `CHANNEL_CONFIG` JSON env var
7. Add to `initContainers` (native) or `containers` (regular)

### 7.4 Soft Failure

Missing or incompatible channels do **not** fail reconciliation:

```go
missingChannels, err := r.injectChannelSidecars(ctx, &podTemplate, claw)
if err != nil {
    return err // real API errors still fail
}
if len(missingChannels) > 0 {
    // Set condition: ChannelsReady=False, reason=ChannelNotFound
    // Requeue with delay
}
```

### 7.5 InitContainers Ordering (Native Mode)

```yaml
initContainers:
  - name: claw-init           # [0] run-to-completion (no restartPolicy)
  - name: channel-slack        # [1] restartPolicy: Always (native sidecar)
  - name: channel-telegram     # [2] restartPolicy: Always (native sidecar)
containers:
  - name: runtime              # starts after all init containers
```

K8s executes sequentially: claw-init completes → native sidecars start and persist → runtime starts.

### 7.6 Controller Watch Requirement

`SetupWithManager` needs ClawChannel watch for reactive reconciliation:

```go
Watches(&v1alpha1.ClawChannel{},
    handler.EnqueueRequestsFromMapFunc(r.channelToClawMapper))
```

`channelToClawMapper` uses field indexer to find Claws referencing the changed ClawChannel.

### 7.7 Channel Sidecar Volume Mounts

```go
func channelVolumeMounts() []corev1.VolumeMount {
    return []corev1.VolumeMount{
        {Name: "ipc-socket", MountPath: "/var/run/claw"},
        {Name: "tmp", MountPath: "/tmp"},
    }
}
```

### 7.8 File Organization

| File | Contents |
|------|----------|
| `internal/controller/channel_sidecar.go` | `injectChannelSidecars()`, `resolveChannelSidecar()`, `buildChannelContainer()` |
| `internal/controller/channel_templates.go` | Built-in channel templates (slack, telegram, etc.) |

## 8. Per-Runtime Specs

| | OpenClaw | NanoClaw | ZeroClaw | PicoClaw |
|---|---|---|---|---|
| Image | k8s4claw-openclaw | k8s4claw-nanoclaw | k8s4claw-zeroclaw | k8s4claw-picoclaw |
| Port | 18900 | 19000 | 3000 | 8080 |
| CPU req/lim | 500m / 2000m | 100m / 500m | 50m / 200m | 25m / 100m |
| Mem req/lim | 1Gi / 4Gi | 256Mi / 512Mi | 32Mi / 128Mi | 16Mi / 64Mi |
| ConfigMode | DeepMerge | Overwrite | Passthrough | Passthrough |
| Probe type | HTTP | Exec (UDS) | HTTP | TCP |
| ExtraVolumes | none | none | none | none |

All runtimes share the same entrypoint (`/usr/bin/claw-entrypoint`) which starts IPC Bus as co-process then exec's the runtime. NanoClaw's bridge adapter is internal to its container image.

## 9. Controller Integration

### 9.1 buildStatefulSet Changes

Remove placeholder container block. New flow:

```go
func (r *ClawReconciler) buildStatefulSet(claw, adapter) *appsv1.StatefulSet {
    podTemplate := adapter.PodTemplate(claw)  // full template from PodBuilder

    // Inject labels (unchanged)
    // Inject pod security context (unchanged)
    // Set terminationGracePeriodSeconds (unchanged)

    // *** REMOVED: placeholder container block ***

    return &appsv1.StatefulSet{
        // ...
        Spec: appsv1.StatefulSetSpec{
            // ...
            Template:             *podTemplate,
            VolumeClaimTemplates: clawruntime.BuildVolumeClaimTemplates(claw),
        },
    }
}
```

### 9.2 Code Migration

| Item | From | To |
|------|------|----|
| `containerSecurityContext()` | `internal/controller/` | `internal/runtime/pod_builder.go` |
| `buildEnvVars()` | `internal/controller/` | `internal/runtime/pod_builder.go` (merged into PodBuilder) |

### 9.3 ensureStatefulSet Constraint

Update path must NOT modify `VolumeClaimTemplates` (immutable):

```go
existing.Spec.Template = desired.Spec.Template
existing.Spec.Replicas = desired.Spec.Replicas
// VolumeClaimTemplates: do NOT update
```

### 9.4 Test Impact

| Test | Change Required |
|------|----------------|
| `TestStatefulSetCreated` | Update: image, init container, volumes, probes source |
| `TestStatusProvisioning` | No change |
| `TestFinalizerAdded/RunsOnDelete` | No change |
| `TestUnknownRuntime` | No change |

## 10. Future Work

- `claw-init` binary implementation
- `ensureConfigMap()` controller method
- Archive sidecar injection
- `ClawChannelReconciler` + field indexer
- PVC resize controller
- Image override (`spec.image`)
- Credential hash annotation for rolling updates
