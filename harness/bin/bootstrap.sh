#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

harness_stack_init

harness_bootstrap_gateway() {
  docker network inspect "${PROJECT_NAME}_default" -f '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null || true
}

harness_bootstrap_core_nextcloud() {
  wait_for_nextcloud 420

  log "Ensuring Talk app is installed/enabled"
  occ_ignore_failure app:install spreed >/dev/null 2>&1
  occ_ignore_failure app:enable spreed >/dev/null 2>&1

  # Per-participant recording access needs two native Nextcloud apps that an
  # ExApp cannot install for itself: Team folders supplies the shared tree and
  # advanced ACLs; Everyone Group supplies the virtual `everyone` group so every
  # account has the read-only mount from creation without membership sweeps.
  # Since D-554 neither is optional — recordings are access-controlled or they
  # are not served. Installing them here mirrors the one-click app-store install
  # a production admin does; the ExApp then creates the "Cassini" folder + ACLs
  # itself on enable (operator nc_provision.go).
  log "Ensuring Group Folders app is installed/enabled"
  occ_ignore_failure app:install groupfolders >/dev/null 2>&1
  occ_ignore_failure app:enable groupfolders >/dev/null 2>&1

  log "Ensuring Everyone Group app is installed/enabled"
  occ_ignore_failure app:install group_everyone >/dev/null 2>&1
  occ_ignore_failure app:enable group_everyone >/dev/null 2>&1

  # The installs above tolerate failure on purpose: all three are app-store apps
  # and a hard install would abort the harness on any box without app-store
  # reachability. But a failed install must not then be INVISIBLE — without
  # these apps the ExApp provisions nothing, and a missing group_everyone in
  # particular makes the provisioner return before the Team folder is ever
  # created, which looks identical to a successful run.
  #
  # spreed is checked here for the same reason, learned the hard way: on a slow
  # link the ~6 MB app-store index times out, `app:install spreed` fails with
  # both streams discarded, and the harness reports a healthy bootstrap. The
  # first visible symptom is /operator/status answering 503 much later, with
  # nothing anywhere naming the cause. There is no Talk to record without it, so
  # a bootstrap that reaches this point without spreed has already failed.
  for required_app in spreed groupfolders group_everyone; do
    if ! occ app:list 2>/dev/null | sed -n '/^Enabled:/,/^Disabled:/p' | grep -q "  - ${required_app}:"; then
      log "FATAL: ${required_app} is required for recordings and is not enabled"
      return 1
    fi
  done

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

harness_configure_recording_backend() {
  local backend="${CASSINI_HARNESS_RECORDING_BACKEND:-legacy}"
  local gateway effective_recording_url recording_json

  case "$backend" in
    none)
      log "Skipping Talk recording backend configuration because backend mode is 'none'"
      occ_ignore_failure config:app:delete spreed recording_servers >/dev/null 2>&1
      occ config:app:set spreed call_recording --value="no"
      return 0
      ;;
    legacy|direct-operator|installed-exapp)
      ;;
    *)
      echo "Invalid CASSINI_HARNESS_RECORDING_BACKEND: $backend" >&2
      return 2
      ;;
  esac

  if ! harness_media_selected; then
    log "Skipping Talk recording backend configuration because media mode is not selected"
    return 0
  fi

  harness_validate_recording_secrets
  gateway="$(harness_bootstrap_gateway)"
  effective_recording_url="${CASSINI_TALK_RECORDING_URL:-}"

  case "$backend" in
    legacy|direct-operator)
      if [[ -z "$effective_recording_url" || "$effective_recording_url" == "http://127.0.0.1:4000" || "$effective_recording_url" == "http://localhost:4000" ]]; then
        if [[ -n "$gateway" ]]; then
          effective_recording_url="http://$gateway:4000"
        else
          effective_recording_url="http://127.0.0.1:4000"
        fi
      fi
      ;;
    installed-exapp)
      if [[ "${CASSINI_HARNESS_CASSINI_MODE:-none}" != "installed-exapp" ]]; then
        echo "Recording backend installed-exapp requires CASSINI_HARNESS_CASSINI_MODE=installed-exapp" >&2
        return 1
      fi
      if [[ -z "$effective_recording_url" ]]; then
        effective_recording_url="http://reverse-proxy/index.php/apps/app_api/proxy/gocassini"
      fi
      ;;
  esac

  log "Configuring Talk recording backend ($backend): $effective_recording_url"
  recording_json=$(printf '{"servers":[{"server":"%s","verify":false}],"secret":"%s"}' "$effective_recording_url" "$CASSINI_TALK_RECORDING_SECRET")
  occ config:app:set spreed recording_servers --value="$recording_json"
  occ config:app:set spreed call_recording --value="yes"
}

harness_bootstrap_core_nextcloud
harness_configure_talk_media
harness_configure_recording_backend

log "Bootstrap complete"
