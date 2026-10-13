# Real kind lifecycle characterization — 2026-10-13

**The unmodified startup path fails. This is failure evidence, not a passing product demo.**
Tested source: [`513849e404e2d494e892850462b519af44fc2424`](https://github.com/Prismer-AI/k8s4claw/commit/513849e404e2d494e892850462b519af44fc2424), also confirmed as remote `main` during this run.
No product source was changed. Only this evidence directory and the root README are proposed.

## Results

| Scenario | Observed result | Evidence |
|---|---|---|
| Stock operator + real init + real OpenClaw | **FAIL**: init attempts `/data/config` on a read-only root filesystem; runtime never starts | [snapshot](03-stock-startup.json), [transcript](03-scenarios.txt) |
| Real OpenClaw with diagnostic init environment | **PASS, diagnostic only**: Pod Ready, runtime log has `mock:false`; no inference request | [snapshot](04-diagnostic-startup.json) |
| Normal image rollout `0.0.1 → 0.0.2` | **PASS, diagnostic only**: new Pod UID, intended image, observed generation and matching StatefulSet/Pod revisions | [snapshot](05-normal-upgrade.json) |
| Bad image `0.0.3 → previous-good 0.0.2` | **FAIL**: controller removes target annotation, StatefulSet targets `latest`; broken `0.0.3` Pod remains unready | [before](06-bad-version-active.json), [after](07-bad-version-rollback.json) |
| Operator interrupted during HealthCheck | **FAIL overall; process resumption works**: new operator UID resumes persisted timeout, but rollback status write conflicts after phase removal; count/history lose the second rollback, workload remains broken | [before](08-before-operator-interruption.json), [after](09-after-operator-recovery.json), [later follow-up](11-recovery-followup.json), [log](04-resumed-scenarios.txt) |
| IPC socket contract, independent real Pod | **FAIL**: `IPC_SOCKET_PATH=/var/run/claw` points at the mounted directory; server exits trying to remove it | [log](04-resumed-scenarios.txt) |
| Existing runtime / IPC / registry / autoupdate unit tests | **PASS** with `TMPDIR=/tmp` | [log](10-unit-tests.txt) |

Raw machine-readable verdicts are in [results.json](results.json). The recovery verdict deliberately requires durable rollback accounting, not just an operator restart or an event saying “rollback.” The read-only follow-up at approximately four minutes after the conflict still shows `rollbackCount: 1` and only the first history entry, despite a second `AutoUpdateRollback` event.

## Recordings

These are **actual child-PTY recordings**, not scripted/fabricated output or captures of the personal desktop. MP4s are terminal replays rendered from the same captured events at **4× speed**, labelled in every frame. Source timing and full text remain available in asciicast v2. There is no audio.

| Recording | Video | Original terminal event stream | Readable transcript |
|---|---|---|---|
| Stock startup, diagnostic startup, normal rollout, bad-version rollback | [MP4, 1.2 MB / 26.5 s](03-scenarios.mp4) | [cast](03-scenarios.cast) | [text](03-scenarios.txt) |
| Operator interruption/recovery and IPC failure | [MP4, 0.67 MB / 23.5 s](04-resumed-scenarios.mp4) | [cast](04-resumed-scenarios.cast) | [text](04-resumed-scenarios.txt) |
| Initial cluster download failure | — | [cast](01-cluster.cast) | [text](01-cluster.txt) |
| Successful cluster retry | — | [cast](01b-cluster-retry.cast) | [text](01b-cluster-retry.txt) |
| Real-source builds | — | [cast](02-build.cast) | [text](02-build.txt) |
| Later recovery follow-up | — | [cast](11-followup.cast) | [text](11-followup.txt) |
| Cluster cleanup | — | [cast](12-cleanup.cast) | [text](12-cleanup.txt) |

Download an MP4 if GitHub does not play the repository file inline. With asciinema already available, replay using `asciinema play 03-scenarios.cast`. No third-party video hosting is required.

The Mac executor disconnected during the first scenario recording after the explicit fixture-recovery Pod deletion. That cast has no normal recorder-exit footer. Its captured events are retained unchanged except path redaction; the next recording explicitly resumes the remaining scenarios. The executor disconnected again while the continuation ran, but that recording completed and ends with child exit code 0. Do not interpret the gap between recordings as seamless footage.

## Environment and isolation

- macOS 26.6.2, ARM64. Docker client 29.8.2; Colima server 29.5.2, Ubuntu 24.04.4 LTS, aarch64, 4 CPUs, 6,197,448,704 bytes RAM.
- kind v0.27.0, kubectl v1.32.2, Kubernetes v1.32.2, containerd 2.0.3; node image `kindest/node:v1.32.2`, manifest digest `sha256:142f543559cc55d64e1ab9341df08e5ced84bd2e893736da8f51320f26f5950b`.
- Go 1.25.9 darwin/arm64, cross-compilation with `CGO_ENABLED=0 GOOS=linux GOARCH=arm64`.
- Dedicated cluster `k8s4claw-lifecycle-20261013`; namespace `lifecycle-test`; all kubectl calls use an explicit `kind-k8s4claw-lifecycle-20261013` context and a task-only kubeconfig outside this repository. No existing Kubernetes configuration was read or used. No cloud resources were provisioned.
- Operator runs in this disposable cluster with test-only cluster-admin RBAC, webhooks disabled and leader election disabled. This does **not** validate production RBAC, webhook admission, or HA deployment.
- Only an invalid placeholder key was supplied. `ANTHROPIC_BASE_URL=http://127.0.0.1:9`; no LLM requests were sent, no production credentials were loaded, and no paid inference was used.
- The dedicated cluster was deleted successfully after evidence collection; see cleanup recording. Local task tools, build caches and test images remain for reproduction.

The first node pull failed through the pre-existing Docker mirror with HTTP 502; an explicit official-registry attempt failed with EOF. A subsequent same-image pull succeeded. No Docker mirror/proxy/security configuration was changed.

## What the diagnostic fixtures do — and do not prove

The actual repository Go binaries are packaged in minimal `scratch` test images ([Dockerfile](images.Dockerfile)); these are not the published images or the repository's distroless release packaging. Build hashes and loaded image IDs are preserved in recordings. Product code, init arguments, security contexts and reconciliation logic are unchanged. Operator image defaults are selected through existing linker variables.

The diagnostic init image sets `CONFIG_SOURCE=/etc/claw`, `CONFIG_DEST=/tmp/config`, and `WORKSPACE_PATH=/tmp/workspace`. This allows the real init binary to finish despite the original argument/environment and writable-path mismatch. It is **not a complete config/workspace integration fix**. The original failure is captured first.

Tags `0.0.1`, `0.0.2` and `latest` deliberately identify the same real OpenClaw binary/image. This tests a real image-reference rollout, Pod replacement, and controller convergence; it does not establish compatibility between different product releases. Tag `0.0.3` contains the binary at the wrong path, producing a genuine OCI container-start failure. No mock runtime is used.

For rollback and interruption tests, the harness writes the real controller's `HealthCheck` annotations and auto-update status, then enables auto-update after the bad Pod is unready. **Registry tag discovery and cron-triggered upgrade initiation are not covered.** This avoids modifying or publishing to the upstream image registry. The seeded state is explicit in the transcript and script.

The operator Deployment used RollingUpdate during the recorded stock→diagnostic switch, briefly overlapping two non-leader-elected operators with different init defaults. This explains transient StatefulSet-generation churn during setup; it is not presented as a product defect. The recovery test later had one operator replica.

## Findings and proposed repair boundaries

1. Align `cmd/claw-init/main.go` with `internal/runtime/pod_builder.go`: flag/env contract, source config mount, writable destination and workspace sharing. Current CLI args are not parsed by the init binary.
2. Align `internal/controller/claw_ipcbus.go` with `cmd/ipcbus/main.go`: the socket must be a file path such as `/var/run/claw/bus.sock`, not the volume root.
3. Persist and restore the exact previous-good image in `internal/controller/autoupdate_controller.go`. Current rollback deletes `target-image`, which returns the adapter to `latest`; it also does not recover the stuck failed StatefulSet Pod in this run.
4. Make phase transitions and rollback accounting retry-safe across status conflicts and restarts. This run observed phase removal followed by a status conflict, permanently losing the second rollback count/history entry.
5. The same controller's success predicate checks `UpdatedReplicas` and `ReadyReplicas` only. Source review confirms it does not verify the target image, `observedGeneration`, or revision convergence. A false-success race was **not dynamically demonstrated** in this run; the test's independent rollout assertion does check these properties.

No fixes to these product behaviors are included in this evidence PR.

## Reproduce

Run from a fresh checkout of the tested commit on an ARM64 Mac with a usable local Docker engine. The scripts intentionally expect a task layout of `TASK/k8s4claw`, `TASK/.tools`, and `TASK/.state`; they do not install globally. `create-cluster.sh` refuses an existing task kubeconfig, and `scenarios.py` verifies the context and exact node name before mutations.

Prepare official kind v0.27.0, kubectl v1.32.2, and Go 1.25.9 in `../.tools/bin` and `../.tools/go`. Download only from their official release endpoints and verify checksums before execution. Full verified values for this run are recorded in [tool-checksums.txt](tool-checksums.txt).

```bash
mkdir -p ../.state
python3 docs/demo/lifecycle-20261013/record.py /tmp/setup bash docs/demo/lifecycle-20261013/create-cluster.sh
python3 docs/demo/lifecycle-20261013/record.py /tmp/build bash docs/demo/lifecycle-20261013/build.sh
python3 docs/demo/lifecycle-20261013/record.py /tmp/scenarios python3 -u docs/demo/lifecycle-20261013/scenarios.py
# Inspect results/snapshots; explicitly remove only this test cluster afterward:
DOCKER_CONTEXT=colima KUBECONFIG=../.state/kind-lifecycle.config \
  ../.tools/bin/kind delete cluster --name k8s4claw-lifecycle-20261013
```

The scripts write snapshots into this evidence directory; run in a separate checkout or preserve the recorded originals first. `resume-after-disconnect.py` is specific to this interrupted run; use `scenarios.py` for a fresh run. The scenario process's exit code indicates harness completion; product PASS/FAIL verdicts are in `results.json` and the transcript.

Related tests actually run:

```bash
PATH="$PWD/../.tools/go/bin:$PATH" \
GOCACHE="$PWD/../.state/go-build" GOMODCACHE="$PWD/../.state/go-mod" \
GOTOOLCHAIN=local TMPDIR=/tmp \
go test ./internal/runtime ./internal/ipcbus ./internal/registry ./internal/controller/autoupdate
```

The initial run under the long macOS default temporary directory failed two IPC UDS tests with `connect: invalid argument`; rerunning with `/tmp` passed. Full envtest, lint, code generation, channel delivery, persistence restoration, inference and other runtimes were not run. No product Go files were changed.

## Publication review

Only test terminal output and selected object status were captured. Recordings and snapshots replace the absolute local task path with `$TASK`; raw originals remain outside Git. No kubeconfig, certificate/key data, real API key, private desktop, inbox or environment dump is included. Both MP4s decode completely; representative frames were visually inspected for content/readability and match the captured test terminal. Videos retain the explicit diagnostic labels and failures. [Video metadata](video-validation.json) and [artifact hashes](SHA256SUMS) accompany the deliverables.

Video conversion uses `render.py` with task-local Pillow 11.3.0, pyte 0.8.2, wcwidth 0.9.2 and PyAV 15.1.0. A first native Swift export attempt did not complete during executor instability; no empty/failed video is included. No screen-recording or other OS permission was accepted.
