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
PROJECT_ROOT="$(cd "$HARNESS_DIR/.." && pwd)"

export PROJECT_NAME="cassini-exapp-test"
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
# Ensure a caller override of CASSINI_TALK_SIGNALING_INTERNAL_SECRET is also
# what the local signaling server uses for HPB internal auth.
export SIGNALING_INTERNAL_SECRET="$CASSINI_TALK_SIGNALING_INTERNAL_SECRET"

COMPOSE=(docker compose -p "$PROJECT_NAME" -f "$HARNESS_DIR/compose.yml")
COMPOSE_FULL=(docker compose -p "$PROJECT_NAME" -f "$HARNESS_DIR/compose.yml" --profile full)

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

IMAGE_LOCAL="cassini-exapp:e2e-v3-cpu-gpu"
# Tag the local image as if it had been pulled from ghcr.io. Combined with
# the daemon's `--registry-from=ghcr.io --registry-to=local` mapping below,
# this lets AppAPI use info.xml verbatim (same registry/image/tag the
# production App Store install would use) without us hosting a local
# registry or rewriting info.xml. The tag must therefore match info.xml's
# <image-tag> exactly — it is version-pinned (no longer `latest`), so derive
# it from the manifest instead of hardcoding it.
source "$SCRIPT_DIR/lib-exapp-image.sh"
IMAGE_TAG="$(exapp_image_tag "$PROJECT_ROOT/appinfo/info.xml")"
IMAGE_AS_PRODUCTION="ghcr.io/codemyriad/gocassini:${IMAGE_TAG}"

if [[ "$FORCE_BUILD" == "true" ]] || ! docker image inspect "$IMAGE_LOCAL" >/dev/null 2>&1; then
  log "2. Building Cassini ExApp image..."
  docker build -f "$PROJECT_ROOT/deployment/Dockerfile.exapp" -t "$IMAGE_LOCAL" "$PROJECT_ROOT"
else
  log "2. Reusing existing $IMAGE_LOCAL image. (pass --build to force rebuild)"
fi
docker tag "$IMAGE_LOCAL" "$IMAGE_AS_PRODUCTION"
success "✓ Tagged $IMAGE_LOCAL as $IMAGE_AS_PRODUCTION (info.xml <image-tag>)"

render_full_profile_signaling_conf() {
  local conf="$HARNESS_DIR/runtime/signaling.manual.conf"
  local port="${NEXTCLOUD_HOST_PORT:-28080}"
  mkdir -p "$HARNESS_DIR/runtime"

  local -a candidates=("127.0.0.1" "localhost" "host.docker.internal")
  if [[ -n "${CASSINI_HARNESS_HOST:-}" ]]; then
    candidates+=("$CASSINI_HARNESS_HOST")
  fi
  if command -v hostname >/dev/null 2>&1; then
    # Include the VM/LAN addresses so Talk requests made through
    # --nextcloud-host <vm-ip> produce a Spreed-Signaling-Backend URL the
    # signaling server accepts.
    # shellcheck disable=SC2207
    candidates+=($(hostname -I 2>/dev/null || true))
  fi

  local -a hosts=()
  local raw host seen
  for raw in "${candidates[@]}"; do
    host="${raw#http://}"
    host="${host#https://}"
    host="${host%%/*}"
    host="${host%%:*}"
    [[ -n "$host" ]] || continue
    seen=false
    for existing in "${hosts[@]}"; do
      if [[ "$existing" == "$host" ]]; then
        seen=true
        break
      fi
    done
    [[ "$seen" == "true" ]] || hosts+=("$host")
  done

  local backend_names=""
  for i in "${!hosts[@]}"; do
    if [[ -n "$backend_names" ]]; then
      backend_names+=", "
    fi
    backend_names+="backend-$((i + 1))"
  done

  {
    cat <<EOF_CONF
[http]
listen = 0.0.0.0:28082

[app]
debug = false

[sessions]
hashkey = 39a5433df8f8334f6eff8a67f6afcfc594a2599f5389b4fef316ec30277fb910

[clients]
internalsecret = $SIGNALING_INTERNAL_SECRET

[backend]
backends = $backend_names
allowall = false
timeout = 10
connectionsperhost = 16

EOF_CONF
    for i in "${!hosts[@]}"; do
      cat <<EOF_CONF
[backend-$((i + 1))]
url = http://${hosts[$i]}:${port}
secret = $SIGNALING_SHARED_SECRET

EOF_CONF
    done
    cat <<EOF_CONF
[nats]
url = nats://127.0.0.1:14222

[mcu]
type = janus
url = ws://127.0.0.1:28188
adminkey = 01e2fcd0d226d7f4cf34a8a61397f110693f05042e57ab68e94f8476a4b8f22a

[turn]
apikey = 127.0.0.1
secret = $TURN_SHARED_SECRET
servers = turn:127.0.0.1:13479?transport=udp,turn:127.0.0.1:13479?transport=tcp
EOF_CONF
  } >"$conf"

  export SIGNALING_CONF="$conf"
  success "✓ Rendered signaling config with Nextcloud backends: ${hosts[*]}"
}

