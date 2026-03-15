# Helm Chart for k8s4claw Operator — Design Document

**Date:** 2026-03-15
**Status:** Approved

## Goal

Package the k8s4claw Kubernetes operator for distribution via Helm, enabling one-command installation with configurable webhook TLS, monitoring, and CRD lifecycle management.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Webhook TLS | cert-manager preferred, self-signed Job fallback | Covers both production (cert-manager) and dev/CI (self-signed) environments |
| CRD installation | `templates/` with `helm.sh/hook` | Ensures CRDs update on `helm upgrade`; `resource-policy: keep` prevents accidental deletion |

## Chart Structure

```
charts/k8s4claw/
├── Chart.yaml                    # apiVersion: v2, type: application
├── values.yaml
├── templates/
│   ├── _helpers.tpl              # Labels, selectors, fullname helpers
│   ├── crds/
│   │   ├── claw.yaml             # Claw CRD
│   │   ├── clawchannel.yaml      # ClawChannel CRD
│   │   └── clawselfconfig.yaml   # ClawSelfConfig CRD
│   ├── serviceaccount.yaml
│   ├── clusterrole.yaml
│   ├── clusterrolebinding.yaml
│   ├── deployment.yaml           # Operator deployment
│   ├── service.yaml              # Webhook service (k8s4claw-webhook-service)
│   ├── webhook-configs.yaml      # MutatingWebhookConfiguration + ValidatingWebhookConfiguration
│   ├── cert-manager.yaml         # Issuer + Certificate (conditional: certManager.enabled)
│   ├── selfsigned-cert.yaml      # Job + Secret (conditional: !certManager.enabled)
│   └── monitoring/
│       ├── servicemonitor.yaml   # Prometheus ServiceMonitor
│       └── prometheusrule.yaml   # PrometheusRule with alerts
└── README.md
```

## values.yaml Schema

```yaml
image:
  repository: ghcr.io/prismer-ai/k8s4claw
  tag: ""                          # Defaults to Chart.appVersion
  pullPolicy: IfNotPresent

replicaCount: 1

resources:
  requests: { cpu: 100m, memory: 128Mi }
  limits:   { cpu: 500m, memory: 256Mi }

serviceAccount:
  create: true
  name: ""                         # Auto-generated if empty

leaderElection:
  enabled: true

nativeSidecars:
  enabled: true                    # Requires Kubernetes 1.28+

webhook:
  port: 9443
  certManager:
    enabled: true                  # Preferred: use cert-manager for TLS
    issuerRef:
      kind: Issuer                 # Namespace-scoped self-signed Issuer
      name: ""                     # Auto-created if empty
  selfSigned:
    enabled: false                 # Auto-enabled when certManager.enabled=false

monitoring:
  enabled: false
  serviceMonitor:
    interval: 30s
    labels: {}                     # e.g. { release: prometheus }
  prometheusRule:
    enabled: true                  # Gated by monitoring.enabled

nodeSelector: {}
tolerations: []
affinity: {}
```

## Template Implementation Details

### CRDs (templates/crds/)

Source: embedded from `config/crd/bases/`. All CRD templates carry:

```yaml
annotations:
  "helm.sh/hook": pre-install,pre-upgrade
  "helm.sh/hook-weight": "-5"
  "helm.sh/resource-policy": keep
```

This ensures CRDs are applied before the operator on both install and upgrade, and are preserved on `helm uninstall`.

### Webhook Service (templates/service.yaml)

Creates the `k8s4claw-webhook-service` Service (missing from current manifests) targeting the operator Deployment on port 9443.

### cert-manager Path (templates/cert-manager.yaml)

Rendered when `.Values.webhook.certManager.enabled=true`:

1. **Issuer** — self-signed, namespace-scoped
2. **Certificate** — `secretName: k8s4claw-webhook-server-cert`, DNS names include the webhook service FQDN

Webhook configurations use the `cert-manager.io/inject-ca-from` annotation for automatic `caBundle` injection.

### Self-Signed Fallback Path (templates/selfsigned-cert.yaml)

Rendered when `.Values.webhook.certManager.enabled=false`:

1. **Job** (hook: `pre-install,pre-upgrade`, weight: `-3`) using a minimal image with `openssl`:
   - Generates a self-signed CA + server certificate
   - Creates Secret `k8s4claw-webhook-server-cert` with `tls.crt`, `tls.key`, `ca.crt`
   - Patches MutatingWebhookConfiguration and ValidatingWebhookConfiguration with the CA bundle

The hook weight `-3` ensures execution after CRDs (`-5`) but before the operator Deployment.

### Deployment (templates/deployment.yaml)

Based on `config/manager/deployment.yaml`, templatized:

- Image: `{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}`
- Resources: `{{ .Values.resources }}`
- Operator flags: `--leader-elect={{ .Values.leaderElection.enabled }}`, `--enable-native-sidecars={{ .Values.nativeSidecars.enabled }}`, `--webhook-port={{ .Values.webhook.port }}`
- Security context: non-root (UID 65532), read-only filesystem
- Volume mount: webhook cert from Secret at `/tmp/k8s-webhook-server/serving-certs`

### Monitoring (templates/monitoring/)

Entire directory gated by `{{ if .Values.monitoring.enabled }}`:

- **ServiceMonitor**: scrapes port 8080 (`/metrics`), interval from `.Values.monitoring.serviceMonitor.interval`
- **PrometheusRule**: 8 alerts (IPC Bus backpressure, DLQ growth, bridge disconnect, auto-update circuit breaker, etc.), gated by `.Values.monitoring.prometheusRule.enabled`

### _helpers.tpl

Standard helpers:

- `k8s4claw.fullname` — release-scoped name
- `k8s4claw.labels` — common labels (`app.kubernetes.io/name`, `app.kubernetes.io/instance`, `app.kubernetes.io/version`, `app.kubernetes.io/managed-by`)
- `k8s4claw.selectorLabels` — subset for selectors
- `k8s4claw.webhookCertManager` — boolean helper resolving cert-manager vs self-signed

## Existing Resources Mapping

| Existing Manifest | Helm Template | Notes |
|---|---|---|
| `config/crd/bases/*.yaml` | `templates/crds/*.yaml` | Wrapped with hook annotations |
| `config/manager/deployment.yaml` | `templates/deployment.yaml` | Templatized image, resources, flags |
| `config/rbac/role.yaml` | `templates/clusterrole.yaml` | Direct embed with labels |
| `config/rbac/role_binding.yaml` | `templates/clusterrolebinding.yaml` | References templatized SA |
| `config/rbac/service_account.yaml` | `templates/serviceaccount.yaml` | Conditional creation |
| `config/webhook/manifests.yaml` | `templates/webhook-configs.yaml` | caBundle injection via cert-manager or Job |
| (missing) | `templates/service.yaml` | New: webhook Service |
| `config/prometheus/servicemonitor.yaml` | `templates/monitoring/servicemonitor.yaml` | Conditional |
| `config/prometheus/prometheusrule.yaml` | `templates/monitoring/prometheusrule.yaml` | Conditional |

## Prerequisites

- Kubernetes 1.28+ (native sidecar support)
- cert-manager (if `webhook.certManager.enabled=true`)
- Prometheus Operator (if `monitoring.enabled=true`)
