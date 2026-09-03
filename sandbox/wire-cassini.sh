#!/usr/bin/env bash
# wire-cassini.sh — install/update Cassini on a Nextcloud All-in-One (AIO) host.
#
# The demo sandbox runs on Nextcloud AIO (the substrate a real admin runs), NOT
# the CI/dev harness. AIO owns Nextcloud, Postgres, the Talk HPB (signaling +
# Janus + TURN), and the reverse-proxy apache. This script owns only the
# Cassini-specific wiring on top of a already-provisioned AIO instance:
#
#   1. enable AppAPI
#   2. run + register a HaRP deploy daemon beside AIO   (see "Why manual HaRP")
#   3. install Cassini from the App Store (or a given image) via that daemon
#   4. wire Talk recording the zero-config way (D-447): install with NO recording
#      secret so Cassini self-generates one, read it back from the ExApp, and
#      point spreed's recording backend at Cassini. Only AIO's HPB INTERNAL_SECRET
#      (which cannot be generated) is injected, read from the AIO Talk container.
#   5. (optional) reset the admin password to SANDBOX_NC_ADMIN_PASSWORD
#
# It is idempotent: re-run it to update Cassini or reconcile config. The one-time
# host setup (install AIO, enable the Talk container, the Caddy /exapps route,
# and opening firewall port 3478) lives in sandbox/README.md — this script
# verifies those are in place and fails loudly with the fix if they are not.
#
# Why manual HaRP: AIO does not yet expose the HaRP deploy-daemon container in
# its UI (the AppAPI HaRP integration is gated behind a newer AIO/Talk bundle).
# Until it does, we run HaRP ourselves and point the host reverse proxy's
# /exapps/* at it. When AIO ships HaRP, steps 2 + the /exapps route collapse into
# "tick the HaRP box" and Cassini installs one-click from the store.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=sandbox/lib-store-release.sh
source "$SCRIPT_DIR/lib-store-release.sh"

ENV_FILE="$SCRIPT_DIR/.env"
if [[ -f "$ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
fi

# --- Configuration -----------------------------------------------------------

SANDBOX_DOMAIN="${SANDBOX_DOMAIN:-demo.nextcloud.codemyriad.io}"
SANDBOX_SCHEME="${SANDBOX_SCHEME:-https}"
PUBLIC_URL="${SANDBOX_PUBLIC_URL:-$SANDBOX_SCHEME://$SANDBOX_DOMAIN}"

# AIO container / network names (AIO defaults; override only for a custom AIO).
AIO_NEXTCLOUD="${AIO_NEXTCLOUD_CONTAINER:-nextcloud-aio-nextcloud}"
AIO_TALK="${AIO_TALK_CONTAINER:-nextcloud-aio-talk}"
AIO_NETWORK="${AIO_NETWORK:-nextcloud-aio}"

# HaRP-beside-AIO daemon.
HARP_IMAGE="${HARP_IMAGE:-ghcr.io/nextcloud/nextcloud-appapi-harp:release}"
HARP_CONTAINER="${HARP_CONTAINER:-appapi-harp}"
HARP_DAEMON="${HARP_DAEMON:-harp_aio}"
HARP_HTTP_PORT="${HARP_HTTP_PORT:-8780}"   # /exapps HTTP frontend the reverse proxy targets
HARP_FRP_PORT="${HARP_FRP_PORT:-8782}"     # frpc tunnel the ExApp dials back on

# Where to source Cassini from: "store" (latest published release) or "image".
CASSINI_INSTALL_SOURCE="${CASSINI_INSTALL_SOURCE:-store}"
# Source-side audio capture (docs/source-audio-capture.md). Both switches are
# passed to AppAPI at registration exactly as a production install would set
# them, and both default to ON: this branch exists to run the feature, so a
# deploy that says nothing gets it. Opting out is per deploy and explicit —
#
#   CASSINI_SOURCE_CAPTURE=0 sandbox/wire-cassini.sh --image ...
#
# — and turns the companion off again on an instance a previous deploy turned
# it on.
CASSINI_SOURCE_CAPTURE="${CASSINI_SOURCE_CAPTURE:-1}"
CASSINI_SOURCE_AUDIO_INGEST="${CASSINI_SOURCE_AUDIO_INGEST:-1}"
CAPTURE_COMPANION_ID="cassini_capture"
CASSINI_EXAPP_IMAGE="${CASSINI_EXAPP_IMAGE:-ghcr.io/codemyriad/gocassini:latest}"
CASSINI_APPSTORE_ID="${CASSINI_APPSTORE_ID:-gocassini}"
CASSINI_APPSTORE_CATALOG_URL="${CASSINI_APPSTORE_CATALOG_URL:-https://apps.nextcloud.com/api/v1/appapi_apps.json}"
# `beta` makes AppAPI's ExApp store accept pre-release (alpha/beta/rc) apps.
SANDBOX_UPDATE_CHANNEL="${SANDBOX_UPDATE_CHANNEL:-beta}"

# Persistent state (the HaRP shared key) outside the repo. A fixed
# shared path (not per-user $HOME) so every operator and CI share ONE HaRP key —
# otherwise a second user's run would regenerate the key and break the daemon.
# Create it once, writable by all deploy users (see require_state below / README).
STATE_DIR="${CASSINI_AIO_STATE:-/opt/cassini-aio}"

REGISTER_ONLY=false
RESET_HARP=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --from-store)    CASSINI_INSTALL_SOURCE=store; shift ;;
    --image)         CASSINI_EXAPP_IMAGE="$2"; CASSINI_INSTALL_SOURCE=image; shift 2 ;;
    --register-only) REGISTER_ONLY=true; shift ;;
    --reset)         RESET_HARP=true; shift ;;
    -h|--help)
      cat <<EOF
