#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

DURATION="${DURATION:-12}"
USERS="${USERS:-1}"
KEEP_UP="${KEEP_UP:-0}"

if [[ "$KEEP_UP" != "1" ]]; then
  cleanup() {
    "$SCRIPT_DIR/down.sh" --volumes || true
  }
  trap cleanup EXIT
fi

"$SCRIPT_DIR/up.sh"
CALL_URL="$("$SCRIPT_DIR/create-room.sh" --name "Smoke room $(date -u +%H%M%S)" | tail -n1)"
"$SCRIPT_DIR/stream-video.sh" --call-url "$CALL_URL" --users "$USERS" --duration "$DURATION"

log "Smoke test passed"
