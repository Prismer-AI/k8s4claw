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