Usage: sandbox/wire-cassini.sh [options]

Installs/updates Cassini on an already-provisioned Nextcloud AIO host
(see sandbox/README.md for the one-time AIO + reverse-proxy + firewall setup).

Options:
  --from-store        Install the latest published release from the App Store (default)
  --image IMAGE       Register this container image instead of the store release
  --register-only     Only (re-)register Cassini; skip HaRP/daemon setup
  --reset             Rebuild the HaRP container + daemon from scratch (use after
                      a HaRP key change or a broken/half-configured daemon)
  -h, --help          Show this help

Source-side audio capture is ON by default on this branch: participants'
browser-captured audio is collected, and uploads may replace the recorded track
in transcripts. Opt out per deploy with the environment, which is also what a
production install would set (docs/source-audio-capture.md):

  CASSINI_SOURCE_CAPTURE=0        collect nothing; retire the companion app
  CASSINI_SOURCE_AUDIO_INGEST=0   keep collecting, but keep it out of transcripts

Installing the cassini_capture companion needs --image: it is built from this
checkout and must carry the same version as the deployed ExApp. A --from-store
deploy therefore registers CASSINI_SOURCE_CAPTURE=0 and says so — a switch that
answers yes with no companion to deliver the payload is worse than one that
answers no.
EOF
      exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done

# Transient work (downloaded catalog/tarball, extracted store dir, rendered
# info.xml) goes in a private per-run temp dir — never in STATE_DIR, so leftovers
# owned by one user can't block another user's / CI's next run. STATE_DIR holds
# only the two persistent secrets.
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/cassini-wire.XXXXXX")"
trap 'rm -rf "$WORK_DIR"' EXIT

log()  { printf '\n\033[1;34m==>\033[0m \033[1m%s\033[0m\n' "$*"; }
die()  { printf '\n\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }
occ()  { docker exec --user www-data "$AIO_NEXTCLOUD" php occ "$@"; }

# bool_on reads one source-capture switch the way the operator's
# parseBoolEnvDefault does (cassini-operator/internal/operator/exapp.go): blank
# is the default (on), the exact strings Go's strconv.ParseBool calls true are
# on, and everything else — an explicit false, or a value it cannot read at
# all — is off.
#
# This is not the last word on any value; resolve_capture_switches is, and it
# rewrites what the ExApp is given into 1 or 0 so that only one of the two ever
# has to parse anything. Mirroring Go's parser in shell exactly is not possible
# to do safely — strings.TrimSpace trims Unicode space that bash's
# [[:space:]] leaves alone, for one — and a disagreement means retiring the
# companion while the ExApp goes on accepting uploads.
bool_on() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"   # trim leading whitespace
  value="${value%"${value##*[![:space:]]}"}"   # trim trailing whitespace
  case "$value" in
    ''|1|t|T|TRUE|true|True) return 0 ;;
    *)                       return 1 ;;
  esac
}

