#!/usr/bin/env bash
# Production-path ExApp install E2E for Cassini.
#
# Covers:
#   - Nextcloud + AppAPI using the HaRP docker-install deploy daemon.
#   - The real appinfo/info.xml install path (`app_api:app:register --info-xml`).
#   - The real ExApp entrypoint (`exapp-start.sh`), including frpc -> HaRP.
#   - AppAPI heartbeat/init/enable reaching the container through
#     reverse-proxy -> HaRP, with install ending in [enabled].
#   - Slice 0's CASSINI_TALK_RECORDING_SECRET env passthrough at install time.
#
# Does NOT cover:
#   - Talk recording, transcription, publishing, or viewer-read phases.
#   - Talk HMAC acceptance beyond provisioning the shared secret for later phases.
#   - The legacy AppAPI manual-install daemon path.
#   - Direct container middleware tests that bypass HaRP.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$HARNESS_DIR/.." && pwd)"
COMPOSE_FILE="$HARNESS_DIR/compose.yml"
INFO_XML="$REPO_ROOT/appinfo/info.xml"

: "${IMAGE_REF:?IMAGE_REF must be set to a locally available ExApp image ref loaded by CI}"

PROJECT_NAME="${PROJECT_NAME:-cassini-prod-install-e2e}"
APP_ID="${APP_ID:-gocassini}"
DAEMON_NAME="${DAEMON_NAME:-harp_local}"
IMAGE_AS_PRODUCTION="${IMAGE_AS_PRODUCTION:-ghcr.io/codemyriad/gocassini:latest}"
APP_PORT="${APP_PORT:-8080}"
NEXTCLOUD_HOST_PORT="${NEXTCLOUD_HOST_PORT:-28080}"
NEXTCLOUD_URL="${NEXTCLOUD_URL:-http://127.0.0.1:${NEXTCLOUD_HOST_PORT}}"
NEXTCLOUD_STATUS_URL="${NEXTCLOUD_STATUS_URL:-${NEXTCLOUD_URL}/status.php}"
HARP_SHARED_KEY="${HARP_SHARED_KEY:-dogfood-shared-key-not-secret}"
CASSINI_TALK_RECORDING_SECRET="${CASSINI_TALK_RECORDING_SECRET:-}"
REGISTER_TIMEOUT_SECONDS="${REGISTER_TIMEOUT_SECONDS:-300}"
REGISTER_SUCCESS_GRACE_SECONDS="${REGISTER_SUCCESS_GRACE_SECONDS:-10}"
LOG_DIR="${LOG_DIR:-/tmp/${PROJECT_NAME}}"
HARP_BACKEND_PORT=""

export NEXTCLOUD_HOST_PORT NEXTCLOUD_URL NEXTCLOUD_STATUS_URL

mkdir -p "$LOG_DIR"

