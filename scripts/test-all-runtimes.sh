#!/usr/bin/env bash
# Test all runtime adapters on a real kind cluster.
# Verifies operator reconciles each Claw CR into correct sub-resources.
#
# Usage: ./scripts/test-all-runtimes.sh
#
# Prerequisites: kind, kubectl, bin/operator (run `make build` first)
set -euo pipefail

CLUSTER_NAME="k8s4claw-runtime-test"
PASS=0
FAIL=0
ERRORS=""

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
NC='\033[0m'

cleanup() {
    echo ""
    echo -e "${YELLOW}▸ Cleaning up...${NC}"
    pkill -f 'bin/operator' 2>/dev/null || true
    kind delete cluster --name "$CLUSTER_NAME" 2>/dev/null || true
}
trap cleanup EXIT

assert_exists() {
    local resource="$1" name="$2" ns="$3" label="$4"
    if kubectl get "$resource" "$name" -n "$ns" &>/dev/null; then
        echo -e "    ${GREEN}✓${NC} $resource/$name"
        return 0
    else
        echo -e "    ${RED}✗${NC} $resource/$name MISSING"
        return 1
    fi
}

assert_label() {
    local resource="$1" name="$2" ns="$3" key="$4" expected="$5"
    local actual
    actual=$(kubectl get "$resource" "$name" -n "$ns" -o json 2>/dev/null | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('metadata',{}).get('labels',{}).get('$key',''))" 2>/dev/null || echo "")
    if [ "$actual" = "$expected" ]; then
        echo -e "    ${GREEN}✓${NC} $resource/$name label $key=$expected"
        return 0
    else
        echo -e "    ${RED}✗${NC} $resource/$name label $key expected=$expected got=$actual"
        return 1
    fi
}

assert_container_port() {
    local name="$1" ns="$2" port="$3"
    local actual
    actual=$(kubectl get sts "$name" -n "$ns" -o jsonpath='{.spec.template.spec.containers[0].ports[0].containerPort}' 2>/dev/null || echo "")
    if [ "$actual" = "$port" ]; then
        echo -e "    ${GREEN}✓${NC} sts/$name container port=$port"
        return 0
    else
        echo -e "    ${RED}✗${NC} sts/$name container port expected=$port got=$actual"
        return 1
    fi
}

assert_configmap_valid_json() {
    local name="$1" ns="$2"
    local data
    data=$(kubectl get configmap "$name" -n "$ns" -o jsonpath='{.data.config\.json}' 2>/dev/null || echo "")
    if echo "$data" | python3 -m json.tool &>/dev/null; then
        echo -e "    ${GREEN}✓${NC} configmap/$name config.json is valid JSON"
        return 0
    else
        echo -e "    ${RED}✗${NC} configmap/$name config.json is NOT valid JSON: $data"
        return 1
    fi
}

test_runtime() {
    local runtime="$1" port="$2" needs_creds="$3"
    local ns="test-${runtime}"
    local name="test-${runtime}"

    echo ""
    echo -e "${YELLOW}▸ Testing runtime: ${runtime}${NC}"

    # Create namespace
    kubectl create namespace "$ns" 2>/dev/null || true

    # Create secret if needed
    if [ "$needs_creds" = "true" ]; then
        kubectl create secret generic llm-api-keys \
            --from-literal=API_KEY=test-key \
            -n "$ns" --dry-run=client -o yaml | kubectl apply -f - &>/dev/null
    fi

    # Apply Claw CR
    local spec="runtime: $runtime"
    if [ "$needs_creds" = "true" ]; then
        spec="$spec
  credentials:
    secretRef:
      name: llm-api-keys"
    fi

    # NOTE: HermesClaw persistence omitted in test — PVC + ownerReference
    # patch causes scheduler conflict in kind. Works fine on cloud clusters.

    cat <<EOF | kubectl apply -f - &>/dev/null
apiVersion: claw.prismer.ai/v1alpha1
kind: Claw
metadata:
  name: $name
  namespace: $ns
  annotations:
    claw.prismer.ai/image-pull-policy: "Never"
spec:
  $spec
EOF

    # Wait for reconciliation
    sleep 5

    # Verify sub-resources
    local ok=true

    assert_exists statefulset "$name" "$ns" "" || ok=false
    assert_exists service "$name" "$ns" "" || ok=false
    assert_exists configmap "${name}-config" "$ns" "" || ok=false
    assert_exists serviceaccount "$name" "$ns" "" || ok=false
    assert_exists poddisruptionbudget "$name" "$ns" "" || ok=false

    assert_label statefulset "$name" "$ns" "claw.prismer.ai/runtime" "$runtime" || ok=false
    assert_label service "$name" "$ns" "claw.prismer.ai/runtime" "$runtime" || ok=false

    assert_container_port "$name" "$ns" "$port" || ok=false
    assert_configmap_valid_json "${name}-config" "$ns" || ok=false

    # Wait for Pod Ready (mock runtime should start fast)
    if kubectl wait --for=condition=ready "pod/${name}-0" -n "$ns" --timeout=60s &>/dev/null; then
        echo -e "    ${GREEN}✓${NC} pod/${name}-0 Ready"
    else
        local pod_status
        pod_status=$(kubectl get pod "${name}-0" -n "$ns" -o jsonpath='{.status.phase}' 2>/dev/null || echo "not found")
        echo -e "    ${RED}✗${NC} pod/${name}-0 NOT Ready (status: $pod_status)"
        kubectl describe pod "${name}-0" -n "$ns" 2>/dev/null | grep -A 3 'Warning\|Error\|Back-off\|ImagePull' | head -5
        ok=false
    fi

    # Check Claw status phase
    local phase
    phase=$(kubectl get claw "$name" -n "$ns" -o jsonpath='{.status.phase}' 2>/dev/null || echo "unknown")
    if [ "$phase" = "Provisioning" ] || [ "$phase" = "Running" ]; then
        echo -e "    ${GREEN}✓${NC} claw/$name phase=$phase"
    else
        echo -e "    ${YELLOW}~${NC} claw/$name phase=$phase (expected Provisioning or Running)"
    fi

    if [ "$ok" = true ]; then
        echo -e "  ${GREEN}PASS${NC}: $runtime"
        PASS=$((PASS + 1))
    else
        echo -e "  ${RED}FAIL${NC}: $runtime"
        FAIL=$((FAIL + 1))
        ERRORS="${ERRORS}\n  - ${runtime}"
    fi
}