# resolve_capture_switches settles what this deploy will register, BEFORE
# anything is registered, so the ExApp's switch and the companion's presence can
# never contradict each other.
#
# It does two things. It canonicalises both switches to 1 or 0, so what AppAPI
# is handed is the value this script acted on and no second parser can read it
# differently. And it turns collection off on a store deploy: capture is on by
# default, but the companion may only be built from the checkout that produced
# the deployed image (see install_capture_companion), so a store deploy cannot
# deliver the payload to Talk at all. Leaving the switch on there would register
# an installation answering "yes, capture" to any browser that asks — including
# a call still running with a payload from a previous image deploy — while this
# script quietly retires the companion underneath it.
resolve_capture_switches() {
  local raw_capture="$CASSINI_SOURCE_CAPTURE" raw_ingest="$CASSINI_SOURCE_AUDIO_INGEST"
  if bool_on "$raw_capture"; then CASSINI_SOURCE_CAPTURE=1; else CASSINI_SOURCE_CAPTURE=0; fi
  if bool_on "$raw_ingest"; then CASSINI_SOURCE_AUDIO_INGEST=1; else CASSINI_SOURCE_AUDIO_INGEST=0; fi
  [[ "$raw_capture" == "$CASSINI_SOURCE_CAPTURE" ]] \
    || log "Read CASSINI_SOURCE_CAPTURE=$(printf '%q' "$raw_capture") as $CASSINI_SOURCE_CAPTURE, and that is what will be registered"
  [[ "$raw_ingest" == "$CASSINI_SOURCE_AUDIO_INGEST" ]] \
    || log "Read CASSINI_SOURCE_AUDIO_INGEST=$(printf '%q' "$raw_ingest") as $CASSINI_SOURCE_AUDIO_INGEST, and that is what will be registered"

  if capture_on && [[ "$CASSINI_INSTALL_SOURCE" != "image" ]]; then
    log "Source capture is on, but the $CAPTURE_COMPANION_ID companion can only be built from the checkout that produced the image, and this is a store install. Registering CASSINI_SOURCE_CAPTURE=0: nothing would reach Talk's pages anyway, and a switch that says yes with no companion is worse than one that says no. Deploy with --image to capture."
    CASSINI_SOURCE_CAPTURE=0
  fi
}

require_state() {
  mkdir -p "$STATE_DIR" 2>/dev/null || true
  [[ -w "$STATE_DIR" ]] || die "State dir '$STATE_DIR' is not writable by $(id -un).
Create it once so every deploy user (and CI) shares one HaRP key, e.g.:
  sudo install -d -g docker -m 2770 $STATE_DIR
(setgid so files inherit the docker group; every user who deploys Cassini is
already in that group), or set
CASSINI_AIO_STATE to a path you can write."
}

# --- Preconditions -----------------------------------------------------------

require_aio() {
  log "Checking AIO substrate"
  docker inspect "$AIO_NEXTCLOUD" >/dev/null 2>&1 \
    || die "AIO Nextcloud container '$AIO_NEXTCLOUD' not found. Provision AIO first — see sandbox/README.md."
  docker inspect "$AIO_TALK" >/dev/null 2>&1 \
    || die "AIO Talk container '$AIO_TALK' not found. Enable the Talk container in the AIO interface — see sandbox/README.md."
  occ status >/dev/null 2>&1 || die "occ not responding in '$AIO_NEXTCLOUD'."
}

# --- HaRP daemon -------------------------------------------------------------

