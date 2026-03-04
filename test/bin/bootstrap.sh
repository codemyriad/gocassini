#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

wait_for_nextcloud 420

log "Ensuring Talk app is installed/enabled"
occ app:install spreed >/dev/null 2>&1 || true
occ app:enable spreed >/dev/null 2>&1 || true

if occ user:info "$BOT_USER" >/dev/null 2>&1; then
  log "Bot user already exists: $BOT_USER"
else
  log "Creating bot user: $BOT_USER"
  export OC_PASS="$BOT_PASSWORD"
  occ user:add --password-from-env --display-name="$BOT_USER" "$BOT_USER"
  unset OC_PASS
fi

if [[ "$SPREED_PROFILE" != "full" ]]; then
  log "Skipping signaling/TURN wiring because profile is '$SPREED_PROFILE'"
  exit 0
fi

if ! compose ps --services --filter status=running | grep -Fxq signaling; then
  log "Signaling service is not running, skipping wiring"
  exit 0
fi

effective_signaling_url="${SIGNALING_URL:-}"
if [[ -z "$effective_signaling_url" || "$effective_signaling_url" == "http://127.0.0.1:18082" || "$effective_signaling_url" == "http://127.0.0.1:28082" ]]; then
  effective_signaling_url="$(default_signaling_url)"
fi

if occ_has "talk:signaling:add"; then
  log "Configuring external signaling server: $effective_signaling_url"
  occ talk:signaling:delete "$effective_signaling_url" >/dev/null 2>&1 || true
  occ talk:signaling:add "$effective_signaling_url" "$SIGNALING_SHARED_SECRET"
else
  log "No talk:signaling:add command; setting legacy app config"
  signaling_json=$(printf '{"servers":[{"server":"%s","verify":false}],"secret":"%s"}' "$effective_signaling_url" "$SIGNALING_SHARED_SECRET")
  occ config:app:set spreed signaling_servers --value="$signaling_json"
fi

if occ_has "talk:turn:add"; then
  log "Configuring TURN server: $TURN_SERVER"
  occ talk:turn:delete turn "$TURN_SERVER" udp,tcp >/dev/null 2>&1 || true
  occ talk:turn:add turn "$TURN_SERVER" udp,tcp --secret="$TURN_SHARED_SECRET"
fi

log "Bootstrap complete"
