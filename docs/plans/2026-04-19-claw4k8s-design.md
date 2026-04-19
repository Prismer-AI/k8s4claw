# claw4k8s Design Spec

> AI Agent 自治管理 Kubernetes 基础设施

**Date:** 2026-04-19
**Status:** Draft
**Author:** willamhou + Claude

## 1. Overview

claw4k8s 是 k8s4claw 的自治运维层：一个 AI Agent 管理自己运行的 Kubernetes 基础设施。它由两个组件构成：

1. **ClawOpsController** — operator 内的 Go controller，处理确定性规则自动修复
2. **Companion Claw** — LLM Agent（runtime: k8sops），处理复杂场景分析与人类审批

### 1.1 Goals

- **D 优先**：Agent 管理自己的 Claw 资源（自治闭环、dogfooding）
- **渐进扩展到 A**：未来扩展到 SRE 通用运维
- **半自动**：低风险操作自动执行，高风险操作需人类审批
- **可审计**：每个操作都有 Signet Ed25519 签名 + hash-chained 审计链

### 1.2 Non-Goals (Phase D)

- 管理非 Claw 的 K8s 资源（Phase A）
- 日志聚合分析（后续扩展）
- 多集群管理
- 自定义 LLM fine-tuning

## 2. Architecture

```
┌─── k8s4claw operator process ──────────────────────────────┐
│                                                             │
│  ClawReconciler (existing)                                  │
│  ClawChannelReconciler (existing)                           │
│  ClawSelfConfigReconciler (existing)                        │
│  AutoUpdateReconciler (existing)                            │
│                                                             │
│  ClawOpsController (NEW)                                    │
│    ├─ PodSensor ─── K8s Informer (watch Pod status)         │
│    ├─ MetricSensor ── Prometheus Query (optional)           │
│    ├─ RuleEngine ─── []Rule (Go, deterministic)             │
│    ├─ AutoExecutor ── write intent annotation → Claw CR     │
│    ├─ Escalator ──── create ClawOpsEscalation CR            │
│    ├─ ApprovalWatcher ─ watch Approved escalations          │
│    │    └─ read proposedAction → write intent annotation    │
│    └─ GC ─────────── TTL cleanup of terminal CRs           │
│                                                             │
└────────────────────────┬────────────────────────────────────┘
                         │ watches ClawOpsEscalation
                         ▼
┌─── Companion Claw Pod (runtime: k8sops) ───────────────────┐
│                                                             │
│  [init] ipc-bus sidecar (existing)                          │
│  [init] signet-proxy sidecar (NEW, signs tool calls)        │
│  [init] channel-slack sidecar (via ClawChannel)             │
│                                                             │
│  [main] claw4k8s runtime                                    │
│    ├─ Escalation Watcher (phase=Pending)                    │
│    ├─ Analysis Pipeline                                     │
│    │   ├─ Context Builder                                   │
│    │   ├─ LLM Agent (tool-calling)                          │
│    │   ├─ Signet Policy Check                               │
│    │   └─ Approval Router                                   │
│    └─ K8s Tool Set (read-only + escalation_propose)         │
│                                                             │
│  Volumes:                                                   │
│    ipc-socket: emptyDir (/var/run/claw/)                    │
│    signet-keys: Secret (read-only)                          │
│    signet-policy: ConfigMap (read-only)                     │
│    signet-audit: PVC (read-only to main, write by proxy)    │
└─────────────────────────────────────────────────────────────┘
```

### 2.1 Key Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Controller vs standalone | Controller in operator | Informer native, fast, reliable |
| LLM location | Companion Claw | Dogfooding, leverages IPC Bus + ClawChannel |
| Rule engine location | Go controller | Deterministic, zero external dependency |
| Signal source | Pod status (primary) + Prometheus (optional) | Pod status is authoritative; Events are supplementary |
| Write path | Intent annotation on Claw CR | Single writer (ClawReconciler), no contention |
| Audit | Signet receipt + ClawOpsEscalation CR | Cryptographic + durable |
| Action classification | Signet Policy YAML | Reuse existing tool, not custom code |