log()  { printf '[prod-install-e2e] %s\n' "$*"; }
fail() { log "FAIL: $*"; exit 1; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

require_cmd curl
require_cmd docker
require_cmd jq
require_cmd openssl

if [[ -z "$CASSINI_TALK_RECORDING_SECRET" ]]; then
  CASSINI_TALK_RECORDING_SECRET="$(openssl rand -hex 32)"
fi
export CASSINI_TALK_RECORDING_SECRET

COMPOSE=(docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE")
compose() { "${COMPOSE[@]}" "$@"; }
occ()     { compose exec -T -u www-data nextcloud php occ "$@"; }

project_network() {
  printf '%s_default\n' "$PROJECT_NAME"
}

remove_non_compose_network_containers() {
  local network id project_label
  network="$(project_network)"
  docker network inspect "$network" >/dev/null 2>&1 || return 0

  while IFS= read -r id; do
    [[ -n "$id" ]] || continue
    project_label="$(docker inspect -f '{{ index .Config.Labels "com.docker.compose.project" }}' "$id" 2>/dev/null || true)"
    if [[ "$project_label" != "$PROJECT_NAME" ]]; then
      docker rm -f "$id" >/dev/null 2>&1 || true
    fi
  done < <(docker ps -aq --filter "network=$network")
}

capture_logs() {
  compose ps >"$LOG_DIR/compose-ps.txt" 2>&1 || true
  compose logs --no-color nextcloud >"$LOG_DIR/nextcloud.log" 2>&1 || true
  compose logs --no-color appapi-harp >"$LOG_DIR/appapi-harp.log" 2>&1 || true
  compose logs --no-color reverse-proxy >"$LOG_DIR/reverse-proxy.log" 2>&1 || true

  local network
  network="$(project_network)"
  docker network inspect "$network" >"$LOG_DIR/network.json" 2>&1 || true
  docker ps -a --filter "network=$network" \
    --format '{{.ID}} {{.Names}} {{.Image}} {{.Status}}' \
    >"$LOG_DIR/network-containers.txt" 2>&1 || true

  local id name
  while read -r id name _; do
    [[ -n "${id:-}" && -n "${name:-}" ]] || continue
    docker inspect "$id" >"$LOG_DIR/container-${name}.inspect.json" 2>&1 || true
    docker logs "$id" >"$LOG_DIR/container-${name}.log" 2>&1 || true
  done < <(docker ps -a --filter "network=$network" --format '{{.ID}} {{.Names}} {{.Image}}')
}

cleanup() {
  local rc=$?
  log "cleanup (rc=$rc)"
  capture_logs
  remove_non_compose_network_containers
  compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  if [[ $rc -ne 0 ]]; then
    log "logs saved under $LOG_DIR"
    log "appapi-harp log tail:"
    tail -n 80 "$LOG_DIR/appapi-harp.log" 2>/dev/null | sed 's/^/    /' || true
    log "nextcloud log tail:"
    tail -n 80 "$LOG_DIR/nextcloud.log" 2>/dev/null | sed 's/^/    /' || true
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

find_exapp_container() {
  local network cid
  network="$(project_network)"

  cid="$(docker ps -q --filter "network=$network" --filter "ancestor=$IMAGE_AS_PRODUCTION" | head -n1)"
  if [[ -n "$cid" ]]; then
    printf '%s\n' "$cid"
    return 0
  fi

  docker ps --filter "network=$network" --format '{{.ID}} {{.Names}} {{.Image}}' \
    | awk -v app="$APP_ID" '$2 ~ app {print $1; exit}'
}

wait_for_nextcloud_after_restart() {
  log "waiting for Nextcloud after restart"
  for attempt in $(seq 1 90); do
    if occ status 2>&1 | grep -q "installed: true"; then
      log "OK Nextcloud back up after ${attempt}s"
      return 0
    fi
    sleep 1
  done
  fail "Nextcloud did not come back after restart"
}

assert_appapi_metadata_json() {
  log "asserting AppAPI HaRP metadata returns JSON for enabled $APP_ID"
  local code url
  url="${NEXTCLOUD_URL}/index.php/apps/app_api/harp/exapp-meta?appId=${APP_ID}"

  code="$(curl -sS \
    -H "harp-shared-key: ${HARP_SHARED_KEY}" \
    -o "$LOG_DIR/exapp-meta.json" \
    -w '%{http_code}' \
    "$url" || echo 000)"
  [[ "$code" == "200" ]] \
    || fail "AppAPI HaRP metadata returned HTTP $code; see $LOG_DIR/exapp-meta.json"

  jq -er '
    ((.host // "") | length > 0)
    and ((.port // 0) > 0)
    and ((.routes // []) | length > 0)
    and any((.routes // [])[]; .url == "^api\\/v1\\/welcome$")
  ' "$LOG_DIR/exapp-meta.json" >/dev/null \
    || fail "AppAPI HaRP metadata did not describe a routable $APP_ID backend; see $LOG_DIR/exapp-meta.json"

  HARP_BACKEND_PORT="$(jq -r '.port' "$LOG_DIR/exapp-meta.json")"
  log "OK AppAPI HaRP metadata JSON describes enabled $APP_ID backend on port $HARP_BACKEND_PORT"
}

assert_app_enabled() {
  log "asserting AppAPI reports $APP_ID as [enabled]"
  occ app_api:app:list >"$LOG_DIR/app-list.txt"
  grep -Eq "(^|[[:space:]])${APP_ID}[[:space:]].*\\[enabled\\]" "$LOG_DIR/app-list.txt" \
    || fail "app_api:app:list did not report $APP_ID as [enabled]; see $LOG_DIR/app-list.txt"

  log "OK AppAPI reports $APP_ID [enabled]"
  assert_appapi_metadata_json
}

assert_exapp_container_healthy() {
  log "asserting installed ExApp container is healthy"
  local cid health
  for attempt in $(seq 1 90); do
    cid="$(find_exapp_container || true)"
    if [[ -n "$cid" ]]; then
      health="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$cid" 2>/dev/null || true)"
      case "$health" in
        healthy)
          log "OK ExApp container ${cid:0:12} is healthy"
          return 0
          ;;
        unhealthy)
          fail "ExApp container ${cid:0:12} became unhealthy"
          ;;
      esac
    fi
    sleep 1
  done
  fail "ExApp container did not become healthy"
}

assert_exapp_secret_env() {
  log "asserting installed ExApp container received Talk recording secret env"
  local cid
  cid="$(find_exapp_container || true)"
  [[ -n "$cid" ]] || fail "cannot find installed ExApp container"

  docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$cid" \
    | awk -F= '$1 == "CASSINI_TALK_RECORDING_SECRET" && length($2) > 0 { found = 1 } END { exit found ? 0 : 1 }' \
    || fail "installed ExApp container is missing non-empty CASSINI_TALK_RECORDING_SECRET"

  log "OK ExApp container has non-empty CASSINI_TALK_RECORDING_SECRET"
}

assert_harp_backend_registered() {
  if [[ -z "$HARP_BACKEND_PORT" ]]; then
    assert_appapi_metadata_json
  fi

  log "asserting HaRP has a backend listener for $APP_ID on port $HARP_BACKEND_PORT"
  for attempt in $(seq 1 30); do
    if compose exec -T appapi-harp sh -c "netstat -tnl | grep -Eq '[:.]${HARP_BACKEND_PORT}[[:space:]]'"; then
      log "OK HaRP has a backend listener on port $HARP_BACKEND_PORT"
      return 0
    fi
    sleep 1
  done
  fail "HaRP did not expose the ExApp backend listener on port $HARP_BACKEND_PORT"
}

assert_proxy_route_through_harp() {
  log "asserting AppAPI proxy routes through HaRP to $APP_ID"
  local url code body
  url="http://127.0.0.1:${NEXTCLOUD_HOST_PORT}/index.php/apps/app_api/proxy/${APP_ID}/api/v1/welcome"
  body="$LOG_DIR/welcome.json"

  for attempt in $(seq 1 30); do
    code="$(curl -sS -o "$body" -w '%{http_code}' "$url" || echo 000)"
    if [[ "$code" == "200" ]] && jq -e '.version == 1' "$body" >/dev/null 2>&1; then
      log "OK AppAPI proxy public route reached Cassini through HaRP"
      return 0
    fi
    sleep 1
  done
  fail "AppAPI proxy welcome route did not reach Cassini through HaRP (last HTTP $code; see $body)"
}

assert_disable_enable_lifecycle_cycle() {
  log "cycling disable+enable to assert PUT /enabled callbacks remain live"
  occ app_api:app:disable "$APP_ID" >/dev/null
  occ app_api:app:enable "$APP_ID" >/dev/null
  assert_app_enabled
}

run_app_register() {
  local pid deadline grace_deadline rc
  rm -f "$LOG_DIR/register.log"

  setsid docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE" \
    exec -T -u www-data nextcloud php occ \
    app_api:app:register "$APP_ID" "$DAEMON_NAME" \
      --info-xml /tmp/gocassini-info.xml \
      --env "CASSINI_TALK_RECORDING_SECRET=$CASSINI_TALK_RECORDING_SECRET" \
      --test-deploy-mode \
      --wait-finish \
    >"$LOG_DIR/register.log" 2>&1 &
  pid=$!
  deadline=$((SECONDS + REGISTER_TIMEOUT_SECONDS))

  while kill -0 "$pid" >/dev/null 2>&1; do
    if grep -Eq "ExApp ${APP_ID} deployed successfully|deployed successfully" "$LOG_DIR/register.log"; then
      grace_deadline=$((SECONDS + REGISTER_SUCCESS_GRACE_SECONDS))
      while kill -0 "$pid" >/dev/null 2>&1 && (( SECONDS < grace_deadline )); do
        sleep 1
      done
      if kill -0 "$pid" >/dev/null 2>&1; then
        log "register reported success; stopping still-running occ wait process"
        kill -TERM "-$pid" >/dev/null 2>&1 || kill -TERM "$pid" >/dev/null 2>&1 || true
        sleep 2
        kill -KILL "-$pid" >/dev/null 2>&1 || kill -KILL "$pid" >/dev/null 2>&1 || true
      fi
      wait "$pid" >/dev/null 2>&1 || true
      return 0
    fi

    if (( SECONDS >= deadline )); then
      kill -TERM "-$pid" >/dev/null 2>&1 || kill -TERM "$pid" >/dev/null 2>&1 || true
      wait "$pid" >/dev/null 2>&1 || true
      tail -n 160 "$LOG_DIR/register.log" >&2 || true
      fail "app:register did not finish within ${REGISTER_TIMEOUT_SECONDS}s"
    fi

    sleep 2
  done

  set +e
  wait "$pid"
  rc=$?
  set -e
  if [[ $rc -ne 0 ]]; then
    tail -n 160 "$LOG_DIR/register.log" >&2 || true
    fail "app:register failed"
  fi
}

log "cleaning previous prod-path install stack state"
remove_non_compose_network_containers
compose down --volumes --remove-orphans >/dev/null 2>&1 || true

log "tagging loaded image $IMAGE_REF as $IMAGE_AS_PRODUCTION for info.xml install"
docker image inspect "$IMAGE_REF" >/dev/null
docker tag "$IMAGE_REF" "$IMAGE_AS_PRODUCTION"

log "starting Nextcloud + AppAPI HaRP prod-path stack"
compose up -d nextcloud db appapi-harp reverse-proxy

log "granting Nextcloud access to the host Docker socket"
SOCKET_GID="$(compose exec -T nextcloud stat -c '%g' /var/run/docker.sock)"
compose exec -T -u root nextcloud sh -c "
  EXISTING_GROUP=\$(getent group $SOCKET_GID | cut -d: -f1)
  if [ -z \"\$EXISTING_GROUP\" ]; then
    groupadd -g $SOCKET_GID docker-host
    GROUP_NAME=docker-host
  else
    GROUP_NAME=\$EXISTING_GROUP
  fi
  usermod -aG \"\$GROUP_NAME\" www-data
"
compose restart nextcloud >/dev/null

log "bootstrapping Nextcloud"
SPREED_PROFILE=default PROJECT_NAME="$PROJECT_NAME" \
  "$SCRIPT_DIR/bootstrap.sh" >"$LOG_DIR/bootstrap.log" 2>&1 \
  || { tail -n 120 "$LOG_DIR/bootstrap.log" >&2; fail "bootstrap failed"; }

log "installing + enabling app_api"
occ app:install app_api >/dev/null 2>&1 || true
occ app:enable app_api >/dev/null

log "patching AppAPI CSP for ExApp proxy responses"
compose exec -T nextcloud php <"$SCRIPT_DIR/patch-csp.php" >"$LOG_DIR/patch-csp.log" 2>&1
compose restart nextcloud >/dev/null
wait_for_nextcloud_after_restart

log "registering HaRP deploy daemon"
occ app_api:daemon:unregister docker_local >/dev/null 2>&1 || true
occ app_api:daemon:unregister "$DAEMON_NAME" >/dev/null 2>&1 || true
occ app_api:daemon:register \
  "$DAEMON_NAME" \
  "HaRP (local prod-path CI)" \
  docker-install \
  http \
  "appapi-harp:8780" \
  "http://reverse-proxy" \
  --net="$(project_network)" \
  --harp \
  --harp_frp_address "appapi-harp:8782" \
  --harp_shared_key "$HARP_SHARED_KEY" \
  --set-default \
  --compute_device=cpu >/dev/null

log "mapping ghcr.io to local Docker images for the deploy daemon"
occ app_api:daemon:registry:add "$DAEMON_NAME" \
  --registry-from=ghcr.io --registry-to=local >/dev/null

log "copying appinfo/info.xml into Nextcloud"
compose cp "$INFO_XML" nextcloud:/tmp/gocassini-info.xml >/dev/null
compose exec -T -u root nextcloud chown www-data:www-data /tmp/gocassini-info.xml

log "registering $APP_ID via info.xml through the HaRP daemon"
occ app_api:app:unregister "$APP_ID" --force >/dev/null 2>&1 || true
run_app_register

grep -q 'heartbeat check failed' "$LOG_DIR/register.log" \
  && fail "register reported heartbeat failure; see $LOG_DIR/register.log"

assert_app_enabled
assert_exapp_container_healthy
assert_exapp_secret_env
assert_harp_backend_registered
assert_proxy_route_through_harp
assert_disable_enable_lifecycle_cycle

log "prod-path install e2e passed"
