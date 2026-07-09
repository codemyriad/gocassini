#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./lib/e2e-local.sh
source "$SCRIPT_DIR/lib/e2e-local.sh"
harness_e2e_local_stack_env full legacy none
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

DURATION="${DURATION:-12}"
USERS="${USERS:-1}"
KEEP_UP="${KEEP_UP:-0}"

# Explicit stack topology for the smoke run: local HTTP, full media services,
# no installed ExApp, legacy recording backend. No --reset: smoke uses the
# default project, so the non-destructive guard must keep protecting an
# existing dev stack.
STACK_TOPOLOGY=(
  --public-mode local-http
  --services full
  --cassini none
  --recording-backend legacy
)

if [[ "$KEEP_UP" != "1" ]]; then
  cleanup() {
    "$REPO_ROOT/bin/cassini" dev stack down --volumes "${STACK_TOPOLOGY[@]}" || true
  }
  trap cleanup EXIT
fi

"$REPO_ROOT/bin/cassini" dev stack up "${STACK_TOPOLOGY[@]}"
CALL_URL="$("$SCRIPT_DIR/create-room.sh" --name "Smoke room $(date -u +%H%M%S)" | tail -n1)"
"$SCRIPT_DIR/stream-video.sh" --call-url "$CALL_URL" --users "$USERS" --duration "$DURATION"

log "Smoke test passed"
