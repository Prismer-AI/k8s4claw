#!/usr/bin/env bash
set -euo pipefail
ROOT=$(git rev-parse --show-toplevel)
TASK=$(dirname "$ROOT")
export PATH="$TASK/.tools/bin:$PATH"
export DOCKER_CONTEXT=colima
export KUBECONFIG="$TASK/.state/kind-lifecycle.config"
CLUSTER=k8s4claw-lifecycle-20261013
test ! -e "$KUBECONFIG" || { echo 'Refusing to reuse kubeconfig'; exit 1; }
echo 'Actual isolated-kind setup; no existing Kubernetes configuration is used.'
date -u
git rev-parse HEAD
kind version
kubectl version --client
docker info --format 'Docker {{.ServerVersion}} {{.Architecture}} CPU={{.NCPU}} RAM={{.MemTotal}}'
kind create cluster --name "$CLUSTER" --image kindest/node:v1.32.2 --kubeconfig "$KUBECONFIG" --wait 120s
kubectl --context "kind-$CLUSTER" get nodes -o wide
echo 'SETUP COMPLETE'
