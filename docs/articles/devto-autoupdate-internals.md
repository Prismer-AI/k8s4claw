---
title: "Auto-updating Kubernetes workloads: an annotation-driven rollout, with circuit breaker"
published: false
description: "How the k8s4claw auto-update controller polls OCI registries on cron, applies semver-filtered upgrades to a StatefulSet, verifies health, and rolls back automatically — using a single annotation to drive the rollout and Status fields for durable state."
tags: kubernetes, go, opensource, distributedsystems
series: "k8s4claw internals"
cover_image:
---

You have ten agent pods on a cluster, each running a different runtime image. Every Tuesday somebody publishes a new version of one of them. Are you going to `kubectl set image` ten things by hand? Are you sure you'll know if v1.4.2 was the one that wedged the pods?

This post is about the auto-update controller in [k8s4claw](https://github.com/Prismer-AI/k8s4claw), a Kubernetes operator for AI agent runtimes. It polls OCI registries on cron, picks the highest semver tag that matches your constraint, flips a single annotation, and lets the main reconciler do the rollout. If the rollout doesn't go ready inside a timeout, it rolls back. If it rolls back too many times, it stops trying and asks for a human.

The whole controller is one Go file ([`autoupdate_controller.go`](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/controller/autoupdate_controller.go)), about 470 lines. This is the design walkthrough — not the API reference, not the README.

## The shape of the problem

A `Claw` resource looks like this when auto-update is on:

```yaml
spec:
  runtime: openclaw
  autoUpdate:
    enabled: true
    schedule: "0 3 * * *"           # daily at 3 AM
    versionConstraint: ">=1.0.0,<2"
    healthTimeout: "10m"
    maxRollbacks: 3
```

Five fields, and the controller has to:

1. Wake up on schedule (cron expression, not "every N seconds").
2. Ask the registry what tags exist for `ghcr.io/prismer-ai/k8s4claw-openclaw`.
3. Filter to semver tags inside the constraint.
4. Pick the highest one that's strictly greater than what's running, skipping any version we've already tried and rolled back.
5. Apply it — but not by patching the StatefulSet directly.
6. Watch readiness for `healthTimeout` (10 min default).
7. If both `sts.Status.UpdatedReplicas` and `sts.Status.ReadyReplicas` reach the desired count: record success, reset rollback counter.
8. If it times out: clear the `target-image` annotation so the main reconciler reverts to the runtime adapter's default image, mark this version as failed, increment rollback counter.
9. After `maxRollbacks` consecutive failures: open the circuit and stop trying. Subsequent version checks then emit a "version available, circuit open" event/condition instead of applying the new image.

The non-obvious bits are *where the state lives* and *how the rollout actually happens*. Both turn out to use the same trick.

## Mechanism 1 — annotations drive the in-flight rollout

The auto-update controller never holds in-memory state across reconciles. State lives in two places on the `Claw` resource:

- **Annotations** drive the in-flight update — what image we want, what phase we're in, when we started.
- **`status.autoUpdate`** holds the durable bookkeeping — current version, available version, rollback count, circuit-breaker flag, failed-version list, version history.

The three annotations:

```go
const (
    annotationTargetImage = "claw.prismer.ai/target-image"
    annotationUpdatePhase = "claw.prismer.ai/update-phase"
    annotationUpdateStart = "claw.prismer.ai/update-started"
)
```

- `target-image` — the full image reference we want running (`ghcr.io/.../openclaw:1.2.0`). Stays set after a successful update.
- `update-phase` — currently only `HealthCheck` or absent. Absent = idle. Anything else falls through to the idle path.
- `update-started` — RFC3339 timestamp of when we set the phase annotation. Used by the health-check timer.

Reconcile is a two-way fork on the phase:

```go
phase := claw.Annotations[annotationUpdatePhase]
if phase == "HealthCheck" {
    return r.reconcileHealthCheck(ctx, &claw)
}
// otherwise: idle — check if a version poll is due
```

This means the controller is **stateless and idempotent**. If the operator pod restarts mid-update, the next reconcile reads the annotation back from etcd and picks up exactly where the old one left off. There's no `map[types.NamespacedName]updateState` to rehydrate, no leader-election dance for in-flight work. Kubernetes is the database. The controller is a function over its current state.

The other thing this gets you: `kubectl describe claw foo` shows the in-flight update verbatim. No tracing, no controller logs to grep. The state is on the resource.

## Mechanism 2 — the rollout is one annotation

Here's the thing that surprised me when I wrote this controller. The auto-update logic does not patch the StatefulSet. It does not touch pods. It does this:

```go
targetImage := baseImage + ":" + newVersion
claw.Annotations[annotationTargetImage] = targetImage
claw.Annotations[annotationUpdatePhase] = "HealthCheck"
claw.Annotations[annotationUpdateStart] = now.Format(time.RFC3339)
r.Update(ctx, &claw)
```

That's it. That's the whole "apply a new version" code path.

The rollout actually happens because the **main** `ClawReconciler` watches the same `Claw` resource and rebuilds the pod template every reconcile. It checks the annotation when it does:

```go
// claw_controller.go
podTemplate := adapter.PodTemplate(claw)

// Auto-update: override runtime image if target-image annotation is set.
if targetImage := claw.Annotations["claw.prismer.ai/target-image"]; targetImage != "" {
    for i := range podTemplate.Spec.Containers {
        if podTemplate.Spec.Containers[i].Name == "runtime" {
            podTemplate.Spec.Containers[i].Image = targetImage
            break
        }
    }
}
```

So the auto-update controller is purely a *signal source*. It says "I want this image to be running." The main reconciler is responsible for translating that into a StatefulSet update, which then translates into a rolling pod replacement, which the auto-update controller observes via `sts.Status.UpdatedReplicas` and `sts.Status.ReadyReplicas` (both required — see Mechanism 4).

This separation matters because:

1. **Rollback is mostly just deleting annotations.** When we roll back, we `delete(claw.Annotations, annotationTargetImage)` and the main reconciler reverts to the adapter's default image on the next pass. No special "rollback path" in the StatefulSet logic. (The `update-phase` and `update-started` annotations also get cleared.)
2. **Manual image overrides keep working.** If somebody set `target-image` by hand for a hotfix, the main reconciler honors it for the pod template. The auto-update controller compares against `status.CurrentVersion` (not the annotation) when deciding whether to propose a new version, so a manual override doesn't accidentally redirect what the controller thinks "current" means.
3. **The auto-update controller can be removed entirely** without breaking anything. Stale annotation, sure, but the cluster doesn't fall over.

If you're writing a new controller and you find yourself directly mutating sub-resources, ask whether you could mutate annotations on the parent CR instead and let the existing reconciler do the work. It's almost always cleaner.

## Mechanism 3 — semver resolution

The version-picking logic is in [`internal/registry/resolver.go`](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/registry/resolver.go):

```go
func ResolveBestVersion(tags []string, constraint, current string, failedVersions []string) (string, bool) {
    c, err := semver.NewConstraint(constraint)
    if err != nil {
        return "", false
    }

    var currentVer *semver.Version
    if current != "" {
        currentVer, _ = semver.NewVersion(current)
    }

    failedSet := make(map[string]bool, len(failedVersions))
    for _, f := range failedVersions {
        failedSet[f] = true
    }

    var best *semver.Version
    for _, tag := range tags {
        v, err := semver.NewVersion(tag)
        if err != nil {
            continue // skip non-semver tags like "latest", "sha-abc"
        }
        if !c.Check(v) {
            continue
        }
        if failedSet[v.Original()] {
            continue
        }
        if currentVer != nil && !v.GreaterThan(currentVer) {
            continue
        }
        if best == nil || v.GreaterThan(best) {
            best = v
        }
    }

    if best == nil {
        return "", false
    }
    return best.Original(), true
}
```

Three subtleties worth flagging:

- **Non-semver tags are silently dropped.** `latest`, `sha-abc1234`, `nightly` — they all fail `semver.NewVersion()` and get skipped. This is the right default for an auto-updater: anything you can't compare to a version constraint is something you don't want to roll into automatically.
- **`failedVersions` is checked after the constraint check, by exact original tag string.** A version that has been rolled back gets recorded in `Status.AutoUpdate.FailedVersions` and is excluded from future auto-selection. The match is on `v.Original()`, so `"1.2.0"` and `"v1.2.0"` would be treated as different strings — the constraint check is semver-aware, but the failed-version filter is not. To retry a failed version automatically you have to clear it from status manually; you can also force a manual rollout via the annotations (see the circuit-breaker section). This is conservative on purpose — the assumption is that if v1.2.0 wedged your pods once, the next 3 AM cron run isn't going to fix that.
- **`!v.GreaterThan(currentVer)` excludes equal.** Reinstalling the same version on every cron tick would be a noisy mistake.

The auto-update controller also has an early bail-out for digest-pinned images:

```go
currentImage := claw.Annotations[annotationTargetImage]
if currentImage != "" && registry.IsDigestPinned(currentImage) {
    logger.Info("skipping auto-update: image is digest-pinned", "image", currentImage)
    return r.requeueAtNextCron(spec), nil
}
```

It checks the `target-image` annotation, not the actual running image. `IsDigestPinned` is just `strings.Contains(image, "@sha256:")`. If you set `target-image` to a digest-pinned reference (manually or via a previous override), the controller stops touching that Claw on its cron schedule. If the annotation is absent, the check is skipped and version polling proceeds normally.

## Mechanism 4 — health verification

Once the annotation is set and the main reconciler has rolled the StatefulSet, the auto-update controller requeues every 15 seconds and watches readiness:

```go
desiredReplicas := int32(1)
if sts.Spec.Replicas != nil {
    desiredReplicas = *sts.Spec.Replicas
}
if sts.Status.UpdatedReplicas >= desiredReplicas &&
   sts.Status.ReadyReplicas >= desiredReplicas {
    // Health check passed.
}
```

Two conditions, both required:

- `UpdatedReplicas` — pods running the *new* template, not the old one. Without this check, you'd "succeed" the moment the old pods are still ready before the rollout has even started.
- `ReadyReplicas` — pods passing their readiness probes.

If both clear within `healthTimeout` (10 min default), we record success: reset rollback counter, reset circuit breaker, append a `Healthy` entry to version history, and clear the `update-phase` and `update-started` annotations. Note we deliberately *keep* `target-image` — it's still the signal the main reconciler uses to override the runtime container image, and clearing it would silently revert the running pods to the adapter default on the next reconcile.

If the timer expires first:

```go
if r.clock().Since(startedAt) > healthTimeout {
    return r.rollback(ctx, claw, "health check timed out")
}
```

We also roll back if the StatefulSet itself disappears past the timeout (the resource was deleted while we were watching), or if the start-time annotation is somehow malformed (you have to handle that — annotations are just strings).

15 seconds is a polling interval, not a deadline. The actual deadline is `healthTimeout`, parsed from the spec. If you're upgrading a heavyweight runtime that takes 8 minutes to warm up, set `healthTimeout: 15m` and the controller will wait that long.

## Mechanism 5 — circuit breaker

Rolling back once is a hiccup. Rolling back three times in a row is a system telling you to stop.

```go
maxRollbacks := defaultMaxRollbacks  // 3
if spec.MaxRollbacks > 0 {
    maxRollbacks = spec.MaxRollbacks
}
if status.RollbackCount >= maxRollbacks {
    status.CircuitOpen = true
    SetAutoUpdateCircuit(claw.Namespace, claw.Name, true)
    r.Recorder.Event(claw, corev1.EventTypeWarning, EventAutoUpdateCircuitOpen,
        fmt.Sprintf("Circuit breaker opened after %d rollbacks", status.RollbackCount))
}
```

When the circuit is open, the main Reconcile path detects new versions and emits an event saying "version X is available, but we're not applying it." The user sees this on `kubectl describe claw foo` and can decide whether to investigate or override.

The recovery story is deliberately blunt: **the controller does not auto-recover the circuit**. There's no "wait 24 hours and try again" timer, no exponential backoff, no separate trial deployment. The gating check is `if status.CircuitOpen` — it doesn't look at `RollbackCount`. So the recovery paths are:

1. A human patches `status.autoUpdate.circuitOpen` to `false` (and usually `rollbackCount` to 0 for a clean slate). The next cron tick will resume normal version polling.
2. A human forces an update path some other way — for example, setting all three annotations (`target-image` to a known-good image, `update-phase` to `HealthCheck`, `update-started` to a fresh RFC3339 timestamp) by hand. The phase check happens before the circuit check, so the next reconcile enters `reconcileHealthCheck` directly and, on a successful rollout, resets `RollbackCount` and `CircuitOpen`. (`FailedVersions` is left intact, so the controller still won't auto-pick the versions that failed before.) Skipping the timestamp or pointing `target-image` at something that won't go ready will just cause an immediate rollback, so the manual path needs all three pieces.

The argument for this design: three consecutive bad versions probably means something is wrong outside the controller's view (broken upstream image, broken probe, broken cluster networking). Auto-recovery would just rediscover the broken state on a fresh schedule and burn through more rollouts. We'd rather page somebody.

If you wanted to add a "soak then retry" mode, the natural place is to have the recovery logic clear `CircuitOpen` after, say, the third consecutive successful version-poll-with-no-update — i.e., a stable period where there's nothing new to try. That's a reasonable PR.

## Mechanism 6 — version history (with a cap)

Every successful update *and* every rollback appends an entry to `Status.AutoUpdate.VersionHistory`:

```go
status.VersionHistory = append(status.VersionHistory, clawv1alpha1.VersionHistoryEntry{
    Version:   version,
    AppliedAt: metav1.Now(),
    Status:    clawv1alpha1.VersionHistoryHealthy,  // or VersionHistoryRolledBack
})
trimVersionHistory(status)
```

`trimVersionHistory` exists because etcd objects have size limits, and a `Claw` that's been updating daily for two years can otherwise accumulate 700+ history entries:

```go
const maxVersionHistory = 50

func trimVersionHistory(status *clawv1alpha1.AutoUpdateStatus) {
    if len(status.VersionHistory) > maxVersionHistory {
        status.VersionHistory = status.VersionHistory[len(status.VersionHistory)-maxVersionHistory:]
    }
}
```

50 entries is enough to debug the last few months of activity. If you need long-term audit, scrape the controller's events into your observability stack. Status fields are not an audit log.

## The Update vs Status.Update dance

Annotations live on the resource (under `metadata`). Status fields live under `.status`. In Kubernetes, these are written through different subresources:

- `r.Update(ctx, claw)` — writes `metadata` and `spec`. Bumps `resourceVersion`.
- `r.Status().Update(ctx, claw)` — writes `.status`. Also bumps `resourceVersion`.

When a single reconcile needs to write both — like the "start an update" path, which sets three annotations *and* writes status fields — the in-memory `claw` object goes stale between the two calls. The controller does an explicit re-fetch in between:

```go
// Update annotations first, then re-fetch and merge status.
if err := r.Update(ctx, &claw); err != nil {
    return ctrl.Result{}, fmt.Errorf("failed to set target-image annotation: %w", err)
}
// Re-fetch to get updated resourceVersion before status update.
if err := r.Get(ctx, req.NamespacedName, &claw); err != nil {
    return ctrl.Result{}, fmt.Errorf("failed to re-fetch after annotation update: %w", err)
}
mergeAutoUpdateStatus(&claw, status)
for _, c := range pendingConditions {
    apimeta.SetStatusCondition(&claw.Status.Conditions, c)
}
if err := r.Status().Update(ctx, &claw); err != nil {
    return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
}
```

The re-fetch picks up the new `resourceVersion` so `Status().Update` doesn't conflict with the write we just did. Without it you'll see 409 errors under any non-trivial reconcile rate.

`mergeAutoUpdateStatus` is the other half. It copies our locally-tracked status fields one at a time into the freshly-fetched object instead of swinging `claw.Status.AutoUpdate` to a different pointer. Field-by-field copy is conservative: if a future field is added to `AutoUpdateStatus` and we forget to track it locally, a wholesale pointer replacement would silently zero it. The merge style makes the controller's status writes additive within the auto-update sub-object.

```go
func mergeAutoUpdateStatus(claw *clawv1alpha1.Claw, local *clawv1alpha1.AutoUpdateStatus) {
    if claw.Status.AutoUpdate == nil {
        claw.Status.AutoUpdate = &clawv1alpha1.AutoUpdateStatus{}
    }
    s := claw.Status.AutoUpdate
    s.CurrentVersion = local.CurrentVersion
    s.AvailableVersion = local.AvailableVersion
    // ... field-by-field copy ...
}
```

If your controller writes both annotations and status, you need this dance. If it only writes one, you don't.

## Testability: Clock and TagLister

Two interfaces, both for tests:

```go
type TagLister interface {
    ListTags(ctx context.Context, image string) ([]string, error)
}

type Clock interface {
    Now() time.Time
    Since(t time.Time) time.Duration
}
```

`TagLister` lets unit tests inject `[]string{"1.0.0", "1.1.0", "2.0.0-rc1"}` instead of hitting GHCR. `Clock` lets them advance time without `time.Sleep`. Both have one-line production implementations and one-line fake implementations.

These get wired in the manager setup:

```go
// cmd/operator/main.go
registryClient := clawregistry.NewRegistryClient()
&controller.AutoUpdateReconciler{
    Client:    mgr.GetClient(),
    Scheme:    mgr.GetScheme(),
    Recorder:  mgr.GetEventRecorderFor("autoupdate-controller"),
    TagLister: registryClient,
    // Clock is left nil; clock() falls back to realClock{}.
}
```

In the reconcile-path tests, both fields get fakes:

```go
cl := fake.NewClientBuilder().
    WithScheme(scheme).
    WithObjects(claw).
    WithStatusSubresource(claw).
    Build()
r := &AutoUpdateReconciler{
    Client:    cl,
    Scheme:    scheme,
    Recorder:  record.NewFakeRecorder(10),
    TagLister: &testTagLister{tags: []string{"1.0.0", "1.1.0"}},
    Clock:     &testClock{now: time.Now()},
}
```

The autoupdate unit tests use `controller-runtime/pkg/client/fake` — no envtest API server, no kube-apiserver process, just an in-memory client backed by typed scheme. They create a `Claw`, run a single `Reconcile` pass with a controlled clock, and assert on annotations and `Status.AutoUpdate`. No real registry calls, no real timers, no flake. Total run time is sub-second per test.

If you find yourself reaching for `time.Now()` or hitting an external API directly inside a reconciler, stop and define the interface first. Future-you writing tests will thank present-you.

## What we didn't do (on purpose)

- **Pre-flight image probe.** We don't pull the new image and try `docker run` it on a node before flipping the StatefulSet. That would be a much heavier dependency (DaemonSet? privileged container?) and the StatefulSet rollout is itself a kind of probe — the readiness check just runs in production.
- **Canary deploys.** Roll one pod, observe, then the rest. For most agent workloads we have, replicas is 1 and there's nothing to canary against. For higher-replica deployments, this is a worthwhile follow-up — the existing state machine could grow a `Canary` phase between idle and `HealthCheck`.
- **Webhook-driven updates.** Push from registry instead of poll. Simpler operationally but creates an inbound dependency from the registry to the cluster, which is not a thing most clusters want. Cron-poll wins on operational simplicity.
- **Cross-namespace coordination.** If you have ten Claws on the same image and a bad version drops, they will all roll back independently. We considered tying them together via a shared `ClawImageGroup` resource and decided the complexity wasn't worth it. The circuit breaker + failed-versions list is good enough: each Claw learns from its own pain.
- **Image signature verification.** Sigstore / cosign integration would slot in at `IsDigestPinned`'s level — verify, *then* set `target-image`. We didn't ship it because the projects we serve aren't there yet, but it's an obvious next step for security-sensitive deployments.

## Testing

Unit tests are split across three files:

- [`internal/controller/autoupdate_reconcile_test.go`](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/controller/autoupdate_reconcile_test.go) — the largest reconcile-path set. Covers initiating an update, skipping digest-pinned images, health-check success, rollback on timeout, circuit-breaker opening after consecutive rollbacks, StatefulSet-not-found behavior, invalid `update-started` triggering an immediate rollback, custom `healthTimeout` override, and the schedule-not-due requeue path.
- [`internal/controller/autoupdate_controller_test.go`](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/controller/autoupdate_controller_test.go) — a mix: helper-function coverage (`extractVersionFromImage`, `trimVersionHistory`, `containsString`, cron-due math, the `realClock` fallback inside `clock()`) plus a smaller batch of reconcile tests for the disabled/no-new-version/not-found/circuit-already-open paths.
- [`internal/controller/autoupdate/autoupdate_controller_test.go`](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/controller/autoupdate/autoupdate_controller_test.go) — an older parallel suite kept alive against the same controller code.

The reconcile-path tests pre-load a `Claw` (and optionally a `StatefulSet` with the desired readiness state), run a single `Reconcile` pass, and assert on annotations or `Status.AutoUpdate`. Most tests are under 50 lines. The fake clock and fake tag lister make timing deterministic, which is the main reason the tests aren't flaky.

## What this bought us

A ~470-line controller that does cron-driven, semver-filtered, health-verified, automatically-rolling-back image updates for a CRD, with a circuit breaker and version history. All in-flight state lives on the `Claw` resource (annotations for phase, `.status` for durable bookkeeping), so the controller has no in-memory state to lose across restarts. Supported runtime types are mapped to their base OCI images via a small `ImageForRuntime(string) string` helper — adding a new runtime there is one switch-case, not a controller change. Runtimes without an entry are silently skipped by auto-update (we currently have a couple of those — `hermesrs` and `k8sops` — that don't track a public OCI release cadence). The rest of the controller works in plain semver tags.

The thing I'd point a junior K8s-controller author at, in this code, is the annotation-driven separation: *the controller doesn't do the rollout, it asks for the rollout*. Once you internalize that, a lot of K8s controllers get smaller.

## What to look at next

- The [k8s4claw repo](https://github.com/Prismer-AI/k8s4claw) if you want the full operator
- [`autoupdate_controller.go`](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/controller/autoupdate_controller.go) for the controller in one file
- [`registry/resolver.go`](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/registry/resolver.go) for the version picker
- [The IPC bus deep dive](https://dev.to/willamhou/building-an-ipc-bus-for-kubernetes-sidecars-wal-dlq-and-ring-buffer-backpressure-4b27) — Post 2 of this series, on how channel sidecars talk to the runtime

Open source, Apache-2.0. If you've built an auto-updater that handles canary deploys or signature verification, I'd genuinely like to read your code. Drop a link in the comments.
