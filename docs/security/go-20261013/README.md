# Go security upgrade — 2026-10-13

This is a separate, stacked security change based on PR #34 at
`034489e0cfd4fe7b0fddcc64d08b7c4cbea3d8d3`. PR #33's historical failures and
PR #34's lifecycle fixes and recordings remain unchanged. This change does not
repeat or claim new Kubernetes lifecycle tests.

## Version scope

- Product toolchain: Go **1.26.9**, in all three Go modules and all nine Go Docker
  builders (`golang:1.26.9-alpine`). CI reads the module's Go version.
- Root dependencies: `x/net v0.60.0`, `x/text v0.42.0`, and their required
  `x/crypto v0.57.0`, `x/sys v0.48.0`, `x/term v0.46.0`, `x/sync v0.23.0`.
  The resolved graph also updates `x/tools v0.49.0` and `x/mod v0.41.0` checksums.
- OpenClaw requires only the toolchain change; its existing SDK and websocket
  versions passed its independent scan. Kubernetes/controller-runtime versions
  and application source are unchanged.
- CI now checks PRs targeting stacked branches, filters nested module manifests,
  independently scans both modules, tests/builds/vets OpenClaw, and builds the
  previously omitted Discord and mock Dockerfiles. Existing thresholds and
  security checks remain enabled.

## Local verification

macOS arm64; Go 1.26.9; envtest Kubernetes 1.32.0; golangci-lint 2.14.0;
govulncheck 1.9.0. The official Darwin/arm64 Go archive SHA-256 was verified:
`f9bb7c0a02506c5d9bf0d1eb1f7ee6c7684f844ae49558308a0427b830e022cc`.
The official linter archive checksum was also verified:
`5ef5f36a7147e91dc58ef9ef4d11bb7bad5ead0c76eb6c01327a73c641d1dcc3`.

| Check | Result |
|---|---|
| Root `go test -race -coverprofile=coverage.out -covermode=atomic ./...` with envtest | PASS; total coverage 66.8% |
| Root `go build ./...`, `go vet ./...`, `golangci-lint run ./...` | PASS; lint 0 issues |
| OpenClaw `go test -race ./...`, build, vet | PASS; **no test files**, so no runtime behavior coverage |
| Mock `go test -race ./...`, vet | PASS; **no test files** |
| `make generate manifests` | PASS; no generated-file changes |
| Root and OpenClaw `go mod verify` | PASS |
| Root and OpenClaw `govulncheck -show verbose ./...` | PASS; zero reachable findings |
| Supplemental OpenClaw lint using the root rules | FAIL; 7 pre-existing issues, listed in `lint-openclaw.log` |

The OpenClaw lint findings are four unchecked error returns, two gocritic
findings and one formatting finding. Its application files are byte-identical to
#34; these findings are reported rather than changing unrelated code or
suppressing rules. The existing required CI lint job checks the root module.

## Residual finding and coverage boundaries

Root scanning still reports **GO-2026-5932** at module level: the unmaintained
`golang.org/x/crypto/openpgp` package has no fixed version. No imported-package or
reachable-symbol finding was reported for it. See the complete verbose scan logs
alongside this report. OpenClaw reported no findings.

The prior CI's 21 reachable findings are absent with the upgraded product
libraries and toolchain. This does not establish absolute safety: scans depend
on the vulnerability database, selected OS/architecture, build tags and static
reachability. Local scans target Darwin/arm64; GitHub CI scans Linux. These are
Go source scans, not an OS-image, container configuration or Rust dependency
audit. No paid cloud resource or existing Kubernetes cluster was used.

## Reproduce

Use Go 1.26.9, govulncheck and golangci-lint, with envtest 1.32.x binaries available.
On macOS set `TMPDIR=/tmp` to avoid Unix socket path length limits.

```sh
export GOTOOLCHAIN=local
export TMPDIR=/tmp
export KUBEBUILDER_ASSETS="$(setup-envtest use 1.32.x -p path)"
go mod verify
go test -race -coverprofile=coverage.out -covermode=atomic ./...
go build ./...
go vet ./...
golangci-lint run ./...
make generate manifests
govulncheck -show verbose ./...
(cd runtimes/openclaw && go mod verify && go test -race ./... && go build ./... && go vet ./... && govulncheck -show verbose ./...)
docker build -f Dockerfile .
docker build -f Dockerfile.claw4k8s .
docker build -f Dockerfile.init .
docker build -f Dockerfile.ipcbus .
docker build -f Dockerfile.channel-slack .
docker build -f Dockerfile.channel-webhook .
docker build -f Dockerfile.channel-discord .
docker build -f runtimes/openclaw/Dockerfile runtimes/openclaw
docker build -f runtimes/mock/Dockerfile runtimes/mock
```

The first local Docker attempt failed before building because the installed
legacy builder does not support `--progress`. The supported `docker build`
commands above are used for the actual retry. Final image-build and remote CI
results are recorded in the PR, tied to its final head commit.

Sources: [Go downloads](https://go.dev/dl/),
[x/net v0.60.0 module](https://github.com/golang/net/blob/v0.60.0/go.mod),
[GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932).
