# Claw Controller MVP Design

**Date:** 2026-03-04
**Status:** Approved
**Scope:** StatefulSet + Status + Finalizer (core reconciliation skeleton)

## Overview

Implement the minimum viable Claw controller that can reconcile a Claw CR into a running StatefulSet, manage lifecycle via finalizers, and report status. This is the foundation for all subsequent controller features (PVC management, channel injection, credential rotation, etc.).

## Architecture

Single-file reconciler (`claw_controller.go`) with private helper methods per phase. No pipeline abstraction — YAGNI at 3 phases.

```
Reconcile(ctx, req)
  ├── Fetch Claw CR (return if NotFound)
  ├── Resolve RuntimeAdapter from Registry
  ├── handleDeletion()     → finalizer + reclaim policy stub
  ├── ensureFinalizer()    → add finalizer if missing
  ├── ensureStatefulSet()  → create/update StatefulSet
  └── updateStatus()       → phase + conditions from StatefulSet state
```

## Phase 1: Finalizer Management

**Finalizer name:** `claw.prismer.ai/cleanup`

**On deletion (DeletionTimestamp set):**
1. If finalizer not present → return (K8s handles deletion)
2. Set phase = `Terminating`
3. Execute cleanup based on `spec.persistence.reclaimPolicy`:
   - `Retain` / `Delete` / `Archive` → no-op in MVP (future: PVC lifecycle)
4. Remove finalizer → update Claw CR
5. Return (K8s proceeds with deletion)

**On create/update (DeletionTimestamp not set):**
- Ensure finalizer is present on the Claw CR

## Phase 2: StatefulSet Management

**Build desired StatefulSet:**
- `metadata.name` = Claw name
- `metadata.namespace` = Claw namespace
- `ownerReferences` → Claw CR (controller: true)
- `spec.replicas` = 1
- `spec.serviceName` = Claw name
- `spec.selector.matchLabels`:
  - `app.kubernetes.io/name: claw`
  - `app.kubernetes.io/instance: <claw.name>`
  - `claw.prismer.ai/runtime: <runtime-type>`

**Pod template construction:**
1. Start from `adapter.PodTemplate()` (currently empty stubs)
2. Overlay controller-managed fields:
   - Labels (same as selector)
   - Security context (pod-level: runAsNonRoot, fsGroup=1000, seccomp)
   - `terminationGracePeriodSeconds` = `adapter.GracefulShutdownSeconds() + 10`
3. If PodTemplate has no containers, inject a placeholder container:
   - Name: `runtime`
   - Image: `busybox:latest` (placeholder until PodTemplate is implemented)
   - Liveness probe from `adapter.HealthProbe()`
   - Readiness probe from `adapter.ReadinessProbe()`
   - Environment variables from `adapter.DefaultConfig().Environment`
   - Container security context (non-root, readOnlyRootFilesystem, drop ALL)

**Reconciliation logic:**
- StatefulSet does not exist → Create + record Event
- StatefulSet exists → Compare relevant spec fields, Update if changed + record Event
- Use `controllerutil.SetControllerReference` for ownerRef

## Phase 3: Status Management

**Phase derivation from StatefulSet state:**

| Condition | Phase |
|-----------|-------|
| StatefulSet not found | `Pending` |
| StatefulSet exists, readyReplicas == 0 | `Provisioning` |
| StatefulSet exists, readyReplicas >= 1 | `Running` |
| StatefulSet exists, Pod crash-looping | `Failed` |
| DeletionTimestamp set | `Terminating` |

**Status fields updated:**
- `status.phase`
- `status.observedGeneration` = `claw.Generation`
- `status.conditions[RuntimeReady]` = True/False based on StatefulSet readiness

## SetupWithManager

```go
ctrl.NewControllerManagedBy(mgr).
    For(&v1alpha1.Claw{}).
    Owns(&appsv1.StatefulSet{}).
    Complete(r)
```

MVP only watches StatefulSet. Future phases add: PVC, ConfigMap, Secret, ClawChannel watches.

## RBAC

Existing RBAC annotations cover the MVP needs. Add `apps` group for StatefulSet:

```go
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
```

## Testing Strategy

**envtest integration tests** (controller-runtime test framework):

1. **Create Claw → StatefulSet created**: Verify StatefulSet spec matches expectations (labels, ownerRef, security context, probes, graceful shutdown)
2. **Delete Claw → finalizer runs**: Verify finalizer is processed, phase set to Terminating
3. **Unknown runtime → error status**: Verify no StatefulSet created, status reflects error
4. **Update Claw → StatefulSet updated**: Verify StatefulSet spec changes propagate
5. **Finalizer added on create**: Verify new Claw gets finalizer

## Files

| File | Purpose |
|------|---------|
| `internal/controller/claw_controller.go` | Reconciler (replace existing skeleton) |
| `internal/controller/claw_controller_test.go` | envtest integration tests |
| `cmd/operator/main.go` | Register controller with manager |

## Out of Scope (future phases)

- PVC lifecycle management
- ConfigMap generation
- ServiceAccount / Role / RoleBinding per instance
- ClawChannel → sidecar injection
- Secret hash annotation for credential rotation
- NetworkPolicy / Ingress / PDB
- ServiceMonitor / PrometheusRule
- Degraded / Updating phases
