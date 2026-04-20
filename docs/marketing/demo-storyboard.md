# claw4k8s Demo Storyboard (2 minutes)

> For asciinema recording + Twitter/HN launch. Target audience: Kubernetes users,
> AI/LLM engineers, DevOps/SRE folks. One-liner: "AI agents that manage their own K8s."

---

## Recording setup

```bash
# 1. Install tools
brew install asciinema
cargo install --git https://github.com/asciinema/agg  # GIF converter

# 2. Pre-build operator (so demo timing is predictable)
make build

# 3. Record
asciinema rec demo.cast -c ./scripts/demo-claw4k8s.sh --title "claw4k8s — self-healing AI agents"

# 4. Upload to asciinema.org OR convert to GIF
asciinema upload demo.cast
# -- OR --
agg demo.cast demo.gif --speed 1.5 --theme monokai --font-size 18
```

---

## Shot-by-shot narration (for voice-over or silent captions)

### 00:00 — 00:15 | Hook
**Visual:** Terminal with ASCII banner "claw4k8s — AI agents that heal themselves"
**Text:**
> "Your AI agent is running in production. Memory leak. OOM crash at 2am.
> Who fixes it?"
>
> "What if the agent could fix itself?"

### 00:15 — 00:30 | Setup
**Visual:** `kind create cluster`, operator starts, `kubectl apply` for Claw CR
**Text:**
> "Deploy the k8s4claw operator and a Claw — our AI agent runtime CRD."
> "One resource, full lifecycle: StatefulSet, Service, NetworkPolicy, RBAC."

### 00:30 — 00:50 | The crash
**Visual:** Pod shows OOMKilled status, restart count climbing
**Text:**
> "Simulate the leak. Pod crashes. Exit 137. Restart count: 3."
> "This is where humans usually get paged."

### 00:50 — 01:20 | Auto-healing (the money shot)
**Visual:** `kubectl get clawopsescalation` appears, intent annotation on Claw
**Text:**
> "ClawOpsController saw the crash. Matched an auto-remediation rule."
> "Wrote an intent annotation on the Claw CR — NOT directly on the StatefulSet."
> "Single writer = no controller contention, no race conditions."
> "ClawReconciler validates, checks generation guard, applies the fix."

### 01:20 — 01:40 | Audit trail
**Visual:** ClawOpsEscalation status with Ed25519 signature, analysis field
**Text:**
> "Every action Ed25519-signed. Receipt stored in the CR."
> "Audit chain: who did what, when, with what proof."

### 01:40 — 02:00 | The pitch
**Visual:** Success banner with feature bullets + GitHub URL
**Text:**
> "AI agents managing their own infrastructure."
> "LLM escalation for unknown issues. Human approval via Slack for risky ops."
> "Degrades gracefully — LLM down → notification mode, not paralysis."
> "github.com/Prismer-AI/k8s4claw"

---

## Twitter launch thread

**Tweet 1** (the hook — include GIF):
> We shipped claw4k8s: AI agents that manage their own Kubernetes infrastructure.
>
> OOM at 2am? The agent detects it, diagnoses it, fixes it — and logs every action cryptographically.
>
> [demo.gif]
>
> 🧵

**Tweet 2** (the problem):
> K8s agent runtimes (LLM apps, vector DBs, RAG pipelines) have unique failure modes:
> - LLM stuck in retry loops → memory blowup
> - Context window growth → OOM
> - Model switching → replica churn
>
> Human on-call doesn't scale. k8sgpt diagnoses but doesn't fix. kubectl-ai needs approval for every step.

**Tweet 3** (the architecture):
> The key insight: don't let the agent patch the StatefulSet directly.
>
> Instead: agent writes an "intent annotation" on a Claw CR.
> A single reconciler consumes intents, validates, executes.
>
> One writer = no contention, clean audit, prompt-injection defense at the controller boundary.

**Tweet 4** (the safety):
> Low-risk auto-execute (bump-memory, restart-pod).
> Unknown issues → LLM analysis → human approves in Slack → executed.
> Every action Ed25519-signed (fallback to pure-Go impl if signet CLI unavailable).
>
> Rate limits + circuit breakers prevent runaway remediation.

**Tweet 5** (the CTA):
> 4,500 lines of Go. 18 test packages. Envtest + E2E coverage.
>
> Try it: github.com/Prismer-AI/k8s4claw
> Design doc (Chinese + English): docs/plans/2026-04-19-claw4k8s-design.md
>
> Feedback welcome. DMs open for K8s operator folks.

---

## Hacker News submission

**Title:** `Show HN: claw4k8s – AI agents that manage their own Kubernetes infrastructure`

**Body:**
> We've been building a Kubernetes operator for running AI agents as first-class
> resources. Each "Claw" CR spawns an agent runtime with batteries included:
> persistence, channel sidecars (Slack/Discord/Webhook), IPC bus, RBAC.
>
> What's new in this PR: **claw4k8s** — autonomous operations layer. The agent
> manages its own K8s infrastructure via a two-layer system:
>
> 1. **ClawOpsController** (Go, in the operator): deterministic rule engine that
>    watches Pod status and auto-remediates known patterns (OOM → bump memory,
>    CrashLoop → investigate logs).
>
> 2. **Companion Claw** (LLM agent): handles novel issues. Analyzes the situation,
>    proposes a fix, routes to human approval via existing ClawChannel
>    infrastructure (Slack).
>
> **Key architectural decision:** neither component directly patches
> StatefulSets. All writes go through an "intent annotation" on the Claw CR,
> consumed by a single reconciler. This gives us:
>
> - **Zero controller contention** (one writer)
> - **Prompt injection defense at the boundary** (action whitelist + generation guard)
> - **Clean audit** (every intent → K8s event + Ed25519 receipt)
> - **Graceful degradation** (LLM down → notification mode, not paralysis)
>
> Differs from existing tools:
> - k8sgpt: diagnoses, doesn't fix
> - kubectl-ai: human-in-the-loop for every action
> - Holmes: SRE-focused, not self-management
>
> claw4k8s is **D-first** (the agent managing its own Claw resources), designed
> to extend naturally to **A** (SRE on general K8s workloads) by expanding RBAC.
>
> Repo: github.com/Prismer-AI/k8s4claw
> Design: docs/plans/2026-04-19-claw4k8s-design.md (Chinese + architecture diagrams)
> Demo: [asciinema link]
>
> Feedback especially welcome on:
> - Intent annotation pattern vs direct patch (controllers folks)
> - LLM resilience (3 retries + fallback to human) — too defensive?
> - Whether "agent managing its own infra" is a real category or we're solving
>   a problem no one has