### 2.2 Data Flow: Auto-Execution Path

```
Pod OOMKilled → PodSensor detects → Signal{OOMKilled, High, count=3}
  → RuleEngine matches "oom-bump-memory"
  → AutoExecutor:
      1. Signet sign (key: rule-engine)
      2. Create ClawOpsEscalation CR (phase=AutoExecuted)
      3. Write intent annotation on Claw CR
      4. Emit K8s Event on Claw
  → ClawReconciler reads intent → patches StatefulSet → clears annotation
```

### 2.3 Data Flow: LLM Escalation Path

```
Unknown signal (no rule match) → Escalator creates ClawOpsEscalation CR (phase=Pending)
  → Companion Claw watches Pending CRs
  → Pipeline:
      1. Build context (events, metrics, pod describe, logs)
      2. LLM analyzes → produces diagnosis + proposed action
      3. Update Escalation status (phase=Proposed, analysis, proposedAction)
      4. Signet Policy check:
         - allow → auto-execute (update phase=Executed)
         - require_approval → send to ClawChannel (phase=AwaitingApproval)
         - deny → reject (phase=Rejected)
      5. Human approves via Slack → Companion Claw updates phase=Approved
  → ClawOpsController watches Approved CRs
      1. Read proposedAction
      2. Signet sign (key: rule-engine, with delegation from companion-claw)
      3. Write intent annotation on Claw CR
      4. Update Escalation (phase=Executed)
  → ClawReconciler reads intent → executes → clears annotation
```

## 3. New CRD: ClawOpsEscalation

### 3.1 Spec

```go
type ClawOpsEscalationSpec struct {
    ClawRef                 corev1.LocalObjectReference `json:"clawRef"`
    Trigger                 TriggerInfo                 `json:"trigger"`
    EventSnapshot           []EventRecord               `json:"eventSnapshot,omitempty"`
    MetricSnapshot          map[string]string           `json:"metricSnapshot,omitempty"`
    Severity                Severity                    `json:"severity"`
    TTLSecondsAfterFinished *int32                      `json:"ttlSecondsAfterFinished,omitempty"`
}

type TriggerInfo struct {
    Type      TriggerType `json:"type"`
    Message   string      `json:"message"`
    FirstSeen metav1.Time `json:"firstSeen"`
    Count     int32       `json:"count"`
}
```

**TriggerType:** `OOMKilled`, `CrashLoop`, `HighCPU`, `HighMemory`, `PodPending`, `ProbeFailure`, `ChannelDisconnect`, `Evicted`, `Unknown`

**Severity:** `Critical`, `High`, `Medium`, `Low`

### 3.2 Status

```go
type ClawOpsEscalationStatus struct {
    Phase           EscalationPhase `json:"phase"`
    MatchedRule     string          `json:"matchedRule,omitempty"`
    Analysis        string          `json:"analysis,omitempty"`
    ProposedAction  string          `json:"proposedAction,omitempty"`
    ExecutedAction  string          `json:"executedAction,omitempty"`
    ExecutedAt      *metav1.Time    `json:"executedAt,omitempty"`
    Result          string          `json:"result,omitempty"`
    SignetReceipt   string          `json:"signetReceipt,omitempty"`
    ApprovedBy      string          `json:"approvedBy,omitempty"`
    ApprovedAt      *metav1.Time    `json:"approvedAt,omitempty"`
    RejectionReason string          `json:"rejectionReason,omitempty"`
}
```

### 3.3 Phase State Machine

```
Fast path (rule engine):
  Pending → AutoExecuted (terminal)

Escalation path (LLM):
  Pending → Analyzing → Proposed → AwaitingApproval → Approved → Executed (terminal)
                                        ↓
                                   Rejected (terminal)

Error:
  Any → Failed (terminal)

Terminal states: AutoExecuted, Executed, Rejected, Failed
TTL GC deletes CRs in terminal states after TTLSecondsAfterFinished expires.
```

