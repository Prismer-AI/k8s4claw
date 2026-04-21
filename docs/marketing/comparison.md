# k8s4claw vs Alternatives

Comparison table for README and HN/Twitter posts. The short version: **everyone else lets the LLM write to the K8s API. k8s4claw doesn't.**

## The core axis: how does the LLM write to Kubernetes?

This is the one question that matters. If you only read one row, read this one.

| Tool                               | LLM's path to mutating a K8s resource                                            | RBAC of the LLM's ServiceAccount                |
| ---------------------------------- | -------------------------------------------------------------------------------- | ----------------------------------------------- |
| **k8sgpt**                         | LLM can't write — read-only diagnostic                                           | read-only                                       |
| **kubectl-ai**                     | LLM generates `kubectl` commands; **human approves each one**                    | the user's kubectl (indirect)                   |
| **Holmes (Robusta)**               | LLM agent runs `kubectl` via tool-calling                                        | equivalent to `kubectl edit/patch/delete`       |
| **Most "LLMops" agent frameworks** | LLM calls `kubectl` / K8s Go client directly                                     | cluster-admin or similar                        |
| **k8s4claw**                       | LLM writes **one annotation** on **one CR**; the reconciler is the sole mutator  | **zero `patch` verbs** on claws or statefulsets |

The last row is the only reason this project exists. Every other feature (runtime registry, IPC bus, auto-update, signed audit) is table-stakes once you accept the premise.

## The positioning matrix

|                              | **Diagnoses** | **Fixes automatically**     | **Self-managing** | **Cryptographic audit** | **Graceful LLM fallback** |
| ---------------------------- | :-----------: | :-------------------------: | :---------------: | :---------------------: | :-----------------------: |
| **k8sgpt**                   |      ✓        |             —               |         —         |            —            |             —             |
| **kubectl-ai**               |      ✓        |       Human-in-loop         |         —         |            —            |             —             |
| **Holmes (Robusta)**         |      ✓        |       Human-in-loop         |         —         |            —            |             —             |
| **RCA-only (Cast.AI, etc.)** |      ✓        |             —               |         —         |            —            |             —             |
| **claw4k8s**                 |      ✓        | ✓ (rules) + approval (LLM)  |      **✓**        |    **✓** (Ed25519)      |           **✓**           |

## When to pick each

- **k8sgpt** — You want a quick diagnostic CLI for a running cluster, no automation.
- **kubectl-ai** — You want an AI-enhanced `kubectl`, with human approval on every step.
- **Holmes** — You're an SRE team, want AI co-pilot for incidents on general K8s workloads, and accept that the agent has kubectl-equivalent power during incident response.
- **claw4k8s** — You run LLM/AI agents on K8s and (a) want them to manage themselves, (b) can't give the LLM cluster-mutate RBAC, (c) want cryptographic audit + human oversight for risky changes.

## The architectural difference

Other tools fall into two patterns:

1. **Read-only** (k8sgpt): "the LLM can observe but not act." Safe but useless for automation.
2. **Direct patching** (kubectl-ai, Holmes, most agent frameworks): "the LLM gets a tool that calls the K8s API; we sanitize/approve the output." **The trust boundary is the review step, not RBAC.**

**k8s4claw uses intent annotations:**

```text
LLM / Rule engine
      │  (writes one annotation: claw.prismer.ai/ops-intent)
      ▼
┌──────────────────────┐
│   Claw CR (spec)     │ ◄── the only thing the LLM can touch
└──────────────────────┘
      │
      ▼
┌──────────────────────┐     allowlist check
│ ClawReconciler (Go)  │     generation guard (no replay)
│   — sole mutator —   │     Ed25519 verify
└──────────────────────┘     bounded param ranges
      │
      ▼
  StatefulSet / Pods / PVCs / Services
```

Why this matters, in order of how much it buys you:

1. **RBAC is the boundary, not the review step.** The LLM's ServiceAccount has **zero `patch` verbs** on `claws`, `statefulsets`, or anything else it could use to escape. A prompt injection that convinces the LLM to "just run `kubectl delete ns kube-system`" can't succeed because **the LLM has no such API call available**. There is nothing to review because nothing was attempted.
2. **One writer** to sub-resources → no controller contention, no race between the LLM and the operator.
3. **Validation at a single chokepoint** → the allowlist is a 5-action Go switch statement. Auditable, testable, not bypassable via clever prompts.
4. **Generation guard** → each intent carries a monotonic counter. Duplicate submissions (retry storms, webhook replays) are dropped.
5. **Audit point** → every mutation flows through the same chokepoint, Ed25519-signed, mirrored to the `ClawOpsEscalation` CR status for durable K8s-native audit.

## What claw4k8s is NOT

- NOT a general-purpose SRE tool (that's Holmes' lane, for now)
- NOT a kubectl replacement (that's kubectl-ai)
- NOT a cluster-wide AIOps platform (that's Cast.AI, PerfectScale)
- NOT yet multi-cluster (roadmap)
- NOT a "trust the LLM more safely" tool. The LLM gets **less** trust than in other frameworks, not more. That's the point.

## The wedge

> "The first Kubernetes operator where AI agents manage their **own** infrastructure — with the LLM behind a real RBAC boundary, not behind a 'please review this patch' prompt."

Dogfooding as the product. Like Docker built with Docker.

## Related reading

- [Threat model](../security/threat-model.md) — prompt injection, replay, supply chain, key rotation
- [claw4k8s design spec](../plans/2026-04-19-claw4k8s-design.md) — full architecture
- [Intent annotation details](../../internal/controller/claw_ops_intent_consumer.go) — the 5-action allowlist in ~200 lines of Go
