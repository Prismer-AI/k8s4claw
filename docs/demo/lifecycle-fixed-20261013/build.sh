#!/usr/bin/env bash
set -euo pipefail
ROOT=$(git rev-parse --show-toplevel)
TASK=$(dirname "$ROOT")
export PATH="$TASK/.tools/go/bin:$TASK/.tools/bin:$PATH"
export GOCACHE="$TASK/.state/go-build" GOMODCACHE="$TASK/.state/go-mod" GOTOOLCHAIN=local
export DOCKER_CONTEXT=colima
BUILD="$TASK/.state/images-fixed"
mkdir -p "$BUILD"
echo 'Build real source, fixed product binaries and transparent test packaging'
git rev-parse HEAD
go version
export CGO_ENABLED=0 GOOS=linux GOARCH=arm64
go build -ldflags '-X github.com/Prismer-AI/k8s4claw/internal/runtime.InitContainerImage=claw-init-fixed:test -X github.com/Prismer-AI/k8s4claw/internal/controller.IPCBusImage=claw-ipcbus-fixed:test' -o "$BUILD/operator" ./cmd/operator/
go build -o "$BUILD/claw-init" ./cmd/claw-init/
go build -o "$BUILD/claw-ipcbus" ./cmd/ipcbus/
(cd runtimes/openclaw && go build -o "$BUILD/claw-entrypoint" .)
cp docs/demo/lifecycle-fixed-20261013/probe.go.txt "$BUILD/probe.go"
go build -o "$BUILD/probe" "$BUILD/probe.go"
for pair in 'operator claw-operator-fixed:test' 'init claw-init-fixed:test' 'ipcbus claw-ipcbus-fixed:test' 'channel claw-channel-probe:test' 'runtime ghcr.io/prismer-ai/k8s4claw-openclaw:0.0.1' 'bad-runtime ghcr.io/prismer-ai/k8s4claw-openclaw:0.0.3'; do
  read -r target tag <<< "$pair"
  docker build --target "$target" -t "$tag" -f docs/demo/lifecycle-fixed-20261013/images.Dockerfile "$BUILD"
done
docker tag ghcr.io/prismer-ai/k8s4claw-openclaw:0.0.1 ghcr.io/prismer-ai/k8s4claw-openclaw:0.0.2
docker tag ghcr.io/prismer-ai/k8s4claw-openclaw:0.0.1 ghcr.io/prismer-ai/k8s4claw-openclaw:latest
shasum -a 256 "$BUILD"/*