### 3.4 Lifecycle

- **OwnerReference:** non-controller ownerRef to target Claw (cascade delete, but does not trigger ClawReconciler on Escalation status updates)
- **Naming:** `metadata.generateName: <claw-name>-ops-` (K8s-native unique naming, avoids timestamp collision)
- **TTL:** configurable `ttlSecondsAfterFinished` (default 7 days), GC by ClawOpsController
- **Field indexer:** on `status.phase` for efficient Companion Claw watch
- **AutoExecuted is terminal:** no separate Closed transition needed; TTL GC deletes terminal CRs (AutoExecuted, Executed, Rejected, Failed) after TTL expires

## 4. ClawOpsController

### 4.1 Controller Registration

```go
type ClawOpsController struct {
    client.Client
    Scheme          *runtime.Scheme
    RuntimeRegistry *clawruntime.Registry
    RuleEngine      *rules.Engine
    MetricClient    *metrics.Client      // nil = degrade to pod-only mode
    SignetCLI       *signet.CLI
    Recorder        record.EventRecorder
    Config          ClawOpsConfig
}

type ClawOpsConfig struct {
    MaxActionsPerClawPerHour int           // default 5
    CircuitBreakerThreshold  int           // default 3
    ClusterCircuitBreakerPct float64       // default 0.3
    MetricPollInterval       time.Duration // default 60s
    EscalationTTL            int32         // default 604800 (7 days)
}
```

All config values are settable via operator CLI flags and overridable via ConfigMap (hot-reload).

Priority: ConfigMap > CLI flags > code defaults.

### 4.2 SetupWithManager

```go
func (r *ClawOpsController) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        // Use Named() to avoid conflict with ClawReconciler's For(&Claw{})
        Named("clawops").
        // Watch Claws for status changes only (not annotation changes, to avoid loop)
        Watches(&v1alpha1.Claw{},
            &handler.EnqueueRequestForObject{},
            builder.WithPredicates(clawStatusOrPhaseChanged()),
        ).
        // Watch Pods for container status changes (OOM, CrashLoop, etc.)
        Watches(&corev1.Pod{},
            handler.EnqueueRequestsFromMapFunc(r.podToClaw),
            builder.WithPredicates(clawPodStatusChanged()),
        ).
        // Watch Approved escalations to write intent annotations
        Watches(&v1alpha1.ClawOpsEscalation{},
            handler.EnqueueRequestsFromMapFunc(r.escalationToClaw),
            builder.WithPredicates(escalationPhaseChanged()),
        ).
        Complete(r)
}

// clawStatusOrPhaseChanged filters out annotation-only changes to prevent
// reconcile loops when ClawOpsController writes intent annotations.
func clawStatusOrPhaseChanged() predicate.Predicate { ... }
```

**Note:** `ClawOpsController` does NOT use `For(&Claw{})` (which implies ownership) or `Owns(&ClawOpsEscalation{})`. Instead, it uses `Watches()` with explicit predicates to avoid reconcile loops caused by its own annotation writes.

Signal source: Pod status via Informer (primary), Prometheus metrics via periodic goroutine (secondary, optional).

### 4.3 Reconcile Flow

1. Fetch Claw CR
2. Collect signals from Pod status + metrics + Claw status
3. Deduplicate against active (non-terminal) Escalation CRs
4. Check cluster-wide circuit breaker (>30% Claws abnormal → pause all auto-actions)
5. For each signal:
   - Rule engine match → auto-execute (fast path) or escalate
   - Auto-execute: check per-Claw rate limit + per-rule cooldown + circuit breaker
6. Watch for Approved escalations → write intent annotation
7. GC expired terminal Escalation CRs

### 4.4 Signal Collection

**From Pod status:**

| Pod Condition | Signal Type | Severity |
|---------------|-------------|----------|
| `lastState.terminated.reason=OOMKilled` | OOMKilled | High |
| `restartCount` increase >3 in 5m | CrashLoop | High |
| `state.waiting.reason=CrashLoopBackOff` | CrashLoop | High |
| Liveness probe failure | ProbeFailure | Medium |
| `FailedScheduling` | PodPending | Medium |
| `state.terminated.reason=Evicted` | Evicted | High |

