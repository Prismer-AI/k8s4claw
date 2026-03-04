# k8s4claw Design Document

**Date:** 2026-03-04
**Status:** Approved (Rev 3 — post-openclaw research)
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

  # Security (see Section 9)
  security:
    podSecurityContext:
      runAsNonRoot: true
      runAsUser: 1000
      runAsGroup: 1000
      fsGroup: 1000
      seccompProfile:
        type: RuntimeDefault
    containerSecurityContext:
      readOnlyRootFilesystem: true
      allowPrivilegeEscalation: false
      capabilities:
        drop: [ALL]
    networkPolicy:
      enabled: true                # default: true (default-deny + selective allow)
      allowedEgressCIDRs: []       # additional CIDR ranges
      allowedIngressNamespaces: [] # namespaces allowed to reach this Claw
      customRules: []              # raw NetworkPolicy rules for advanced use

  # Ingress (see Section 9.5)
  ingress:
    enabled: false
    className: nginx
    host: agent.example.com
    tls:
      enabled: true
      secretName: agent-tls
    annotations: {}
    basicAuth:
      enabled: false
      secretRef:
        name: agent-basic-auth

  # Availability (see Section 9.6)
  availability:
    pdb:
      enabled: true
      minAvailable: 1              # for single replica: prevents drain eviction

  # Auto-Update (see Section 14)
  autoUpdate:
    enabled: false
    versionConstraint: "~1.x"      # semver constraint
    schedule: "0 3 * * *"          # check schedule (cron)
    preBackup: true                # backup before update
    healthTimeout: 10m             # health verification timeout (2m-30m)
    maxRollbacks: 3                # circuit breaker threshold

  # Agent self-configuration (see Section 3.6)
  selfConfigure:
    enabled: false
    allowedActions:
      - skills
      - config
      # - workspaceFiles
      # - envVars

  # Observability
  observability:
    metrics: true
    logs: true
    tracing: false
    alerts:
      enabled: true                # deploy PrometheusRule
    dashboard:
      enabled: true                # deploy Grafana dashboard ConfigMap
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
4. **Rotation:** The Operator uses a **Secret hash annotation** mechanism for zero-downtime credential rotation:
   - On each reconcile, compute SHA-256 of all referenced Secret data
   - Inject hash as Pod annotation: `claw.prismer.ai/secret-hash: <sha256>`
   - When a Secret changes, the hash changes → StatefulSet detects annotation diff → triggers rolling update
   - The Operator watches referenced Secrets (via indexer) and enqueues the owning Claw for re-reconciliation

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

### 3.6 ClawSelfConfig (Agent Self-Configuration)

Allows AI agents to modify their own configuration at runtime through the K8s API, with Operator-enforced security boundaries. Inspired by openclaw-rocks/k8s-operator's `OpenClawSelfConfig` pattern.

```yaml
apiVersion: claw.prismer.ai/v1alpha1
kind: ClawSelfConfig
metadata:
  name: my-agent-install-skill
spec:
  clawRef: my-research-agent        # Target Claw instance (required)

  # Each action category requires explicit authorization in Claw.spec.selfConfigure.allowedActions
  addSkills:                         # max 10 per request
    - "@anthropic/tool-use"
  removeSkills: []                   # max 10

  configPatch:                       # Partial config merge
    model: "claude-sonnet-4"

  addWorkspaceFiles:                 # max 10
    my-prompt.md: |
      You are a research assistant...
  removeWorkspaceFiles: []           # max 10

  addEnvVars:                        # max 10
    - name: CUSTOM_FLAG
      value: "true"
  removeEnvVars: []                  # max 10

status:
  phase: Pending                     # Pending | Applied | Failed | Denied
  message: ""
  appliedAt: null
```

**Security model:**

1. `spec.selfConfigure.enabled` must be `true` on the parent Claw CR
2. Each action category (`skills`, `config`, `workspaceFiles`, `envVars`) must be in `allowedActions`
3. Per-request quantity limits (max 10) prevent bulk abuse
4. Unauthorized actions are marked `Denied` with a K8s Warning Event for audit
5. Applied SelfConfig resources are auto-deleted after 1h TTL
6. OwnerReference set to parent Claw for cascade deletion

