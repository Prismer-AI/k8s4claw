#!/usr/bin/env bash
# Run claw4k8s locally against the current kubeconfig context.
# Loads env vars from cmd/claw4k8s/.env if present.
#
# Usage:
#   1. cp cmd/claw4k8s/.env.example cmd/claw4k8s/.env
#   2. edit cmd/claw4k8s/.env  (set CLAW4K8S_LLM_GATEWAY_URL, _API_KEY, _MODEL)
#   3. ./scripts/run-claw4k8s-local.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$REPO_ROOT/cmd/claw4k8s/.env"

if [ -f "$ENV_FILE" ]; then
    echo "loading env from $ENV_FILE"
    # Parse line by line: skip comments / blanks / lines without `=`.
    # Strip optional surrounding quotes and any whitespace around `=`.
    while IFS= read -r line || [ -n "$line" ]; do
        # Trim leading whitespace.
        line="${line#"${line%%[![:space:]]*}"}"
        # Skip comments and blank lines.
        case "$line" in
            ''|'#'*) continue ;;
        esac
        # Must contain `=`.
        case "$line" in
            *=*) ;;
            *) continue ;;
        esac
        key="${line%%=*}"
        val="${line#*=}"
        # Trim whitespace around key.
        key="${key// /}"
        # Trim leading whitespace from value.
        val="${val#"${val%%[![:space:]]*}"}"
        # Trim trailing whitespace from value.
        val="${val%"${val##*[![:space:]]}"}"
        # Strip optional surrounding quotes from value.
        case "$val" in
            \"*\") val="${val#\"}"; val="${val%\"}" ;;
            \'*\') val="${val#\'}"; val="${val%\'}" ;;
        esac
        export "$key=$val"
    done < "$ENV_FILE"
else
    echo "no .env at $ENV_FILE — using current shell env only"
    echo "(copy cmd/claw4k8s/.env.example to cmd/claw4k8s/.env to populate)"
fi

# Build if missing or any source file is newer than the binary.
NEEDS_BUILD=0
if [ ! -x "$REPO_ROOT/bin/claw4k8s" ]; then
    NEEDS_BUILD=1
elif [ -n "$(find "$REPO_ROOT/cmd/claw4k8s" -name '*.go' -newer "$REPO_ROOT/bin/claw4k8s" 2>/dev/null | head -1)" ]; then
    NEEDS_BUILD=1
fi
if [ "$NEEDS_BUILD" = "1" ]; then
    echo "building bin/claw4k8s..."
    (cd "$REPO_ROOT" && go build -o bin/claw4k8s ./cmd/claw4k8s)
fi

# Quick sanity check.
if [ -z "${CLAW4K8S_LLM_GATEWAY_URL:-}" ]; then
    echo "WARNING: CLAW4K8S_LLM_GATEWAY_URL is not set — running in noop fallback mode (no real LLM calls)"
fi

echo "kubeconfig context: $(kubectl config current-context 2>/dev/null || echo '<none>')"
echo "starting claw4k8s..."
exec "$REPO_ROOT/bin/claw4k8s"
