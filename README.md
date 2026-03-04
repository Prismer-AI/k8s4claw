# k8s4claw

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
- **IPC Bus** — Unified streaming protocol with backpressure
- **Persistence** — PVC lifecycle, CSI snapshots, S3 archival
- **Observability** — Prometheus metrics, status conditions, K8s Events
- **Go SDK** — Simple client for infrastructure integration

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

See [docs/plans/2026-03-04-k8s4claw-design.md](docs/plans/2026-03-04-k8s4claw-design.md) for the full design document.

## Development

```bash
make build          # Build operator
make test           # Run tests
make lint           # Lint
make manifests      # Generate CRD YAML
make generate       # Generate deepcopy
make docker-build   # Build container image
```

## License

Apache-2.0
