#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

harness_stack_init
harness_check_existing_resources_for_up
harness_prepare_exapp_image
harness_render_stack_configs
harness_start_compose_stack
harness_verify_lan_signaling_reachability

wait_for_nextcloud 420
"$SCRIPT_DIR/bootstrap.sh"
harness_configure_appapi_phase
harness_install_exapp_phase

# Seeding runs last, and only when asked. Everything above it is the stack we
# have always brought up: provisioning creates the recordings tree on empty
# disk, exactly as it does on a stack that is never seeded, and only then is a
# pack copied in. Nothing is laid down ahead of it.
if [[ -n "${CASSINI_HARNESS_SEED_DIR:-}" ]]; then
  "$SCRIPT_DIR/seed-nc-files.sh" --pack "$CASSINI_HARNESS_SEED_DIR"
fi

log "Stack is up."
log "Create a room: $REPO_ROOT/bin/cassini dev room create --name 'Local room'"
log "Stream media:  $REPO_ROOT/harness/bin/stream-video.sh --duration 20"