**From Prometheus metrics (optional):**

| Condition | Signal Type | Severity |
|-----------|-------------|----------|
| `container_memory_usage / limit > 0.9` for 5m | HighMemory | Medium |
| `cpu_usage / request > 0.95` for 10m | HighCPU | Medium |

### 4.5 Rule Engine

Rules are Go structs with:
- `ID`: unique identifier
- `MatchCriteria`: signal type, min severity, min count (debounce), conditions
- `ActionSpec`: action type + params
- `Cooldown`: per-rule per-Claw cooldown duration

Action types: `PatchResource` (bump memory/cpu), `RestartPod`, `ScaleReplicas`

Source: code `DefaultRules` + ConfigMap override (merge by ID, hot-reload on ConfigMap change). ConfigMap rules are schema-validated on load; invalid rules are skipped with warning.

### 4.6 Rate Limiting and Circuit Breaking

| Mechanism | Scope | Default | Behavior |
|-----------|-------|---------|----------|
| Rule cooldown | Per-rule per-Claw | Rule-specific | Prevent re-firing within cooldown window |
| Action budget | Per-Claw per-hour | 5 | Exceed → force escalate all signals |
| Circuit breaker | Per-Claw | 3 consecutive failures | Break → force escalate all signals |
| Cluster breaker | Cluster-wide | 30% Claws abnormal | Break → pause all auto-actions globally |
| Single reconcile | Per-reconcile | 1 action | Multiple signals → execute highest priority only, escalate rest |

## 5. Companion Claw

### 5.1 K8sOps RuntimeAdapter

```go
type K8sOpsAdapter struct{}

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

func (a *K8sOpsAdapter) runtimeSpec() *RuntimeSpec {
    return &RuntimeSpec{
        Image:         "ghcr.io/prismer-ai/claw4k8s:latest",
        ConfigMode:    ConfigModeDeepMerge,
        WorkspacePath: "/workspace",
        Ports: []corev1.ContainerPort{
            {Name: "gateway", ContainerPort: 18910},
        },
        Resources: corev1.ResourceRequirements{
            Requests: corev1.ResourceList{
                corev1.ResourceCPU:    resource.MustParse("100m"),
                corev1.ResourceMemory: resource.MustParse("256Mi"),
            },
            Limits: corev1.ResourceList{
                corev1.ResourceCPU:    resource.MustParse("500m"),
                corev1.ResourceMemory: resource.MustParse("512Mi"),
            },
        },
    }
}

func (a *K8sOpsAdapter) PodTemplate(claw *v1alpha1.Claw) *corev1.PodTemplateSpec {
    return BuildPodTemplate(claw, a.runtimeSpec())
}
```

Registered as `registry.Register(clawv1alpha1.RuntimeK8sOps, &clawruntime.K8sOpsAdapter{})`.

**Note:** `RuntimeK8sOps` must be added to the `+kubebuilder:validation:Enum` annotation in `common_types.go` and CRDs regenerated via `make manifests`.

Validation: webhook rejects k8sops Claws where `spec.security.networkPolicy.enabled` is not true.

### 5.2 Analysis Pipeline

1. **Watch** Pending ClawOpsEscalation CRs
2. **Update** phase to Analyzing
3. **Build context**: trigger info + event snapshot + metric snapshot + Pod describe + recent logs (truncated to 4096 chars)
4. **LLM analyze**: tool-calling agent produces diagnosis + proposed action
5. **Update** phase to Proposed, write analysis + proposedAction to status
6. **Signet Policy check**: allow / require_approval / deny
7. **Route**: auto-execute, request human approval, or reject

### 5.3 LLM Tool Set (Phase D)

**Read-only (Signet policy: allow):**
- `kubectl_get` — Get K8s resources
- `kubectl_describe` — Describe resources in detail
- `kubectl_logs` — Get pod logs (last 100 lines default)

