# Runtime startup and durable upgrade recovery — 2026-10-13

This is the follow-up to the unchanged failure evidence in [draft PR #33](https://github.com/Prismer-AI/k8s4claw/pull/33). Product fixes and this new evidence belong to [draft PR #34](https://github.com/Prismer-AI/k8s4claw/pull/34). Neither PR is merged.

## Scope and source

The fixes give init separate read-only source and writable output volumes, share its output with the runtime, provide an ephemeral workspace when no PVC is requested, and pass the actual socket filename to IPC. IPC waits for the runtime connection before starting its reader, because a native sidecar may start first. Auto-update captures a verified previous-good image, checks the target image/generation/revision and actual owned Pods, persists rollback accounting before cleanup, and removes only owned unready failed-image Pods after Kubernetes observes the restored template. A missing previous-good reference blocks rollback explicitly; it never guesses `latest`.

- Base: `513849e404e2d494e892850462b519af44fc2424`.
- First fix build: local `48274b25a228503dcdc2cab08fe2be2be669588e`, identical source tree published as `5e0b2bd402b4325d1957cc98144b71480fc6917b`.
- Final tested binaries: local `e52f47be4b3c56559bb0fd438a1f3c6700c71996`, identical source tree published as `ff59e6ee510ce3a7c8e402159517d29410be3572`.
- Final harness checkout prints `4134caace81c79272d2945675a6a0d974adc3073`; its only change after the build was test import formatting, published as `7c5e4eacabe17731f46c15a3798c1b7f1b03ffa8`. Product source is unchanged. GitHub Git-object publishing produces different commit metadata, so tree equality was checked explicitly.

## What was actually exercised

All six final checks passed:

| Scenario | Observed result |
| --- | --- |
| Real runtime startup | `mock:false`, Pod 3/3 Ready |
| Init/config/workspace/IPC | Five in-container probes passed |
| Normal upgrade | `0.0.1 → 0.0.2`, new Pod UID, one Healthy history entry |
| Bad version | Real `0.0.3` executable failure; automatic healthy `0.0.2` recovery |
| Operator interruption | Forced deletion during HealthCheck; replacement UID; second recovery |
| Stable follow-up | 35 seconds later: rollbackCount=2, exactly three history entries |

Both dedicated test clusters were deleted; [cleanup transcript](09-cleanup.txt) verifies their node containers are absent. No production context was accessed.

See [results.json](results.json) and the numbered snapshots for authoritative final outcomes. The test requires actual Pod image, UID and controller revision convergence; Claw status alone is insufficient.

The normal `0.0.1` and `0.0.2` tags contain the same real repository OpenClaw gateway binary. `0.0.3` deliberately puts that binary at the wrong executable path. These test image tags are local to Docker/kind, never published to a registry. Scratch packaging is explicit test infrastructure, not the released distroless packaging. The gateway logs `mock:false`; the custom probe/channel fixture is separate from the product runtime and checks config mounts, workspace access, a real Unix socket connection and HTTP readiness. Its placeholder API key is invalid and the API endpoint is localhost port 9. No paid inference or external message delivery occurs.

The harness seeds the real controller's persisted `HealthCheck` annotations and status (including the previous image verified from a healthy StatefulSet). This excludes upstream OCI tag discovery and scheduling, while exercising the real health, timeout, rollback and recovery code against Kubernetes. Registry selection/initiation and forced status-conflict handling are covered by Go regression tests, not claimed as end-to-end kind observations.

## Failed attempts are retained

1. `03-scenarios`: test deployment omitted the repository's additional `clawops_role.yaml`. Operator cache startup was denied. No StatefulSet was created. Both existing repository RBAC roles are used afterward; no cluster-admin workaround.
2. `04-scenarios-rerun`: init succeeded, but IPC attempted to read a nil WebSocket after the runtime was not yet listening. The process panic is preserved. This led to the scoped startup retry fix.
3. `06-final-scenarios`: deleting the namespace containing both operator and Claw left a Claw finalizer after the operator disappeared. The namespace reset timed out before any new lifecycle test. The dedicated cluster was deleted and recreated; no finalizers or production objects were force-edited.
4. `08-fresh-final-scenarios`: final run in a fresh cluster, with rebuilt binaries and complete test RBAC. The recorder captures operator force deletion during HealthCheck, actual replacement UID, and recovery observations.

The earlier three result files and harness revisions are included alongside the full transcripts and casts. Failures are not overwritten by the final result file.

## Recordings

- [Final successful lifecycle recording](08-fresh-final-scenarios.mp4)
- [Initial RBAC setup failure](03-scenarios.mp4)
- [IPC startup panic before the follow-up fix](04-scenarios-rerun.mp4)
- [Test namespace cleanup timeout](06-final-scenarios.mp4)

Each `.cast` is asciicast v2 data from a real child PTY, with original timing and output. `.txt` is the corresponding complete transcript. MP4s replay those captured events at explicitly labeled **4× speed**, 2 fps, 1440×960 H.264. They are terminal-only recordings, not synthetic demonstrations or desktop/screen captures. No OS screen-recording permission was requested. The only content redaction is the local task path prefix to `$TASK`; raw backups remain outside Git. Event order and timestamps are preserved.

The build, setup and cleanup casts are also included. Playback can be reproduced with `render.py` (Pillow 11.3.0, pyte 0.8.2, av 15.1.0; macOS Menlo font).

## Reproduce on this Mac setup

Environment: macOS 26.6.2 arm64; Colima Docker server 29.5.2, 4 CPUs, approximately 6.2 GB RAM; kind 0.27.0; kubectl 1.32.2; Kubernetes node `kindest/node:v1.32.2` (containerd 2.0.3); Go 1.25.9. Full Go tests use local envtest Kubernetes 1.32.0. All tools/caches are task-local. No existing kubeconfig/context was used.

The scripts assume checkout at `$TASK/k8s4claw`, verified tools in `$TASK/.tools/bin`, Go in `$TASK/.tools/go`, envtest in `$TASK/.tools/envtest/k8s/1.32.0-darwin-arm64`, and the existing `colima` Docker context. They do not install software or alter system permissions. Obtain prerequisites from their official release sources and verify checksums before running; do not run against an existing production context.

From the repository root, with the final named test cluster absent and its private kubeconfig path unused:

```sh
bash docs/demo/lifecycle-fixed-20261013/checks.sh
python3 docs/demo/lifecycle-fixed-20261013/record.py build bash docs/demo/lifecycle-fixed-20261013/build.sh
python3 docs/demo/lifecycle-fixed-20261013/record.py setup bash docs/demo/lifecycle-fixed-20261013/create-cluster.sh
python3 docs/demo/lifecycle-fixed-20261013/record.py scenarios python3 docs/demo/lifecycle-fixed-20261013/scenarios.py
python3 docs/demo/lifecycle-fixed-20261013/record.py cleanup bash docs/demo/lifecycle-fixed-20261013/cleanup.sh
```

The harness creates only `kind-k8s4claw-fixed-final-20261013` resources and `lifecycle-test`. Cleanup deletes the named test cluster. For another run, choose another unique cluster/kubeconfig name consistently; never reuse an existing cluster by guessing its purpose. Local test images/tool caches remain for reproducibility.

## Validation and limits

Full `go test -p 2 -race -coverprofile=... -covermode=atomic ./...` and `go vet ./...` pass, with 66.8% total coverage. Unit regressions exercise stale generation/revision/image, foreign or unready Pods, zero replicas, unhealthy baseline, missing previous-good reference, repeated reconcile, status conflicts, process restart, and interruption between status commit and cleanup. [local-checks.txt](local-checks.txt) records local results.

Remote CI's security scan reports existing Go 1.25.9 and `x/net`/`x/text` vulnerabilities (21 reachable findings). Dependency and Go version upgrades are outside this lifecycle fix; the scan remains enabled and the PR must not be represented as merge-ready. Local golangci-lint 2.8.0 also reports an unchanged PDB integer-conversion warning; remote latest lint does not report that warning. On product commit `7c5e4ea`, remote lint, race tests, coverage gate, build/vet, codegen, DCO and all seven applicable Docker builds passed. `govulncheck` and consequently `ci-gate` failed; the unrelated Hermes Rust build was skipped. See [CI job results](ci-product-commit.json) and [finding summary](ci-vulnerability-summary.txt).

Not verified: real version-to-version application compatibility, live OCI discovery/cron, paid LLM inference, actual Slack/other channel delivery, PVC/data recovery, released image runtime behavior, all other runtime adapters, multi-replica or multi-node failures, and leader-election failover (single operator, leader election disabled). Readiness remains an HTTP/Pod health gate, not an end-to-end application correctness guarantee. Previous-good is the exact image reference; mutable tags are not an immutable content guarantee. IPC startup retry does not implement reconnection after a later established connection loss. Legacy in-flight updates without a saved previous image require an explicit operator decision instead of a guessed rollback.

Additional observed limits: scratch images lack the shell used by the existing runtime preStop hook, and the bad-runtime IPC shutdown socket is unavailable while waiting for connection. `FailedPreStopHook` warnings are preserved; graceful shutdown is not a pass claim. A transient ConfigMap cache mount warning recovered automatically during the final run. Webhooks were disabled for the certificate-free test deployment; webhook admission was not exercised.
