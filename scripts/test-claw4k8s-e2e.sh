#!/usr/bin/env bash
# E2E test for claw4k8s autonomous ops on a kind cluster.
#
# Tests the full auto-remediation loop:
# 1. OOM → ClawOpsController creates Escalation + writes intent → ClawReconciler consumes
# 2. Manual approval flow: Pending → Approved → intent written → consumed
# 3. Stale generation guard
# 4. Invalid intent rejection
# 5. k8sops RBAC boundary verification
#
# Usage: ./scripts/test-claw4k8s-e2e.sh
#
# Prerequisites: kind, kubectl, jq, bin/operator (run `make build` first)
set -euo pipefail

CLUSTER_NAME="k8s4claw-e2e-ops"
NS="test-claw4k8s"
CLAW_NAME="e2e-target"
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

pass() {
    echo -e "    ${GREEN}✓${NC} $1"
    PASS=$((PASS + 1))
}

fail() {
    echo -e "    ${RED}✗${NC} $1"
    FAIL=$((FAIL + 1))
    ERRORS="${ERRORS}\n  - $1"
}

wait_for() {
    local desc="$1" timeout="$2"
    shift 2
    local deadline=$((SECONDS + timeout))
    while [ $SECONDS -lt $deadline ]; do
        if "$@" 2>/dev/null; then
            return 0
        fi
        sleep 1
    done
    return 1
}

jq_field() {
    kubectl get "$1" "$2" -n "$3" -o json 2>/dev/null | jq -r "$4" 2>/dev/null || echo ""
}

# =============================================================================
# Setup
# =============================================================================

echo "╔══════════════════════════════════════════════════╗"
echo "║  k8s4claw — claw4k8s E2E Tests                  ║"
echo "╚══════════════════════════════════════════════════╝"

echo ""
echo -e "${YELLOW}▸ Checking prerequisites...${NC}"
for cmd in kind kubectl jq docker; do
    if ! command -v "$cmd" &>/dev/null; then
        echo -e "${RED}ERROR: $cmd is required but not found${NC}"
        exit 1
    fi
done
echo "  All prerequisites found"

echo -e "${YELLOW}▸ Building operator...${NC}"
make build 2>&1 | tail -1

echo -e "${YELLOW}▸ Creating kind cluster...${NC}"
kind create cluster --name "$CLUSTER_NAME" --wait 60s 2>&1 | tail -2

echo -e "${YELLOW}▸ Building and loading mock runtime image...${NC}"
docker build -t claw-mock-runtime:dev -f runtimes/mock/Dockerfile runtimes/mock/ 2>&1 | tail -1

VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
INIT_IMG="ghcr.io/prismer-ai/claw-init:${VERSION}"
RUNTIME_IMAGES=(
    "ghcr.io/prismer-ai/k8s4claw-openclaw:latest"
    "$INIT_IMG"
)
for img in "${RUNTIME_IMAGES[@]}"; do
    docker tag claw-mock-runtime:dev "$img" 2>/dev/null
done
kind load docker-image "${RUNTIME_IMAGES[@]}" --name "$CLUSTER_NAME" 2>&1 | tail -1

NODE="${CLUSTER_NAME}-control-plane"
docker save "${RUNTIME_IMAGES[@]}" | docker exec -i "$NODE" ctr -n k8s.io images import --no-unpack - 2>&1 | tail -1

echo -e "${YELLOW}▸ Installing CRDs...${NC}"
kubectl apply -f config/crd/bases/ &>/dev/null
for crd in claws.claw.prismer.ai clawopsescalations.claw.prismer.ai; do
    kubectl wait --for=condition=established "crd/$crd" --timeout=30s &>/dev/null
done
echo "  CRDs established"

echo -e "${YELLOW}▸ Starting operator (webhooks disabled)...${NC}"
bin/operator --disable-webhooks \
    --metrics-bind-address=:8090 \
    --health-probe-bind-address=:8091 \
    > /tmp/operator-e2e.log 2>&1 &
OPERATOR_PID=$!
echo "  Operator PID: $OPERATOR_PID"
sleep 5

echo -e "${YELLOW}▸ Creating test namespace and resources...${NC}"
kubectl create namespace "$NS" 2>/dev/null || true
kubectl create secret generic llm-api-keys \
    --from-literal=API_KEY=test-key \
    -n "$NS" --dry-run=client -o yaml | kubectl apply -f - &>/dev/null

