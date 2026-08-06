#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
HARNESS_DIR="$PROJECT_ROOT/harness"
RUNTIME_DIR="$SCRIPT_DIR/runtime"
ENV_FILE="$SCRIPT_DIR/.env"

# shellcheck source=sandbox/lib-store-release.sh
source "$SCRIPT_DIR/lib-store-release.sh"
# The AppAPI register dance is shared with the harness install phase and the
# production deploy (ops/deploy/deploy-exapp.sh) — one implementation, three
# callers. See that file's header for the rules it encodes.
# shellcheck source=ops/deploy/lib/exapp-register.sh
source "$PROJECT_ROOT/ops/deploy/lib/exapp-register.sh"

if [[ -f "$ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
fi

PROJECT_NAME="${PROJECT_NAME:-cassini-sandbox}"
SPREED_PROFILE="${SPREED_PROFILE:-full}"
SANDBOX_SCHEME="${SANDBOX_SCHEME:-https}"
SANDBOX_DOMAIN="${SANDBOX_DOMAIN:-127.0.0.1:28080}"
SANDBOX_HOSTNAME="${SANDBOX_DOMAIN%%:*}"
SANDBOX_PUBLIC_URL="${SANDBOX_PUBLIC_URL:-$SANDBOX_SCHEME://$SANDBOX_DOMAIN}"
NEXTCLOUD_HOST_PORT="${NEXTCLOUD_HOST_PORT:-28080}"
NEXTCLOUD_ADMIN_USER="${NEXTCLOUD_ADMIN_USER:-admin}"
NEXTCLOUD_ADMIN_PASSWORD="${NEXTCLOUD_ADMIN_PASSWORD:-admin}"
SANDBOX_USER="${SANDBOX_USER:-alice}"
SANDBOX_USER_PASSWORD="${SANDBOX_USER_PASSWORD:-Tn8mY3qVrJ2x!E2e}"
CASSINI_EXAPP_IMAGE="${CASSINI_EXAPP_IMAGE:-ghcr.io/codemyriad/gocassini:latest}"
SANDBOX_SIGNALING_URL="${SANDBOX_SIGNALING_URL:-$SANDBOX_PUBLIC_URL/spreed}"
SANDBOX_TURN_HOST="${SANDBOX_TURN_HOST:-${SANDBOX_DOMAIN%%:*}}"
SANDBOX_TURN_EXTERNAL_IP="${SANDBOX_TURN_EXTERNAL_IP:-}"
# Nextcloud update channel. `beta` makes AppAPI's ExApp store accept pre-release
# (alpha/beta/rc) apps like Cassini as well as stable ones, so the sandbox can
# install whichever is newest; see lib/Fetcher/ExAppFetcher.php.
SANDBOX_UPDATE_CHANNEL="${SANDBOX_UPDATE_CHANNEL:-beta}"
# Where Cassini comes from on deploy:
#   store  -> install the latest published release (alpha/beta/rc/stable) from
#             the Nextcloud App Store
#   image  -> register a specific container image (set by --image/--build)
CASSINI_INSTALL_SOURCE="${CASSINI_INSTALL_SOURCE:-store}"
CASSINI_APPSTORE_ID="${CASSINI_APPSTORE_ID:-gocassini}"
CASSINI_APPSTORE_CATALOG_URL="${CASSINI_APPSTORE_CATALOG_URL:-https://apps.nextcloud.com/api/v1/appapi_apps.json}"
FORCE_BUILD=false
RESET=false
REGISTER_ONLY=false

export PROJECT_NAME SPREED_PROFILE
export SANDBOX_SCHEME SANDBOX_DOMAIN SANDBOX_HOSTNAME SANDBOX_PUBLIC_URL
export NEXTCLOUD_HOST_PORT NEXTCLOUD_ADMIN_USER NEXTCLOUD_ADMIN_PASSWORD

usage() {
  cat <<EOF
Usage: sandbox/deploy.sh [options]

By default Cassini is installed from the Nextcloud App Store (latest published
release, be it alpha/beta/rc or stable). Passing --image or --build switches to
registering a specific container image instead.

Options:
  --from-store        Install the latest release from the App Store (default)
  --image IMAGE       Register this container image instead of the store release
  --build             Build IMAGE locally from deployment/Dockerfile.exapp
  --reset             Recreate Docker volumes before deploying
  --register-only     Re-register Cassini without restarting the stack
  -h, --help          Show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --from-store) CASSINI_INSTALL_SOURCE=store; shift ;;
    --image) CASSINI_EXAPP_IMAGE="$2"; CASSINI_INSTALL_SOURCE=image; shift 2 ;;
    --build) FORCE_BUILD=true; CASSINI_INSTALL_SOURCE=image; shift ;;
    --reset) RESET=true; shift ;;
    --register-only) REGISTER_ONLY=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