if [[ "$SPREED_PROFILE" == "full" ]]; then
  render_full_profile_signaling_conf
fi

log "3. Starting nextcloud, db, appapi-harp, reverse-proxy..."
compose_services=(nextcloud db appapi-harp reverse-proxy)
if [[ "$SPREED_PROFILE" == "full" ]]; then
  compose_services+=(nats janus signaling coturn)
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

log "4. Granting Nextcloud access to host docker socket..."
SOCKET_GID=$("${COMPOSE[@]}" exec -T nextcloud stat -c '%g' /var/run/docker.sock)
"${COMPOSE[@]}" exec -T -u root nextcloud sh -c "
  EXISTING_GROUP=\$(getent group $SOCKET_GID | cut -d: -f1)
  if [ -z \"\$EXISTING_GROUP\" ]; then
    groupadd -g $SOCKET_GID docker-host
    GROUP_NAME=docker-host
  else
    GROUP_NAME=\$EXISTING_GROUP
  fi
  usermod -aG \"\$GROUP_NAME\" www-data
"
"${COMPOSE[@]}" restart nextcloud

log "5. Bootstrapping Nextcloud (trusted domains, Talk, admin)..."
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

log "6. Installing AppAPI..."
occ app:install app_api || true
occ app:enable app_api

log "7. Patching AppAPI CSP for ExApp proxy responses..."
"${COMPOSE[@]}" exec -T nextcloud php < "$SCRIPT_DIR/patch-csp.php"
"${COMPOSE[@]}" restart nextcloud

# bootstrap.sh already waits for NC the first time; do it again post-restart.
log "8. Waiting for Nextcloud after CSP-patch restart..."
for attempt in $(seq 1 60); do
  if "${COMPOSE[@]}" exec -T -u www-data nextcloud php occ status 2>&1 | grep -q "installed: true"; then
    success "✓ Nextcloud back up"
    break
  fi
  if [[ $attempt -eq 60 ]]; then error "Nextcloud did not come back up after restart"; exit 1; fi
  sleep 1
done

log "9. Registering HaRP deploy daemon..."
occ app_api:daemon:unregister docker_local >/dev/null 2>&1 || true
occ app_api:daemon:unregister harp_local   >/dev/null 2>&1 || true
# HP_SHARED_KEY must match the value in compose.yml's appapi-harp service.
# nextcloud_url is BOTH the URL AppAPI uses internally for heartbeat
# (${nc_url}/exapps/<appId>/heartbeat — must route to HaRP) AND the value
# injected into the ExApp container as NEXTCLOUD_URL (callbacks to /index.
# php/apps/app_api/... must reach NC). The reverse-proxy splits both paths,
# so pointing the daemon at it satisfies both directions.
occ app_api:daemon:register \
    harp_local \
    "HaRP (local)" \
    docker-install \
    http \
    "appapi-harp:8780" \
    "http://reverse-proxy" \
    --net="${PROJECT_NAME}_default" \
    --harp \
    --harp_frp_address "appapi-harp:8782" \
    --harp_shared_key "dogfood-shared-key-not-secret" \
    --set-default \
    --compute_device=cpu

log "10. Mapping ghcr.io to local so the daemon uses our locally-built image..."
# AppAPI sees info.xml's <registry>ghcr.io</registry>, looks for the mapping,
# finds --registry-to=local, and skips the docker pull — the image must
# already exist locally (we tagged it in step 2). Lets the dev path use
# info.xml verbatim, same content the production App Store install reads.
occ app_api:daemon:registry:add harp_local \
    --registry-from=ghcr.io --registry-to=local

log "11. Copying info.xml into the Nextcloud container for app:register..."
"${COMPOSE[@]}" cp "$PROJECT_ROOT/appinfo/info.xml" nextcloud:/tmp/gocassini-info.xml
"${COMPOSE[@]}" exec -T -u root nextcloud chown www-data:www-data /tmp/gocassini-info.xml

log "12. Creating standard user 'alice' for viewer testing..."
export OC_PASS="Tn8mY3qVrJ2x!E2e"
"${COMPOSE[@]}" exec -T -e OC_PASS -u www-data nextcloud php occ user:add --password-from-env --display-name=Alice alice >/dev/null 2>&1 || true
unset OC_PASS

PROXY_URL="http://127.0.0.1:28080/index.php/apps/app_api/proxy/gocassini"
PUBLIC_NEXTCLOUD_HOST="${CASSINI_HARNESS_HOST:-127.0.0.1}"
PUBLIC_NEXTCLOUD_URL="http://${PUBLIC_NEXTCLOUD_HOST}:${NEXTCLOUD_HOST_PORT:-28080}"
PUBLIC_PROXY_URL="${PUBLIC_NEXTCLOUD_URL}/index.php/apps/app_api/proxy/gocassini"
REGISTER_LOG="$HARNESS_DIR/runtime/manual-test-register.log"
mkdir -p "$HARNESS_DIR/runtime"

http_ok_with_retry() {
  local desc="$1"
  shift
  local last=""
  for attempt in $(seq 1 60); do
    if last=$(curl -fsS "$@" 2>&1); then
      success "✓ $desc"
      return 0
    fi
    sleep 2
  done
  error "$desc failed: $last"
  return 1
}

http_body_with_retry() {
  local desc="$1"
  shift
  local out=""
  for attempt in $(seq 1 60); do
    if out=$(curl -fsS "$@" 2>&1); then
      printf '%s' "$out"
      return 0
    fi
    sleep 2
  done
  error "$desc failed: $out"
  return 1
}

if [[ "$INSTALL_EXAPP" == "true" ]]; then
  log "13. Registering and enabling Cassini as an installed ExApp..."
  register_args=(
    app_api:app:register gocassini harp_local
    --info-xml /tmp/gocassini-info.xml
    --env "CASSINI_TALK_RECORDING_SECRET=$CASSINI_TALK_RECORDING_SECRET"
    --env "CASSINI_TALK_SIGNALING_INTERNAL_SECRET=$CASSINI_TALK_SIGNALING_INTERNAL_SECRET"
    --test-deploy-mode
    --wait-finish
  )
  if [[ -n "${CASSINI_TALK_BACKEND_URL:-}" ]]; then
    register_args+=(--env "CASSINI_TALK_BACKEND_URL=$CASSINI_TALK_BACKEND_URL")
  fi
  for optional_env in OPENROUTER_API_KEY LLM_BASE_URL LLM_MODEL CASSINI_OPERATOR_API_TOKEN; do
    if [[ -n "${!optional_env:-}" ]]; then
      register_args+=(--env "$optional_env=${!optional_env}")
    fi
  done

  if ! occ "${register_args[@]}" >"$REGISTER_LOG" 2>&1; then
    tail -200 "$REGISTER_LOG" >&2 || true
    error "app_api:app:register failed; see $REGISTER_LOG"
    exit 1
  fi
  if grep -q 'heartbeat check failed' "$REGISTER_LOG"; then
    tail -200 "$REGISTER_LOG" >&2 || true
    error "app_api:app:register reported heartbeat failure; see $REGISTER_LOG"
    exit 1
  fi
  success "✓ ExApp registration completed"

  log "14. Cycling enable state so AppAPI sends PUT /enabled..."
  occ app_api:app:disable gocassini >/dev/null 2>&1 || true
  occ app_api:app:enable gocassini >/dev/null
  success "✓ ExApp enabled"

  log "15. Verifying installed ExApp proxy routes and Talk config presence..."
  occ app_api:app:list | grep -q 'gocassini' || { error "gocassini missing from app_api:app:list"; exit 1; }

  welcome_json=$(http_body_with_retry "welcome route" "$PROXY_URL/api/v1/welcome")
  echo "$welcome_json" | grep -q '"version":1' || { error "unexpected welcome response: $welcome_json"; exit 1; }
  success "✓ welcome route returned {\"version\":1}"

  status_json=$(http_body_with_retry "operator status" -u admin:admin "$PROXY_URL/operator/status")
  echo "$status_json" | grep -q '"secret_configured":true' || { error "recording secret missing from status: $status_json"; exit 1; }
  echo "$status_json" | grep -q '"signaling_internal_secret_configured":true' || { error "signaling internal secret missing from status: $status_json"; exit 1; }
  success "✓ status reports Talk recording + signaling secrets configured"

  http_ok_with_retry "admin control panel route" -u admin:admin -o /dev/null "$PROXY_URL/control-panel/"
  http_ok_with_retry "viewer route" -u alice:Tn8mY3qVrJ2x!E2e -o /dev/null "$PROXY_URL/viewer/"
else
  log "13. Skipping ExApp registration because --no-install was passed."
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
  Admin control panel: $PUBLIC_PROXY_URL/control-panel/
  User viewer:         $PUBLIC_PROXY_URL/viewer/

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
