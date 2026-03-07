# k8s4claw Operations Runbook

## Deployment

### Prerequisites

- Kubernetes 1.28+ (native sidecar support required for IPC Bus)
- `kubectl` configured with cluster access
- CRDs installed: `make install`

### Deploy the Operator

```bash
# Build and push the operator image
make docker-build IMG=ghcr.io/prismer-ai/k8s4claw:v0.1.0
make docker-push IMG=ghcr.io/prismer-ai/k8s4claw:v0.1.0

# Deploy
make deploy
```

### Deploy a Claw Instance

```bash
kubectl apply -f config/samples/openclaw-basic.yaml
```

### Verify

```bash
kubectl get claws
kubectl get pods -l app.kubernetes.io/managed-by=k8s4claw
```

## IPC Bus

The IPC Bus runs as a native sidecar (init container with `restartPolicy: Always`) in each Claw pod. It is automatically injected by the operator.

### Environment Variables

| Variable              | Default                    | Description                          |
| --------------------- | -------------------------- | ------------------------------------ |
| `CLAW_SOCKET_PATH`    | `/var/run/claw/bus.sock`   | UDS listen path                      |
| `CLAW_WAL_DIR`        | `/var/run/claw/wal`        | WAL storage directory                |
| `CLAW_DLQ_PATH`       | `/var/run/claw/dlq.db`     | BoltDB DLQ file path                 |
| `CLAW_DLQ_MAX_SIZE`   | `10000`                    | Max DLQ entries before eviction      |
| `CLAW_DLQ_TTL`        | `24h`                      | DLQ entry time-to-live               |
| `CLAW_RUNTIME_TYPE`   | `openclaw`                 | Runtime bridge type                  |
| `CLAW_GATEWAY_PORT`   | `3000`                     | Runtime gateway port                 |
| `CLAW_METRICS_PORT`   | `9091`                     | Prometheus metrics port              |
| `CLAW_LOG_LEVEL`      | `info`                     | Log level (debug, info, warn, error) |

### Graceful Shutdown

Shutdown sequence:

1. IPC Bus preStop hook sends shutdown command to all sidecars
2. Runtime preStop hook sleeps 2s (allows IPC Bus to drain)
3. IPC Bus polls until no sidecars remain or drain timeout expires
4. WAL is flushed and bridge is closed
5. `terminationGracePeriodSeconds` is set to base + 15s

## Monitoring

### Prometheus Metrics

| Metric                              | Type    | Labels              | Description                        |
| ----------------------------------- | ------- | ------------------- | ---------------------------------- |
| `claw_ipcbus_messages_total`        | Counter | `channel` `direction` | Total messages routed              |
| `claw_ipcbus_messages_inflight`     | Gauge   |                     | Currently unACKed messages         |
| `claw_ipcbus_buffer_usage_ratio`    | Gauge   | `channel`           | Ring buffer fill ratio (0.0-1.0)   |
| `claw_ipcbus_spill_total`           | Counter |                     | Messages spilled (buffer full)     |
| `claw_ipcbus_dlq_total`             | Counter |                     | Messages moved to DLQ              |
| `claw_ipcbus_dlq_size`              | Gauge   |                     | Current DLQ entry count            |
| `claw_ipcbus_retry_total`           | Counter |                     | Delivery retry attempts            |
| `claw_ipcbus_bridge_connected`      | Gauge   |                     | Bridge connection status (0 or 1)  |
| `claw_ipcbus_sidecar_connections`   | Gauge   |                     | Connected sidecar count            |
| `claw_ipcbus_wal_entries`           | Gauge   |                     | Pending WAL entries                |

### Key Alerts

```yaml
# Buffer backpressure active
- alert: IPCBusBackpressure
  expr: claw_ipcbus_buffer_usage_ratio > 0.8
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "IPC Bus buffer above high watermark"

# DLQ growing
- alert: IPCBusDLQGrowing
  expr: rate(claw_ipcbus_dlq_total[5m]) > 0
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: "Messages being sent to DLQ"

# Bridge disconnected
- alert: IPCBusBridgeDown
  expr: claw_ipcbus_bridge_connected == 0
  for: 1m
  labels:
    severity: critical
  annotations:
    summary: "IPC Bus lost connection to runtime bridge"
```

## Common Issues

### IPC Bus pod not starting

**Symptom:** Init container `claw-ipcbus` in CrashLoopBackOff.

**Check:**

```bash
kubectl logs <pod> -c claw-ipcbus
```

**Common causes:**

- Missing emptyDir volume mount at `/var/run/claw`
- Wrong `CLAW_RUNTIME_TYPE` for the runtime
- Socket path conflict with another process

### Messages stuck in WAL

**Symptom:** `claw_ipcbus_wal_entries` gauge is growing.

**Causes:**

- Runtime bridge disconnected — check `claw_ipcbus_bridge_connected`
- Runtime container not ready — check pod status
- Network policy blocking localhost connections

**Fix:** WAL auto-replays on reconnection. If entries are stale, restart the pod.

### DLQ filling up

**Symptom:** `claw_ipcbus_dlq_size` approaching `CLAW_DLQ_MAX_SIZE`.

**Causes:**

- Messages exceeding 5 retry attempts (runtime consistently rejecting)
- Malformed messages that the runtime cannot process

**Investigation:**

```bash
# Check DLQ contents via debug endpoint (if enabled)
kubectl exec <pod> -c claw-ipcbus -- cat /var/run/claw/dlq.db | head
```

**Fix:** Investigate why the runtime is rejecting messages. Old entries auto-expire after `CLAW_DLQ_TTL` (default 24h). Oldest entries are evicted when max size is reached.

