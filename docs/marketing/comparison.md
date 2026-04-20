# claw4k8s vs Alternatives

Comparison table for README and HN/Twitter posts.

## The positioning matrix

|                                | **Diagnoses** | **Fixes automatically** | **Self-managing** | **Cryptographic audit** | **Graceful LLM fallback** |
|--------------------------------|:-------------:|:-----------------------:|:-----------------:|:-----------------------:|:-------------------------:|
| **k8sgpt**                     |      ✓        |           —             |         —         |            —            |             —             |
| **kubectl-ai**                 |      ✓        |     Human-in-loop       |         —         |            —            |             —             |
| **Holmes (Robusta)**           |      ✓        |     Human-in-loop       |         —         |            —            |             —             |
| **RCA-only (Cast.AI, etc.)**   |      ✓        |           —             |         —         |            —            |             —             |
| **claw4k8s**                   |      ✓        |       ✓ (rules) + approval (LLM)      |         **✓**         |         **✓** (Ed25519)         |             **✓**             |

## When to pick each

- **k8sgpt** — You want a quick diagnostic CLI for a running cluster, no automation.
- **kubectl-ai** — You want an AI-enhanced `kubectl`, with human approval on every step.
- **Holmes** — You're an SRE team, want AI co-pilot for incidents on general K8s workloads.
- **claw4k8s** — You run LLM/AI agents on K8s and want them to manage themselves with cryptographic audit + human oversight for risky changes.

## The architectural difference

Other tools either:
1. **Read-only** (k8sgpt): they tell you what's wrong, you fix it
2. **Direct patching** (kubectl-ai, most agent frameworks): LLM output → kubectl command → cluster

**claw4k8s uses intent annotations:**

```
LLM/Rule engine → ops-intent annotation on Claw CR
                           ↓
              ClawReconciler (validates, signs, executes)
                           ↓
                    StatefulSet / Pods
```

Why this matters:
- **One writer** to sub-resources → no controller contention
- **Validation at boundary** → LLM prompt injection can't escape the allowlist
- **Generation guard** → no duplicate execution on retry
- **Audit point** → every change flows through the same chokepoint, Ed25519-signed

## What claw4k8s is NOT

- NOT a general-purpose SRE tool (that's Holmes' lane, for now)
- NOT a kubectl replacement (that's kubectl-ai)
- NOT a cluster-wide AIOps platform (that's Cast.AI, PerfectScale)
- NOT yet multi-cluster (roadmap)

## The wedge

> "The first Kubernetes operator where AI agents manage their **own** infrastructure."

Dogfooding as the product. Like Docker built with Docker.