mkdir -p "$RUNTIME_DIR"

COMPOSE=(
  docker compose
  -p "$PROJECT_NAME"
  -f "$HARNESS_DIR/compose.yml"
  -f "$SCRIPT_DIR/compose.sandbox.yml"
)
if [[ "$SPREED_PROFILE" == "full" ]]; then
  COMPOSE+=(--profile full)
fi

log() {
  printf '\n\033[1;34m==>\033[0m \033[1m%s\033[0m\n' "$*"
}
exapp_log() { log "$@"; }

occ() {
  "${COMPOSE[@]}" exec -T -u www-data nextcloud php occ "$@"
}

compose_network_gateway() {
  docker network inspect "${PROJECT_NAME}_default" \
    --format '{{range .IPAM.Config}}{{if .Gateway}}{{.Gateway}}{{end}}{{end}}'
}

compose_service_ip() {
  local service="$1"
  local container_id
  container_id="$("${COMPOSE[@]}" ps -q "$service" | head -n1)"
  [[ -n "$container_id" ]] || return 0
  docker inspect "$container_id" \
    --format '{{range .NetworkSettings.Networks}}{{if .IPAddress}}{{println .IPAddress}}{{end}}{{end}}' \
    | head -n1
}

configure_reverse_proxy_headers() {
  log "Configuring Nextcloud reverse proxy headers"
  local proxies=()
  local gateway reverse_proxy_ip index

  gateway="$(compose_network_gateway)"
  if [[ -n "$gateway" ]]; then
    proxies+=("$gateway")
  fi

  reverse_proxy_ip="$(compose_service_ip reverse-proxy)"
  if [[ -n "$reverse_proxy_ip" && "$reverse_proxy_ip" != "$gateway" ]]; then
    proxies+=("$reverse_proxy_ip")
  fi

  if [[ "${#proxies[@]}" -eq 0 ]]; then
    echo "Could not determine Compose proxy addresses for Nextcloud trusted_proxies" >&2
    return 1
  fi

  occ config:system:delete trusted_proxies >/dev/null 2>&1 || true
  index=0
  for proxy in "${proxies[@]}"; do
    occ config:system:set trusted_proxies "$index" --value="$proxy"
    index=$((index + 1))
  done

  occ config:system:delete forwarded_for_headers >/dev/null 2>&1 || true
  occ config:system:set forwarded_for_headers 0 --value=HTTP_X_FORWARDED_FOR
}

write_secret_cache() {
  cat > "$RUNTIME_DIR/secrets.env" <<EOF
SIGNALING_SHARED_SECRET=$SIGNALING_SHARED_SECRET
SIGNALING_INTERNAL_SECRET=$SIGNALING_INTERNAL_SECRET
TURN_SHARED_SECRET=$TURN_SHARED_SECRET
CASSINI_TALK_RECORDING_SECRET=$CASSINI_TALK_RECORDING_SECRET
EOF
  chmod 600 "$RUNTIME_DIR/secrets.env"
}

cached_secret() {
  local key="$1"
  if [[ -f "$RUNTIME_DIR/secrets.env" ]]; then
    awk -F= -v key="$key" '$1 == key {print substr($0, length(key) + 2); exit}' "$RUNTIME_DIR/secrets.env"
  fi
}