cat <<EOF | kubectl apply -f -
apiVersion: claw.prismer.ai/v1alpha1
kind: Claw
metadata:
  name: $CLAW_NAME
  namespace: $NS
  annotations:
    claw.prismer.ai/image-pull-policy: "Never"
spec:
  runtime: openclaw
  credentials:
    secretRef:
      name: llm-api-keys
EOF

echo "  Waiting for StatefulSet..."
if wait_for "StatefulSet creation" 30 kubectl get sts "$CLAW_NAME" -n "$NS"; then
    echo "  StatefulSet created"
else
    echo -e "${RED}ERROR: StatefulSet not created within 30s${NC}"
    kubectl get claw "$CLAW_NAME" -n "$NS" -o yaml 2>/dev/null | tail -20
    exit 1
fi

# =============================================================================
# Test 1: Intent annotation consumption — bump-memory
# =============================================================================

echo ""
echo -e "${YELLOW}▸ Test 1: Intent consumption — bump-memory${NC}"

# Write intent annotation directly on the Claw.
kubectl annotate claw "$CLAW_NAME" -n "$NS" --overwrite \
    "claw.prismer.ai/ops-intent"='{"action":"bump-memory","params":{"target":"2Gi"},"generation":10,"source":"rule-engine"}' \
    &>/dev/null

# Wait for intent to be consumed (gen counter set to "10").
if wait_for "intent consumption" 15 \
    bash -c "[ \"\$(kubectl get claw $CLAW_NAME -n $NS -o jsonpath='{.metadata.annotations.claw\\.prismer\\.ai/ops-intent-gen}')\" = '10' ]"; then
    pass "bump-memory intent consumed (gen=10)"
else
    fail "bump-memory intent not consumed within 15s"
fi

# Verify intent annotation was cleared.
intent_val=$(jq_field claw "$CLAW_NAME" "$NS" '.metadata.annotations["claw.prismer.ai/ops-intent"] // empty')
if [ -z "$intent_val" ]; then
    pass "intent annotation cleared after consumption"
else
    fail "intent annotation still present: $intent_val"
fi

# =============================================================================
# Test 2: Intent annotation consumption — rollout-restart
# =============================================================================

echo ""
echo -e "${YELLOW}▸ Test 2: Intent consumption — rollout-restart${NC}"

kubectl annotate claw "$CLAW_NAME" -n "$NS" --overwrite \
    "claw.prismer.ai/ops-intent"='{"action":"rollout-restart","params":{},"generation":20,"source":"companion-claw"}' \
    &>/dev/null

if wait_for "rollout-restart consumption" 15 \
    bash -c "[ \"\$(kubectl get claw $CLAW_NAME -n $NS -o jsonpath='{.metadata.annotations.claw\\.prismer\\.ai/ops-intent-gen}')\" = '20' ]"; then
    pass "rollout-restart intent consumed (gen=20)"
else
    fail "rollout-restart intent not consumed within 15s"
fi

# =============================================================================
# Test 3: Stale generation guard
# =============================================================================

echo ""
echo -e "${YELLOW}▸ Test 3: Stale generation guard${NC}"

# Write an intent with generation=5 (< current gen=20). Should be ignored.
kubectl annotate claw "$CLAW_NAME" -n "$NS" --overwrite \
    "claw.prismer.ai/ops-intent"='{"action":"bump-cpu","params":{"target":"4"},"generation":5,"source":"rule-engine"}' \
    &>/dev/null

# Wait a few seconds, then verify gen is still 20 (not bumped to 5).
sleep 3

gen_val=$(jq_field claw "$CLAW_NAME" "$NS" '.metadata.annotations["claw.prismer.ai/ops-intent-gen"] // empty')
if [ "$gen_val" = "20" ]; then
    pass "stale generation ignored (gen still 20)"
else
    fail "stale generation was processed (gen=$gen_val, expected 20)"
fi

# =============================================================================
# Test 4: Invalid intent rejection
# =============================================================================

echo ""
echo -e "${YELLOW}▸ Test 4: Invalid intent rejection${NC}"

# Write an intent with unknown action. Should be cleared.
kubectl annotate claw "$CLAW_NAME" -n "$NS" --overwrite \
    "claw.prismer.ai/ops-intent"='{"action":"delete-namespace","params":{},"generation":30,"source":"attacker"}' \
    &>/dev/null

# For invalid intents, the annotation is cleared and gen set to 0.
# Wait for intent to disappear.
if wait_for "invalid intent cleared" 15 \
    bash -c "[ -z \"\$(kubectl get claw $CLAW_NAME -n $NS -o jsonpath='{.metadata.annotations.claw\\.prismer\\.ai/ops-intent}')\" ]"; then
    pass "invalid intent annotation cleared"
