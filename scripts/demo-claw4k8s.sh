#!/usr/bin/env bash
# claw4k8s demo — 60-second self-healing showcase for asciinema recording.
#
# What's real (not scripted):
#   - Real kind cluster, real k8s4claw operator binary
#   - Real Pod with 32Mi memory limit + real OOM-inducing workload
#   - Real ClawOpsController detecting the crash
#   - Real intent annotation flowing through ClawReconciler
#   - Real Ed25519 signature on the escalation receipt
#
# Usage:
#   make build
#   asciinema rec demo.cast -c ./scripts/demo-claw4k8s.sh
#   agg demo.cast demo.gif --speed 1.5 --theme monokai --font-size 18
set -euo pipefail

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
CYAN='\033[0;36m'
YELLOW='\033[0;33m'
PURPLE='\033[0;35m'
BOLD='\033[1m'
DIM='\033[2m'
NC='\033[0m'

CLUSTER_NAME="claw4k8s-demo"
NS="demo"
CLAW_NAME="chatbot"

caption() {
    echo -e "${DIM}# $1${NC}"
    sleep 0.8
}

heading() {
    echo ""
    echo -e "${CYAN}▸${NC} ${BOLD}$1${NC}"
    sleep 0.4
}

cmd() {
    echo -e "${YELLOW}\$${NC} ${BOLD}$*${NC}"
    sleep 0.5
    "$@"
}

cleanup() {
    pkill -f 'bin/operator' 2>/dev/null || true
    kind delete cluster --name "$CLUSTER_NAME" 2>/dev/null || true
}
trap cleanup EXIT

clear

# =============================================================================
# 0:00–0:05 | Hook — the punchline first
# =============================================================================

echo -e "${RED}${BOLD}2am. Production OOM. No human on-call.${NC}"
echo -e "${GREEN}${BOLD}Watch the AI agent fix itself.${NC}"
echo ""
sleep 2

# =============================================================================
# 0:05–0:15 | Silent setup (spinner)
# =============================================================================

caption "Setup: kind cluster + k8s4claw operator + a Claw (AI agent)"

# Start setup tasks (each runs in foreground of this shell so operator is not
# orphaned by subshell SIGHUP). Spinner runs alongside via background PID trick.
(
    while true; do
        for c in '|' '/' '-' '\\'; do
            printf "\r  ${CYAN}%s${NC} Setting up (kind, CRDs, operator, Claw)..." "$c"
            sleep 0.15
        done
    done
) &
SPINNER_PID=$!

kind create cluster --name "$CLUSTER_NAME" --wait 60s &>/dev/null
kubectl apply -f config/crd/bases/ &>/dev/null
# Wait for CRDs to be established before starting operator (otherwise operator
# fails to register field indexers for Claw/ClawOpsEscalation).
for crd in claws.claw.prismer.ai clawopsescalations.claw.prismer.ai clawchannels.claw.prismer.ai; do
    kubectl wait --for=condition=established "crd/$crd" --timeout=30s &>/dev/null
done

# Pre-load stress image so OOM triggers fast (no network pull delay).
kind load docker-image polinux/stress:latest --name "$CLUSTER_NAME" &>/dev/null || true

setsid bin/operator --disable-webhooks \
    --metrics-bind-address=:8090 \
    --health-probe-bind-address=:8091 \
    > /tmp/claw4k8s-demo.log 2>&1 < /dev/null &
disown $! 2>/dev/null || true
sleep 4
kubectl create namespace "$NS" &>/dev/null

kill $SPINNER_PID 2>/dev/null
wait $SPINNER_PID 2>/dev/null || true
printf "\r  ${GREEN}✓${NC} Ready                                         \n"
sleep 1

# =============================================================================
# 0:15–0:25 | The crash — REAL OOM, not a fake status patch
# =============================================================================

heading "Deploying a workload that will OOMKill (32Mi limit, malloc loop)"

# Note: we use a separate Pod name ("oom-demo") instead of "${CLAW_NAME}-0"
# because the Claw reconciler's StatefulSet owns "${CLAW_NAME}-0". Our stress
# Pod just needs the instance label so ClawOpsController associates it with
# the Claw CR (label-based lookup, not ownerReference-based).
OOM_POD="oom-demo"
cat <<EOF | kubectl apply -f - &>/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: ${OOM_POD}
  namespace: ${NS}
  labels:
    claw.prismer.ai/instance: ${CLAW_NAME}
spec:
  restartPolicy: Always
  containers:
  - name: runtime
    image: polinux/stress:latest
    imagePullPolicy: IfNotPresent
    command: ["stress"]
    args: ["--vm", "1", "--vm-bytes", "128M", "--vm-hang", "0"]
    resources:
      limits:
        memory: "32Mi"
      requests:
        memory: "32Mi"
EOF

caption "waiting for OOM (kernel + cgroup limit)..."
caption "rule engine needs 2+ OOMs within window to auto-remediate"

# Poll until restart count ≥ 2 (matches oom-bump-memory rule's MinCount).
for i in $(seq 1 90); do
    restarts=$(kubectl get pod "${OOM_POD}" -n "$NS" \
        -o jsonpath='{.status.containerStatuses[0].restartCount}' 2>/dev/null || echo 0)
    reason=$(kubectl get pod "${OOM_POD}" -n "$NS" \
        -o jsonpath='{.status.containerStatuses[0].lastState.terminated.reason}' 2>/dev/null || echo "")
    if [ "$reason" = "OOMKilled" ] && [ "$restarts" -ge 2 ]; then break; fi
    printf "\r  ${DIM}...OOMs: %s (need 2+ to match rule)${NC}" "$restarts"
    sleep 1