SIGNALING_SHARED_SECRET="${SIGNALING_SHARED_SECRET:-$(cached_secret SIGNALING_SHARED_SECRET)}"
SIGNALING_INTERNAL_SECRET="${SIGNALING_INTERNAL_SECRET:-$(cached_secret SIGNALING_INTERNAL_SECRET)}"
TURN_SHARED_SECRET="${TURN_SHARED_SECRET:-$(cached_secret TURN_SHARED_SECRET)}"
CASSINI_TALK_RECORDING_SECRET="${CASSINI_TALK_RECORDING_SECRET:-$(cached_secret CASSINI_TALK_RECORDING_SECRET)}"
SIGNALING_SHARED_SECRET="${SIGNALING_SHARED_SECRET:-$(openssl rand -hex 32)}"
SIGNALING_INTERNAL_SECRET="${SIGNALING_INTERNAL_SECRET:-$(openssl rand -hex 32)}"
TURN_SHARED_SECRET="${TURN_SHARED_SECRET:-$(openssl rand -hex 32)}"
CASSINI_TALK_RECORDING_SECRET="${CASSINI_TALK_RECORDING_SECRET:-$(openssl rand -hex 32)}"
write_secret_cache

render_configs() {
  local turn_external_ip="$SANDBOX_TURN_EXTERNAL_IP"
  if [[ -z "$turn_external_ip" ]]; then
    turn_external_ip="$SANDBOX_TURN_HOST"
  fi
  local janus_external_ip="${SANDBOX_JANUS_EXTERNAL_IP:-$turn_external_ip}"

  cat > "$RUNTIME_DIR/turnserver.conf" <<EOF
listening-port=13479
tls-listening-port=0

external-ip=$turn_external_ip
relay-ip=0.0.0.0

min-port=49160
max-port=49200

use-auth-secret
static-auth-secret=$TURN_SHARED_SECRET
realm=${SANDBOX_DOMAIN%%:*}

fingerprint
no-cli
no-tls
no-dtls

log-file=stdout
verbose
EOF

  cat > "$RUNTIME_DIR/janus.jcfg" <<EOF
general: {
  debug_level = 4
  admin_secret = "01e2fcd0d226d7f4cf34a8a61397f110693f05042e57ab68e94f8476a4b8f22a"
  plugins_folder = "/usr/local/lib/janus/plugins"
  transports_folder = "/usr/local/lib/janus/transports"
  events_folder = "/usr/local/lib/janus/events"
}

nat: {
  nat_1_1_mapping = "$janus_external_ip"
}

media: {
  rtp_port_range = "20000-20100"
}

admin: {
  admin_port = 17088
  admin_secret = "01e2fcd0d226d7f4cf34a8a61397f110693f05042e57ab68e94f8476a4b8f22a"
}
EOF

  cat > "$RUNTIME_DIR/signaling.conf" <<EOF
[http]
listen = 0.0.0.0:28082

[app]
debug = false

[sessions]
hashkey = $(openssl rand -hex 32)

[clients]
internalsecret = $SIGNALING_INTERNAL_SECRET

[backend]
backends = backend-1, backend-2, backend-3
allowall = false
timeout = 10
connectionsperhost = 16

[backend-1]
url = $SANDBOX_PUBLIC_URL
secret = $SIGNALING_SHARED_SECRET

[backend-2]
url = http://nextcloud
secret = $SIGNALING_SHARED_SECRET

[backend-3]
url = http://reverse-proxy
secret = $SIGNALING_SHARED_SECRET

[nats]
url = nats://127.0.0.1:14222

[mcu]
type = janus
url = ws://127.0.0.1:28188
adminkey = 01e2fcd0d226d7f4cf34a8a61397f110693f05042e57ab68e94f8476a4b8f22a

[turn]
apikey = 127.0.0.1
secret = $TURN_SHARED_SECRET
servers = turn:$SANDBOX_TURN_HOST:13479?transport=udp,turn:$SANDBOX_TURN_HOST:13479?transport=tcp
EOF
}