**Reconciliation flow:**

```
ClawSelfConfig created by Agent
  → Operator validates selfConfigure enabled
  → Operator validates each action against allowedActions
  → Unauthorized? → Phase=Denied + Warning Event
  → Authorized? → Optimistic concurrent update of Claw spec (with retry)
  → Set OwnerReference → Phase=Applied + Normal Event
  → TTL expiry (1h) → Auto-delete
```

**RBAC for Agent Pod** (only when `selfConfigure.enabled: true`):

```yaml
rules:
  - apiGroups: ["claw.prismer.ai"]
    resources: [claws]
    verbs: [get]
    resourceNames: ["<instance-name>"]  # Scoped to own instance only
  - apiGroups: ["claw.prismer.ai"]
    resources: [clawselfconfigs]
    verbs: [create, get, list]
```

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
│  initContainers (one-shot, run-to-completion):          │
│  ┌─────────────────────────────────────────────┐        │
│  │ claw-init (busybox or runtime image)        │        │
│  │  1. Merge/overwrite runtime config          │        │
│  │  2. Seed workspace files from ConfigMap     │        │
│  │  3. Install runtime dependencies            │        │
│  │  4. Install declared skills (NPM_CONFIG_    │        │
│  │     IGNORE_SCRIPTS=true for supply chain    │        │
│  │     protection)                             │        │
│  └─────────────────────────────────────────────┘        │
│                                                         │
│  initContainers (native sidecar, restartPolicy: Always) │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐              │
│  │ Slack    │  │ Telegram │  │ Archive  │              │
│  │ Sidecar  │  │ Sidecar  │  │ Sidecar  │              │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘              │
│       │              │              │                    │
│       ▼              ▼              ▼                    │
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
│    config-vol    (ConfigMap)         — Runtime config    │
│    session-pvc   (PVC, RWO)          — Session state    │
│    output-pvc    (PVC, RWO)          — Output artifacts │
│    workspace-pvc (PVC, RWO)          — Workspace        │
│    shared-*      (PVC, RWX)          — Shared volumes   │
│    cache         (emptyDir, tmpfs)   — Model cache      │
│    tmp           (emptyDir)          — Writable /tmp     │
└─────────────────────────────────────────────────────────┘
```

**Key design decisions:**
- IPC Bus is a co-process (not sidecar) → shares lifecycle with runtime, no restart-gap
- WAL stored on `emptyDir` named `wal-data` → survives container restart, acceptable loss on pod eviction (sidecars re-buffer)
- Channel sidecars are K8s native sidecars (init containers with `restartPolicy: Always`) → guaranteed to start before runtime and survive runtime restarts
- Init container runs before everything else → ensures config, workspace, and skills are ready before runtime starts
- `readOnlyRootFilesystem: true` on all containers → writable paths via explicit volume mounts (`/tmp`, `/data`, `/var/run/claw`)
- Archive sidecar is a native sidecar (not CronJob) → shares PVC access, solves mount problem

### 4.3 Resource Defaults

Each container has sensible defaults. Users can override via CRD spec.

| Component | CPU Request | Memory Request | CPU Limit | Memory Limit |
|-----------|-------------|----------------|-----------|--------------|
| **OpenClaw runtime** | 500m | 1Gi | 2000m | 4Gi |
| **NanoClaw runtime** | 100m | 256Mi | 500m | 512Mi |
| **ZeroClaw runtime** | 50m | 32Mi | 200m | 128Mi |
| **IPC Bus** (co-process) | — | — | — | — |
| **Channel sidecar** (default) | 50m | 64Mi | 200m | 128Mi |
| **Archive sidecar** | 50m | 64Mi | 200m | 128Mi |
| **Init container** | 100m | 128Mi | 500m | 256Mi |

Note: IPC Bus shares the runtime container's resource allocation (co-process).

### 4.4 ConfigMap Management

The Operator generates a ConfigMap per Claw instance containing runtime configuration. Three merge modes are supported:

| Mode | Behavior | Use Case |
|------|----------|----------|
| **Overwrite** | Init container replaces config entirely | Clean deployments, no runtime config drift |
| **DeepMerge** | Init container deep-merges Operator config over existing | Preserve runtime modifications (e.g., installed skills) |
| **Passthrough** | Mount ConfigMap read-only, runtime reads directly | Stateless runtimes (ZeroClaw) |

Default mode per runtime:
- OpenClaw: `DeepMerge` (preserves skill configs installed at runtime)
- NanoClaw: `Overwrite` (SQLite-based config, managed externally)
- ZeroClaw: `Passthrough` (immutable config model)

**PostStart hook:** On container restart (not pod recreation), a PostStart lifecycle hook re-applies Operator-managed config for `DeepMerge` mode, preventing config drift after OOM kills or crashes.

### 4.5 Security Context Defaults

All containers run with hardened security context by default:

```yaml
securityContext:
  runAsNonRoot: true
  runAsUser: 1000
  runAsGroup: 1000
  readOnlyRootFilesystem: true
  allowPrivilegeEscalation: false
  capabilities:
    drop: [ALL]
  seccompProfile:
    type: RuntimeDefault
