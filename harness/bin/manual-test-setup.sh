#!/usr/bin/env bash
#
# Cassini App Store Dogfood Testbed
#
# Stands up a pristine Nextcloud instance, a local Docker registry, and a
# mock Nextcloud App Store catalog server. The mock catalog advertises
# Cassini exactly as apps.nextcloud.com would after submission, pointing
# AppAPI at a locally-built gocassini image hosted in the local registry.
#
# After this script finishes, Cassini is installed and enabled as a real
# AppAPI ExApp. AppAPI pulls the locally tagged image, spawns the ExApp
# container on the host Docker socket, wires up proxy routes, and Talk points
# its recording backend at the AppAPI proxy path.
#
# This is the same flow that will run against the production App Store
# once Cassini is submitted; only the catalog/install trigger changes.
#
# Usage:
#   ./harness/bin/manual-test-setup.sh                # use existing image and install ExApp
#   ./harness/bin/manual-test-setup.sh --build        # force rebuild and install ExApp
#   ./harness/bin/manual-test-setup.sh --no-install   # stop after preparing the harness
#

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
export PROJECT_NAME="cassini-exapp-test"
export CASSINI_HARNESS_CASSINI_MODE="${CASSINI_HARNESS_CASSINI_MODE:-installed-exapp}"
export CASSINI_HARNESS_RECORDING_BACKEND="${CASSINI_HARNESS_RECORDING_BACKEND:-installed-exapp}"
# SPREED_PROFILE controls which compose profiles get pulled in. Default
# brings up the install-flow stack (NC, db, HaRP, reverse-proxy). Set
# SPREED_PROFILE=full before invoking this script to also bring up
# signaling/Janus/TURN/coturn so the Talk record button can actually
# record a call end-to-end via the ExApp.
export SPREED_PROFILE="${SPREED_PROFILE:-default}"

# Load the shared dev-only Talk/signaling defaults so the values written into
# Talk by bootstrap.sh match the values passed into the installed ExApp.
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"
harness_stack_init
# Ensure a caller override of CASSINI_TALK_SIGNALING_INTERNAL_SECRET is also
# what the local signaling server uses for HPB internal auth.
export SIGNALING_INTERNAL_SECRET="$CASSINI_TALK_SIGNALING_INTERNAL_SECRET"

# On a loopback harness host (no VM), Talk advertises http://127.0.0.1:28080 as
# its recording backend. The installed ExApp resolves that to its OWN container
# loopback and cannot reach Nextcloud -> the recorder fails to join with
# "connection refused". Default the backend override to the reverse-proxy the
# ExApp container can actually reach. VM/LAN runs use a routable
# CASSINI_HARNESS_HOST and don't need this.
if [[ -z "${CASSINI_TALK_BACKEND_URL:-}" ]] \
  && [[ "${CASSINI_HARNESS_HOST:-127.0.0.1}" == "127.0.0.1" || "${CASSINI_HARNESS_HOST:-}" == "localhost" ]]; then
  export CASSINI_TALK_BACKEND_URL="http://reverse-proxy"
fi

COMPOSE=(docker compose -p "$PROJECT_NAME" -f "$HARNESS_DIR/compose.yml")
COMPOSE_FULL=(docker compose -p "$PROJECT_NAME" -f "$HARNESS_DIR/compose.yml" --profile full)
if harness_remote_config_requested; then
  COMPOSE_FULL+=(--profile remote)
fi

log() {
  printf '\n\033[1;34m==>\033[0m \033[1m%s\033[0m\n' "$*"
}

success() {
  printf '\033[1;32m%s\033[0m\n' "$*"
}

error() {
  printf '\033[1;31m[ERROR] %s\033[0m\n' "$*" >&2
}