harp_run_container() {
  # 8780 is published on loopback so the host reverse proxy (Caddy) can route
  # /exapps/* to it; the FRP tunnel (8782) and docker socket stay on the
  # internal AIO network.
  local key="$1"
  docker rm -f "$HARP_CONTAINER" >/dev/null 2>&1 || true
  docker run -d \
    --name "$HARP_CONTAINER" -h "$HARP_CONTAINER" \
    --network "$AIO_NETWORK" \
    --restart unless-stopped \
    -p "127.0.0.1:${HARP_HTTP_PORT}:${HARP_HTTP_PORT}" \
    -e HP_SHARED_KEY="$key" \
    -e NC_INSTANCE_URL="$PUBLIC_URL" \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v appapi_harp_certs:/certs \
    "$HARP_IMAGE" >/dev/null
  sleep 4
}

ensure_harp() {
  log "Ensuring HaRP deploy daemon"
  occ app:enable app_api >/dev/null 2>&1 || true

  local keyfile="$STATE_DIR/harp_shared_key"
  # 660: readable/writable by the STATE_DIR group (docker) so any deploy user or
  # CI shares the key. With a setgid STATE_DIR the group is inherited (see README).
  [[ -s "$keyfile" ]] || { openssl rand -hex 32 > "$keyfile"; chmod 660 "$keyfile"; }
  local key; key="$(cat "$keyfile")"

  local daemon_exists=false container_up=false
  occ app_api:daemon:list 2>/dev/null | grep -q "$HARP_DAEMON" && daemon_exists=true
  [[ "$(docker inspect -f '{{.State.Running}}' "$HARP_CONTAINER" 2>/dev/null)" == "true" ]] && container_up=true

  # Healthy and not forcing a reset: leave it alone. Recreating the container
  # here would give it a fresh key while the daemon config keeps the old one —
  # a key mismatch that breaks the frpc tunnel (502). Idempotent no-op instead.
  if [[ "$RESET_HARP" != "true" && "$daemon_exists" == "true" && "$container_up" == "true" ]]; then
    log "HaRP daemon '$HARP_DAEMON' + container already present; leaving as-is"
    return 0
  fi

  # (Re)establish a consistent container + daemon (both keyed with $key). The
  # daemon can't be removed while an ExApp is attached, so drop the ExApp first
  # (--force clears AppAPI's record even if the old container is unreachable; it
  # is re-registered later), then the daemon.
  log "(Re)establishing HaRP container + daemon"
  if occ app_api:app:list 2>/dev/null | grep -qi "$CASSINI_APPSTORE_ID"; then
    occ app_api:app:unregister "$CASSINI_APPSTORE_ID" --force >/dev/null 2>&1 || true
    docker rm -f "nc_app_${CASSINI_APPSTORE_ID}" >/dev/null 2>&1 || true
  fi
  occ app_api:daemon:unregister "$HARP_DAEMON" >/dev/null 2>&1 || true

  harp_run_container "$key"

  occ app_api:daemon:register \
    "$HARP_DAEMON" "HaRP (AIO)" \
    docker-install http "${HARP_CONTAINER}:${HARP_HTTP_PORT}" "$PUBLIC_URL" \
    --net="$AIO_NETWORK" \
    --harp \
    --harp_frp_address "${HARP_CONTAINER}:${HARP_FRP_PORT}" \
    --harp_shared_key "$key" \
    --set-default \
    --compute_device=cpu
}

# --- Talk secret from AIO ----------------------------------------------------

aio_talk_internal_secret() {
  docker exec "$AIO_TALK" printenv INTERNAL_SECRET 2>/dev/null \
    || die "Could not read INTERNAL_SECRET from '$AIO_TALK'."
}

# --- Cassini source (store release or image) ---------------------------------

