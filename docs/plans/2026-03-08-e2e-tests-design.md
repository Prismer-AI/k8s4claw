# E2E Tests Design Document

**Date:** 2026-03-08
**Status:** Approved

## 1. Overview

Add envtest-based E2E lifecycle tests that verify the full operator reconciliation across multiple sub-resources and controller interactions. These complement the existing unit/integration tests which verify individual features in isolation.

### Goals

1. Verify complete Claw lifecycle: create → sub-resources provisioned → update → delete → cleanup
2. Verify cross-controller interactions (ClawReconciler + ClawChannelReconciler + SelfConfig)
3. Verify webhook validation and defaulting in realistic scenarios
4. Catch regressions in multi-feature combinations (e.g., persistence + channels + security together)

### Non-Goals

- Real container execution (no kind/minikube cluster)
- IPC Bus binary testing (covered by `internal/ipcbus/` unit tests)
- Network connectivity testing (envtest has no CNI)

## 2. Approach

### File Organization

Single file `internal/controller/e2e_lifecycle_test.go` in the existing `controller` package, reusing `suite_test.go`'s envtest environment (manager, all controllers, webhooks, runtime adapters).

Each scenario is a top-level `Test*` function with ordered `t.Run` subtests. Each scenario uses a dedicated namespace for isolation.

### Test Helpers

Reuse existing helpers from the test suite:
- `waitForCondition(t, timeout, interval, condFn)` — polls until condition true
- `createNamespace(t, name)` — creates test namespace

Add a small set of E2E-specific helpers:
- `createClawAndWait(t, claw)` — create + wait for StatefulSet to appear
- `assertSubResourceExists(t, name, ns, obj)` — generic GVK existence check
- `cleanupClaw(t, claw)` — delete + wait for finalizer completion

## 3. Test Scenarios

### Scenario 1: `TestE2E_OpenClawFullLifecycle`

Full lifecycle for OpenClaw with credentials, persistence, and observability.

Steps:
1. Create Secret with test credentials
2. Create Claw (runtime=openclaw, credentials, persistence with session+workspace, observability enabled)
3. Wait for reconcile → verify all sub-resources created:
   - StatefulSet (correct image, probes, security context, volume mounts, env vars from credentials)
   - Service (headless, correct ports)
   - ConfigMap (merged default + user config)
   - ServiceAccount
   - PDB (minAvailable=1)
4. Verify Status phase transitions: Pending → Provisioning → Running
5. Update Claw spec.config → verify ConfigMap updated, StatefulSet generation incremented
6. Delete Claw → verify finalizer runs, all sub-resources cleaned up

### Scenario 2: `TestE2E_MultiRuntime`

Table-driven test across all 4 runtime types.

For each runtime (openclaw, nanoclaw, zeroclaw, picoclaw):
1. Create minimal Claw
2. Verify StatefulSet has correct: image prefix, gateway port, default config, probe paths
3. Cleanup

### Scenario 3: `TestE2E_ChannelWithIPCBus`

Channel sidecar injection with IPC Bus.

Steps:
1. Create ClawChannel (type=webhook, config with URL)
2. Create Claw referencing the channel
3. Verify StatefulSet contains:
   - IPC Bus init container with `restartPolicy=Always`
   - Webhook sidecar container with correct env vars (CHANNEL_NAME, CHANNEL_TYPE)
   - Shared emptyDir volume at `/var/run/claw`
4. Delete Claw → verify ClawChannel refCount decremented

### Scenario 4: `TestE2E_SecurityAndNetwork`

NetworkPolicy and Ingress creation/deletion.

Steps:
1. Create Claw with NetworkPolicy enabled + Ingress (host, TLS, className)
2. Verify NetworkPolicy: default-deny ingress, allow egress to DNS + HTTPS + gateway port
3. Verify Ingress: host rule, TLS secret, ingressClassName
4. Update Claw: disable NetworkPolicy → verify NetworkPolicy deleted, Ingress still exists
5. Cleanup

### Scenario 5: `TestE2E_PersistenceReclaimPolicies`

PVC lifecycle with different reclaim policies.

Sub-tests:
- **Delete policy:** Create Claw with persistence + reclaimPolicy=Delete → delete Claw → verify PVCs deleted
- **Retain policy:** Create Claw with persistence + reclaimPolicy=Retain → delete Claw → verify PVC ownerReferences removed, PVCs still exist

### Scenario 6: `TestE2E_WebhookValidation`

Webhook validation and defaulting.

Sub-tests:
- **Runtime immutability:** Create Claw → update runtime field → expect admission rejection
- **Credential exclusivity:** Create Claw with both secretRef and externalSecret → expect rejection
- **Defaulting:** Create Claw without reclaimPolicy → verify it's set to default value after creation

### Scenario 7: `TestE2E_SelfConfig`

Self-configuration lifecycle.

Steps:
1. Create Claw with `selfConfigure.enabled=true` and allowlist
2. Verify Role + RoleBinding created for the Claw's ServiceAccount
3. Create ClawSelfConfig with allowed operations (addSkills, configPatch, addEnvVars)
4. Verify SelfConfig status shows Applied
5. Create ClawSelfConfig with disallowed operations → verify status shows Rejected
6. Cleanup

## 4. Test Infrastructure

### Namespace Isolation

Each scenario creates a unique namespace (`e2e-<scenario>-<random>`) and cleans it up after the test. This prevents cross-test interference.

### Timeouts

- Sub-resource creation: 10s (existing `testTimeout`)
- Status transitions: 15s (controller may need multiple reconciles)
- Deletion/cleanup: 10s

### CI Integration

No changes needed — tests run as part of `make test` / `go test -race ./internal/...` using the existing envtest setup.
