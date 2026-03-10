#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

log "Starting Docker Compose stack (profile: $SPREED_PROFILE)"
compose up -d

wait_for_nextcloud 420
"$SCRIPT_DIR/bootstrap.sh"

log "Stack is up."
log "Create a room: $TEST_DIR/bin/create-room.sh --name 'Local room'"
log "Stream media:  $TEST_DIR/bin/stream-video.sh --duration 20"
log "Synthetic call: $TEST_DIR/bin/stream-synthetic-meeting.sh --call-url <CALL_URL>"
