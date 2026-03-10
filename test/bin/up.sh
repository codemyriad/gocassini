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
log "Create a room: $REPO_ROOT/cassini-lab/bin/create-room.sh --name 'Local room'"
log "Stream media:  $REPO_ROOT/cassini-player/bin/stream-video.sh --duration 20"
log "Showcase call: $REPO_ROOT/cassini-player/bin/stream-showcase-meeting.sh --call-url <CALL_URL>"
