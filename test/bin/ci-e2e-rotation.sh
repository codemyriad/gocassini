#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

export PROJECT_NAME="${PROJECT_NAME:-gocassini-ci-rotation}"
export SPREED_PROFILE="${SPREED_PROFILE:-full}"
export NEXTCLOUD_URL="${NEXTCLOUD_URL:-http://127.0.0.1:28080}"
export NEXTCLOUD_STATUS_URL="${NEXTCLOUD_STATUS_URL:-$NEXTCLOUD_URL/status.php}"
export SIGNALING_URL="${SIGNALING_URL:-}"

export ADMIN_USER="${ADMIN_USER:-admin}"
export ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin}"
export BOT_USER="${BOT_USER:-ci-botuser}"
export BOT_PASSWORD="${BOT_PASSWORD:-ci-bot-password}"
export SIGNALING_SHARED_SECRET="${SIGNALING_SHARED_SECRET:-7f4dca67263621ba7f9f9917e13de95a201f6f360be0d303e3008c2e6c8ad37d}"
export TURN_SERVER="${TURN_SERVER:-127.0.0.1:13479}"
export TURN_SHARED_SECRET="${TURN_SHARED_SECRET:-3c04d2fc2f7fe39d48eb4dc77f652c8c778a4ea178b0e486529b284afca7b648}"

export REC_DURATION="${REC_DURATION:-75}"
export PUB_DURATION="${PUB_DURATION:-60}"
export PUB_USERS="${PUB_USERS:-3}"
export CALL_NAME="${CALL_NAME:-CI Rotation room}"
export CHECK_COMPOSED_AUDIO_TAIL="${CHECK_COMPOSED_AUDIO_TAIL:-1}"

CI_OUTPUT_BASE="/tmp/gocassini-ci-rotation-$(date -u +%Y%m%dT%H%M%S)-$$"
export OUTPUT="${OUTPUT:-$CI_OUTPUT_BASE.requested-output}"
export FINAL_OUTPUT="${FINAL_OUTPUT:-$CI_OUTPUT_BASE.mkv}"
export REC_LOG="${REC_LOG:-/tmp/gocassini-ci-rotation-recorder.log}"
export PUB_LOG="${PUB_LOG:-/tmp/gocassini-ci-rotation-publisher.log}"

cleanup() {
  log "Cleaning up local test stack"
  "$SCRIPT_DIR/down.sh" --volumes || true
}

trap cleanup EXIT INT TERM

log "Starting local Nextcloud Talk stack for rotation E2E"
"$SCRIPT_DIR/up.sh"

log "Creating temporary room for rotation capture"
CALL_URL="$(create_room_with_retry "$CALL_NAME")"
log "Test room URL: $CALL_URL"
export CALL_URL

log "Running recorder + 3-user rotating publisher E2E"
(
  cd "$REPO_ROOT/cassini-go-recorder"
  ./e2e_with_publisher.sh
)

log "Rotation E2E run complete"