```

Pod-level:

```yaml
securityContext:
  runAsNonRoot: true
  fsGroup: 1000
  seccompProfile:
    type: RuntimeDefault
```

The mutating webhook injects these defaults. Users can relax specific settings via `spec.security` but the validating webhook **rejects**:
- `runAsUser: 0` (root)
- `privileged: true`
- Adding `SYS_ADMIN` capability

### 4.6 Graceful Shutdown

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

### 4.7 ClawChannel → Pod Injection

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

## 9. Security

### 9.1 Pod Security (Defense-in-Depth)

All Claw Pods run with hardened defaults (see Section 4.5 for exact SecurityContext). The security model follows defense-in-depth:

| Layer | Mechanism | Default |
|-------|-----------|---------|
| **User** | Non-root UID 1000 | Enforced (webhook rejects root) |
| **Filesystem** | Read-only root | Enforced (writable via explicit mounts) |
| **Capabilities** | Drop ALL | Enforced (webhook rejects SYS_ADMIN) |
| **Seccomp** | RuntimeDefault profile | Enforced |
| **Privilege escalation** | Blocked | Enforced |
| **Network** | Default-deny NetworkPolicy | Enabled by default |
| **Service token** | Not mounted | Unless selfConfigure enabled |

The mutating webhook injects these defaults on CREATE. The validating webhook rejects unsafe overrides (root, privileged, SYS_ADMIN).

### 9.2 NetworkPolicy

Each Claw instance gets an auto-generated NetworkPolicy with **default-deny + selective allow**:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: <claw-name>-netpol
spec:
  podSelector:
    matchLabels:
      claw.prismer.ai/instance: <claw-name>
  policyTypes: [Ingress, Egress]

  egress:
    # DNS — always allowed
    - ports:
        - port: 53
          protocol: UDP
        - port: 53
          protocol: TCP

    # HTTPS — AI provider APIs (OpenAI, Anthropic, etc.)
    - ports:
        - port: 443
          protocol: TCP

    # Kubernetes API — only when selfConfigure enabled
    # (conditionally injected by Operator)
    - ports:
        - port: 6443
          protocol: TCP

    # User-defined additional egress CIDRs
    # (from spec.security.networkPolicy.allowedEgressCIDRs)

  ingress:
    # Same namespace — always allowed (sidecars, probes)
    - from:
        - podSelector: {}

    # Cross-namespace access (from spec.security.networkPolicy.allowedIngressNamespaces)
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: <ns>

    # Ingress controller (when spec.ingress.enabled)
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: ingress-nginx
      ports:
        - port: <gateway-port>
          protocol: TCP
```

When `spec.security.networkPolicy.enabled: false`, the Operator skips NetworkPolicy creation entirely (for clusters using alternative CNI policies).

### 9.3 RBAC

