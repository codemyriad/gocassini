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
  # Installing them here mirrors the one-click app-store install a production
  # admin does. Since D-616 they are the prerequisites of the ACCESS-CONTROLLED
  # storage mode rather than of Cassini as such: without them Cassini runs in
  # its default mode instead. The harness installs them because that is the mode
  # the e2e suites assert.
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

# The recordings substrate the access-controlled storage mode needs, which
# Cassini no longer builds for itself (D-616).
#
# It used to: the ExApp created the `cassini` service account, its narrow owner
# group and the Team folder on its enabled edge, so a harness that installed the
# two apps was enough. Now the app only CHECKS and reports what is missing, so
# the environment has to supply the same things a production administrator does
# — and the harness IS that administrator here.
#
# Every step is idempotent and none of them is fatal on its own. A box without
# app-store reachability has already failed the app check above; a box where
# these fail leaves Cassini in its default storage mode, which is a legitimate
# deployment and says so on /operator/status rather than breaking silently. The
# e2e suites that require access control assert `recordings_access.ok` and will
# fail loudly there.
harness_bootstrap_recordings_substrate() {
  local owner="cassini" mount="Cassini" everyone="everyone" folder_id=""

  log "Ensuring the ${owner} recordings service account"
  occ_ignore_failure group:add "$owner" >/dev/null 2>&1
  if occ user:info "$owner" >/dev/null 2>&1; then
    log "Recordings service account already exists: $owner"
  else
    log "Creating recordings service account: $owner"
    # The password only satisfies `occ user:add`. Nothing authenticates with it:
    # every Cassini call acts as this account through AppAPI's act-as-user
    # header, which is signed with the app secret.
    OC_PASS="cassini-service-account-$(date +%s)"
    export OC_PASS
    occ_ignore_failure user:add --password-from-env --display-name="Cassini recordings" \
      --group="$owner" "$owner" >/dev/null 2>&1
    unset OC_PASS
  fi
  occ_ignore_failure group:adduser "$owner" "$owner" >/dev/null 2>&1

  log "Ensuring the ${mount} Team folder"
  folder_id="$(harness_groupfolder_id "$mount")"
  if [[ -z "$folder_id" ]]; then
    occ_ignore_failure groupfolders:create "$mount" >/dev/null 2>&1
    folder_id="$(harness_groupfolder_id "$mount")"
  fi
  if [[ -z "$folder_id" ]]; then
    log "WARNING: no ${mount} Team folder; Cassini will run in its default storage mode"
    return 0
  fi

  # The owner group gets a write-capable mount; the virtual all-users group gets
  # read, which is what lets every account traverse to the recordings it is
  # granted. Advanced ACL supplies the default-deny floor, and the service
  # account is delegated as its manager so it can write each recording's
  # audience. This is exactly the recipe Cassini's Setup tab prints.
  occ_ignore_failure groupfolders:group "$folder_id" "$owner" read write share delete >/dev/null 2>&1
  occ_ignore_failure groupfolders:group "$folder_id" "$everyone" read >/dev/null 2>&1
  occ_ignore_failure groupfolders:permissions "$folder_id" --enable >/dev/null 2>&1
  occ_ignore_failure groupfolders:permissions "$folder_id" -m --user "$owner" >/dev/null 2>&1
  log "Recordings substrate ready: folder=${folder_id} mount=${mount} owner=${owner}"
}

# harness_groupfolder_id prints the id of the Team folder at a mount point, or
# nothing. `occ groupfolders:list` keys the mount as `mountPoint`; the HTTP API
# calls the same value `mount_point`. Both are accepted so this does not become
# a version trap.
harness_groupfolder_id() {
  local mount="$1"
  occ groupfolders:list --output=json_pretty 2>/dev/null \
    | jq -r --arg mp "$mount" \
        '[.[] | select((.mountPoint // .mount_point) == $mp) | .id] | sort | .[0] // empty' \
        2>/dev/null || true
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
harness_bootstrap_recordings_substrate
harness_configure_talk_media
harness_configure_recording_backend

log "Bootstrap complete"