resolve_info_xml() {
  local out="$WORK_DIR/gocassini-info.xml"
  if [[ "$CASSINI_INSTALL_SOURCE" == "store" ]]; then
    log "Resolving latest $CASSINI_APPSTORE_ID release from the App Store"
    local catalog="$WORK_DIR/appapi_apps.json"
    curl -fsSL "$CASSINI_APPSTORE_CATALOG_URL" -o "$catalog"
    local url; url="$(store_latest_release_url "$CASSINI_APPSTORE_ID" "$catalog")" \
      || die "No $CASSINI_APPSTORE_ID release found in $CASSINI_APPSTORE_CATALOG_URL"
    log "Latest release artifact: $url"
    local tgz="$WORK_DIR/gocassini-store.tar.gz" ex="$WORK_DIR/store-extract"
    curl -fsSL "$url" -o "$tgz"; rm -rf "$ex"; mkdir -p "$ex"; tar xzf "$tgz" -C "$ex"
    local info; info="$(find "$ex" -name info.xml | head -n1)"
    [[ -n "$info" ]] || die "info.xml not found in store artifact"
    cp "$info" "$out"
  else
    log "Rendering info.xml for image $CASSINI_EXAPP_IMAGE"
    local ref="$CASSINI_EXAPP_IMAGE" reg img tag without
    tag="${ref##*:}"; without="${ref%:*}"
    if [[ "$without" == "$ref" || "$without" != */* ]]; then tag="latest"; without="$ref"; fi
    reg="${without%%/*}"; img="${without#*/}"
    if [[ "$reg" != *.* && "$reg" != *:* && "$reg" != "localhost" ]]; then reg="docker.io"; img="$without"; fi
    cp "$SCRIPT_DIR/../appinfo/info.xml" "$out"
    perl -0pi -e "s#<registry>.*?</registry>#<registry>$reg</registry>#s; s#<image>.*?</image>#<image>$img</image>#s; s#<image-tag>.*?</image-tag>#<image-tag>$tag</image-tag>#s" "$out"
  fi
  # Pre-pull the pinned image on the host engine so --wait-finish doesn't stall.
  local pinned; pinned="$(perl -0ne 'print "$1/$2:$3" if m#<registry>(.*?)</registry>.*?<image>(.*?)</image>.*?<image-tag>(.*?)</image-tag>#s' "$out")"
  log "Pre-pulling $pinned"
  docker pull "$pinned" >/dev/null 2>&1 || true
}

register_cassini() {
  log "Registering Cassini ExApp"
  # The Talk RECORDING secret is deliberately NOT set here: this is the default,
  # zero-config install path (D-447). Cassini generates and persists its own
  # recording secret on first start; handoff_talk_recording reads it back and
  # registers it in spreed. Only the HPB INTERNAL secret is injected — it must
  # equal AIO's existing INTERNAL_SECRET (it cannot be generated), so we read it
  # from the AIO Talk container. No human sets any secret.
  local int_secret; int_secret="$(aio_talk_internal_secret)"

  # Two prerequisites the Cassini ExApp cannot install for itself (it reaches
  # Nextcloud over HTTP, never occ), and since D-554 neither is optional:
  # recordings are access-controlled or they are not served. For a dogfood
  # instance that people actually keep recordings in, a silent miss means
  # nobody can see anything, so enable is hard here on purpose.
  #
  # The two fail differently, which is why both are hard. Without groupfolders
  # you get a visible folder-creation failure. Without group_everyone the
  # provisioner returns before the folder is ever created — a silent no-op
  # provision, strictly harder to diagnose.
  occ app:install groupfolders >/dev/null 2>&1 || true
  occ app:enable groupfolders
  occ app:install group_everyone >/dev/null 2>&1 || true
  occ app:enable group_everyone

  occ config:system:set updater.release.channel --value "$SANDBOX_UPDATE_CHANNEL" >/dev/null

  docker cp "$WORK_DIR/gocassini-info.xml" "$AIO_NEXTCLOUD:/tmp/gocassini-info.xml"
  docker exec -u root "$AIO_NEXTCLOUD" chown www-data:www-data /tmp/gocassini-info.xml

  # A stale registration holds a secret the freshly-deployed container no longer
  # shares, which 401s at /init. Unregister first (keep the data volume).
  if occ app_api:app:list 2>/dev/null | grep -qi "$CASSINI_APPSTORE_ID"; then
    occ app_api:app:unregister "$CASSINI_APPSTORE_ID" || true
  fi
  occ app_api:app:register "$CASSINI_APPSTORE_ID" "$HARP_DAEMON" \
    --info-xml /tmp/gocassini-info.xml \
    --env "CASSINI_TALK_SIGNALING_INTERNAL_SECRET=$int_secret" \
    --env "CASSINI_TALK_BACKEND_URL=$PUBLIC_URL" \
    --env "CASSINI_PUBLISH_SINK=${CASSINI_PUBLISH_SINK:-nextcloud-files}" \
    --env "CASSINI_SOURCE_CAPTURE=$CASSINI_SOURCE_CAPTURE" \
    --env "CASSINI_SOURCE_AUDIO_INGEST=$CASSINI_SOURCE_AUDIO_INGEST" \
    --wait-finish || true
  # --wait-finish can outlive its window on first deploy; ensure enabled.
  occ app_api:app:enable "$CASSINI_APPSTORE_ID" || true
}

# cassini_recording_secret reads the recording secret Cassini generated and
# persisted on the ExApp's AppAPI volume (D-447). Reading it from the container
# rather than the provisioning HTTP endpoint keeps the deploy auth-free (no admin
# password needed) and self-contained for CI. The find fallback tolerates a
# change in the operator's on-volume data layout.
cassini_recording_secret() {
  local c="nc_app_$CASSINI_APPSTORE_ID"
  docker exec "$c" sh -c '
    f="$APP_PERSISTENT_STORAGE/operator/talk-provisioning.json"
    [ -s "$f" ] || f="$(find "$APP_PERSISTENT_STORAGE" -name talk-provisioning.json 2>/dev/null | head -n1)"
    [ -n "$f" ] && cat "$f"
  ' 2>/dev/null | perl -0ne 'print $1 if /"recording_secret"\s*:\s*"([^"]+)"/'
}

