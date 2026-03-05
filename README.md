# k8s4claw

[![CI](https://github.com/Prismer-AI/k8s4claw/actions/workflows/ci.yml/badge.svg)](https://github.com/Prismer-AI/k8s4claw/actions/workflows/ci.yml)
[![CodeQL](https://github.com/Prismer-AI/k8s4claw/actions/workflows/codeql.yml/badge.svg)](https://github.com/Prismer-AI/k8s4claw/actions/workflows/codeql.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/Prismer-AI/k8s4claw)](https://goreportcard.com/report/github.com/Prismer-AI/k8s4claw)
[![License](https://img.shields.io/github/license/Prismer-AI/k8s4claw)](LICENSE)

Kubernetes Operator + Go SDK for managing heterogeneous AI agent runtimes on Kubernetes.

## Overview

k8s4claw provides unified lifecycle management for multiple AI agent runtimes:

| Runtime | Description |
|---------|-------------|
| **OpenClaw** | Full-featured AI assistant platform (TypeScript/Node.js) |
| **NanoClaw** | Lightweight secure personal assistant (TypeScript/Node.js) |
| **ZeroClaw** | High-performance agent runtime (Rust) |
| **Custom** | Bring your own runtime |

## Features

- **CRD-based management** — Declarative `Claw` and `ClawChannel` resources
- **Runtime adapters** — Extensible interface for any agent runtime
- **Channel sidecars** — Pluggable communication (Slack, Telegram, Discord, custom)
- **IPC Bus** — Native sidecar with WAL-backed delivery, backpressure, and DLQ
- **Persistence** — PVC lifecycle, CSI snapshots, S3 archival
- **Observability** — Prometheus metrics, status conditions, K8s Events
- **Go SDK** — Simple client for infrastructure integration

## Architecture

```text
┌─────────────────── Claw Pod ───────────────────┐
│                                                 │
│  ┌──────────┐   UDS    ┌──────────┐   Bridge   │
│  │ Channel  │◄────────►│ IPC Bus  │◄──────────►│ Runtime
│  │ Sidecar  │ bus.sock │ (sidecar)│  WS/UDS/  │ Container
│  └──────────┘          │          │  SSE/TCP   │
│                        │ WAL+DLQ  │            │
│                        └──────────┘            │
└─────────────────────────────────────────────────┘
```

The **IPC Bus** is a standalone Go binary deployed as a Kubernetes native sidecar (init container with `restartPolicy: Always`). It routes JSON messages between channel sidecars and the AI runtime via:

- **Unix Domain Socket** (`/var/run/claw/bus.sock`) — sidecar-facing
- **RuntimeBridge** — runtime-facing (WebSocket for OpenClaw, protocol-specific for others)
- **WAL** — append-only write-ahead log on emptyDir for at-least-once delivery
- **DLQ** — BoltDB dead letter queue for messages exceeding retry limits
- **Backpressure** — ring buffer with high/low watermark flow control

## Quick Start

```bash
# Install CRDs
make install

# Run operator locally
make run

# Deploy a Claw
kubectl apply -f config/samples/openclaw-basic.yaml
```

## SDK Usage

```go
import "github.com/Prismer-AI/k8s4claw/sdk"

client, err := sdk.NewClient()
if err != nil {
    log.Fatal(err)
}

claw, err := client.Create(ctx, &sdk.ClawSpec{
    Runtime: sdk.OpenClaw,
    Config: &sdk.RuntimeConfig{
        Environment: map[string]string{"MODEL": "claude-sonnet-4"},
    },
})
```

## Architecture

Design documents:

- [Operator Core Design](docs/plans/2026-03-04-k8s4claw-design.md)
- [IPC Bus + Resilience Design](docs/plans/2026-03-05-phase4-ipcbus-resilience-design.md)

## Development

```bash
make build          # Build operator binary
make build-ipcbus   # Build IPC Bus binary
make test           # Run tests (requires setup-envtest for controller tests)
make lint           # Lint
make vet            # Run go vet
make fmt            # Run gofmt + goimports
make manifests      # Generate CRD YAML
make generate       # Generate deepcopy
make docker-build   # Build container image
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full development guide.

## License

Apache-2.0