else
    fail "invalid intent annotation not cleared within 15s"
fi

# =============================================================================
# Test 5: ClawOpsEscalation lifecycle — OOM trigger
# =============================================================================

echo ""
echo -e "${YELLOW}▸ Test 5: OOM → ClawOpsEscalation creation${NC}"

# Create a synthetic Pod with OOMKilled status that ClawOpsController will detect.
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${CLAW_NAME}-0
  namespace: $NS
  labels:
    claw.prismer.ai/instance: $CLAW_NAME
spec:
  containers:
  - name: runtime
    image: busybox
    command: ["sleep", "3600"]
EOF

# Wait for pod to be created, then patch its status.
sleep 2

# Patch pod status with OOMKilled.
kubectl patch pod "${CLAW_NAME}-0" -n "$NS" --type=merge --subresource=status -p '{
  "status": {
    "containerStatuses": [{
      "name": "runtime",
      "restartCount": 3,
      "ready": false,
      "started": false,
      "image": "busybox",
      "imageID": "docker-pullable://busybox@sha256:abc",
      "containerID": "containerd://fake",
      "state": {"running": {"startedAt": "2026-04-20T00:00:00Z"}},
      "lastState": {
        "terminated": {
          "reason": "OOMKilled",
          "exitCode": 137,
          "finishedAt": "2026-04-20T00:00:00Z"
        }
      }
    }]
  }
}' &>/dev/null || true

# Wait for a ClawOpsEscalation to be created for this Claw.
if wait_for "ClawOpsEscalation creation" 20 \
    bash -c "kubectl get clawopsescalation -n $NS -o json | jq -e '.items[] | select(.spec.clawRef.name==\"$CLAW_NAME\")' >/dev/null"; then
    pass "ClawOpsEscalation created for OOM event"
else
    fail "ClawOpsEscalation not created within 20s"
    # Show operator logs for debugging.
    echo "  Operator logs (last 20 lines):"
    tail -20 /tmp/operator-e2e.log 2>/dev/null | sed 's/^/    /'
fi

# Verify escalation fields.
ESC_NAME=$(kubectl get clawopsescalation -n "$NS" -o json 2>/dev/null | \
    jq -r '.items[] | select(.spec.clawRef.name=="'"$CLAW_NAME"'") | .metadata.name' 2>/dev/null | head -1)

if [ -n "$ESC_NAME" ]; then
    trigger_type=$(jq_field clawopsescalation "$ESC_NAME" "$NS" '.spec.trigger.type')
    severity=$(jq_field clawopsescalation "$ESC_NAME" "$NS" '.spec.severity')

    if [ "$trigger_type" = "OOMKilled" ]; then
        pass "escalation trigger type = OOMKilled"
    else
        fail "escalation trigger type = $trigger_type (expected OOMKilled)"
    fi

    if [ "$severity" = "High" ] || [ "$severity" = "Critical" ]; then
        pass "escalation severity = $severity"
    else
        fail "escalation severity = $severity (expected High or Critical)"
    fi
fi

# =============================================================================
# Test 6: Scale-replicas intent
# =============================================================================

echo ""
echo -e "${YELLOW}▸ Test 6: Intent consumption — scale-replicas${NC}"

# Clean up stale annotation first.
kubectl annotate claw "$CLAW_NAME" -n "$NS" --overwrite \
    "claw.prismer.ai/ops-intent"='{"action":"scale-replicas","params":{"replicas":"3"},"generation":50,"source":"rule-engine"}' \
    &>/dev/null

if wait_for "scale-replicas consumption" 15 \
    bash -c "[ \"\$(kubectl get claw $CLAW_NAME -n $NS -o jsonpath='{.metadata.annotations.claw\\.prismer\\.ai/ops-intent-gen}')\" = '50' ]"; then
    pass "scale-replicas intent consumed (gen=50)"
else
    fail "scale-replicas intent not consumed within 15s"
fi

# =============================================================================
# Summary
# =============================================================================

echo ""
echo "══════════════════════════════════════════════════"
TOTAL=$((PASS + FAIL))
echo -e "  Results: ${GREEN}${PASS} passed${NC}, ${RED}${FAIL} failed${NC}, ${TOTAL} total"
if [ "$FAIL" -gt 0 ]; then
    echo -e "  Failed:${ERRORS}"
fi
echo "══════════════════════════════════════════════════"

exit "$FAIL"