handoff_talk_recording() {
  log "Handing Talk recording to Cassini"
  local rec_secret; rec_secret="$(cassini_recording_secret)"
  [[ -n "$rec_secret" ]] \
    || die "Could not read Cassini's generated recording secret from 'nc_app_$CASSINI_APPSTORE_ID'. Is the ExApp registered and running?"
  occ config:app:set spreed recording_servers --value \
    "{\"servers\":[{\"server\":\"$PUBLIC_URL/index.php/apps/app_api/proxy/$CASSINI_APPSTORE_ID\",\"verify\":true}],\"secret\":\"$rec_secret\"}" >/dev/null
  occ config:app:set spreed call_recording --value yes >/dev/null
}

# capture_on is only meaningful after resolve_capture_switches has canonicalised
# the variable, which is why it compares rather than parses: past that point the
# value this script holds is exactly the value the ExApp is registered with.
capture_on() { [[ "$CASSINI_SOURCE_CAPTURE" == "1" ]]; }

# companion_enabled answers whether Nextcloud currently has the companion app
# enabled. The listing is taken on its own line, because inside an
# `if occ ... | grep -q` the shell suspends set -e and an occ that failed would
# read as "not enabled" — which is the answer that skips the retirement.
companion_enabled() {
  local listing
  listing="$(occ app:list 2>/dev/null)" \
    || die "Could not read Nextcloud's app list, so whether $CAPTURE_COMPANION_ID is enabled is unknown. Refusing to guess."
  printf '%s\n' "$listing" | sed -n '/^Enabled:/,/^Disabled:/p' | grep -q "  - ${CAPTURE_COMPANION_ID}:"
}

