# Contributing to k8s4claw

We welcome contributions! This document explains how to get involved.

## Development Setup

### Prerequisites

- Go 1.25+
- Docker
- kubectl + access to a Kubernetes cluster (kind/minikube for local dev)
- [controller-gen](https://book.kubebuilder.io/reference/controller-gen) (for CRD generation)
- [golangci-lint](https://golangci-lint.run/) (for linting)

### Getting Started

```bash
git clone https://github.com/Prismer-AI/k8s4claw.git
cd k8s4claw
make build
make test
make lint
```

## Making Changes

### Workflow

1. Fork the repository
2. Create a feature branch from `main`
3. Make your changes
4. Ensure tests pass: `make test`
5. Ensure lint passes: `make lint`
6. Ensure generated files are up to date: `make generate && make manifests`
7. Commit with DCO sign-off: `git commit -s`
8. Open a pull request

### Commit Messages

We follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(scope): description

[optional body]

Signed-off-by: Your Name <your.email@example.com>
```

Types: `feat`, `fix`, `docs`, `test`, `refactor`, `chore`, `ci`, `perf`

### DCO Sign-Off

All commits must be signed off per the [Developer Certificate of Origin](DCO). Use `git commit -s` to add the sign-off automatically.

### Pull Request Guidelines

- Keep PRs focused — one logical change per PR
- Include tests for new functionality
- Update documentation if behavior changes
- Ensure CI passes before requesting review
- Fill out the PR template completely

## Code Standards

- Follow idiomatic Go patterns
- Run `gofmt` and `goimports` on all files
- Maintain test coverage for new code
- Use `context.Context` for cancellation and timeouts
- Wrap errors with context: `fmt.Errorf("failed to X: %w", err)`

## Adding a New Runtime

Implement the `RuntimeAdapter` interface in `internal/runtime/`:

1. Create `internal/runtime/yourruntime.go`
2. Implement `RuntimeBuilder` + `RuntimeValidator`
3. Register in the adapter registry
4. Add a sample CR in `config/samples/`
5. Add tests

## IPC Bus Development

The IPC Bus is a standalone binary in `cmd/ipcbus/` with core logic in `internal/ipcbus/`.

```bash
make build-ipcbus   # Build IPC Bus binary
go test -race ./internal/ipcbus/...  # Run IPC Bus tests
```

### Key packages

| Package                              | Purpose                             |
| ------------------------------------ | ----------------------------------- |
| `internal/ipcbus/message.go`         | Message types and envelope (UUIDv7) |
| `internal/ipcbus/framing.go`         | Length-prefix wire protocol         |
| `internal/ipcbus/ringbuffer.go`      | Backpressure ring buffer            |
| `internal/ipcbus/wal.go`             | Write-ahead log (JSON-lines)        |
| `internal/ipcbus/dlq.go`             | Dead letter queue (BoltDB)          |
| `internal/ipcbus/bridge*.go`         | RuntimeBridge adapters              |
| `internal/ipcbus/server.go`          | UDS server + connection handling    |
| `internal/ipcbus/router.go`          | Message routing with retry logic    |
| `internal/controller/claw_ipcbus.go` | Operator sidecar injection          |

### Auto-Update Controller

The auto-update controller monitors OCI registries for new runtime image versions, applies updates with health verification, and rolls back on failure with circuit-breaker protection.

| Package | Purpose |
| --- | --- |
| `internal/controller/autoupdate_controller.go` | Reconciler with cron scheduling, health checks, rollback |
| `internal/registry/resolver.go` | OCI registry client, semver resolution, tag/token caching |
| `internal/controller/autoupdate/` | Controller tests (separate dir to avoid envtest TestMain) |

### Adding a new RuntimeBridge

1. Create `internal/ipcbus/bridge_yourprotocol.go` implementing `RuntimeBridge`
2. Add a `RuntimeType` constant in `bridge.go`
3. Register in the `NewBridge` factory
4. Add tests

## Adding a New Channel

For built-in channels, add to `internal/channel/`. For custom channels, use the Channel SDK (`sdk/channel/`).

## Reporting Issues

- Use [GitHub Issues](https://github.com/Prismer-AI/k8s4claw/issues)
- Check existing issues before opening a new one
- Use the provided issue templates

## Getting Help

- Open a [Discussion](https://github.com/Prismer-AI/k8s4claw/discussions)
- Tag your issue with `question` for general questions
