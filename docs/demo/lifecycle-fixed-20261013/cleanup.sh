#!/usr/bin/env bash
set -euo pipefail
TASK=$(dirname "$(git rev-parse --show-toplevel)")
export DOCKER_CONTEXT=colima KUBECONFIG="$TASK/.state/kind-fixed-final.config"
echo 'Delete only the final isolated test cluster; no other context is accessed.'
"$TASK/.tools/bin/kind" delete cluster --name k8s4claw-fixed-final-20261013
if docker --context colima ps -a --format '{{.Names}}' | rg '^k8s4claw-fixed(-final)?-20261013-control-plane$'; then
  echo 'Unexpected test node remains'; exit 1
fi
echo 'PASS both dedicated test node containers are absent'