# install_capture_companion installs or retires the native companion app that
# delivers the capture payload to Talk's call page. The ExApp cannot do this
# for itself: it reaches Nextcloud over HTTP, never occ, and a native app can
# only be placed by something with the filesystem.
#
# The payload is taken from the RUNNING ExApp container rather than built here.
# That keeps the sandbox free of a Node toolchain, and it makes the companion
# carry byte-for-byte the script the image serves at /ui/capture-payload.js —
# the harness leg proves that identity and this deploy inherits it.
#
# Lockstep is why this needs --image. The companion manifest must carry the
# same version as the ExApp's (scripts/test-info-schema.sh enforces it in CI),
# and only a checkout that produced the image is guaranteed to match it. A
# store release was cut from some other commit, so its version and this
# checkout's companion would drift the first time they differed. That is why
# resolve_capture_switches turns collection off on a store deploy: this function
# then simply takes the retire path.
install_capture_companion() {
  if ! capture_on; then
    # Backing out completely means the companion goes too: it is a separate
    # app that outlives the ExApp's own switch and keeps injecting the payload
    # into every Talk call page until it is disabled.
    #
    # After register_cassini, deliberately, and that order is the documented one
    # (docs/exapp-install.md): the ExApp learns capture is off first, which
    # reaches calls already in progress through the payload's 30-second poll,
    # and only then does the payload stop being delivered to new page loads.
    if companion_enabled; then
      log "Source capture is off; disabling $CAPTURE_COMPANION_ID so Talk pages stop carrying the payload"
      # Not swallowed. A companion left enabled while capture is being turned
      # off is the one outcome this branch exists to prevent, and a deploy that
      # cannot achieve it has to say so rather than report success.
      occ app:disable "$CAPTURE_COMPANION_ID" >/dev/null \
        || die "Could not disable $CAPTURE_COMPANION_ID. Talk pages are still being given the capture payload; disable it by hand before treating this host as capture-free."
    fi
    return 0
  fi

  log "Installing $CAPTURE_COMPANION_ID from the payload the deployed image serves"
  local c="nc_app_$CASSINI_APPSTORE_ID"
  local payload="$WORK_DIR/capture-payload.js"
  docker exec "$c" cat /opt/cassini/cassini-app/dist/capture/capture-payload.js >"$payload" 2>/dev/null \
    || die "Could not read the capture payload from '$c'. Is the ExApp registered and running, and does this image carry it?"
  [[ -s "$payload" ]] || die "The capture payload read from '$c' is empty"

  "$SCRIPT_DIR/../scripts/build-capture-companion.sh" \
    --payload "$payload" \
    --staging "$WORK_DIR/companion" \
    --output "$WORK_DIR/$CAPTURE_COMPANION_ID.tar.gz" >"$WORK_DIR/build-companion.log" 2>&1 \
    || { cat "$WORK_DIR/build-companion.log" >&2; die "Building the $CAPTURE_COMPANION_ID package failed"; }

  # Replace, never merge: a leftover file from an older version is exactly
  # the kind of drift the lockstep check exists to prevent.
  docker exec -u root "$AIO_NEXTCLOUD" rm -rf "/var/www/html/custom_apps/$CAPTURE_COMPANION_ID"
  docker cp "$WORK_DIR/companion/$CAPTURE_COMPANION_ID" "$AIO_NEXTCLOUD:/var/www/html/custom_apps/"
  docker exec -u root "$AIO_NEXTCLOUD" chown -R www-data:www-data "/var/www/html/custom_apps/$CAPTURE_COMPANION_ID"
  occ app:enable "$CAPTURE_COMPANION_ID" >/dev/null
}

reset_admin_password() {
  [[ -n "${SANDBOX_NC_ADMIN_PASSWORD:-}" ]] || { log "SANDBOX_NC_ADMIN_PASSWORD unset; leaving admin password as-is"; return 0; }
  log "Reconciling admin password to SANDBOX_NC_ADMIN_PASSWORD"
  docker exec --user www-data -e OC_PASS="$SANDBOX_NC_ADMIN_PASSWORD" "$AIO_NEXTCLOUD" \
    php occ user:resetpassword --password-from-env admin >/dev/null
}

