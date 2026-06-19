#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

wait_for_nextcloud 420

log "Ensuring Talk app is installed/enabled"
occ_ignore_failure app:install spreed >/dev/null 2>&1
occ_ignore_failure app:enable spreed >/dev/null 2>&1

# The VM harness is intended to be driven from a host browser. Disable the
# first-run wizard so logging in at / leaves the user on the normal Nextcloud UI
# where the Talk app entry can be clicked immediately.
log "Disabling first-run wizard for VM browser flow"
occ_ignore_failure app:disable firstrunwizard >/dev/null 2>&1

if occ user:info "$BOT_USER" >/dev/null 2>&1; then
  log "Bot user already exists: $BOT_USER"
else
  log "Creating bot user: $BOT_USER"
  export OC_PASS="$BOT_PASSWORD"
  occ user:add --password-from-env --display-name="$BOT_USER" "$BOT_USER"
  unset OC_PASS
fi

log "Ensuring local operator-facing trusted domains"
# The Nextcloud image's entrypoint already populates trusted_domains 1..4
# from NEXTCLOUD_TRUSTED_DOMAINS (typically localhost, 127.0.0.1, nextcloud,
# host.docker.internal, plus any configured CASSINI_HARNESS_HOST). Writing to
# those low indices clobbers the image-supplied entries — most importantly
# "nextcloud" itself, which the ExApp container uses to reach Nextcloud over
# the compose network. Without it, any in-network POST/PUT from the ExApp
# returns Nextcloud's "Access through untrusted domain" 400 page — which is
# how install-E2E was hanging (operator's init_progress=100 callback got 400,
# AppAPI --wait-finish polled forever). Append our additions at high indices
# instead.
occ config:system:set trusted_domains 10 --value="host.docker.internal"
gateway="$(docker network inspect "${PROJECT_NAME}_default" -f '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null || true)"
if [[ -n "$gateway" ]]; then
  occ config:system:set trusted_domains 11 --value="$gateway"
fi
custom_public_host="${CASSINI_HARNESS_HOST:-}"
if [[ -n "$custom_public_host" \
  && "$custom_public_host" != "localhost" \
  && "$custom_public_host" != "127.0.0.1" \
  && "$custom_public_host" != "nextcloud" \
  && "$custom_public_host" != "host.docker.internal" \
  && "$custom_public_host" != "$gateway" ]]; then
  occ config:system:set trusted_domains 12 --value="$custom_public_host"
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
if [[ -z "$effective_signaling_url" || "$effective_signaling_url" == "http://127.0.0.1:18082" || "$effective_signaling_url" == "http://127.0.0.1:28082" || "$effective_signaling_url" == "http://signaling.localhost:28082" ]]; then
  effective_signaling_url="$(default_signaling_url)"
fi

if occ_has "talk:signaling:add"; then
  log "Configuring external signaling server: $effective_signaling_url"
  occ_ignore_failure config:app:delete spreed signaling_servers >/dev/null 2>&1
  occ_ignore_failure talk:signaling:delete "$effective_signaling_url" >/dev/null 2>&1
  occ talk:signaling:add "$effective_signaling_url" "$SIGNALING_SHARED_SECRET"
else
  log "No talk:signaling:add command; setting legacy app config"
  signaling_json=$(printf '{"servers":[{"server":"%s","verify":false}],"secret":"%s"}' "$effective_signaling_url" "$SIGNALING_SHARED_SECRET")
  occ config:app:set spreed signaling_servers --value="$signaling_json"
fi

if occ_has "talk:turn:add"; then
  log "Configuring TURN server: $TURN_SERVER"
  occ_ignore_failure talk:turn:delete turn "$TURN_SERVER" udp,tcp >/dev/null 2>&1
  occ talk:turn:add turn "$TURN_SERVER" udp,tcp --secret="$TURN_SHARED_SECRET"
fi

effective_recording_url="${CASSINI_TALK_RECORDING_URL:-}"
if [[ -z "$effective_recording_url" || "$effective_recording_url" == "http://127.0.0.1:4000" || "$effective_recording_url" == "http://localhost:4000" ]]; then
  if [[ -n "$gateway" ]]; then
    effective_recording_url="http://$gateway:4000"
  else
    effective_recording_url="http://127.0.0.1:4000"
  fi
fi

log "Configuring Talk recording backend: $effective_recording_url"
recording_json=$(printf '{"servers":[{"server":"%s","verify":false}],"secret":"%s"}' "$effective_recording_url" "$CASSINI_TALK_RECORDING_SECRET")
occ config:app:set spreed recording_servers --value="$recording_json"
occ config:app:set spreed call_recording --value="yes"

log "Bootstrap complete"
