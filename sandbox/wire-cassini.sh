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
  # Nextcloud over HTTP, never occ). Since D-616 they are prerequisites of the
  # ACCESS-CONTROLLED storage model rather than of Cassini, and this box runs
  # that model deliberately — see CASSINI_STORAGE_MODE below. For a dogfood
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
    --env "CASSINI_STORAGE_MODE=${CASSINI_STORAGE_MODE:-access_controlled}" \
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
}

# --- Run ---------------------------------------------------------------------

require_state
require_aio
if [[ "$REGISTER_ONLY" != "true" ]]; then
  ensure_harp
fi
resolve_info_xml
register_cassini
handoff_talk_recording
reset_admin_password
verify

cat <<EOF

Cassini wired onto AIO.

  Nextcloud:  $PUBLIC_URL
  Cassini:    open the "Cassini" entry in the top bar (admins get the operator surface)
  Source:     $CASSINI_INSTALL_SOURCE
  State dir:  $STATE_DIR  (HaRP shared key)
  Talk secret: self-generated by Cassini (D-447), registered in spreed automatically
EOF
