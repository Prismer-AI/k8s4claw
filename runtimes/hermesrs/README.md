# HermesRS runtime

Container image for the [hermes-agent-rs](https://github.com/willamhou/hermes-agent-rs)
runtime, used by the `runtime: hermesrs` Claw adapter.

## Image contract

The HermesRS adapter (see [internal/runtime/hermesrs.go](../../internal/runtime/hermesrs.go))
expects:

| Property | Value |
| --- | --- |
| Binary path | `/usr/local/bin/hermes` |
| Default command | `gateway` |
| Listen address | `0.0.0.0:8080` (set in `config.yaml` under `gateway.api_server.bind_addr`) |
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

## Configuration

The binary requires a YAML config file at `$HERMES_HOME/config.yaml`
(default: `/data/config.yaml`). Pure environment variables are NOT sufficient
to enable the gateway; the YAML must explicitly enable `gateway.api_server`.

A minimal example is at [config.example.yaml](config.example.yaml). To run
locally with a Kimi-via-new-api proxy:

```bash
docker run -d --name hermesrs \
  -e OPENAI_API_KEY="$YOUR_KEY" \
  -v $(pwd)/runtimes/hermesrs/config.example.yaml:/data/config.yaml:ro \
  -p 8080:8080 \
  ghcr.io/prismer-ai/hermes-agent-rs:latest

curl http://localhost:8080/health
curl -X POST http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"openai/claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}]}'
```

When deployed via the HermesRS Claw adapter, the operator generates a ConfigMap
from `spec.config` and mounts it at `/data/config.yaml`.

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
