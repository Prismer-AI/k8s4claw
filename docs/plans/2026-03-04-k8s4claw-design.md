# k8s4claw Design Document

**Date:** 2026-03-04
**Status:** Approved (Rev 2 — post-review)
**Author:** Prismer-AI Team

## 1. Overview

k8s4claw is a Kubernetes Operator + Go SDK for managing heterogeneous AI agent runtimes ("Claws") on Kubernetes. It provides unified lifecycle management, communication channels, credential handling, persistence, observability, and a simple SDK for infrastructure integration.

### Goals

1. **Unified lifecycle** — Manage OpenClaw, NanoClaw, ZeroClaw (and custom runtimes) through a single K8s Operator
2. **Simple SDK** — Enable quick infrastructure integration with minimal code
3. **Persistence** — Durable session state, output archival, shared storage
4. **Observability** — Metrics, logs, status conditions, K8s Events
5. **Extensibility** — New runtimes and channels via adapter interfaces

### Non-Goals

- Replacing each runtime's internal orchestration
- Multi-cluster federation (future scope)
- GUI / dashboard (use existing K8s tooling)

## 2. Runtime Comparison

| | OpenClaw | NanoClaw | ZeroClaw |
|---|---|---|---|
| **Language** | TypeScript/Node.js | TypeScript/Node.js | 100% Rust |
| **Purpose** | Full-featured AI assistant platform | Lightweight secure personal assistant | High-performance agent runtime |
| **Memory** | >1GB | ~200MB | <5MB |
| **Startup** | >500ms | Medium | <10ms |
| **Isolation** | App-level permissions | Docker container sandbox | WASM/Landlock/Bubblewrap |
| **Channels** | 25+ built-in | On-demand (Skills) | Pluggable (Matrix/Discord/Lark...) |
| **Gateway Port** | 18900 | SQLite polling | 3000 |
| **Extension** | Plugins + Skills | Claude Code Skills | WASM plugin engine |

## 3. CRD Design

### 3.1 Claw (Primary Resource)

```yaml
apiVersion: claw.prismer.ai/v1alpha1
kind: Claw
metadata:
  name: my-research-agent
spec:
  # IMMUTABLE after creation. Changing runtime requires delete + recreate.
  runtime: openclaw          # openclaw | nanoclaw | zeroclaw | custom

  # Runtime-specific configuration
  config:
    model: "claude-sonnet-4"
    workspace: "/workspace"

  # Credential management (see Section 3.3 for semantics)
  credentials:
    # Base: choose ONE of secretRef or externalSecret
    secretRef:
      name: my-llm-keys
    # Optional overrides: fine-grained key mappings (merged on top of base)
    keys:
      - name: ANTHROPIC_API_KEY
        secretKeyRef:
          name: llm-secrets
          key: anthropic-key

  # Channel references
  channels:
    - name: slack-channel
      mode: bidirectional
    - name: webhook-internal
      mode: inbound

  # Persistence (see Section 7)
  persistence:
    reclaimPolicy: Retain
    session: { ... }
    output: { ... }
    workspace: { ... }
    shared: [ ... ]
    cache: { ... }

  # Observability
  observability:
    metrics: true
    logs: true
    tracing: false
```

**Note:** `replicas` is intentionally excluded from v1alpha1. Multi-replica semantics (state sharing, message routing, runtime compatibility) are not yet defined. This will be introduced in a future API version after the design is validated. The validating webhook rejects any attempt to set `replicas`.

### 3.2 ClawChannel

```yaml
apiVersion: claw.prismer.ai/v1alpha1
kind: ClawChannel
metadata:
  name: slack-team
spec:
  type: slack                # slack | telegram | whatsapp | discord | matrix | webhook | custom
  mode: bidirectional        # inbound | outbound | bidirectional

  credentials:
    secretRef:
      name: slack-bot-token

  config:
    appId: A0123456789
    channels: ["#research", "#general"]

  # Backpressure tuning (per-channel, overrides defaults)
  backpressure:
    bufferSize: 1024           # ring buffer capacity (default: 1024)
    highWatermark: 0.8         # trigger slow_down (default: 0.8)
    lowWatermark: 0.3          # trigger resume (default: 0.3)

  # Resource limits for built-in sidecar (ignored for type: custom)
  resources:
    requests: { cpu: 50m, memory: 64Mi }
    limits: { cpu: 200m, memory: 128Mi }

  # Custom sidecar (for type: custom)
  sidecar:
    image: my-registry/my-channel-adapter:v1
    resources:
      requests: { cpu: 50m, memory: 64Mi }
      limits: { cpu: 200m, memory: 128Mi }
    ports:
      - name: webhook
        containerPort: 9090
    livenessProbe:
      httpGet: { path: /healthz, port: 9090 }
    readinessProbe:
      httpGet: { path: /ready, port: 9090 }
```

### 3.3 Credential Semantics

The three credential mechanisms have clear precedence and are **not** all used simultaneously:

```
┌─────────────────────────────────────────────────────────┐
│  Base (choose ONE):                                     │
│    secretRef:       → Direct K8s Secret reference       │
│    externalSecret:  → Creates ExternalSecret CR         │
│                       (delegates to external-secrets-   │
│                        operator for Vault/AWS SM/GCP)   │
│                                                         │
│  Override (optional, merged on top of base):            │
│    keys:            → Fine-grained per-key mappings     │
│                       from different Secrets             │
└─────────────────────────────────────────────────────────┘
```

**Rules:**
1. `secretRef` and `externalSecret` are **mutually exclusive**. Webhook rejects both set simultaneously.
2. `keys` is always optional. When present, individual keys override the base Secret's keys by env var name.
3. `externalSecret` does NOT talk to Vault directly. The Operator creates an `ExternalSecret` CR, delegating to the [external-secrets-operator](https://external-secrets.io/).
4. **Rotation:** When the underlying Secret changes, the Operator detects the change (via Secret watch) and triggers a rolling restart of the Claw Pod to pick up new values.

```yaml
# Example: Vault-based credentials with one override
credentials:
  externalSecret:
    provider: vault
    store: my-secret-store       # References a ClusterSecretStore
    path: secret/data/claw/agent
    refreshInterval: 1h
  keys:
    - name: CUSTOM_TOKEN         # Override: this key comes from a different Secret
      secretKeyRef:
        name: custom-secrets
        key: token
```

### 3.4 Admission Webhooks

The Operator deploys validating and mutating webhooks:

**Validating webhook:**
- Calls `RuntimeAdapter.Validate()` for runtime-specific field validation
- Rejects invalid credential combinations (both `secretRef` and `externalSecret` set)
- Rejects `runtime` field changes on UPDATE (immutable)
- Rejects `replicas` field (not supported in v1alpha1)
- Validates PVC size formats, storageClass references
- Validates ClawChannel references exist

**Mutating webhook:**
- Sets default `reclaimPolicy: Retain` if not specified
- Sets default storageClass from cluster default if not specified
- Sets default resource limits for IPC Bus and built-in sidecars
- Injects standard labels and annotations

### 3.5 API Versioning Strategy

**Current:** `v1alpha1` — all fields are experimental, breaking changes allowed between minor versions.

**Evolution plan:**

| Version | Criteria | Migration |
|---------|----------|-----------|
| `v1alpha1` | Initial development. Breaking changes allowed. | N/A |
| `v1alpha2` | Stabilize core fields (runtime, credentials, persistence). Add new experimental fields. | Conversion webhook: auto-migrate v1alpha1 → v1alpha2 |
| `v1beta1` | All core fields stable. Only additive changes. | Conversion webhook: v1alpha2 → v1beta1. Storage version switches. |
| `v1` | GA. No breaking changes. | Conversion webhook: v1beta1 → v1 |

**Field stability markers** (tracked in Go struct tags):

```go
type ClawSpec struct {
    // +stable: v1alpha1
    Runtime RuntimeType `json:"runtime"`

    // +stable: v1alpha1
    Credentials *CredentialSpec `json:"credentials,omitempty"`

    // +experimental
    Observability *ObservabilitySpec `json:"observability,omitempty"`
}
```

**Operator upgrade strategy:**
- Operator Deployment uses rolling update. New version starts, old drains.
- Running Claw Pods are NOT disrupted during operator upgrade (Pods are independent of operator process).
- CRD schema updates are applied via `kubectl apply`. The operator includes a CRD migration check on startup.
- PVC ownership is tracked via `ownerReferences` + finalizers — orphaned PVCs are never silently deleted.

## 4. Architecture

### 4.1 RuntimeAdapter Interface

The Operator uses a strategy pattern to handle runtime differences. Split into two interfaces for single-responsibility:

```go
// RuntimeBuilder constructs K8s resources for a specific runtime.
type RuntimeBuilder interface {
    PodTemplate(claw *v1alpha1.Claw) *corev1.PodTemplateSpec
    HealthProbe(claw *v1alpha1.Claw) *corev1.Probe
    ReadinessProbe(claw *v1alpha1.Claw) *corev1.Probe
    DefaultConfig() *RuntimeConfig  // typed config, not map[string]any
    GracefulShutdownSeconds() int32
}

// RuntimeValidator validates CRD specs for a specific runtime.
type RuntimeValidator interface {
    Validate(spec *v1alpha1.ClawSpec) field.ErrorList
    ValidateUpdate(old, new *v1alpha1.ClawSpec) field.ErrorList
}

// RuntimeAdapter combines both for convenience.
type RuntimeAdapter interface {
    RuntimeBuilder
    RuntimeValidator
}

// RuntimeConfig provides typed defaults instead of map[string]any.
type RuntimeConfig struct {
    GatewayPort   int               `json:"gatewayPort"`
    WorkspacePath string            `json:"workspacePath"`
    Environment   map[string]string `json:"environment"`
}
```

New runtimes only need to implement `RuntimeAdapter`.

### 4.2 Pod Structure

The IPC Bus runs as a **co-process inside the Claw runtime container** (not a separate sidecar), eliminating the restart-gap problem. It is started as a child process by the container entrypoint and shares the same lifecycle as the runtime.

Channel sidecars connect to the Bus via Unix Domain Socket. If the Bus (and runtime) restart, sidecars buffer locally and reconnect.

```
┌──────────────────────── Pod ────────────────────────────┐
│                                                         │
│  initContainers (native sidecar, restartPolicy: Always) │
│  ┌──────────┐  ┌──────────┐                             │
│  │ Slack    │  │ Telegram │                             │
│  │ Sidecar  │  │ Sidecar  │                             │
│  └────┬─────┘  └────┬─────┘                             │
│       │              │                                   │
│       ▼              ▼                                   │
│  /var/run/claw/bus.sock (Unix Domain Socket)             │
│       ▲                                                  │
│       │                                                  │
│  containers                                              │
│  ┌─────────────────────────────────┐                    │
│  │   Claw Runtime Container        │                    │
│  │  ┌────────────┐ ┌────────────┐  │                    │
│  │  │ IPC Bus    │ │ Runtime    │  │                    │
│  │  │ (co-proc)  │ │ (openclaw/ │  │                    │
│  │  │            │ │  nano/zero)│  │                    │
│  │  └─────┬──────┘ └─────┬──────┘  │                    │
│  │        └── localhost ──┘         │                    │
│  └──────────────────────────────────┘                    │
│                                                         │
│  volumes:                                               │
│    ipc-socket    (emptyDir)          — Bus socket        │
│    wal-data      (emptyDir)          — WAL + DLQ        │
│    session-pvc   (PVC, RWO)          — Session state    │
│    output-pvc    (PVC, RWO)          — Output artifacts │
│    workspace-pvc (PVC, RWO)          — Workspace        │
│    shared-*      (PVC, RWX)          — Shared volumes   │
│    cache         (emptyDir, tmpfs)   — Model cache      │
└─────────────────────────────────────────────────────────┘
```

**Key design decisions:**
- IPC Bus is a co-process (not sidecar) → shares lifecycle with runtime, no restart-gap
- WAL stored on `emptyDir` named `wal-data` → survives container restart, acceptable loss on pod eviction (sidecars re-buffer)
- Channel sidecars are K8s native sidecars (init containers with `restartPolicy: Always`) → guaranteed to start before runtime and survive runtime restarts

### 4.3 Graceful Shutdown

Each runtime defines `GracefulShutdownSeconds()` via the `RuntimeBuilder` interface:

| Runtime | Default | Reason |
|---------|---------|--------|
| OpenClaw | 30s | Large session state to flush |
| NanoClaw | 15s | Moderate state |
| ZeroClaw | 5s | Minimal state, fast shutdown |

**Shutdown sequence:**

```
1. Claw CR deleted / Pod termination signal
   ↓
2. preStop hook:
   a. IPC Bus sends "shutdown" to all sidecars (drain in-flight messages)
   b. IPC Bus flushes WAL to disk
   c. Runtime saves session state to PVC
   d. Snapshot sidecar takes final snapshot (if enabled)
   ↓
3. SIGTERM → runtime process exits
   ↓
4. K8s terminates sidecars
```

`terminationGracePeriodSeconds` is set to `GracefulShutdownSeconds() + 10` (buffer for sidecar drain).

### 4.4 ClawChannel → Pod Injection

When the Operator reconciles a `Claw` CR, it:

1. Lists all `ClawChannel` CRs referenced in `spec.channels`
2. For each channel, resolves the sidecar container spec:
   - Built-in types (slack, telegram, etc.): Operator generates the sidecar spec from built-in templates
   - Custom type: Uses `spec.sidecar` from the ClawChannel CR
3. Injects sidecar containers into the Pod spec as native sidecars
4. Mounts shared `ipc-socket` volume into each sidecar

**Cross-resource watch:** The Operator watches `ClawChannel` CRs and triggers re-reconciliation of any `Claw` that references a changed channel.

## 5. IPC Bus + Channel Sidecar

### 5.1 IPC Protocol

Unix Domain Socket at `/var/run/claw/bus.sock`, JSON-lines protocol.

The IPC Bus co-process is the sole translation layer between channel sidecars and the Claw runtime. Sidecars only speak the standard protocol; the Bus handles runtime-specific translation via RuntimeBridge.

### 5.2 Message Format

```go
type ClawMessage struct {
    ID            string            `json:"id"`
    StreamID      string            `json:"stream_id,omitempty"`
    CorrelationID string            `json:"correlation_id,omitempty"` // Links response to triggering message
    ReplyTo       string            `json:"reply_to,omitempty"`       // Thread/reply support
    Type          MessageType       `json:"type"`
    Channel       string            `json:"channel"`
    Direction     string            `json:"direction"`
    Sender        string            `json:"sender"`
    Content       string            `json:"content,omitempty"`
    Delta         *DeltaPayload     `json:"delta,omitempty"`
    ToolCall      *ToolPayload      `json:"tool_call,omitempty"`
    Media         []MediaAttachment `json:"media,omitempty"`
    Metadata      map[string]any    `json:"metadata,omitempty"`
    Timestamp     time.Time         `json:"ts"`
}
```

### 5.3 Message Types

| Type | Direction | Description |
|------|-----------|-------------|
| `message` | inbound | Complete message from user |
| `stream.start` | outbound | Stream begins |
| `stream.delta` | outbound | Incremental token chunk |
| `stream.tool` | outbound | Tool call event |
| `stream.error` | outbound | Error during stream |
| `stream.end` | outbound | Stream complete, contains full content |
| `backpressure` | control | Flow control signal |
| `shutdown` | control | Graceful shutdown notification |
| `ping` / `pong` | control | Keepalive |

### 5.4 Streaming

Delta payload:

```go
type DeltaPayload struct {
    Text       string `json:"text,omitempty"`
    TokenCount int    `json:"token_count,omitempty"`
    Role       string `json:"role,omitempty"` // "assistant" | "thinking"
}

type ToolPayload struct {
    Name   string         `json:"name"`
    Status string         `json:"status"` // "calling" | "result" | "error"
    Input  map[string]any `json:"input,omitempty"`
    Output string         `json:"output,omitempty"`
}
```

### 5.5 RuntimeBridge

Translates between IPC Bus standard protocol and runtime-native protocols:

```go
type RuntimeBridge interface {
    Forward(ctx context.Context, msg *ClawMessage) error
    Listen(ctx context.Context, out chan<- *ClawMessage) error
    Health(ctx context.Context) error
    Pause(ctx context.Context) error
    Resume(ctx context.Context) error
}
```

| Runtime | Bridge Implementation | Notes |
|---------|----------------------|-------|
| OpenClaw | WebSocket → localhost:18900 | Native streaming via WS frames |
| NanoClaw | UDS wrapper → localhost:19000 | See Section 5.7 |
| ZeroClaw | HTTP SSE → localhost:3000 | Native SSE streaming |

### 5.6 Sidecar Stream Consumption Strategies

Sidecars choose how to consume streams based on their channel's capabilities:

| Strategy | Use Case | Behavior |
|----------|----------|----------|
| `BufferFlush` | Slack (message editing) | Accumulate deltas, flush at interval |
| `PassThrough` | WebSocket frontends | Forward each delta immediately |
| `WaitComplete` | Email / SMS | Wait for `stream.end`, send once |

### 5.7 NanoClaw Bridge: UDS Wrapper

NanoClaw's native IPC (shared SQLite + filesystem) is fragile for concurrent access. The k8s4claw runtime container image for NanoClaw includes a lightweight **UDS wrapper** process:

```
┌──── Claw Runtime Container ──────────────────┐
│                                               │
│  IPC Bus ←→ UDS Wrapper ←→ NanoClaw Runtime  │
│    (co-proc)  (co-proc)     (main process)    │
│       ↕           ↕              ↕            │
│   bus.sock    localhost:19000  SQLite + fs     │
└───────────────────────────────────────────────┘
```

The UDS wrapper:
- Exposes a Unix Domain Socket on localhost:19000
- Translates to NanoClaw's SQLite write + filesystem IPC internally
- Serializes writes to SQLite (single-writer lock)
- Uses `fsnotify` to detect NanoClaw's file-based responses (polling as fallback at 200ms)
- Handles WAL mode configuration for SQLite

This avoids direct shared-SQLite access between the IPC Bus and NanoClaw.

## 6. Backpressure

### 6.1 Four-Layer Design

```
L1 (Inbound)  → Token bucket rate limiter at sidecar entry
L2 (Outbound) → Per-channel ring buffer with configurable high/low watermarks
L3 (Bus)      → IPC Bus flow controller, per-channel pressure state
L4 (Runtime)  → Pause/Resume signals to Claw via RuntimeBridge
```

### 6.2 Pressure States

Watermarks are **configurable per-channel** via `ClawChannel.spec.backpressure` (defaults shown):

| State | Buffer Usage | Behavior |
|-------|-------------|----------|
| `normal` | < lowWatermark (default 0.3) | Pass-through |
| `degraded` | > highWatermark (default 0.8) | Merge deltas |
| `blocked` | 100% | Spill to disk, pause upstream |

### 6.3 Backpressure Flow

```
Sidecar buffer hits highWatermark
  → Sidecar sends {"type":"backpressure","action":"slow_down"}
  → IPC Bus marks channel as degraded (merges deltas)
  → Still at 100%?
    → IPC Bus marks channel as blocked (spill to disk)
    → All channels blocked?
      → IPC Bus calls RuntimeBridge.Pause()
      → Claw pauses output
  → Sidecar buffer drops to lowWatermark
    → Sidecar sends {"type":"backpressure","action":"resume"}
    → IPC Bus replays spilled messages
    → RuntimeBridge.Resume()
```

## 7. Persistence

### 7.1 Data Classification

| Data Type | Lifecycle | Loss Impact | Storage |
|-----------|-----------|-------------|---------|
| Session state | Per-instance | Agent loses context | PVC (fast SSD) + CSI snapshots |
| Output artifacts | Long-term | User work lost | PVC + S3 archival |
| Workspace | Medium-term | Regeneratable, wastes compute | PVC (standard) |
| Model cache | Rebuildable | Slower cold start | emptyDir (tmpfs) |
| Runtime data | Per-instance | Cannot audit | PVC (session) |
| Shared resources | Long-term | Team collaboration breaks | RWX PVC |

### 7.2 CRD Persistence Spec

```yaml
persistence:
  reclaimPolicy: Retain      # Retain | Archive | Delete

  session:
    enabled: true
    storageClass: fast-ssd
    size: 2Gi
    maxSize: 20Gi              # Auto-expansion ceiling
    mountPath: /data/session
    snapshot:
      enabled: true
      schedule: "*/30 * * * *"
      retain: 5
      # Uses CSI VolumeSnapshot, not tar.gz CronJob
      volumeSnapshotClass: csi-snapclass

  output:
    enabled: true
    storageClass: standard
    size: 20Gi
    maxSize: 200Gi             # Auto-expansion ceiling
    mountPath: /data/output
    archive:
      enabled: true
      destination:
        type: s3
        bucket: prismer-outputs
        prefix: "{{.Namespace}}/{{.Name}}/"
        secretRef:
          name: s3-credentials
      trigger:
        # Primary: periodic scan (works on all filesystems)
        schedule: "*/15 * * * *"
        # Optimization: inotify-based (only on supported filesystems)
        inotify: true
      lifecycle:
        localRetention: 7d
        archiveRetention: 365d
        compress: true

  workspace:
    enabled: true
    storageClass: standard
    size: 10Gi
    maxSize: 100Gi
    mountPath: /workspace

  shared:
    - name: paper-library
      claimName: shared-papers
      mountPath: /data/papers
      readOnly: true

  cache:
    enabled: true
    medium: Memory
    size: 512Mi
    mountPath: /data/cache
```

### 7.3 Lifecycle Management

**Creation:** Operator auto-creates PVCs when Claw CR is created. PVCs have `ownerReferences` pointing to the Claw CR + a finalizer on the Claw CR to ensure archival before deletion.

**Deletion:** Controlled by `reclaimPolicy`:
- `Retain` — Finalizer removes ownerReference (PVCs become orphaned with labels), then removes finalizer. PVCs preserved for manual recovery.
- `Archive` — Finalizer triggers full archival to object storage, waits for completion, then deletes PVCs and removes finalizer.
- `Delete` — Finalizer deletes PVCs immediately, then removes finalizer.

**Snapshot (CSI VolumeSnapshot):**

Snapshots use the CSI VolumeSnapshot API (not tar.gz CronJob), which provides crash-consistent snapshots without quiescing the runtime:

```
Operator creates VolumeSnapshot CR
  → CSI driver takes point-in-time snapshot (storage-level)
  → No need to stop writes or exec into pod
  → Snapshot stored by storage backend (EBS, GCE PD, Ceph, etc.)
```

The Operator manages snapshot lifecycle:
- Creates VolumeSnapshot CRs on the defined schedule
- Prunes old snapshots beyond `retain` count
- On recovery: creates a new PVC from the latest VolumeSnapshot, mounts to new Pod

**Recovery flow:**

```
Claw Pod restart detected
  → Operator checks: does session PVC have data?
    → Yes: mount existing PVC (normal restart)
    → No (PVC was lost):
      → Find latest VolumeSnapshot
      → Create PVC from snapshot (dataSource)
      → Mount restored PVC to new Pod
```

**Archival:**

Output archival runs as an **in-pod sidecar** (not a CronJob), solving the PVC access problem:

```
┌──── Archive Sidecar (native sidecar) ─────┐
│                                            │
│  Periodic scan (primary):                  │
│    Every 15 min, scan /data/output         │
│    Upload new/changed files to S3          │
│                                            │
│  inotify optimization (when supported):    │
│    Watch /data/output for IN_CLOSE_WRITE   │
│    Upload immediately after file close     │
│    Falls back to periodic scan silently    │
│    if inotify not supported (NFS, FUSE)    │
│                                            │
│  Lifecycle enforcement:                    │
│    Delete local files older than           │
│    localRetention (7d default)             │
└────────────────────────────────────────────┘
```

**Auto-expansion:**

```
Operator monitors PVC usage via kubelet metrics
(kubelet_volume_stats_used_bytes / kubelet_volume_stats_capacity_bytes)
  → Usage > 85%?
    → Check: current size < maxSize?
      → Yes: expand PVC by 50% (capped at maxSize)
        → Emit K8s Event: "StorageExpanding"
        → Update status condition: StorageExpanding=True
        → Verify StorageClass has allowVolumeExpansion: true
          → If not: emit Event "StorageExpansionUnsupported", skip
      → No: emit Event "StorageAtMaxSize", alert only
    → Cooldown: 1 hour between expansions per PVC
```

## 8. Error Recovery

### 8.1 Fault Classification

| Level | Fault | Recovery |
|-------|-------|----------|
| P0 | Claw container crash | K8s auto-restart + PVC preserved + VolumeSnapshot restore if needed |
| P1 | IPC Bus crash (co-process) | Entrypoint auto-restarts Bus + WAL replay. Sidecars buffer locally during gap. |
| P2 | Channel sidecar crash | Independent restart, other channels unaffected |
| P3 | Stream interruption | StreamTracker detects stale → synthetic end event |
| P4 | External service unreachable | Exponential backoff retry + dead letter queue |

### 8.2 IPC Bus WAL

All messages written to Write-Ahead Log (on `wal-data` emptyDir volume) before delivery. On Bus restart:

1. Read WAL, find unacknowledged messages
2. Wait for sidecars to reconnect (30s timeout)
3. Replay unacked messages in order
4. Failed replays go to dead letter queue

**Note:** WAL is on `emptyDir` — survives container restart but not pod eviction. This is acceptable: pod eviction is a P0 event where VolumeSnapshot-based recovery applies.

### 8.3 Stream Recovery

StreamTracker monitors active streams:
- Idle > 30s → check with RuntimeBridge if stream alive
- If dead → emit synthetic `stream.end` with accumulated content
- Max stream duration: 10 minutes (configurable)

### 8.4 Dead Letter Queue

DLQ is **centralized in the IPC Bus** (not per-sidecar), stored on the `wal-data` emptyDir volume:

- Single SQLite instance managed by the Bus co-process
- Max 10,000 messages, 24h retention
- Retry: 5 attempts, exponential backoff (1s → 5min)
- Exhausted retries → K8s Event + Prometheus metric
- Sidecars do NOT need SQLite — they only need the Channel SDK (lightweight)

When a sidecar cannot deliver a message to its external service:
1. Sidecar reports delivery failure to Bus via `{"type":"delivery_failed", ...}`
2. Bus enqueues the message in centralized DLQ
3. Bus retries delivery to the sidecar on schedule
4. Sidecar re-attempts external delivery

### 8.5 Sidecar Reconnection

SDK built-in exponential backoff reconnector:
- Initial delay: 100ms
- Max delay: 30s
- Multiplier: 2.0
- Jitter: ±10%

**Bus-down buffering:** When the sidecar detects the Bus socket is unavailable, it buffers inbound messages in an in-memory ring buffer (default 256 messages). On reconnect, buffered messages are forwarded to the Bus in order. If the buffer overflows, oldest messages are dropped and a metric is incremented.

## 9. RBAC

### 9.1 Operator ServiceAccount

The operator runs with a dedicated ServiceAccount with minimum required permissions:

```yaml
# Namespace-scoped by default. ClusterRole only if managing CRDs across namespaces.
rules:
  # Core resources
  - apiGroups: [""]
    resources: [pods, services, persistentvolumeclaims, secrets, events, configmaps]
    verbs: [get, list, watch, create, update, patch, delete]

  # Native sidecars
  - apiGroups: [""]
    resources: [pods/exec]
    verbs: [create]    # For preStop session flush

  # CSI VolumeSnapshots
  - apiGroups: ["snapshot.storage.k8s.io"]
    resources: [volumesnapshots]
    verbs: [get, list, watch, create, delete]

  # CRDs
  - apiGroups: ["claw.prismer.ai"]
    resources: [claws, claws/status, claws/finalizers, clawchannels, clawchannels/status]
    verbs: [get, list, watch, create, update, patch, delete]

  # ExternalSecrets (if using external-secrets-operator)
  - apiGroups: ["external-secrets.io"]
    resources: [externalsecrets]
    verbs: [get, list, watch, create, update, patch, delete]

  # Coordination (leader election)
  - apiGroups: ["coordination.k8s.io"]
    resources: [leases]
    verbs: [get, list, watch, create, update, patch, delete]
```

### 9.2 Claw Pod ServiceAccount

Claw Pods run with a **restricted ServiceAccount** — no K8s API access by default:

```yaml
automountServiceAccountToken: false
```

If a runtime needs K8s API access (e.g., custom runtimes managing sub-resources), it must be explicitly opted in via the Claw CR:

```yaml
spec:
  serviceAccount:
    name: my-custom-sa    # User-managed ServiceAccount
```

### 9.3 Namespace Scoping

The operator supports both modes:

| Mode | Use Case | Configuration |
|------|----------|---------------|
| **Namespace-scoped** (default) | Single team, simple setup | `--watch-namespace=my-ns` |
| **Cluster-scoped** | Multi-team, shared operator | No namespace flag, ClusterRole required |

For multi-tenancy, each team gets their own namespace. The operator's RBAC is limited to watched namespaces. Claw Pods in different namespaces cannot access each other's Secrets or PVCs.

## 10. Observability

### 10.1 CR Status

```yaml
status:
  phase: Running
  conditions:
    - type: RuntimeReady
      status: "True"
    - type: IPCBusHealthy
      status: "True"
    - type: ChannelsReady
      status: "False"
      message: "1/3 channels degraded"
    - type: StorageHealthy
      status: "True"
    - type: SnapshotHealthy
      status: "True"
    - type: WebhookReady
      status: "True"
  channels:
    - name: slack-team
      status: Connected
      backpressure: normal
      deadLetterCount: 0
    - name: telegram-personal
      status: Reconnecting
      lastError: "connection timeout"
      retryCount: 3
      deadLetterCount: 12
  persistence:
    session:
      pvcName: research-agent-session
      usagePercent: 24
      capacityBytes: 2147483648
      lastSnapshot: "2026-03-04T10:30:00Z"
      snapshotCount: 3
    output:
      pvcName: research-agent-output
      usagePercent: 25
      archivedFiles: 142
      lastArchive: "2026-03-04T10:55:00Z"
```

### 10.2 Metrics (Prometheus)

- `claw_runtime_status` — Runtime health gauge
- `claw_messages_total` — Message count by channel, direction, type
- `claw_stream_duration_seconds` — Stream duration histogram
- `claw_backpressure_state` — Per-channel pressure state
- `claw_dead_letter_count` — DLQ size gauge (centralized)
- `claw_storage_usage_bytes` — PVC usage by volume type
- `claw_storage_expansion_total` — Auto-expansion events counter
- `claw_snapshot_duration_seconds` — Snapshot creation time
- `claw_sidecar_buffer_dropped_total` — Messages dropped during Bus-down buffering

## 11. SDK

### 11.1 Claw SDK (Infrastructure Integration)

```go
import "github.com/Prismer-AI/k8s4claw/sdk"

client, err := sdk.NewClient()
if err != nil {
    log.Fatalf("failed to create SDK client: %v", err)
}

claw, err := client.Create(ctx, &sdk.ClawSpec{
    Runtime: sdk.OpenClaw,
    Config: &sdk.RuntimeConfig{
        Environment: map[string]string{"MODEL": "claude-sonnet-4"},
    },
})
if err != nil {
    log.Fatalf("failed to create claw: %v", err)
}

if err := claw.SendMessage(ctx, "Analyze this paper"); err != nil {
    log.Fatalf("failed to send message: %v", err)
}

result, err := claw.WaitForResult(ctx)
if err != nil {
    log.Fatalf("failed to get result: %v", err)
}
fmt.Println(result.Content)
```

### 11.2 Channel SDK (Custom Channel Development)

```go
import "github.com/Prismer-AI/k8s4claw/sdk/channel"

adapter := channel.NewAdapter("feishu", channel.Opts{
    Stream: channel.StreamOpts{
        Strategy:      channel.BufferFlush,
        FlushInterval: 500 * time.Millisecond,
    },
    Inbound: channel.InboundLimiter{
        Rate:  10,
        Burst: 20,
    },
    // Bus-down buffering config
    BusDownBuffer: 256,  // in-memory ring buffer size
})

adapter.OnStream(func(stream *channel.Stream) {
    stream.OnFlush(func(text string) { feishu.Update(text) })
    stream.OnEnd(func(final string) { feishu.Send(final) })
    stream.OnDeliveryFailed(func(msg *channel.Message, err error) {
        // SDK automatically reports to Bus DLQ
        log.Warn("delivery failed, queued for retry", "err", err)
    })
})

adapter.Run() // connects to /var/run/claw/bus.sock
```

## 12. Project Structure

```
k8s4claw/
├── cmd/
│   ├── operator/              # Operator entrypoint
│   │   └── main.go
│   └── ipcbus/                # IPC Bus binary (embedded in runtime container)
│       └── main.go
├── api/
│   └── v1alpha1/              # CRD type definitions
│       ├── claw_types.go
│       ├── channel_types.go
│       ├── common_types.go    # CredentialSpec, PersistenceSpec, etc.
│       ├── groupversion_info.go
│       ├── webhook.go         # Validating + Mutating webhooks
│       └── zz_generated.deepcopy.go
├── internal/
│   ├── controller/            # Reconcilers
│   │   ├── claw_controller.go
│   │   ├── channel_controller.go
│   │   ├── persistence.go
│   │   └── finalizer.go       # Finalizer logic for reclaim policies
│   ├── runtime/               # RuntimeAdapter interface + implementations
│   │   ├── builder.go         # RuntimeBuilder interface
│   │   ├── validator.go       # RuntimeValidator interface
│   │   ├── openclaw.go
│   │   ├── nanoclaw.go
│   │   ├── zeroclaw.go
│   │   └── custom.go
│   ├── channel/               # ChannelAdapter implementations
│   │   ├── adapter.go
│   │   ├── slack.go
│   │   ├── telegram.go
│   │   └── webhook.go
│   ├── ipcbus/                # IPC Bus implementation
│   │   ├── bus.go
│   │   ├── bridge.go          # RuntimeBridge interface
│   │   ├── bridge_openclaw.go
│   │   ├── bridge_nanoclaw.go # UDS wrapper bridge
│   │   ├── bridge_zeroclaw.go
│   │   ├── flow_control.go
│   │   ├── stream_tracker.go
│   │   ├── recovery.go
│   │   ├── wal.go
│   │   └── deadletter.go     # Centralized DLQ
│   ├── archive/               # Output archiver (S3-compatible)
│   │   ├── archiver.go
│   │   ├── s3.go
│   │   └── watcher.go        # inotify + periodic scan
│   └── snapshot/              # CSI VolumeSnapshot manager
│       └── snapshotter.go
├── sdk/
│   ├── client.go              # Claw SDK
│   ├── types.go
│   └── channel/               # Channel SDK
│       ├── adapter.go
│       ├── stream.go
│       ├── ratelimit.go
│       ├── outbound_buffer.go
│       ├── busdown_buffer.go  # Bus-down local buffering
│       └── connection.go
├── config/
│   ├── crd/                   # Generated CRD YAML
│   │   └── bases/
│   ├── rbac/                  # RBAC manifests
│   │   ├── role.yaml
│   │   ├── role_binding.yaml
│   │   └── service_account.yaml
│   ├── webhook/               # Webhook configuration
│   │   ├── manifests.yaml
│   │   └── service.yaml
│   ├── manager/               # Operator Deployment
│   └── samples/               # Example CRs
│       ├── openclaw-basic.yaml
│       ├── nanoclaw-minimal.yaml
│       ├── zeroclaw-edge.yaml
│       └── custom-runtime.yaml
├── charts/
│   └── k8s4claw/              # Helm chart
│       ├── Chart.yaml
│       ├── values.yaml
│       └── templates/
├── docs/
│   └── plans/
│       └── 2026-03-04-k8s4claw-design.md
├── hack/                      # Dev scripts
├── Dockerfile
├── Makefile
├── go.mod
├── go.sum
├── LICENSE
└── README.md
```

## 13. Implementation Phases

### Phase 1 — Foundation
- Project scaffolding (kubebuilder)
- CRD types (`Claw`, `ClawChannel`) with field stability markers
- Validating + Mutating admission webhooks
- RuntimeAdapter interface (split Builder + Validator) + OpenClaw implementation
- Basic Reconciler (create/update/delete Pod) with finalizers
- Credential mounting from K8s Secrets (secretRef + keys)
- RBAC manifests (operator + claw pod ServiceAccounts)

### Phase 2 — Communication
- IPC Bus co-process binary
- RuntimeBridge implementations (OpenClaw WS, NanoClaw UDS wrapper, ZeroClaw SSE)
- Standard message protocol + streaming (with CorrelationID/ReplyTo)
- Channel SDK with Bus-down buffering
- Built-in Slack + Webhook sidecars with configurable resources
- ClawChannel → Pod injection reconciliation

### Phase 3 — Persistence
- PVC lifecycle management with ownerReferences + finalizers
- CSI VolumeSnapshot-based session snapshots
- Output archiver sidecar (periodic scan primary + inotify optimization)
- Auto-expansion with maxSize ceiling + kubelet metrics monitoring
- Reclaim policies (Retain/Archive/Delete)

### Phase 4 — Resilience
- Backpressure (4-layer) with per-channel configurable watermarks
- WAL + recovery (on wal-data emptyDir)
- Stream tracker with synthetic end events
- Centralized dead letter queue in IPC Bus
- Sidecar reconnection + Bus-down local buffering
- Graceful shutdown (preStop hooks, per-runtime timeouts)

### Phase 5 — Observability + SDK
- CR status conditions (all condition types)
- Prometheus metrics (expanded set)
- Claw SDK (Go client) with proper error handling
- ExternalSecret integration (for Vault/AWS SM/GCP SM)
- API versioning: conversion webhook scaffolding for future v1alpha2
- Helm chart with configurable values
- Documentation

## Appendix A: Review Issues Addressed

| Issue | Severity | Fix |
|-------|----------|-----|
| C1: IPC Bus restart gap | CRITICAL | Bus moved to co-process inside runtime container (Section 4.2) |
| C2: No API versioning | CRITICAL | Added Section 3.5 with evolution plan + field stability markers |
| C3: Snapshot data loss | CRITICAL | Replaced CronJob tar.gz with CSI VolumeSnapshot (Section 7.3) |
| C4: No admission webhook | CRITICAL | Added Section 3.4 with validating + mutating webhooks |
| H1: Undefined replicas | HIGH | Removed from v1alpha1, webhook rejects (Section 3.1 note) |
| H2: Missing RBAC | HIGH | Added Section 9 with operator + pod + namespace scoping |
| H3: Credential confusion | HIGH | Added Section 3.3 with clear semantics and mutual exclusion |
| H4: PVC expansion no guardrails | HIGH | Added maxSize, kubelet metrics monitoring, Events (Section 7.3) |
| H5: NanoClaw bridge fragile | HIGH | Added UDS wrapper co-process (Section 5.7) |
| H6: Per-sidecar SQLite DLQ | HIGH | Centralized DLQ in IPC Bus (Section 8.4) |