**Write via escalation (Signet policy: require_approval or allow):**
- `escalation_propose` — Update ClawOpsEscalation status with proposed action (NOT direct Claw modification)

**Blocked (Signet policy: deny):**
- `kubectl_patch`, `kubectl_scale`, `kubectl_rollout`, `kubectl_delete` — Reserved for Phase A

### 5.4 LLM Resilience

- Retry: exponential backoff, 3 attempts (5s, 15s, 45s)
- Fallback on total failure: skip analysis, send raw context to human via ClawChannel, set phase=AwaitingApproval
- LLM is enhancement, not dependency — system degrades to notification mode

### 5.5 Human Approval Flow

```
Companion Claw → IPC Bus → channel-slack sidecar → Slack message
  "[Severity] Claw my-agent: OOM detected
   Analysis: memory leak in handler
   Proposed: bump memory 512Mi→768Mi
   [Approve] [Reject]"

Human clicks Approve → Slack callback → channel sidecar → IPC Bus → Companion Claw
  → Update Escalation phase=Approved, approvedBy=user@corp.com
```

**Timeout behavior by severity:**

| Severity | Timeout Action |
|----------|---------------|
| Critical | Re-notify every 30 min, max 3 times, then escalate to admin channel |
| High | Re-notify once, then reject |
| Medium/Low | Reject |

Default timeout: 30 minutes (configurable).

### 5.6 Deployment (User Perspective)

```yaml
apiVersion: claw.prismer.ai/v1alpha1
kind: Claw
metadata:
  name: ops-companion
spec:
  runtime: k8sops
  config:
    watchNamespaces: ["default", "production"]
    llm:
      model: claude-sonnet-4-6
      maxTokens: 4096
    approval:
      timeoutMinutes: 30
      defaultAction: reject
  credentials:
    secretRef:
      name: llm-api-key
  channels:
    - name: ops-slack
  security:
    networkPolicy:
      enabled: true   # mandatory for k8sops
  selfConfigure:
    enabled: false
```

## 6. Signet Integration

### 6.1 Integration Pattern

| Component | Signet Integration | Method |
|-----------|-------------------|--------|
| ClawOpsController | Sign auto-executed actions | CLI wrapper (`os/exec`) |
| Companion Claw | Sign all LLM tool calls | Sidecar proxy (`signet proxy`) |
| Audit trail | Hash-chained JSONL | Signet proxy writes to PVC |

### 6.2 Key Management

Three independent Ed25519 key pairs stored as K8s Secrets:

| Key | Secret Name | Holder | Purpose |
|-----|-------------|--------|---------|
| `rule-engine` | `claw4k8s-signet-rule-engine` | Operator process | Sign auto-executed actions |
| `companion-claw` | `claw4k8s-signet-companion` | Companion Claw Pod | Sign LLM-recommended actions |
| Human approver | N/A (identity from ClawChannel) | Slack user | Approval identity |

Auto-generated on first operator startup. 90-day rotation.

### 6.3 Policy Engine

Signet Policy YAML replaces custom action classification:

```yaml
version: 1
name: claw4k8s-phase-d
default_action: deny

rules:
  # Read-only tools: always allow, scoped to Claw namespace
  - id: allow-diagnostics
    match:
      tool: "kubectl_get"
      agent: "companion-claw"
      target: "claw://*"
    action: allow
  - id: allow-describe
    match:
      tool: "kubectl_describe"
      agent: "companion-claw"
      target: "claw://*"
    action: allow
  - id: allow-logs
    match:
      tool: "kubectl_logs"
      agent: "companion-claw"
      target: "claw://*"
    action: allow

  # Escalation proposal: allow (only writes to Escalation status)
  - id: allow-propose
    match:
      tool: "escalation_propose"
      agent: "companion-claw"
      target: "claw://*"
    action: allow

  # Rule engine intent: allow (controller writes Claw annotation)
  - id: allow-rule-engine-intent
    match:
      tool: "claw_set_intent"
      agent: "rule-engine"
      target: "claw://*"
    action: allow

  # Block companion from direct K8s writes
  - id: deny-companion-direct-write
    match:
      agent: "companion-claw"
      tool: "kubectl_patch"
    action: deny
  - id: deny-companion-scale
    match:
      agent: "companion-claw"
      tool: "kubectl_scale"
    action: deny
  - id: deny-companion-delete
    match:
      agent: "companion-claw"
      tool: "kubectl_delete"
    action: deny
```

