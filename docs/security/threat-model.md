# Threat Model — claw4k8s

> Last updated: 2026-04-21 · Applies to: `feat/claw4k8s` merged into main, Helm chart 0.3.0+

This document describes the threat model for the `claw4k8s` autonomous ops layer. It exists because "the LLM writes to Kubernetes" is the entire problem we're claiming to solve, and you should be able to check our work.

## Trust model

We treat the LLM as **untrusted**. Not adversarial in the network-attacker sense, but not trustworthy either: it can be prompt-injected, it can hallucinate, it can be tricked into producing outputs its operator would not sanction. The LLM's authority over the cluster is therefore limited to what its ServiceAccount's RBAC permits, which is deliberately minimal.

**In scope:**

- Prompt injection leading to unsafe actions
- Replay / duplicate execution of intents
- Intent payload tampering (by an attacker with write access to the CR)
- Compromised Companion Claw pod (RCE in the runtime image or sidecar)
- Rogue rule engine configuration (malicious operator admin)
- Key theft (Ed25519 signing key extraction)

**Out of scope (inherited from K8s / operator baseline):**

- A compromised K8s API server
- A compromised operator pod itself (cluster-admin-equivalent)
- Supply chain attacks on the operator image at build time (covered by image signing, not this doc)
- Node-level escapes

## The boundary

There is **one** path from "LLM decides something" to "cluster state changes":

```
LLM  →  writes intent annotation on Claw CR
          ↓  (single writer: the LLM's ServiceAccount)
        Claw CR metadata.annotations[claw.prismer.ai/ops-intent]
          ↓  (single reader: ClawReconciler in the operator)
        ClawReconciler.consumeAndExecuteOpsIntent()
          ↓  (validates: allowlist + gen guard + signature + param bounds)
        StatefulSet / Pods (the only resources touched)
```

Every threat below is either (a) prevented by the boundary, (b) contained by the boundary, or (c) a residual risk we accept and call out explicitly.

## Threats

### T1. Prompt injection → unsafe action

**Scenario:** An attacker injects prompt content (via logs the Companion Claw reads, a user message, a channel message) that tries to convince the LLM to `kubectl delete ns kube-system`, exfiltrate secrets, or patch unrelated workloads.

**Why this fails:**

- The Companion Claw's ServiceAccount has **zero `patch` verbs** on any resource. There is no `kubectl delete`, no `kubectl patch`, no K8s write API it can reach. (See [`config/rbac/companion_role.yaml`](../../config/rbac/companion_role.yaml).)
- The only write verb it has is `update`/`patch` on `clawopsescalations/status` — for recording its own analysis. It cannot create intents directly.
- The LLM can only *propose* an action, written into `ClawOpsEscalation.status.proposedAction`. The intent is applied by the operator (`ClawOpsController`), which runs the proposal through the allowlist again before writing to the Claw CR.

**Residual risk:** An attacker that can make the LLM output a **valid allowlisted action** (e.g., `bump-memory` to 100Gi) can cause resource amplification. Mitigated by: (1) bounded parameter ranges (memory ≤ 50% of node allocatable, replicas ∈ [1, 10]), (2) rate limits (max N actions per Claw per hour), (3) cluster-wide circuit breaker (pause all auto-actions if > 30% of Claws are abnormal).

### T2. Replay / duplicate execution

**Scenario:** A retry loop, webhook replay, or malicious reposting causes the same intent to be executed multiple times. E.g., "bump replicas by 1" run 50 times.

**Why this fails:**

- Every intent carries a monotonic `generation: int64` counter, set by the producer.
- The Claw CR stores the last processed generation in `claw.prismer.ai/ops-intent-gen` annotation.
- `parseOpsIntent` returns `nil` (skip) if `intent.Generation <= lastGen`. See [`claw_ops_intent_consumer.go:44-60`](../../internal/controller/claw_ops_intent_consumer.go).
- `clearOpsIntentAnnotations` **never lowers** the generation counter. An invalid intent with `generation: 0` does not reset the high-water mark.

**Residual risk:** If a producer somehow generates `generation: 2^63 - 1` (monotonic wraparound), subsequent intents are locked out. Mitigated by: producers use `time.Now().UnixMilli()`, which won't wrap until year 292 million.

### T3. Intent payload tampering

**Scenario:** An attacker with write access to the Claw CR (e.g., a compromised user SA, a rogue namespace admin) edits the `ops-intent` annotation to inject an action the LLM never proposed.

**Why this (mostly) fails:**

- The allowlist is enforced at *both* ends: the producer (ClawOpsController / Companion Claw) writes actions from a 5-entry whitelist; the consumer (ClawReconciler) validates again via `ValidateIntent` and `buildIntentPatch`.
- Unknown actions, invalid quantities, out-of-range replicas are all rejected. The annotation is cleared and a `OpsIntentRejected` event is emitted.

**Residual risk:** A namespace admin who can patch the Claw CR can cause legal-but-unwanted actions (e.g., `scale-replicas: 10` on a workload the author wanted at 1). This is a namespace-admin threat, not an LLM threat. Mitigated by: namespace admin is already trusted at that level; use `OPA/Kyverno` to further restrict who can write the annotation if needed.

