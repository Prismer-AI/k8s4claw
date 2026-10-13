#!/usr/bin/env bash
set -euo pipefail
export DOCKER_CONTEXT=colima
export KUBECONFIG="$PWD/../.state/kind-fixed.config"
echo 'Delete only the previous isolated test cluster: namespace teardown was blocked by a Claw finalizer after its co-located operator was deleted.'
../.tools/bin/kind delete cluster --name k8s4claw-fixed-20261013
bash docs/demo/lifecycle-fixed-20261013/create-cluster.sh