verify() {
  log "Verifying"
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' "$PUBLIC_URL/index.php/apps/app_api/proxy/$CASSINI_APPSTORE_ID/api/v1/welcome")"
  if [[ "$code" == "200" ]]; then
    printf '  ok   welcome endpoint (200)\n'
  else
    printf '  FAIL welcome endpoint returned %s\n' "$code" >&2
    printf '       If 502: the reverse proxy is not routing /exapps/* to HaRP:%s.\n' "$HARP_HTTP_PORT" >&2
    printf '       Add to the Caddyfile for %s (see sandbox/README.md):\n' "$SANDBOX_DOMAIN" >&2
    printf '         handle /exapps/* { reverse_proxy 127.0.0.1:%s }\n' "$HARP_HTTP_PORT" >&2
  fi
  occ app_api:app:list 2>/dev/null | grep -i "$CASSINI_APPSTORE_ID" || true
  # What the CONTAINER carries, not what this script decided. Registration
  # tolerates its own failure above (--wait-finish can outlive its window on a
  # first deploy), so a stale container still holding the previous switch is a
  # real outcome — and it is precisely the one where an opt-out deploy reports
  # success while a payload already loaded in somebody's browser goes on
  # capturing and uploading.
  local exapp_container="nc_app_$CASSINI_APPSTORE_ID" exapp_env
  exapp_env="$(docker inspect "$exapp_container" --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null)" \
    || die "Could not read the environment of '$exapp_container', so which source-capture switch is actually running is unknown. Registration may have failed; re-run the deploy."
  grep -qx "CASSINI_SOURCE_CAPTURE=$CASSINI_SOURCE_CAPTURE" <<<"$exapp_env" \
    || die "The running ExApp does not carry CASSINI_SOURCE_CAPTURE=$CASSINI_SOURCE_CAPTURE. A container from an earlier deploy survived registration and is still answering with its own switch; re-run the deploy before trusting this host's capture state."
  grep -qx "CASSINI_SOURCE_AUDIO_INGEST=$CASSINI_SOURCE_AUDIO_INGEST" <<<"$exapp_env" \
    || die "The running ExApp does not carry CASSINI_SOURCE_AUDIO_INGEST=$CASSINI_SOURCE_AUDIO_INGEST. A container from an earlier deploy survived registration; re-run the deploy."
  printf '  ok   ExApp carries CASSINI_SOURCE_CAPTURE=%s CASSINI_SOURCE_AUDIO_INGEST=%s\n' \
    "$CASSINI_SOURCE_CAPTURE" "$CASSINI_SOURCE_AUDIO_INGEST"

  # resolve_capture_switches has already ruled out "capture on without a
  # companion", so these two are the only outcomes: on with the companion
  # enabled, or off with it gone. Either contradiction is one this run just
  # created and could not repair, so it fails the deploy rather than printing a
  # FAIL the caller has to read — unlike the welcome check above, whose remedy
  # is on the host's reverse proxy and not in this script's hands.
  if capture_on; then
    companion_enabled \
      || die "Capture is on but $CAPTURE_COMPANION_ID is not enabled: no Talk page will carry the payload, so nothing would be captured."
    printf "  ok   %s enabled; capture follows Talk's recording per docs/source-audio-capture.md \"Trying it\"\n" "$CAPTURE_COMPANION_ID"
  elif companion_enabled; then
    die "Capture is off but $CAPTURE_COMPANION_ID is still enabled: Talk pages are still being given the capture payload. Disable it by hand before treating this host as capture-free."
  fi
}

# --- Run ---------------------------------------------------------------------

require_state
require_aio
if [[ "$REGISTER_ONLY" != "true" ]]; then
  ensure_harp
fi
resolve_info_xml
resolve_capture_switches
register_cassini
handoff_talk_recording
install_capture_companion
reset_admin_password
verify

cat <<EOF

Cassini wired onto AIO.

  Nextcloud:  $PUBLIC_URL
  Cassini:    open the "Cassini" entry in the top bar (admins get the operator surface)
  Source:     $CASSINI_INSTALL_SOURCE
  Capture:    collect=$CASSINI_SOURCE_CAPTURE ingest=$CASSINI_SOURCE_AUDIO_INGEST  (docs/source-audio-capture.md)
  State dir:  $STATE_DIR  (HaRP shared key)
  Talk secret: self-generated by Cassini (D-447), registered in spreed automatically
EOF
