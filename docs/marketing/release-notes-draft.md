# Release Notes Draft — v0.2.0-claw4k8s

> Target release: v0.2.0 (first release including claw4k8s autonomous ops)
> Status: **DRAFT** — edit before publishing
> Planned date: after PR #12 merges to `main`

---

## Suggested GitHub Release copy

### Title

```
v0.2.0 — claw4k8s: AI agents manage their own Kubernetes
```

### Tag

```
v0.2.0-claw4k8s
```

### Body

```markdown
## Highlights

This release introduces **claw4k8s**, an autonomous ops layer for k8s4claw. AI agents
now manage their own Kubernetes infrastructure: detect crashes, match rules, execute
fixes, escalate unknowns to LLM + human approval. All actions Ed25519-signed.

![claw4k8s self-healing demo](https://github.com/Prismer-AI/k8s4claw/raw/main/docs/marketing/media/demo.gif)

### What's new

**claw4k8s autonomous ops**

- 🧠 **ClawOpsController** (in-operator) — watches Pod status, matches deterministic
  rules (OOM, CrashLoop, HighCPU, Evicted), auto-executes low-risk fixes
- 🤖 **Companion Claw** (`runtime: k8sops`) — LLM agent for novel issues,
  routes to human approval via Slack
- 📝 **Intent annotation pattern** — agents never patch StatefulSets directly;
  a single reconciler (`ClawReconciler`) consumes intents through a 5-action
  whitelist with generation-based idempotency
- 🔐 **Ed25519 signing** — every action produces a signed receipt stored in the
  `ClawOpsEscalation` CR; graceful fallback if `signet` CLI is unavailable
- 🏥 **Graceful LLM fallback** — 3 retries with exponential backoff, then
  degrades to human notification (not paralysis)

**New CRDs**

- `ClawOpsEscalation` — dual-purpose audit trail + workflow state machine
  (Pending → Analyzing → Proposed → AwaitingApproval → Approved → Executed)

**New runtime**

- `K8sOpsAdapter` — locked-down runtime for the Companion Claw with mandatory
  `NetworkPolicy` enforcement at webhook admission

**Rule engine**

- 5 default rules: `oom-bump-memory`, `crashloop-restart-pod`,
  `high-cpu-bump-request`, `evicted-rollout-restart`, (and rule for scaling)
- Deterministic Go code with cooldown + debounce + circuit breaker
- Rate limiting (default: 5 actions per Claw per hour)

### Architecture

See the [claw4k8s architecture overview](docs/marketing/architecture.md) for
Mermaid diagrams of the full auto-remediation loop and the intent annotation
pattern.

### How is this different?

See the [comparison with k8sgpt / kubectl-ai / Holmes](docs/marketing/comparison.md).

Short answer: others diagnose or patch with approval. claw4k8s lets AI agents
run their own infra with cryptographic audit — and falls back gracefully when
the LLM is unavailable.

### Breaking changes

None. `Claw` CR is backward-compatible. Existing deployments continue to work
without any changes. claw4k8s features activate only when you set
`runtime: k8sops` on a Claw CR.

### Upgrading

```bash
# 1. Apply updated CRDs (includes new ClawOpsEscalation)
kubectl apply -f https://github.com/Prismer-AI/k8s4claw/releases/download/v0.2.0-claw4k8s/crds.yaml

# 2. Update operator deployment
kubectl set image deployment/k8s4claw-operator \
  operator=ghcr.io/prismer-ai/k8s4claw-operator:v0.2.0-claw4k8s \
  -n k8s4claw-system