### 6.4 Receipt Storage

- **ClawOpsEscalation CR**: `status.signetReceipt` field stores the receipt JSON
- **PVC audit log**: signet-proxy sidecar appends to `/home/signet/.signet/audit/YYYY-MM-DD.jsonl`
- **Audit PVC mounted read-only** to main runtime container; only signet-proxy sidecar has write access

## 7. RBAC and Security

### 7.1 ClawOpsController Permissions (Incremental to Operator)

```yaml
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["get", "list", "create"]  # create needed for Recorder.Event()
  - apiGroups: ["claw.prismer.ai"]
    resources: ["clawopsescalations"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["claw.prismer.ai"]
    resources: ["clawopsescalations/status"]
    verbs: ["update", "patch"]
```

### 7.2 Companion Claw ServiceAccount (Namespace-Scoped Role)

```yaml
rules:
  # Read-only: diagnostics
  - apiGroups: [""]
    resources: ["pods", "services", "events"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
  - apiGroups: ["apps"]
    resources: ["statefulsets"]
    verbs: ["get", "list"]
  - apiGroups: ["claw.prismer.ai"]
    resources: ["claws", "clawopsescalations"]
    verbs: ["get", "list", "watch"]
  # Only write: Escalation status
  - apiGroups: ["claw.prismer.ai"]
    resources: ["clawopsescalations/status"]
    verbs: ["update", "patch"]
```

**Explicitly absent:**
- No `patch`/`update` on Claw CRs (prevents annotation injection / image injection)
- No `delete` on any resource
- No `create` on Pods/StatefulSets/Deployments
- No access to Secrets via API
- No ConfigMap read (prevents ConfigMap poisoning)
- Namespace-scoped Role, not ClusterRole

**Multi-namespace support:** If `watchNamespaces` includes namespaces other than the Companion Claw's own, a Role must be created in each watched namespace. The K8sOpsAdapter creates these Roles during Claw reconciliation (same pattern as ClawReconciler creating sub-resources). Alternatively, for cluster-wide watch, upgrade to a ClusterRole in Phase A only.

### 7.3 Intent Annotation Specification

**Annotation key:** `claw.prismer.ai/ops-intent`
**Generation key:** `claw.prismer.ai/ops-intent-gen` (monotonic counter, prevents re-execution)

**JSON schema:**

```go
type OpsIntent struct {
    Action     string            `json:"action"`     // must be in allowlist
    Params     map[string]string `json:"params"`     // action-specific params
    Generation int64             `json:"generation"` // must be > last processed gen
    Source     string            `json:"source"`     // "rule-engine" or "companion-claw"
    EscalationRef string         `json:"escalationRef,omitempty"` // originating ClawOpsEscalation name
}
```

**Allowlist:**

```go
var allowedIntentActions = map[string]bool{
    "bump-memory":     true,
    "bump-cpu":        true,
    "restart-pod":     true,
    "rollout-restart": true,
    "scale-replicas":  true,
}
```