#### 9.3.1 Operator ServiceAccount

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

  # Networking
  - apiGroups: ["networking.k8s.io"]
    resources: [networkpolicies, ingresses]
    verbs: [get, list, watch, create, update, patch, delete]

  # RBAC (per-instance ServiceAccount/Role/RoleBinding)
  - apiGroups: [""]
    resources: [serviceaccounts]
    verbs: [get, list, watch, create, update, patch, delete]
  - apiGroups: ["rbac.authorization.k8s.io"]
    resources: [roles, rolebindings]
    verbs: [get, list, watch, create, update, patch, delete]

  # Autoscaling
  - apiGroups: ["policy"]
    resources: [poddisruptionbudgets]
    verbs: [get, list, watch, create, update, patch, delete]

  # CSI VolumeSnapshots
  - apiGroups: ["snapshot.storage.k8s.io"]
    resources: [volumesnapshots]
    verbs: [get, list, watch, create, delete]

  # Monitoring
  - apiGroups: ["monitoring.coreos.com"]
    resources: [servicemonitors, prometheusrules]
    verbs: [get, list, watch, create, update, patch, delete]

  # CRDs
  - apiGroups: ["claw.prismer.ai"]
    resources: [claws, claws/status, claws/finalizers,
                clawchannels, clawchannels/status,
                clawselfconfigs, clawselfconfigs/status]
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

#### 9.3.2 Claw Pod ServiceAccount

Claw Pods run with a **per-instance ServiceAccount** created by the Operator:

```yaml
# Default: no K8s API access
automountServiceAccountToken: false
```

When `spec.selfConfigure.enabled: true`, the Operator:
1. Sets `automountServiceAccountToken: true`
2. Creates a scoped Role (see Section 3.6 RBAC)
3. Creates a RoleBinding to the per-instance ServiceAccount

For custom K8s API access needs:

```yaml
spec:
  serviceAccount:
    name: my-custom-sa         # User-managed ServiceAccount (overrides Operator-managed)
    annotations:               # For AWS IRSA / GCP Workload Identity
      eks.amazonaws.com/role-arn: arn:aws:iam::role/claw-role
```

#### 9.3.3 Namespace Scoping

The operator supports both modes:

| Mode | Use Case | Configuration |
|------|----------|---------------|
| **Namespace-scoped** (default) | Single team, simple setup | `--watch-namespace=my-ns` |
| **Cluster-scoped** | Multi-team, shared operator | No namespace flag, ClusterRole required |

For multi-tenancy, each team gets their own namespace. The operator's RBAC is limited to watched namespaces. Claw Pods in different namespaces cannot access each other's Secrets or PVCs.

### 9.4 Skill Installation Security

Declared skills in `spec.config` are installed during init container execution with supply chain protections:

- `NPM_CONFIG_IGNORE_SCRIPTS=true` — disables npm lifecycle scripts (prevents malicious postinstall)
- Skills are installed in a writable volume, not the read-only root filesystem
- Future: signature verification for skill packages (post-v1alpha1)

### 9.5 Ingress Management

When `spec.ingress.enabled: true`, the Operator creates an Ingress resource for external HTTP access:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: <claw-name>
  annotations:
    # User-defined annotations (e.g., cert-manager, rate limiting)
    # + optional basic auth annotation
spec:
  ingressClassName: <spec.ingress.className>
  tls:
    - hosts: [<spec.ingress.host>]
      secretName: <spec.ingress.tls.secretName>
  rules:
    - host: <spec.ingress.host>
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: <claw-name>
                port:
                  number: <gateway-port>
```

**Basic Auth** (optional): When `spec.ingress.basicAuth.enabled: true`, the Operator adds nginx basic auth annotations and references the specified Secret containing htpasswd data.

Primary use case: exposing webhook channels that need inbound HTTP from external services (e.g., Slack Event Subscriptions, GitHub Webhooks).

### 9.6 PodDisruptionBudget

When `spec.availability.pdb.enabled: true` (default), the Operator creates a PDB:

```yaml
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: <claw-name>
spec:
  minAvailable: <spec.availability.pdb.minAvailable>  # default: 1
  selector:
    matchLabels:
      claw.prismer.ai/instance: <claw-name>
