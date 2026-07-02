#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

harness_bootstrap_gateway() {
  docker network inspect "${PROJECT_NAME}_default" -f '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null || true
}

harness_bootstrap_core_nextcloud() {
  wait_for_nextcloud 420

  log "Ensuring Talk app is installed/enabled"
  occ_ignore_failure app:install spreed >/dev/null 2>&1
  occ_ignore_failure app:enable spreed >/dev/null 2>&1

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
  # host.docker.internal, and the browser-facing VM host). Writing to those low
  # indices clobbers the image-supplied entries — most importantly "nextcloud"
  # itself, which the ExApp container uses to reach Nextcloud over the compose
  # network. Append our additions at high indices instead.
  occ config:system:set trusted_domains 10 --value="host.docker.internal"
  local gateway
  gateway="$(harness_bootstrap_gateway)"
  if [[ -n "$gateway" ]]; then
    occ config:system:set trusted_domains 11 --value="$gateway"
  fi
  local trusted_index=12
  add_trusted_domain() {
    local host="$1"
    host="$(harness_url_host "$host")"
    if [[ -n "$host" \
      && "$host" != "localhost" \
      && "$host" != "127.0.0.1" \
      && "$host" != "nextcloud" \
      && "$host" != "host.docker.internal" \
      && "$host" != "reverse-proxy" \
      && "$host" != "$gateway" ]]; then
      occ config:system:set trusted_domains "$trusted_index" --value="$host"
      trusted_index=$((trusted_index + 1))
    fi
  }
  add_trusted_domain "${CASSINI_HARNESS_HOST:-}"
  add_trusted_domain "${CASSINI_HARNESS_PUBLIC_HOST:-}"

  if [[ -n "${CASSINI_HARNESS_PUBLIC_URL:-}" ]]; then
    local public_scheme public_hostport
    public_scheme="$(harness_url_scheme "$CASSINI_HARNESS_PUBLIC_URL")"
    public_hostport="$(harness_url_hostport "$CASSINI_HARNESS_PUBLIC_URL")"
    log "Configuring public HTTPS origin: $CASSINI_HARNESS_PUBLIC_URL"
    occ config:system:set overwriteprotocol --value="$public_scheme"
    occ config:system:set overwritehost --value="$public_hostport"
    occ config:system:set overwrite.cli.url --value="$CASSINI_HARNESS_PUBLIC_URL"
    occ config:system:set trusted_proxies 10 --value="127.0.0.1"
    occ config:system:set trusted_proxies 11 --value="::1"
    if [[ -n "$gateway" ]]; then
      occ config:system:set trusted_proxies 12 --value="$gateway"
    fi
  fi
}

harness_configure_talk_media() {
  if ! harness_media_selected; then
    log "Skipping signaling/TURN wiring because media mode is not selected"
    return 0
  fi

  if ! compose ps --services --filter status=running | grep -Fxq signaling; then
    echo "Media mode is selected but the signaling service is not running" >&2
    return 1
  fi

  local effective_signaling_url
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
    local signaling_json
    signaling_json=$(printf '{"servers":[{"server":"%s","verify":false}],"secret":"%s"}' "$effective_signaling_url" "$SIGNALING_SHARED_SECRET")
    occ config:app:set spreed signaling_servers --value="$signaling_json"
  fi

  if occ_has "talk:turn:add"; then
    log "Configuring TURN server: $TURN_SERVER"
    occ_ignore_failure talk:turn:delete turn "$TURN_SERVER" udp,tcp >/dev/null 2>&1
    occ talk:turn:add turn "$TURN_SERVER" udp,tcp --secret="$TURN_SHARED_SECRET"
  fi
}

harness_configure_legacy_recording_backend() {
  if ! harness_media_selected; then
    return 0
  fi

  local gateway effective_recording_url recording_json
  gateway="$(harness_bootstrap_gateway)"
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
}

harness_bootstrap_core_nextcloud
harness_configure_talk_media
harness_configure_legacy_recording_backend

log "Bootstrap complete"