image_parts() {
  local ref="$1"
  local without_tag registry image tag
  if [[ "$ref" == *@sha256:* ]]; then
    echo "Digest image references are not supported by sandbox info.xml generation yet: $ref" >&2
    return 1
  fi
  tag="${ref##*:}"
  without_tag="${ref%:*}"
  if [[ "$without_tag" == "$ref" || "$without_tag" != */* ]]; then
    tag="latest"
    without_tag="$ref"
  fi
  registry="${without_tag%%/*}"
  image="${without_tag#*/}"
  if [[ "$registry" != *.* && "$registry" != *:* && "$registry" != "localhost" ]]; then
    registry="docker.io"
    image="$without_tag"
  fi
  printf '%s\n%s\n%s\n' "$registry" "$image" "$tag"
}

render_info_xml() {
  local registry image tag
  mapfile -t parts < <(image_parts "$CASSINI_EXAPP_IMAGE")
  registry="${parts[0]}"
  image="${parts[1]}"
  tag="${parts[2]}"
  cp "$PROJECT_ROOT/appinfo/info.xml" "$RUNTIME_DIR/gocassini-info.xml"
  perl -0pi -e "s#<registry>.*?</registry>#<registry>$registry</registry>#s; s#<image>.*?</image>#<image>$image</image>#s; s#<image-tag>.*?</image-tag>#<image-tag>$tag</image-tag>#s" "$RUNTIME_DIR/gocassini-info.xml"
}

resolve_store_info_xml() {
  # Resolve the latest published Cassini release (alpha/beta/rc or stable) from
  # the AppAPI ExApp store catalog and extract its info.xml. AppAPI pulls the
  # image pinned in that info.xml during registration, so no separate image is
  # needed.
  log "Resolving latest $CASSINI_APPSTORE_ID release from the App Store"
  local catalog="$RUNTIME_DIR/appapi_apps.json"
  curl -fsSL "$CASSINI_APPSTORE_CATALOG_URL" -o "$catalog"

  # Pick the newest release across all channels (see store_latest_release_url in
  # lib-store-release.sh; ordering is pinned by test-store-release.sh).
  local url
  if ! url="$(store_latest_release_url "$CASSINI_APPSTORE_ID" "$catalog")"; then
    echo "No $CASSINI_APPSTORE_ID release artifact found in $CASSINI_APPSTORE_CATALOG_URL" >&2
    return 1
  fi
  log "Latest release artifact: $url"

  local tgz="$RUNTIME_DIR/${CASSINI_APPSTORE_ID}-store.tar.gz"
  local extract="$RUNTIME_DIR/store-extract"
  curl -fsSL "$url" -o "$tgz"
  rm -rf "$extract"
  mkdir -p "$extract"
  tar xzf "$tgz" -C "$extract"

  local info
  info="$(find "$extract" -name info.xml | head -n1)"
  [[ -n "$info" ]] || { echo "info.xml not found in store artifact $url" >&2; return 1; }
  cp "$info" "$RUNTIME_DIR/gocassini-info.xml"
}

wait_for_nextcloud() {
  local status_url="$SANDBOX_PUBLIC_URL/status.php"
  local fallback_url="http://127.0.0.1:$NEXTCLOUD_HOST_PORT/status.php"
  local end=$((SECONDS + 420))
  log "Waiting for Nextcloud"
  until (( SECONDS >= end )); do
    if curl -fsS "$status_url" 2>/dev/null | grep -q '"installed":true'; then
      return 0
    fi
    if curl -fsS "$fallback_url" 2>/dev/null | grep -q '"installed":true'; then
      return 0
    fi
    sleep 2
  done
  echo "Nextcloud did not become ready" >&2
  return 1
}

grant_docker_socket() {
  log "Granting Nextcloud access to the Docker socket"
  local socket_gid
  socket_gid=$("${COMPOSE[@]}" exec -T nextcloud stat -c '%g' /var/run/docker.sock)
  "${COMPOSE[@]}" exec -T -u root nextcloud sh -c "
    group_name=\$(getent group $socket_gid | cut -d: -f1)
    if [ -z \"\$group_name\" ]; then
      groupadd -g $socket_gid docker-host
      group_name=docker-host
    fi
    usermod -aG \"\$group_name\" www-data
  "
  "${COMPOSE[@]}" restart nextcloud
  wait_for_nextcloud
}

bootstrap_nextcloud() {
  log "Bootstrapping Nextcloud, Talk, and AppAPI"
  export PROJECT_NAME SPREED_PROFILE
  export NEXTCLOUD_URL="$SANDBOX_PUBLIC_URL"
  export NEXTCLOUD_STATUS_URL="http://127.0.0.1:$NEXTCLOUD_HOST_PORT/status.php"
  export ADMIN_USER="$NEXTCLOUD_ADMIN_USER"
  export ADMIN_PASSWORD="$NEXTCLOUD_ADMIN_PASSWORD"
  export SIGNALING_URL="$SANDBOX_SIGNALING_URL"
  export TURN_SERVER="$SANDBOX_TURN_HOST:13479"
  export SIGNALING_SHARED_SECRET SIGNALING_INTERNAL_SECRET TURN_SHARED_SECRET CASSINI_TALK_RECORDING_SECRET
  export CASSINI_TALK_RECORDING_URL="http://reverse-proxy/index.php/apps/app_api/proxy/gocassini"
  "$HARNESS_DIR/bin/bootstrap.sh"

  occ config:system:set trusted_domains 20 --value="${SANDBOX_DOMAIN%%:*}"
  occ config:system:set overwrite.cli.url --value="$SANDBOX_PUBLIC_URL"
  occ config:system:set overwritehost --value="$SANDBOX_DOMAIN"
  occ config:system:set overwriteprotocol --value="$SANDBOX_SCHEME"
  configure_reverse_proxy_headers

  occ app:install app_api >/dev/null 2>&1 || true
  occ app:enable app_api

  log "Setting app update channel to '$SANDBOX_UPDATE_CHANNEL' (accept pre-release ExApps)"
  occ config:system:set updater.release.channel --value "$SANDBOX_UPDATE_CHANNEL"

  # Cassini renders its control-panel/viewer through AppAPI's native UI
  # mechanism (the nonce'd embedded page loading the registered ui/viewer.js),
  # so the sandbox leaves AppAPI untouched and the integrity check stays green.

  "${COMPOSE[@]}" restart nextcloud
  wait_for_nextcloud
}

register_daemon() {
  occ app_api:daemon:registry:remove harp_sandbox \
    --registry-from=ghcr.io \
    --registry-to=local >/dev/null 2>&1 || true
  # SANDBOX_COMPUTE_DEVICE is the only knob that separates a CPU sandbox from a
  # GPU one: AppAPI derives the -cuda image variant from the daemon. Same
  # parameter, same code path, as ops/deploy/inventory/*.env in production.
  exapp_register_daemon \
    --name harp_sandbox \
    --display "HaRP (Cassini sandbox)" \
    --host "appapi-harp:8780" \
    --nc-url "http://reverse-proxy" \
    --net "${PROJECT_NAME}_default" \
    --harp \
    --frp-address "appapi-harp:8782" \
    --shared-key "dogfood-shared-key-not-secret" \
    --compute-device "${SANDBOX_COMPUTE_DEVICE:-cpu}" \
    --set-default \
    --replace
  occ app_api:daemon:registry:remove harp_sandbox \
    --registry-from=ghcr.io \
    --registry-to=local >/dev/null 2>&1 || true
}

pull_cassini_image() {
  if [[ "$FORCE_BUILD" == "true" ]]; then
    return 0
  fi
  log "Pulling Cassini ExApp image $CASSINI_EXAPP_IMAGE"
  docker pull "$CASSINI_EXAPP_IMAGE"
}

register_cassini() {
  if [[ "$CASSINI_INSTALL_SOURCE" == "store" ]]; then
    resolve_store_info_xml
    log "Registering Cassini ExApp from the App Store (latest release)"
  else
    log "Registering Cassini ExApp from image $CASSINI_EXAPP_IMAGE"
    render_info_xml
  fi
  "${COMPOSE[@]}" cp "$RUNTIME_DIR/gocassini-info.xml" nextcloud:/tmp/gocassini-info.xml
  "${COMPOSE[@]}" exec -T -u root nextcloud chown www-data:www-data /tmp/gocassini-info.xml
  # --replace: a leftover registration (an earlier App Store install, or a
  # previous deploy) leaves AppAPI holding a secret the freshly-redeployed
  # ExApp container no longer shares, so registering over it fails at /init
  # with a 401 "invalid AppAPI authentication". The shared library unregisters
  # first and never passes --rm-data, so the persistent volume (jobs DB,
  # published site) survives.
  exapp_register_app \
    --app-id gocassini \
    --daemon harp_sandbox \
    --info-xml /tmp/gocassini-info.xml \
    --env "CASSINI_TALK_RECORDING_SECRET=$CASSINI_TALK_RECORDING_SECRET" \
    --env "CASSINI_TALK_BACKEND_URL=$SANDBOX_PUBLIC_URL" \
    --env "CASSINI_TALK_SIGNALING_INTERNAL_SECRET=$SIGNALING_INTERNAL_SECRET" \
    --test-deploy-mode \
    --replace
}

create_demo_user() {
  log "Ensuring demo user exists: $SANDBOX_USER"
  export OC_PASS="$SANDBOX_USER_PASSWORD"
  if occ user:info "$SANDBOX_USER" >/dev/null 2>&1; then
    "${COMPOSE[@]}" exec -T -e OC_PASS -u www-data nextcloud php occ user:resetpassword \
      --password-from-env \
      "$SANDBOX_USER"
  else
    "${COMPOSE[@]}" exec -T -e OC_PASS -u www-data nextcloud php occ user:add \
      --password-from-env \
      --display-name="$SANDBOX_USER" \
      "$SANDBOX_USER"
  fi
  unset OC_PASS
}

render_configs

if [[ "$CASSINI_INSTALL_SOURCE" == "image" ]]; then
  if [[ "$FORCE_BUILD" == "true" ]]; then
    log "Building $CASSINI_EXAPP_IMAGE"
    docker build -f "$PROJECT_ROOT/deployment/Dockerfile.exapp" -t "$CASSINI_EXAPP_IMAGE" "$PROJECT_ROOT"
  else
    pull_cassini_image
  fi
fi

if [[ "$REGISTER_ONLY" != "true" ]]; then
  if [[ "$RESET" == "true" ]]; then
    log "Resetting sandbox volumes"
    "${COMPOSE[@]}" down --volumes --remove-orphans
  fi

  log "Starting sandbox stack"
  "${COMPOSE[@]}" up -d nextcloud db appapi-harp reverse-proxy
  if [[ "$SPREED_PROFILE" == "full" ]]; then
    "${COMPOSE[@]}" up -d nats janus signaling coturn
    # signaling.conf is bind-mounted; restart to pick up regenerated secrets.
    "${COMPOSE[@]}" restart signaling
  fi

  wait_for_nextcloud
  grant_docker_socket
  bootstrap_nextcloud
  register_daemon
  create_demo_user
fi

register_cassini

cat <<EOF

Cassini sandbox deployed.

Nextcloud:
  $SANDBOX_PUBLIC_URL

Admin:
  $NEXTCLOUD_ADMIN_USER / $NEXTCLOUD_ADMIN_PASSWORD

Demo user:
  $SANDBOX_USER / $SANDBOX_USER_PASSWORD

Cassini:
  Open the "Cassini" entry in the Nextcloud top bar at $SANDBOX_PUBLIC_URL
  (admins also get the operator surface inside it).

Teardown:
  sandbox/destroy.sh
  sandbox/destroy.sh --volumes
EOF