```

Even for single-replica deployments, PDB prevents `kubectl drain` from evicting the Claw pod without explicit override (`--delete-emptydir-data --force`), protecting stateful agents during node maintenance.

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

**Operator metrics** (exposed on operator `/metrics` endpoint):

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `claw_reconcile_total` | Counter | instance, namespace, result | Reconcile invocations |
| `claw_reconcile_duration_seconds` | Histogram | instance, namespace | Reconcile latency |
| `claw_resource_creation_failures_total` | Counter | instance, namespace, resource | Sub-resource creation failures |
| `claw_managed_instances` | Gauge | — | Total managed Claw instances |
| `claw_instance_phase` | Gauge | instance, namespace, phase | Instance phase (1=active) |
| `claw_instance_ready` | Gauge | instance, namespace | Pod readiness (1/0) |
| `claw_instance_info` | Gauge | instance, namespace, runtime, version | Instance metadata (for PromQL joins) |
| `claw_autoupdate_checks_total` | Counter | instance, namespace, result | Auto-update version checks |
| `claw_autoupdate_applied_total` | Counter | instance, namespace | Updates applied |
| `claw_autoupdate_rollbacks_total` | Counter | instance, namespace | Rollbacks triggered |

**Runtime metrics** (exposed via IPC Bus `/metrics` sidecar endpoint):

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `claw_runtime_status` | Gauge | instance | Runtime health (1=healthy) |
| `claw_messages_total` | Counter | channel, direction, type | Message throughput |
| `claw_stream_duration_seconds` | Histogram | channel | Stream duration |
| `claw_backpressure_state` | Gauge | channel | Pressure state (0=normal, 1=degraded, 2=blocked) |
| `claw_dead_letter_count` | Gauge | — | DLQ size (centralized) |
| `claw_storage_usage_bytes` | Gauge | volume_type | PVC usage by type |
| `claw_storage_expansion_total` | Counter | volume_type | Auto-expansion events |
| `claw_snapshot_duration_seconds` | Histogram | — | Snapshot creation time |
| `claw_sidecar_buffer_dropped_total` | Counter | channel | Messages dropped during Bus-down buffering |

### 10.3 PrometheusRule (Alerting)

When `spec.observability.alerts.enabled: true`, the Operator deploys a PrometheusRule with production-ready alerts:

| Alert | Condition | Severity | For |
|-------|-----------|----------|-----|
| `ClawReconcileErrors` | Reconcile error rate > 0 in 5m | Warning | 5m |
| `ClawInstanceDegraded` | Phase = Failed or Degraded | Critical | 2m |
| `ClawSlowReconciliation` | P99 reconcile > 30s | Warning | 10m |
| `ClawPodCrashLooping` | Container restarts > 2 in 10m | Critical | 0m |
| `ClawPodOOMKilled` | OOM kill detected | Warning | 0m |
| `ClawPVCNearlyFull` | PVC usage > 85% | Warning | 5m |
| `ClawDLQBacklog` | Dead letter count > 100 | Warning | 5m |
| `ClawAutoUpdateRollback` | Rollback in last 1h | Warning | 0m |
| `ClawChannelDisconnected` | Channel disconnected > 5m | Warning | 5m |
| `ClawAllChannelsBlocked` | All channels in blocked state | Critical | 1m |

Each alert includes:
- `runbook_url` annotation pointing to `https://docs.prismer.ai/runbooks/{{ $labels.alertname }}`
- Standard labels: `team`, `service`, `severity`

### 10.4 Grafana Dashboard

When `spec.observability.dashboard.enabled: true`, the Operator deploys Grafana dashboard ConfigMaps (auto-discovered via `grafana_dashboard: "1"` label):

**Dashboard 1: Operator Overview** (multi-instance):
- Managed instances count / ready count
- Reconcile success/error rates (time series)
- Resource creation failure breakdown
- Instance table (name, runtime, phase, version, uptime)
- Auto-update activity (checks, applies, rollbacks)

**Dashboard 2: Instance Detail** (single instance, variable selector):
- Health: phase, readiness, CPU/memory/PVC utilization
- Communication: message rate by channel, stream duration P50/P95/P99
- Backpressure: per-channel state timeline, DLQ depth
- Storage: PVC usage trends, snapshot history, archive throughput
- Errors: container restarts, OOM kills, sidecar reconnections

### 10.5 Operator Health Endpoints

The operator exposes standard health endpoints:

