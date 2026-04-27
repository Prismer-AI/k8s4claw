# HermesRS runtime

Container image for the [hermes-agent-rs](https://github.com/willamhou/hermes-agent-rs)
runtime, used by the `runtime: hermesrs` Claw adapter.

## Image contract

The HermesRS adapter (see [internal/runtime/hermesrs.go](../../internal/runtime/hermesrs.go))
expects:

| Property | Value |
|----------|-------|
| Binary path | `/usr/local/bin/hermes` |
| Default command | `gateway run` |
| Listen address | `0.0.0.0:8080` (configurable via `HERMES_GATEWAY_API_SERVER_BIND_ADDR`) |
| Health endpoint | `GET /health` |
| Home directory | `/data` (`HERMES_HOME`) |
| Workspace path | `/data/skills` |
| User / group | `10000:10000` |

## Build

### From upstream Git

```bash
docker build -f runtimes/hermesrs/Dockerfile \
  --build-arg HERMES_REPO=https://github.com/willamhou/hermes-agent-rs.git \
  --build-arg HERMES_REF=main \
  -t ghcr.io/prismer-ai/hermes-agent-rs:latest .
```

### From a vendored copy

Place the hermes-agent-rs source tree at `runtimes/hermesrs/src/` (e.g. via a
git submodule or `cargo vendor`), then:

```bash
docker build -f runtimes/hermesrs/Dockerfile \
  -t hermesrs:dev runtimes/hermesrs/
```

## CI / release

This image is **not yet** built on every push (see `.github/workflows/release.yml`).
It will be added to the release matrix once the upstream repo URL stabilizes.

For now, build and push manually when needed:

```bash
docker build -f runtimes/hermesrs/Dockerfile \
  --build-arg HERMES_REPO=https://github.com/willamhou/hermes-agent-rs.git \
  --build-arg HERMES_REF=v0.1.0 \
  -t ghcr.io/prismer-ai/hermes-agent-rs:v0.1.0 .

docker push ghcr.io/prismer-ai/hermes-agent-rs:v0.1.0
```
