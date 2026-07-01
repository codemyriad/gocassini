#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

if [[ "$SPREED_PROFILE" == "full" ]] && harness_remote_config_requested; then
  harness_render_full_profile_configs false
fi

log "Starting Docker Compose stack (profile: $SPREED_PROFILE)"
compose up -d

wait_for_nextcloud 420
"$SCRIPT_DIR/bootstrap.sh"

log "Stack is up."
log "Create a room: $REPO_ROOT/bin/cassini dev room create --name 'Local room'"
log "Stream media:  $REPO_ROOT/harness/bin/stream-video.sh --duration 20"
