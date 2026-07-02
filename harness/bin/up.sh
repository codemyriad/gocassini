#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

harness_check_existing_resources_for_up
harness_prepare_exapp_image
harness_render_stack_configs
harness_start_compose_stack

wait_for_nextcloud 420
"$SCRIPT_DIR/bootstrap.sh"
harness_configure_appapi_phase
harness_install_exapp_phase

log "Stack is up."
log "Create a room: $REPO_ROOT/bin/cassini dev room create --name 'Local room'"
log "Stream media:  $REPO_ROOT/harness/bin/stream-video.sh --duration 20"