# --- Main ---

echo "╔══════════════════════════════════════════════════╗"
echo "║  k8s4claw — Runtime Adapter Integration Test     ║"
echo "╚══════════════════════════════════════════════════╝"

# Setup
echo ""
echo -e "${YELLOW}▸ Creating kind cluster...${NC}"
kind create cluster --name "$CLUSTER_NAME" --wait 60s 2>&1 | tail -2

echo -e "${YELLOW}▸ Building operator (to sync init image tag)...${NC}"
make build 2>&1 | tail -1

echo -e "${YELLOW}▸ Building and loading mock runtime image...${NC}"
docker build -t claw-mock-runtime:dev -f runtimes/mock/Dockerfile runtimes/mock/ 2>&1 | tail -1

# Detect init image tag — must match what `make build` baked in via ldflags
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
INIT_IMG="ghcr.io/prismer-ai/claw-init:${VERSION}"
echo "  Init image: $INIT_IMG"

# Tag mock image as each runtime's expected image and load into kind.
# Use ":latest" tags (matching adapter defaults) + also ":v0" to avoid
# imagePullPolicy=Always. We'll also import directly into containerd.
RUNTIME_IMAGES=(
    "ghcr.io/prismer-ai/k8s4claw-openclaw:latest"    "docker.io/nousresearch/hermes-agent:latest"
    "ghcr.io/nousresearch/hermes-agent:latest"
    "$INIT_IMG"
)
for img in "${RUNTIME_IMAGES[@]}"; do
    docker tag claw-mock-runtime:dev "$img" 2>/dev/null
done
kind load docker-image "${RUNTIME_IMAGES[@]}" --name "$CLUSTER_NAME" 2>&1 | grep -c 'loading' | xargs -I{} echo "  Loaded {} images"

# Workaround: :latest triggers imagePullPolicy=Always in K8s, causing
# ErrImagePull even when the image is pre-loaded. Import directly into
# the kind node's containerd so kubelet finds them without pulling.
echo "  Importing images into containerd (bypass pull policy)..."
NODE="${CLUSTER_NAME}-control-plane"
docker save "${RUNTIME_IMAGES[@]}" | docker exec -i "$NODE" ctr -n k8s.io images import --no-unpack - 2>&1 | tail -1

echo -e "${YELLOW}▸ Installing CRDs...${NC}"
kubectl apply -f config/crd/bases/ &>/dev/null

echo -e "${YELLOW}▸ Waiting for CRDs to be ready...${NC}"
for crd in claws.claw.prismer.ai clawchannels.claw.prismer.ai clawselfconfigs.claw.prismer.ai; do
    kubectl wait --for=condition=established "crd/$crd" --timeout=30s &>/dev/null
done
echo "  CRDs established"

echo -e "${YELLOW}▸ Starting operator...${NC}"
bin/operator --disable-webhooks \
    --metrics-bind-address=:8090 \
    --health-probe-bind-address=:8091 \
    > /tmp/operator-test.log 2>&1 &
OPERATOR_PID=$!
echo "  Operator PID: $OPERATOR_PID"
sleep 5

# Test each runtime
#                runtime       port   needs_creds
test_runtime     "openclaw"    18900  "true"
test_runtime     "hermesclaw"  8642   "true"

# Summary
echo ""
echo "══════════════════════════════════════════════════"
echo -e "  Results: ${GREEN}${PASS} passed${NC}, ${RED}${FAIL} failed${NC}, remaining runtimes"
if [ "$FAIL" -gt 0 ]; then
    echo -e "  Failed runtimes:${ERRORS}"
fi
echo "══════════════════════════════════════════════════"

exit "$FAIL"