FORCE_BUILD=false
INSTALL_EXAPP=true
while [[ $# -gt 0 ]]; do
  case "$1" in
    --build|--force-build) FORCE_BUILD=true; shift ;;
    --no-install) INSTALL_EXAPP=false; shift ;;
    -h|--help)
      echo "Usage: $0 [--build] [--no-install]"
      echo "  --build       Force rebuilding the Cassini ExApp Docker image from source."
      echo "  --no-install  Prepare Nextcloud/AppAPI/HaRP but leave ExApp registration to you."
      exit 0
      ;;
    *) error "Unknown option: $1"; exit 1 ;;
  esac
done

log "1. Wiping previous state..."
docker rm -f cassini-exapp nc_app_gocassini >/dev/null 2>&1 || true
docker volume rm cassini-exapp-state cassini-exapp-site nc_app_gocassini_data >/dev/null 2>&1 || true
"${COMPOSE_FULL[@]}" down --volumes --remove-orphans

if [[ "$FORCE_BUILD" == "true" ]]; then
  export CASSINI_HARNESS_EXAPP_IMAGE_MODE="build"
else
  export CASSINI_HARNESS_EXAPP_IMAGE_MODE="${CASSINI_HARNESS_EXAPP_IMAGE_MODE:-reuse-local}"
fi

log "2. Preparing Cassini ExApp image..."
harness_prepare_exapp_image
success "✓ ExApp image mode completed: $CASSINI_HARNESS_EXAPP_IMAGE_MODE"

if [[ "$SPREED_PROFILE" == "full" ]]; then
  harness_render_full_profile_configs true
fi

log "3. Starting nextcloud, db, appapi-harp, reverse-proxy..."
compose_services=(nextcloud db appapi-harp reverse-proxy)
if [[ "$SPREED_PROFILE" == "full" ]]; then
  compose_services+=(nats janus signaling coturn)
  if harness_remote_config_requested; then
    compose_services+=(signaling-public-proxy)
  fi
fi
"${COMPOSE[@]}" up -d "${compose_services[@]}"

log "3b. Waiting for initial Nextcloud installation before restart..."
for attempt in $(seq 1 240); do
  if "${COMPOSE[@]}" exec -T -u www-data nextcloud php occ status 2>&1 | grep -q "installed: true"; then
    success "✓ Nextcloud installed"
    break
  fi
  if [[ $attempt -eq 240 ]]; then
    docker logs "${PROJECT_NAME}-nextcloud-1" --tail=120 >&2 || true
    error "Nextcloud did not finish initial installation"
    exit 1
  fi
  sleep 2
done

log "4. Bootstrapping Nextcloud (trusted domains, Talk, admin)..."
# Tell bootstrap.sh to wire Talk's recording_servers at Cassini's AppAPI
# proxy URL instead of the default gateway:4000 (which expects a
# standalone cassini-operator bot on the host). The proxy URL routes
# Talk's recording-protocol calls through reverse-proxy → AppAPI →
# HaRP → ExApp container, exercising the same path a published-store
# install would. The AppAPI route is declared PUBLIC in info.xml; Talk's
# HMAC over secret+Talk-Recording-Random+body is independent of NC auth.
export CASSINI_TALK_RECORDING_URL="http://reverse-proxy/index.php/apps/app_api/proxy/gocassini"
"$SCRIPT_DIR/bootstrap.sh"

occ() {
  "${COMPOSE[@]}" exec -T -u www-data nextcloud php occ "$@"
}

log "5. Configuring AppAPI/HaRP deploy daemon..."
harness_configure_appapi_phase

PUBLIC_NEXTCLOUD_HOST="${CASSINI_HARNESS_PUBLIC_HOST:-${CASSINI_HARNESS_HOST:-127.0.0.1}}"
PUBLIC_NEXTCLOUD_URL="${CASSINI_HARNESS_PUBLIC_URL:-http://${PUBLIC_NEXTCLOUD_HOST}:${NEXTCLOUD_HOST_PORT:-28080}}"
PUBLIC_PROXY_URL="${PUBLIC_NEXTCLOUD_URL%/}/index.php/apps/app_api/proxy/gocassini"
mkdir -p "$HARNESS_DIR/runtime"