### T4. Compromised Companion Claw pod

**Scenario:** RCE in the runtime image, a compromised channel sidecar, or a supply-chain attack on the pod gives an attacker shell access to the Companion Claw pod.

**What they get:**

- The pod's ServiceAccount: read-only on pods/events/logs/claws/clawopsescalations in its own namespace; write on `clawopsescalations/status` only.
- NetworkPolicy: outbound limited to K8s API (HTTPS), LLM API (HTTPS), and DNS. **No egress to arbitrary IPs.** (See the mandatory NetworkPolicy for runtime `k8sops` — enforced by webhook.)
- No `patch` on Claws. No way to create intents. Can only write their own escalation status.

**Residual risk:** Attacker can produce convincingly-legitimate-looking escalation analyses, hoping a human approver green-lights a malicious action. Mitigated by: (a) approvals go through `ClawChannel` (Slack) with identity-bound callbacks; (b) the approved action is re-validated by `ClawOpsController` against the allowlist before it writes the intent.

### T5. Rogue rule engine configuration

**Scenario:** A malicious operator admin edits the default rules to auto-execute destructive actions (e.g., "OOM → scale-replicas: 0").

**Why this partly fails:**

- The allowlist still applies: `scale-replicas: 0` is rejected by `buildIntentPatch` (`n < 1` → error).
- Action severity is bounded: the rule engine can only produce actions in the 5-entry whitelist. Deletion, namespace ops, cross-namespace access are all not actions.

**Residual risk:** An admin with write access to the operator's rule configuration *is* a cluster-operator-equivalent threat. This tool does not defend against a malicious operator. Mitigated by: rule config is a ConfigMap under the operator's RBAC; audit who can write it with K8s audit logging.

### T6. Ed25519 key theft

**Scenario:** An attacker extracts the signing key from the operator pod and forges valid-looking intent receipts.

**Current state:** The key is generated in-memory by `NewEd25519Signer` on operator startup. It is not persisted. There is no key rotation.

**What key theft buys the attacker:** The ability to produce receipt JSON that passes signature verification. It does **not** grant the ability to execute intents, because:

- The receipt is an audit artifact, not an authorization token.
- The intent allowlist and generation guard are enforced by the reconciler regardless of receipt validity.
- The reconciler does not require a valid signature to execute; a missing / invalid signature is logged as a warning, not a rejection. (Design choice: we want the system to degrade to "unsigned" rather than halt on signer outage.)

**Residual risk:** An attacker who replaces the operator image can generate fake audit trails. This is an operator-pod-compromise threat (out of scope). Mitigated by: pod security context (read-only root fs, non-root, seccomp), image signing (out-of-band), and K8s audit logs as an independent source of truth.

**Roadmap:** K8s Secret-backed key with 90-day rotation, detached signatures required for intents (not just advisory).

## What we still depend on

We explicitly depend on the following being trustworthy. If any of these are broken, the whole model falls apart, and we're not worse than any other K8s operator:

1. **The operator pod itself.** The operator has cluster-wide `patch` on claws, statefulsets, etc. A compromised operator is a cluster compromise. Use pod security, image signing, and RBAC minimization.
2. **The K8s API server.** If admission is bypassed, all bets are off.
3. **The LLM API provider's TLS.** The Companion Claw talks to Anthropic / OpenAI over HTTPS. A MITM here can feed malicious prompts. Mitigated by TLS cert validation and egress NetworkPolicy.
4. **The Slack webhook signing secret** (when using ClawChannel for approvals). An attacker with this secret can forge approvals.

## Red-team checklist

If you want to verify this model, these are the tests we run and recommend you repeat:

- [ ] Exec into the Companion Claw pod. Try `kubectl patch claw my-agent --type merge -p '...'`. Should fail with Forbidden.
- [ ] Exec into the Companion Claw pod. Try `kubectl auth can-i patch claws`. Should print `no`.
- [ ] Submit an intent with `generation: 1`. Confirm it runs. Submit identical intent again. Confirm it is skipped (log: `stale generation`).
- [ ] Submit an intent with `action: delete-namespace`. Confirm rejection, `OpsIntentRejected` event.
- [ ] Submit an intent with `bump-memory: target: 9999Gi`. Confirm rejection (out of range).
- [ ] Submit an intent with `scale-replicas: replicas: 0`. Confirm rejection.
- [ ] Submit an intent with `generation: 5` after already processing `generation: 100`. Confirm skip (high-water mark preserved).
- [ ] Submit a malformed-JSON intent. Confirm it is cleared with gen counter unchanged.
- [ ] Kill the signing key (rotate the operator pod). Confirm system continues to execute intents with unsigned receipts.
- [ ] Apply a Claw with runtime `k8sops` but no NetworkPolicy. Confirm webhook rejection.

The E2E test harness at [`scripts/test-claw4k8s-e2e.sh`](../../scripts/test-claw4k8s-e2e.sh) automates the intent-boundary checks; the others are manual for now.

## Reporting issues

Security issues: open a private advisory at https://github.com/Prismer-AI/k8s4claw/security/advisories/new rather than a public issue.