| Endpoint | Purpose | Implementation |
|----------|---------|----------------|
| `/healthz` | Liveness probe | Ping check (always passes if process alive) |
| `/readyz` | Readiness probe | Waits for informer cache sync before passing |

**Readiness design:** Uses `atomic.Bool` set by a background goroutine that waits for all informer caches to sync. This prevents the operator from accepting reconcile events before its view of the cluster is consistent — critical for avoiding race conditions on startup.

```go
// Operator readiness check
mgr.AddReadyzCheck("informer-sync", func(req *http.Request) error {
    if !cacheReady.Load() {
        return fmt.Errorf("informer cache not yet synced")
    }
    return nil
})
```

### 10.6 Kubernetes Events

The Operator emits structured Events for key lifecycle transitions:

| Event Type | Reason | Description |
|------------|--------|-------------|
| Normal | `ClawProvisioning` | Instance entering provisioning phase |
| Normal | `ClawRunning` | Instance reached running state |
| Normal | `ResourceCreated` | Sub-resource created/updated |
| Normal | `SecretRotated` | Secret hash changed, rolling update triggered |
| Normal | `StorageExpanding` | PVC auto-expansion initiated |
| Normal | `AutoUpdateApplied` | New version applied |
| Normal | `SelfConfigApplied` | Agent self-configuration applied |
| Warning | `ClawDegraded` | Instance entered degraded state |
| Warning | `StorageAtMaxSize` | PVC at maxSize, cannot expand |
| Warning | `AutoUpdateRollback` | Update failed, rolling back |
| Warning | `SelfConfigDenied` | Unauthorized self-configuration attempt |
| Warning | `DLQExhausted` | Message exhausted retry attempts |

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

## 12. Auto-Update + Circuit Breaker

When `spec.autoUpdate.enabled: true`, the Operator manages automatic runtime image updates with safety guarantees.

### 12.1 Version Resolution

The Operator periodically checks for new versions (on `spec.autoUpdate.schedule`):

1. Query OCI registry for available tags (with anonymous token exchange + TTL cache)
2. Parse tags as semantic versions
3. Filter against `spec.autoUpdate.versionConstraint` (e.g., `~1.x`, `^2.0.0`)
4. Skip versions in the `failedVersions` list
5. If newer version found → set status condition `AutoUpdateAvailable=True`

### 12.2 Update State Machine

```
Idle
  → New version detected
  → Phase 1: Pre-backup (if spec.autoUpdate.preBackup: true)
    → Scale StatefulSet to 0 → Wait for Pod termination
    → Create backup Job (rclone to S3 or CSI snapshot)
    → Wait for backup completion
  → Phase 2: Apply update
    → Update image tag in StatefulSet spec
    → StatefulSet controller creates new Pod
  → Phase 3: Health verification
    → Monitor Pod readiness for spec.autoUpdate.healthTimeout (default: 10m)
    → Pod ready? → Phase = Running, record version in history
    → Timeout? → Trigger rollback

Rollback:
  → Revert image tag to previous version
  → If pre-backup was taken: restore PVC from backup
  → Increment rollback counter
  → Add version to failedVersions list
  → Emit Warning Event + increment claw_autoupdate_rollbacks_total metric

Circuit Breaker:
  → rollbackCount >= spec.autoUpdate.maxRollbacks (default: 3)
  → Auto-update paused, condition: AutoUpdateCircuitOpen=True
  → Emit Critical Event: "AutoUpdateCircuitOpen"
  → Requires manual reset: set annotation claw.prismer.ai/reset-circuit-breaker: "true"
```

### 12.3 Safety Guarantees

| Guarantee | Mechanism |
|-----------|-----------|
| No data loss | Pre-backup before update (optional but default) |
| No silent failures | Health verification with configurable timeout |
| No infinite retry | Failed versions are permanently skipped |
| No cascading failures | Circuit breaker after N rollbacks |
| Digest pinning | If image uses `@sha256:...` digest, auto-update is skipped |
| Manual override | User can always set image tag directly in spec |

### 12.4 Status