# 3. (Optional) Deploy a Companion Claw for LLM escalation
kubectl apply -f examples/companion-claw.yaml
```

### What's tested

- 18 test packages, all green
- Unit tests: rule engine, signal extraction, intent validation, Ed25519
  signing + verification
- Envtest integration: full intent consumption loop (bump-memory, rollout-restart,
  stale generation skip, invalid intent rejection, scale-replicas) + OOM-triggered
  escalation creation
- E2E: `scripts/test-claw4k8s-e2e.sh` on a real kind cluster
- Demo recording: `scripts/demo-claw4k8s.sh` — verified 104s end-to-end on
  polinux/stress OOM + 32Mi cgroup limit

### What's NOT yet in this release

- Helm chart (install via `kubectl apply -f config/crd/bases/` for now)
- Real LLM integration (Companion Claw uses a placeholder LLM client;
  pipeline infrastructure is complete, Anthropic SDK wiring is next)
- Signet key persistence (keys are in-memory; K8s Secret storage planned)
- Multi-cluster support

### Contributors

- @willamhou — claw4k8s design + implementation
- Claude (via Claude Code) + Happy (CLI) — pair-programming

### Full changelog

**feat:**
- Add ClawOpsEscalation CRD types
- Add rule engine with 5 default rules + cooldown/debounce
- Add ClawOpsController with rate limiting + circuit breaker
- Add K8sOpsAdapter + mandatory NetworkPolicy webhook
- Add Companion Claw binary with LLM pipeline (retry + fallback)
- Add escalation watcher (Pending → Analyzing → AwaitingApproval)
- Add ClawReconciler intent consumption with 5 action handlers
- Add Ed25519Signer (pure Go) + CLISigner fallback + factory

**test:**
- Add envtest integration tests for ClawOpsController
- Add 24 tests for intent consumer (19 unit + 5 envtest integration)

**docs:**
- Add claw4k8s design spec + implementation plan (Chinese + English)
- Add demo script + asciinema recording + GIF
- Add comparison table vs k8sgpt / kubectl-ai / Holmes
- Add architecture diagrams (Mermaid)

**chore:**
- Bump Go to 1.25.9 (CVE fixes)
- DCO sign-off on all commits
```

---

## Pre-publish checklist

- [ ] Merge PR #12 to `main`
- [ ] Tag: `git tag -s v0.2.0-claw4k8s -m "claw4k8s: AI agents manage their own K8s"`
- [ ] Push tag: `git push origin v0.2.0-claw4k8s`
- [ ] GitHub Actions should auto-build release assets (operator binary, Docker images)
- [ ] Manually attach: `crds.yaml` (concatenated `config/crd/bases/*.yaml`)
- [ ] Manually attach: `demo.gif` and `demo.cast`
- [ ] Publish release
- [ ] Update repo description + topics (see `docs/marketing/README.md`)

## Post-publish promo checklist

- [ ] Tweet thread (template: `docs/marketing/demo-storyboard.md`)
- [ ] HN Show HN (template: `docs/marketing/demo-storyboard.md`)
- [ ] r/kubernetes post
- [ ] r/devops post
- [ ] CNCF Slack announcements
- [ ] Cross-link from Dev.to article (when written)
- [ ] Submit to awesome-kubernetes-operators

## Metrics to watch (first 48h)

- GitHub stars (baseline → +N)
- HN front page time
- Tweet impressions / retweets by K8s KOLs
- Incoming issues (especially "how do I..." vs "I found a bug")
- Repo traffic (`gh api repos/Prismer-AI/k8s4claw/traffic/views`)

If HN lands above position 10 for > 4 hours: expect 500–2000 stars day 1.

## Response readiness

Anticipated top HN questions + pre-written answers:

**Q: "How is this different from k8sgpt?"**
A: k8sgpt diagnoses and tells the human what's wrong. claw4k8s fixes it — either
with deterministic rules (zero-trust auto-execute) or via LLM with human approval.
The unique angle: agents managing their own infra, not a generic SRE tool.

**Q: "What happens if the LLM hallucinates a bad action?"**
A: Three lines of defense: (1) LLM output is JSON inside a K8s annotation;
ClawReconciler validates against a 5-action allowlist before executing.
(2) Generation guard prevents duplicate execution on retry. (3) Signet policy
YAML can further restrict (deny on `kubectl_delete` etc.). RBAC is the
ultimate boundary — LLM can't escape its ServiceAccount permissions.

**Q: "Why not just use FluxCD / ArgoCD for self-healing?"**
A: GitOps tools restore desired state from Git. They don't detect novel
failure modes (OOM patterns, LLM context blowup) or propose new desired
state. claw4k8s complements them — think of it as a control plane that
edits the desired state Git repo based on operational signals.

**Q: "Isn't this a security nightmare?"**
A: Every action is Ed25519-signed with per-key scopes. Companion Claw has
NO write access to Claw CRs — only to ClawOpsEscalation/status. Only
ClawOpsController (in-operator) can write intent annotations. This
prevents LLM output injection from escaping the allowlist.

**Q: "Can I use this without an LLM?"**
A: Yes. ClawOpsController handles rule-matched signals without any LLM call.
The LLM is only needed for novel/high-severity escalations, and those
gracefully degrade to human notification if the LLM is unavailable.
