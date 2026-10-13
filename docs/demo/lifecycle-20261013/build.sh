#!/usr/bin/env bash
set -euo pipefail
ROOT=$(git rev-parse --show-toplevel)
TASK=$(dirname "$ROOT")
export PATH="$TASK/.tools/go/bin:$TASK/.tools/bin:$PATH"
export GOCACHE="$TASK/.state/go-build" GOMODCACHE="$TASK/.state/go-mod" GOTOOLCHAIN=local
export DOCKER_CONTEXT=colima
BUILD="$TASK/.state/images"
mkdir -p "$BUILD"
echo 'Build real source, stock and explicitly diagnostic init variants'
git rev-parse HEAD
go version
export CGO_ENABLED=0 GOOS=linux GOARCH=arm64
go build -ldflags '-X github.com/Prismer-AI/k8s4claw/internal/runtime.InitContainerImage=claw-init-test:stock -X github.com/Prismer-AI/k8s4claw/internal/controller.IPCBusImage=claw-ipcbus-test:stock' -o "$BUILD/operator" ./cmd/operator/
go build -ldflags '-X github.com/Prismer-AI/k8s4claw/internal/runtime.InitContainerImage=claw-init-test:diagnostic -X github.com/Prismer-AI/k8s4claw/internal/controller.IPCBusImage=claw-ipcbus-test:stock' -o "$BUILD/operator-diagnostic" ./cmd/operator/
go build -o "$BUILD/claw-init" ./cmd/claw-init/
go build -o "$BUILD/claw-ipcbus" ./cmd/ipcbus/
(cd runtimes/openclaw && go build -o "$BUILD/claw-entrypoint" .)
for pair in 'operator-stock claw-operator-test:stock' 'operator-diagnostic claw-operator-test:diagnostic' 'init-stock claw-init-test:stock' 'init-diagnostic claw-init-test:diagnostic' 'ipcbus claw-ipcbus-test:stock' 'runtime ghcr.io/prismer-ai/k8s4claw-openclaw:0.0.1' 'bad-runtime ghcr.io/prismer-ai/k8s4claw-openclaw:0.0.3'; do
  read -r target tag <<< "$pair"
  docker build --target "$target" -t "$tag" -f docs/demo/lifecycle-20261013/images.Dockerfile "$BUILD"
done
docker tag ghcr.io/prismer-ai/k8s4claw-openclaw:0.0.1 ghcr.io/prismer-ai/k8s4claw-openclaw:0.0.2
docker tag ghcr.io/prismer-ai/k8s4claw-openclaw:0.0.1 ghcr.io/prismer-ai/k8s4claw-openclaw:latest
shasum -a 256 "$BUILD"/*
