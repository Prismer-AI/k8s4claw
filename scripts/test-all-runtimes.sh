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

    # HermesClaw needs fixed mount paths
    if [ "$runtime" = "hermesclaw" ]; then
        spec="$spec
  persistence:
    session:
      enabled: true
      size: 1Gi
      mountPath: /opt/data
    workspace:
      enabled: true
      size: 1Gi
      mountPath: /opt/data/skills"
    fi

    cat <<EOF | kubectl apply -f - &>/dev/null
apiVersion: claw.prismer.ai/v1alpha1
kind: Claw
metadata:
  name: $name
  namespace: $ns
spec:
  $spec
EOF

    # Wait for reconciliation
    sleep 3

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

echo -e "${YELLOW}▸ Installing CRDs...${NC}"
kubectl apply -f config/crd/bases/ &>/dev/null

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
test_runtime     "nanoclaw"    19000  "false"
test_runtime     "zeroclaw"    3000   "false"
test_runtime     "picoclaw"    8080   "false"
test_runtime     "ironclaw"    3001   "true"
test_runtime     "hermesclaw"  8642   "true"

# Summary
echo ""
echo "══════════════════════════════════════════════════"
echo -e "  Results: ${GREEN}${PASS} passed${NC}, ${RED}${FAIL} failed${NC}, 6 total"
if [ "$FAIL" -gt 0 ]; then
    echo -e "  Failed runtimes:${ERRORS}"
fi
echo "══════════════════════════════════════════════════"

exit "$FAIL"
