#!/usr/bin/env bash
set -euo pipefail
TASK=$(dirname "$(git rev-parse --show-toplevel)")
export PATH="$TASK/.tools/go/bin:$TASK/.tools/bin:$PATH"
export GOCACHE="$TASK/.state/go-build" GOMODCACHE="$TASK/.state/go-mod" GOTOOLCHAIN=local TMPDIR=/tmp
export KUBEBUILDER_ASSETS="$TASK/.tools/envtest/k8s/1.32.0-darwin-arm64"
export KUBECONFIG="$TASK/.state/unused-kubeconfig"
go test -p 2 -race -coverprofile="$TASK/.state/fix-coverage.out" -covermode=atomic ./...
go vet ./...
make generate manifests
git diff --exit-code -- api/v1alpha1/zz_generated.deepcopy.go config/crd/bases
go tool cover -func="$TASK/.state/fix-coverage.out" | tail -1
