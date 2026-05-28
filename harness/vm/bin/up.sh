#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

log "Starting Docker Compose stack (profile: $SPREED_PROFILE, host: $CASSINI_HARNESS_HOST)"
compose up -d

wait_for_nextcloud 420
"$SCRIPT_DIR/bootstrap.sh"
wait_for_public_nextcloud 60

log "Stack is up."
log "Create a room: $VM_DIR/bin/create-room.sh --name 'VM room'"
log "Native dev room/player commands can also target this stack when NEXTCLOUD_URL=$NEXTCLOUD_URL is exported."
