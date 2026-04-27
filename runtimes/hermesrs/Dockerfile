# hermes-agent-rs runtime image for k8s4claw HermesRS adapter.
#
# Build:
#   # 1. From a vendored source tree at runtimes/hermesrs/src:
#   docker build -f runtimes/hermesrs/Dockerfile -t hermesrs:dev runtimes/hermesrs/
#
#   # 2. From upstream Git (recommended for releases):
#   docker build -f runtimes/hermesrs/Dockerfile \
#     --build-arg HERMES_REPO=https://github.com/willamhou/hermes-agent-rs.git \
#     --build-arg HERMES_REF=main \
#     -t ghcr.io/prismer-ai/hermes-agent-rs:latest .
#
# The HermesRS adapter expects:
#   - Binary at /usr/local/bin/hermes
#   - Listens on 0.0.0.0:8080 (HERMES_GATEWAY_API_SERVER_BIND_ADDR)
#   - Exposes /health endpoint
#   - Workspace at /data/skills, home at /data

ARG HERMES_REPO=
ARG HERMES_REF=main

# ---------- Builder ----------
# rust 1.88+ required: hermes-agent-rs deps (home, time) need rustc >= 1.88.
FROM rust:1.88-bookworm AS builder

WORKDIR /workspace

ARG HERMES_REPO
ARG HERMES_REF

# Two source modes:
#   - If HERMES_REPO is set, clone from upstream.
#   - Otherwise, expect source to be present in the build context (./src).
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*

COPY . /workspace/ctx
RUN if [ -n "$HERMES_REPO" ]; then \
      echo "cloning $HERMES_REPO@$HERMES_REF"; \
      git clone --depth 1 --branch "$HERMES_REF" "$HERMES_REPO" /workspace/src; \
    elif [ -d /workspace/ctx/src ]; then \
      echo "using vendored source"; \
      cp -r /workspace/ctx/src /workspace/src; \
    else \
      echo "ERROR: neither HERMES_REPO build-arg nor ./src directory provided" >&2; \
      exit 1; \
    fi

WORKDIR /workspace/src
RUN cargo build --release --bin hermes -p hermes-cli

# ---------- Runtime ----------
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 10000 hermes \
    && useradd -u 10000 -g 10000 -m -d /data hermes

COPY --from=builder /workspace/src/target/release/hermes /usr/local/bin/hermes

USER 10000:10000
WORKDIR /data

EXPOSE 8080
ENV HERMES_HOME=/data \
    HERMES_GATEWAY_API_SERVER_BIND_ADDR=0.0.0.0:8080 \
    HERMES_GATEWAY_API_SERVER_MODEL_NAME=hermes-agent-rs \
    RUST_LOG=info

ENTRYPOINT ["/usr/local/bin/hermes"]
CMD ["gateway"]