### Backpressure slow_down not releasing

**Symptom:** Channel sidecar receiving `slow_down` but never `resume`.

**Causes:**

- Consumer (runtime) not processing messages
- Ring buffer stuck at high watermark

**Fix:** Check runtime health. The `resume` signal is sent when buffer drops below low watermark (default 0.3).

## Auto-Update Controller

The auto-update controller monitors OCI registries for new runtime image versions and applies updates with health verification. It uses a state machine (Idle → HealthCheck → Success/Rollback) with circuit-breaker protection.

### Configuration

Auto-update is configured per Claw CR via `spec.autoUpdate`:

```yaml
spec:
  autoUpdate:
    enabled: true
    schedule: "0 3 * * *"           # Cron schedule (default: 3 AM daily)
    versionConstraint: "^1.0.0"     # Semver constraint
    healthTimeout: "10m"            # Health check timeout (default: 10m)
    maxRollbacks: 3                 # Rollbacks before circuit opens (default: 3)
```

### Prometheus Metrics

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `claw_autoupdate_checks_total` | Counter | `namespace` | Total version check attempts |
| `claw_autoupdate_results_total` | Counter | `namespace`, `result` | Update outcomes (`success`, `rollback`) |
| `claw_autoupdate_circuit_open` | Gauge | `namespace`, `name` | Circuit breaker state (0 or 1) |

### Key Alerts

```yaml
# Circuit breaker open — auto-updates are blocked
- alert: AutoUpdateCircuitOpen
  expr: claw_autoupdate_circuit_open == 1
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "Auto-update circuit breaker open for {{ $labels.namespace }}/{{ $labels.name }}"
    description: "Repeated rollbacks have triggered the circuit breaker. Manual intervention required."

# Repeated rollbacks
- alert: AutoUpdateRollbackRate
  expr: rate(claw_autoupdate_results_total{result="rollback"}[1h]) > 0
  for: 30m
  labels:
    severity: warning
  annotations:
    summary: "Auto-update rollbacks occurring in {{ $labels.namespace }}"
```

### Status Conditions

The controller sets two status conditions on the Claw resource:

| Condition | Description |
| --- | --- |
| `AutoUpdateAvailable` | `True` when a newer version matching the constraint is found |
| `AutoUpdateInProgress` | `True` during the health check phase after an update is applied |

```bash
kubectl get claw my-claw -o jsonpath='{.status.conditions}' | jq .
```

### Common Issues

#### Registry unreachable

**Symptom:** Version checks fail, no updates applied. Check operator logs for `failed to list tags from registry`.

**Causes:**
- Network policy blocking egress to the registry
- Registry credentials expired or missing
- Registry rate limiting

**Fix:** Verify network connectivity from the operator pod. The controller retries after 5 minutes on failure.

#### Circuit breaker open

**Symptom:** `claw_autoupdate_circuit_open` is 1. Kubernetes events show `AutoUpdateCircuitOpen`.

**Cause:** The controller hit `maxRollbacks` consecutive failed updates. Failed versions are tracked in `status.autoUpdate.failedVersions`.

**Investigation:**

```bash
# Check failed versions and rollback count
kubectl get claw my-claw -o jsonpath='{.status.autoUpdate}' | jq .

# Check Kubernetes events for rollback reasons
kubectl describe claw my-claw | grep -A 2 AutoUpdate
```

**Fix:** Resolve the underlying issue (broken image, resource limits, etc.), then reset the circuit breaker:

```bash
kubectl patch claw my-claw --type=merge --subresource=status \
  -p '{"status":{"autoUpdate":{"circuitOpen":false,"rollbackCount":0,"failedVersions":[]}}}'
```

#### Health check timeout

**Symptom:** Updates are applied but roll back after `healthTimeout` (default 10m).

**Cause:** The new version's pods are not becoming Ready within the timeout. The controller checks that both `UpdatedReplicas` and `ReadyReplicas` match the desired replica count.

**Investigation:**

```bash
# Check StatefulSet rollout status
kubectl rollout status statefulset my-claw

# Check pod events
kubectl describe pod my-claw-0 | tail -20
```

**Fix:** Increase `healthTimeout` if the runtime needs more startup time, or fix the image causing readiness probe failures.

#### Image is digest-pinned

**Symptom:** Auto-update enabled but no version checks occur.

**Cause:** The `claw.prismer.ai/target-image` annotation contains a `@sha256:` digest reference. The controller skips digest-pinned images by design.

**Fix:** Remove the digest pin to allow tag-based updates.

### Manual Update Rollback

To manually trigger a rollback to the default runtime image:

```bash
# Remove the target-image annotation
kubectl annotate claw my-claw \
  claw.prismer.ai/target-image- \
  claw.prismer.ai/update-phase- \
  claw.prismer.ai/update-started-
```

The ClawReconciler will rebuild the StatefulSet with the default image for the runtime type on the next reconciliation.

## Rollback

### Rollback Operator

```bash
# Revert to previous operator image
kubectl set image deployment/k8s4claw-operator \
  operator=ghcr.io/prismer-ai/k8s4claw:<previous-tag>
```

### Rollback CRDs

```bash
# Reinstall previous CRD version
git checkout <previous-tag> -- config/crd/bases/
make install
```

### Emergency: Remove IPC Bus from a Claw

The IPC Bus is injected by the operator. To temporarily disable it, remove the `ipcBus` annotation or field from the Claw CR and the operator will rebuild the StatefulSet without it on the next reconciliation.