if [[ "$INSTALL_EXAPP" == "true" ]]; then
  log "6. Installing and verifying Cassini as an installed ExApp..."
  harness_install_exapp_phase
  success "✓ ExApp registration and verification completed"
else
  log "6. Skipping ExApp registration because --no-install was passed."
fi

cat <<EOF

$(printf '\033[1;32m======================================================================\033[0m')
$(printf '\033[1;32m   Cassini App Store dogfood testbed ready                            \033[0m')
$(printf '\033[1;32m======================================================================\033[0m')

What admins actually do (post-publish):
  Once Cassini is in the App Store, admins install it with one click
  from the Apps page in the web UI, same as a regular PHP app. The
  "Install" button calls app_api:app:register under the hood; admins
  don't touch the CLI. The only one-time setup an admin has to do
  before any ExApp will work is configure a Deploy Daemon (HaRP or
  Docker Socket Proxy) from the AppAPI admin settings page. That's the
  real friction point — link to the HaRP quickstart from the README.

What this script does during development (pre-publish):
  Until Cassini is in the store, there is no store endpoint handing
  Nextcloud the info.xml on the admin's behalf. This script supplies the
  local info.xml via occ and registers Cassini with --test-deploy-mode,
  the same deploy path the store-published install uses.

  By default, registration is already done. The command shape is:

      docker compose -p cassini-exapp-test exec -u www-data nextcloud \\
          php occ app_api:app:register gocassini harp_local \\
              --info-xml /tmp/gocassini-info.xml \\
              --env CASSINI_TALK_RECORDING_SECRET=... \\
              --env CASSINI_TALK_SIGNALING_INTERNAL_SECRET=... \\
              --env CASSINI_NC_ACCESS_CONTROL=true \\
              --test-deploy-mode --wait-finish

  Secret values are intentionally not printed. Pass --no-install only if
  you want to stop before this registration step and run it by hand.

  After setup:
  1. Open  $PUBLIC_NEXTCLOUD_URL/
  2. Log in as  admin / admin
  3. Navigate to the post-deploy URLs below to verify the proxy routes.

  AppAPI daemon config is visible at:
     $PUBLIC_NEXTCLOUD_URL/index.php/settings/admin/app_api

Standard user (for /viewer access after deploy):
  alice / Tn8mY3qVrJ2x!E2e

Post-deploy URLs (ExApp UIs proxied through AppAPI):
  Cassini (all users): $PUBLIC_PROXY_URL/viewer/

Testing the Talk record button:
  bootstrap.sh wired Talk's recording_servers at the AppAPI proxy URL
  for Cassini, and info.xml declares /api/v1/welcome + /api/v1/room/...
  as PUBLIC routes. So when the admin clicks "Start recording" in a
  Talk call:
    Talk → http://reverse-proxy/.../proxy/gocassini/api/v1/room/<token>
         → AppAPI proxy
         → HaRP → cassini-operator in the ExApp container

  The operator verifies Talk's HMAC, uses HPB-internal signaling auth,
  captures audio, and uploads via NEXTCLOUD_URL when the call ends. No
  standalone cassini-operator process required.

  To exercise real call recording you need signaling + TURN running too.
  Start setup with the 'full' profile:

      SPREED_PROFILE=full ./harness/bin/manual-test-setup.sh --build

  Then create a Talk room, start a call, click record, or run the
  installed-ExApp private validation helper. Without the 'full' profile
  signaling isn't wired and the recorder can't join.

Tear down later:
  docker compose -p $PROJECT_NAME down --volumes

EOF

if command -v xdg-open >/dev/null 2>&1; then
  xdg-open "$PUBLIC_NEXTCLOUD_URL/" >/dev/null 2>&1 &
elif command -v open >/dev/null 2>&1; then
  open "$PUBLIC_NEXTCLOUD_URL/" >/dev/null 2>&1 &
fi
