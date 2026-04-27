# HermesClaw runtime

Bridge to [Nous Research's hermes-agent](https://github.com/NousResearch/hermes-agent),
the upstream Python Hermes Agent. Used by the `runtime: hermesclaw` Claw adapter.

## Status

This runtime **does not ship a prebuilt image**. The HermesClaw adapter
(see [internal/runtime/hermesclaw.go](../../internal/runtime/hermesclaw.go))
points at `ghcr.io/nousresearch/hermes-agent:latest`, but as of this writing
NousResearch has not published an image to GHCR. Users who want to deploy
HermesClaw must build the image themselves and either push it under that
default tag or override `spec.image` on the Claw CR.

## Image contract

The adapter expects the upstream Hermes Agent container to:

| Property | Value |
| --- | --- |
| Listen address | `0.0.0.0:8642` (set via `API_SERVER_HOST` + `API_SERVER_PORT`) |
| Health endpoint | `GET /health` |
| Home directory | `/opt/data` (`HERMES_HOME`) |
| Workspace path | `/opt/data/skills` |
| API server enabled | `API_SERVER_ENABLED=true` |
| Default model name | `hermes-agent` (`API_SERVER_MODEL_NAME`) |

These are exactly the defaults of [NousResearch/hermes-agent's
Dockerfile](https://github.com/NousResearch/hermes-agent/blob/main/Dockerfile),
so the official upstream image (when one exists) drops in without changes.

## Build from upstream

```bash
git clone https://github.com/NousResearch/hermes-agent.git
cd hermes-agent
docker build -t ghcr.io/nousresearch/hermes-agent:latest .
# WARNING: ~30 min build, ~3GB image (Python + Node + Playwright Chromium).
```

To use a private registry instead:

```bash
docker tag ghcr.io/nousresearch/hermes-agent:latest your-registry/hermes-agent:v1
docker push your-registry/hermes-agent:v1
```

Then set the image on your Claw CR:

```yaml
apiVersion: claw.prismer.ai/v1alpha1
kind: Claw
metadata:
  name: my-hermes
spec:
  runtime: hermesclaw
  image: your-registry/hermes-agent:v1   # override adapter default
  credentials:
    secretRef:
      name: llm-keys
```

## Alternative: HermesRS

For a faster, smaller, single-binary alternative, use the `hermesrs` runtime
which wraps [hermes-agent-rs](https://github.com/willamhou/hermes-agent-rs) —
the Rust port. Build is ~5 min, image is ~125MB, with managed-agents API
support out of the box. See [runtimes/hermesrs/README.md](../hermesrs/README.md).