**ClawReconciler integration point:** Insert intent check after finalizer handling and before sub-resource reconciliation (after line ~80 in `claw_controller.go`'s Reconcile method):

```go
// In ClawReconciler.Reconcile(), after ensureFinalizer:
if intent, err := r.consumeOpsIntent(ctx, claw); err != nil {
    return ctrl.Result{}, err
} else if intent != nil {
    if err := r.executeIntent(ctx, claw, intent, adapter); err != nil {
        log.Error(err, "failed to execute ops intent", "action", intent.Action)
    }
    // Clear annotation regardless of success (prevent infinite retry)
    // Error is recorded in the originating ClawOpsEscalation CR
}
```

Unknown actions and invalid params are ignored (logged as warning). Params are range-checked (e.g., memory cannot exceed 50% of node allocatable).

### 7.4 Prompt Injection Defense

| Layer | Mechanism | Defense |
|-------|-----------|---------|
| 1. RBAC | K8s ServiceAccount | Hard boundary, LLM cannot exceed permissions |
| 2. Signet Policy | deny rules | Dangerous tools blocked at signing layer |
| 3. Intent whitelist | ClawReconciler validation | Unknown actions ignored |
| 4. Context isolation | External data tagged `[EXTERNAL_DATA]` | Auxiliary, not relied upon for security |
| 5. Schema validation | Tool call params checked | Malformed params rejected |

RBAC is the ultimate defense. All other layers are defense-in-depth.

### 7.5 NetworkPolicy (Mandatory for k8sops)

```yaml
spec:
  podSelector:
    matchLabels:
      claw.prismer.ai/runtime: k8sops
  policyTypes: ["Ingress", "Egress"]
  ingress: []  # No inbound traffic
  egress:
    - to: [{ipBlock: {cidr: "${K8S_API_SERVER_CIDR}"}}]
      ports: [{port: 443, protocol: TCP}]
    - to: [{ipBlock: {cidr: "0.0.0.0/0"}}]
      ports: [{port: 443, protocol: TCP}]  # LLM API
    - to:  # DNS
        - namespaceSelector: {}
          podSelector: {matchLabels: {k8s-app: kube-dns}}
      ports: [{port: 53, protocol: UDP}]
```

Webhook rejects k8sops Claw creation where `spec.security.networkPolicy.enabled` is not true.

### 7.6 Container Hardening

K8sOps adapter forces:

```go
SecurityContext: &corev1.SecurityContext{
    RunAsNonRoot:             ptr(true),
    ReadOnlyRootFilesystem:   ptr(true),
    AllowPrivilegeEscalation: ptr(false),
    Capabilities: &corev1.Capabilities{
        Drop: []corev1.Capability{"ALL"},
    },
    SeccompProfile: &corev1.SeccompProfile{
        Type: corev1.SeccompProfileTypeRuntimeDefault,
    },
}
```

### 7.7 Audit Trail Integrity

- **ClawOpsEscalation CRs**: durable K8s records, owned by Claw
- **Signet receipts**: Ed25519 signed, embedded in CR status
- **Hash-chained JSONL**: append-only on PVC, written by signet-proxy sidecar (main container has read-only access)
- **K8s Events**: real-time notifications (short-lived, supplementary)
- **Verification**: `signet audit --verify` checks chain integrity

## 8. Testing Strategy

### 8.1 Layer 1: Unit Tests (Pure Go, No Dependencies)

| File | Coverage |
|------|----------|
| `internal/rules/engine_test.go` | Rule matching, cooldown, debounce |
| `internal/rules/builtin_test.go` | All default rules |
| `internal/controller/clawops_signals_test.go` | Signal extraction from Pod status |
| `internal/controller/clawops_intent_test.go` | Intent validation, whitelist, range checks |
| `internal/runtime/k8sops_test.go` | Adapter config, ports, probes, security context |

Table-driven tests. Target: 100% coverage on deterministic logic.

### 8.2 Layer 2: Controller Integration (envtest)

Using existing `suite_test.go` envtest infrastructure:

- OOM signal creates AutoExecuted Escalation
- Unknown signal creates Pending Escalation
- Approved Escalation triggers intent annotation write
- TTL GC deletes expired terminal CRs
- Per-Claw circuit breaker activates after consecutive failures
- Cluster-wide circuit breaker activates when >30% Claws abnormal
- Intent annotation flow: controller writes → ClawReconciler reads → annotation cleared

Signet: mock `SignetCLI` interface (no real binary in envtest).

**Note:** envtest has no StatefulSet controller, so pods don't start automatically. Pod status-based signal tests must manually create Pod objects with synthetic `containerStatuses` (OOMKilled, CrashLoopBackOff, etc.).

### 8.3 Layer 3: Companion Claw Integration (Mock LLM)

- Full Analysis Pipeline with mock LLM returning fixed responses
- LLM failure → 3 retries → fallback to human notification
- Signet Policy deny → Escalation rejected
- Context builder truncation and sanitization

### 8.4 Layer 4: E2E Tests (Kind Cluster)

Extending `scripts/test-all-runtimes.sh`:

1. Deploy operator + ClawOpsController
2. Deploy Companion Claw (k8sops runtime, mock LLM server)
3. Create test Claw, simulate OOM → verify AutoExecuted Escalation + memory bump
4. Simulate unknown signal → verify Proposed → mock approval → verify Executed
5. RBAC verification: `kubectl auth can-i patch claws --as=system:serviceaccount:default:claw4k8s-companion` → "no"
6. NetworkPolicy verification
7. Signet audit chain integrity check

### 8.5 Test Matrix

| Scenario | Unit | envtest | Mock LLM | E2E |
|----------|:----:|:-------:|:--------:|:---:|
| Rule engine matching | x | | | |
| Signal extraction | x | | | |
| Intent validation | x | | | |
| Cooldown / debounce | x | x | | |
| Circuit breaker | x | x | | |
| Escalation lifecycle | | x | | x |
| TTL cleanup | | x | | |
| Controller interaction | | x | | x |
| LLM analysis pipeline | | | x | |
| LLM fallback | | | x | |
| Approval flow | | | x | x |
| RBAC boundary | | | | x |
| NetworkPolicy | | | | x |
| Signet signing + audit | | mock | mock | x |
| K8sOps adapter | x | x | | x |

## 9. New Files

```
api/v1alpha1/
  clawopsescalation_types.go        # CRD types
  common_types.go                   # Add RuntimeK8sOps constant

internal/controller/
  clawops_controller.go             # Main controller
  clawops_signals.go                # Signal collection
  clawops_executor.go               # AutoExecutor + intent writer
  clawops_controller_test.go        # envtest tests

internal/rules/
  engine.go                         # Rule engine
  builtin.go                        # Default rules
  engine_test.go                    # Unit tests
  builtin_test.go                   # Unit tests

internal/controller/clawops_intent.go  # Intent annotation validation

internal/runtime/
  k8sops.go                        # K8sOps RuntimeAdapter
  k8sops_test.go                   # Adapter tests

internal/signet/
  signer.go                        # Signer interface + CLI implementation
  signer_test.go                   # Mock implementation for testing

cmd/claw4k8s/
  main.go                          # Companion Claw binary entrypoint
  pipeline.go                      # Analysis Pipeline
  tools.go                         # K8s Tool Set
  llm.go                           # LLM client
  pipeline_test.go                 # Mock LLM tests

config/rbac/
  clawops_role.yaml                # ClawOpsController RBAC increment
  companion_role.yaml              # Companion Claw ServiceAccount + Role

config/crd/bases/
  claw.prismer.ai_clawopsescalations.yaml  # Generated CRD YAML

scripts/
  test-claw4k8s-e2e.sh            # E2E test script
```

## 10. Phase D → A Extension Path

When extending from D (self-management) to A (SRE):

1. **RBAC**: Companion Claw Role → ClusterRole, add namespaces and resource types incrementally
2. **Tool Set**: Unlock `kubectl_patch`, `kubectl_scale`, `kubectl_rollout` for non-Claw resources
3. **Signet Policy**: Update policy YAML to allow broader operations
4. **Signal Sources**: Add node-level signals, cluster-level events
5. **Rule Engine**: Add rules for non-Claw resources (Deployments, Services)
6. **Intent Mechanism**: Extend beyond Claw annotation to general resource patches

Each extension requires explicit RBAC changes (not configuration toggles).