done
echo ""

cmd kubectl get pod "${OOM_POD}" -n "$NS" \
    -o jsonpath='reason={.status.containerStatuses[0].lastState.terminated.reason} restarts={.status.containerStatuses[0].restartCount}{"\n"}'
echo -e "${RED}${BOLD}  ↑ OOMKilled ${restarts}x. Real. Kernel killed the process.${NC}"
sleep 2

# Now register the Claw CR. Operator's first reconcile sees count≥2 signal,
# matches the oom-bump-memory rule, auto-executes: creates escalation
# (phase=AutoExecuted), signs, writes intent on the Claw.
heading "Registering the Claw so operator can auto-remediate"
cat <<EOF | kubectl apply -f - &>/dev/null
apiVersion: claw.prismer.ai/v1alpha1
kind: Claw
metadata:
  name: $CLAW_NAME
  namespace: $NS
spec:
  runtime: openclaw
EOF
caption "waiting for rule engine to match + execute..."

# Poll for AutoExecuted escalation (proves full loop).
for i in $(seq 1 30); do
    phase=$(kubectl get clawopsescalation -n "$NS" -o jsonpath='{.items[0].status.phase}' 2>/dev/null || echo "")
    if [ "$phase" = "AutoExecuted" ]; then break; fi
    sleep 1
done

# =============================================================================
# 0:25–0:45 | Auto-healing — show the intent annotation in real time
# =============================================================================

heading "ClawOpsController detected the crash → auto-executed"

# Poll for escalation.
for i in $(seq 1 20); do
    count=$(kubectl get clawopsescalation -n "$NS" 2>/dev/null | grep -c "$CLAW_NAME" || true)
    if [ "$count" -gt 0 ]; then break; fi
    sleep 0.5
done

cmd kubectl get clawopsescalation -n "$NS"
sleep 1

# Prefer AutoExecuted escalation (the one the rule engine handled) over any
# residual Pending one from first-OOM signal before Claw was registered.
ESC_NAME=$(kubectl get clawopsescalation -n "$NS" -o json 2>/dev/null | \
    python3 -c "
import json, sys
d = json.load(sys.stdin)
items = d.get('items', [])
for e in items:
    if e.get('status', {}).get('phase') == 'AutoExecuted':
        print(e['metadata']['name']); break
else:
    if items: print(items[0]['metadata']['name'])
" 2>/dev/null || echo "")

heading "The architectural trick: intent annotation on the Claw CR"
caption "other tools patch StatefulSets directly — we don't"

# Show the annotation appearing (may already be consumed, try both).
echo -e "${YELLOW}\$${NC} ${BOLD}kubectl get claw $CLAW_NAME -n $NS -o yaml | grep -A3 ops-intent${NC}"
kubectl get claw "$CLAW_NAME" -n "$NS" -o yaml 2>/dev/null | \
    grep -A1 "ops-intent" | head -6 || echo "  (intent already consumed → annotation cleared, gen counter bumped)"
echo ""

cmd kubectl get claw "$CLAW_NAME" -n "$NS" \
    -o jsonpath='{"gen: "}{.metadata.annotations.claw\.prismer\.ai/ops-intent-gen}{"\n"}'
sleep 1

caption "single writer (ClawReconciler) consumes intents → zero controller contention"
sleep 1

# =============================================================================
# 0:45–0:55 | Audit trail
# =============================================================================

heading "Every action Ed25519-signed for audit"

if [ -n "$ESC_NAME" ]; then
    kubectl get clawopsescalation "$ESC_NAME" -n "$NS" -o json 2>/dev/null | \
        python3 -c "
import json, sys
d = json.load(sys.stdin)
status = d.get('status', {})
spec = d.get('spec', {})
print(f\"  escalation:  {d['metadata']['name']}\")
print(f\"  trigger:     {spec.get('trigger', {}).get('type', 'N/A')} (count: {spec.get('trigger', {}).get('count', 0)})\")
print(f\"  severity:    {spec.get('severity', 'N/A')}\")
print(f\"  matched:     {status.get('matchedRule', 'N/A')}\")
action = status.get('executedAction', '')
if action:
    print(f\"  action:      {action[:70]}...\" if len(action) > 70 else f\"  action:      {action}\")
receipt = status.get('signetReceipt', '')
if receipt:
    import json as _j
    try:
        r = _j.loads(receipt)
        print(f\"  signed:      key={r.get('key', '?')}, sig={r.get('sig', '')[:16]}...\")
    except Exception:
        print(f\"  signed:      {receipt[:80]}\")
" 2>/dev/null
fi
sleep 2

# =============================================================================
# 0:55–1:00 | Pitch
# =============================================================================

echo ""
echo -e "${GREEN}${BOLD}  AI agents managing their own Kubernetes.${NC}"
echo -e "${DIM}  No 2am page. Every action signed. LLM optional — degrades to notification.${NC}"
echo ""
echo -e "  ${BOLD}→ github.com/Prismer-AI/k8s4claw${NC}"
echo ""
sleep 3