```yaml
status:
  autoUpdate:
    currentVersion: "1.2.3"
    availableVersion: "1.3.0"
    lastCheck: "2026-03-04T03:00:00Z"
    lastUpdate: "2026-03-03T03:05:00Z"
    rollbackCount: 0
    failedVersions: ["1.2.5"]  # Versions that failed health check
    circuitOpen: false
    versionHistory:
      - version: "1.2.3"
        appliedAt: "2026-03-03T03:05:00Z"
        status: Healthy
      - version: "1.2.5"
        appliedAt: "2026-03-02T03:05:00Z"
        status: RolledBack
```

## 13. Project Structure

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
│       ├── selfconfig_types.go # ClawSelfConfig CRD
│       ├── common_types.go    # CredentialSpec, PersistenceSpec, SecuritySpec, etc.
│       ├── groupversion_info.go
│       ├── webhook.go         # Validating + Mutating webhooks
│       └── zz_generated.deepcopy.go
├── internal/
│   ├── controller/            # Reconcilers
│   │   ├── claw_controller.go
│   │   ├── channel_controller.go
│   │   ├── selfconfig_controller.go  # ClawSelfConfig reconciler
│   │   ├── persistence.go
│   │   ├── autoupdate.go      # Auto-update state machine
│   │   ├── metrics.go         # Prometheus metric definitions
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
│   ├── resources/             # Sub-resource builders (pure functions)
│   │   ├── statefulset.go     # StatefulSet with init containers + sidecars
│   │   ├── configmap.go       # ConfigMap generation (merge modes)
│   │   ├── networkpolicy.go   # Default-deny NetworkPolicy
│   │   ├── rbac.go            # Per-instance ServiceAccount + Role + RoleBinding
│   │   ├── service.go
│   │   ├── ingress.go         # Ingress with optional Basic Auth
│   │   ├── pdb.go             # PodDisruptionBudget
│   │   ├── pvc.go             # PVC lifecycle
│   │   ├── secret.go          # Gateway token + secret hash
│   │   ├── servicemonitor.go  # Prometheus ServiceMonitor
│   │   ├── prometheusrule.go  # Alerting rules
│   │   └── grafana_dashboard.go # Grafana dashboard ConfigMaps
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
│   ├── registry/              # OCI image version resolver
│   │   └── resolver.go       # Semver parsing, TTL cache, anonymous token exchange
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
│   ├── networkpolicy/         # Default NetworkPolicy templates
│   ├── webhook/               # Webhook configuration
│   │   ├── manifests.yaml
│   │   └── service.yaml
│   ├── prometheus/            # PrometheusRule + ServiceMonitor templates
│   ├── grafana/               # Grafana dashboard JSON templates
│   ├── manager/               # Operator Deployment
│   └── samples/               # Example CRs
│       ├── openclaw-basic.yaml
│       ├── openclaw-full.yaml     # All features enabled
│       ├── nanoclaw-minimal.yaml
│       ├── zeroclaw-edge.yaml
│       ├── custom-runtime.yaml
│       └── selfconfig-example.yaml # ClawSelfConfig example
├── charts/
│   └── k8s4claw/              # Helm chart
│       ├── Chart.yaml
│       ├── values.yaml
│       └── templates/
├── docs/
│   ├── plans/
│   │   └── 2026-03-04-k8s4claw-design.md
│   └── runbooks/              # Alert runbook templates
├── hack/                      # Dev scripts
├── Dockerfile
├── Makefile
├── go.mod
├── go.sum
├── LICENSE
└── README.md
```

## 14. Implementation Phases

### Phase 1 — Foundation
- Project scaffolding (kubebuilder)
- CRD types (`Claw`, `ClawChannel`, `ClawSelfConfig`) with field stability markers
- Validating + Mutating admission webhooks (security context enforcement, root rejection)
- RuntimeAdapter interface (split Builder + Validator) + OpenClaw implementation
- Basic Reconciler (create/update/delete StatefulSet) with finalizers
- Credential mounting from K8s Secrets (secretRef + keys) with Secret hash annotation rotation
- Per-instance ServiceAccount + Role + RoleBinding creation
- RBAC manifests (operator + claw pod ServiceAccounts)
- Pod security context defaults (non-root, read-only rootfs, drop ALL, seccomp)
- Init container for config merge + workspace seed + dependency install
- ConfigMap management (Overwrite / DeepMerge / Passthrough modes)
- Resource defaults per runtime type
- Operator `/healthz` + `/readyz` endpoints (with informer cache sync check)

### Phase 2 — Communication
- IPC Bus co-process binary
- RuntimeBridge implementations (OpenClaw WS, NanoClaw UDS wrapper, ZeroClaw SSE)
- Standard message protocol + streaming (with CorrelationID/ReplyTo)
- Channel SDK with Bus-down buffering
- Built-in Slack + Webhook sidecars with configurable resources
- ClawChannel → Pod injection reconciliation

### Phase 3 — Persistence + Security
- PVC lifecycle management with ownerReferences + finalizers
- CSI VolumeSnapshot-based session snapshots
- Output archiver sidecar (periodic scan primary + inotify optimization)
- Auto-expansion with maxSize ceiling + kubelet metrics monitoring
- Reclaim policies (Retain/Archive/Delete)
- NetworkPolicy per instance (default-deny + selective allow)
- Ingress management (with optional Basic Auth)
- PodDisruptionBudget per instance
- Skill installation security (`NPM_CONFIG_IGNORE_SCRIPTS=true`)

### Phase 4 — Resilience
- Backpressure (4-layer) with per-channel configurable watermarks
- WAL + recovery (on wal-data emptyDir)
- Stream tracker with synthetic end events
- Centralized dead letter queue in IPC Bus
- Sidecar reconnection + Bus-down local buffering
- Graceful shutdown (preStop hooks, per-runtime timeouts)

### Phase 5 — Observability + SDK
- CR status conditions (all condition types)
- Prometheus metrics (operator + runtime, expanded set with labels)
- PrometheusRule with 10 production alerts
- Grafana Dashboard ConfigMaps (operator overview + instance detail)
- ServiceMonitor for Prometheus Operator integration
- K8s Events for all lifecycle transitions
- Claw SDK (Go client) with proper error handling
- ExternalSecret integration (for Vault/AWS SM/GCP SM)
- API versioning: conversion webhook scaffolding for future v1alpha2
- Helm chart with configurable values
- Documentation

### Phase 6 — Auto-Update + Self-Configuration
- OCI registry version resolver (with anonymous token exchange + TTL cache)
- Auto-update state machine (pre-backup → update → health verify → rollback)
- Circuit breaker (configurable max rollbacks, manual reset)
- Failed version tracking + semver constraint filtering
- ClawSelfConfig reconciler (allowlist validation, TTL cleanup)
- SelfConfigure RBAC (scoped per-instance Role)

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

## Appendix B: Additions from openclaw-rocks/k8s-operator Research (Rev 3)

| Addition | Section | Rationale |
|----------|---------|-----------|
| Pod security context defaults | 4.5 | Defense-in-depth: non-root, read-only rootfs, drop ALL, seccomp |
| NetworkPolicy (default-deny) | 9.2 | Network-level isolation per instance, critical for multi-tenancy |
| Secret hash annotation rotation | 3.3 | Concrete mechanism for zero-downtime credential rotation |
| Init container strategy | 4.2 | Config merge, workspace seed, dependency + skill installation |
| Resource defaults table | 4.3 | Explicit defaults per runtime + sidecar type |
| ConfigMap management modes | 4.4 | Overwrite / DeepMerge / Passthrough for different runtime models |
| Ingress management | 9.5 | External HTTP access for webhook channels |
| PodDisruptionBudget | 9.6 | Drain protection for stateful agents |
| Auto-Update + Circuit Breaker | 12 | Automated image updates with safety guarantees |
| PrometheusRule (10 alerts) | 10.3 | Production-ready alerting out of the box |
| Grafana Dashboards | 10.4 | Operator overview + instance detail dashboards |
| Operator health endpoints | 10.5 | /healthz + /readyz with informer cache sync |
| K8s Events catalog | 10.6 | Structured events for lifecycle audit |
| ClawSelfConfig CRD | 3.6 | Agent self-modification with Operator-enforced security |
| Expanded Prometheus metrics | 10.2 | Operator + runtime metrics with proper labels |
| Skill installation security | 9.4 | NPM_CONFIG_IGNORE_SCRIPTS for supply chain protection |
