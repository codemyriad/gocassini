#!/usr/bin/env bash
# End-to-end install test for the Cassini ExApp against a real Nextcloud +
# AppAPI. This is what the existing container-level e2e (ci-e2e-exapp.sh)
# cannot cover: the install handshake — heartbeat, /init progress callback,
# proxy route patterns, the disable+enable cycle that drives PUT /enabled.
#
# Flow:
#   1. Bring up Nextcloud + db via the harness compose.yml (default profile).
#   2. Install + enable the app_api app inside Nextcloud.
#   3. Register a manual-install daemon pointed at the cassini-exapp container
#      (NOT the literal string "null" — AppAPI builds the heartbeat URL from
#      the daemon's host).
#   4. Run the Cassini ExApp image on the same compose network, with the env
#      AppAPI would inject (APP_SECRET, NEXTCLOUD_URL, etc.).
#   5. Register the ExApp via `app_api:app:register --json-info`, embedding
#      the route allowlist from appinfo/info.xml directly in the JSON.
#   6. Cycle disable→enable so AppAPI actually PUTs /enabled?enabled=1 to the
#      container (register alone only sets the Nextcloud-side flag).
#   7. Assert the proxied routes: admin sees control-panel + operator + viewer,
#      regular user sees only viewer, neither sees the others past their tier.
#
# Tear down on success and on failure. Logs land in $LOG_DIR.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$HARNESS_DIR/.." && pwd)"
COMPOSE_FILE="$HARNESS_DIR/compose.yml"
INFO_XML="$REPO_ROOT/appinfo/info.xml"

: "${IMAGE_REF:?IMAGE_REF must be set (e.g. cassini-exapp:local or ghcr.io/codemyriad/gocassini:latest)}"
PROJECT_NAME="${PROJECT_NAME:-cassini-install-e2e-$$}"
DAEMON_NAME="${DAEMON_NAME:-manual_install}"
APP_ID="${APP_ID:-gocassini}"
APP_VERSION="${APP_VERSION:-0.1.0}"
CONTAINER_NAME="${CONTAINER_NAME:-cassini-exapp-install-e2e}"
LOG_DIR="${LOG_DIR:-/tmp/cassini-install-e2e-$$}"
NEXTCLOUD_HOST_PORT="${NEXTCLOUD_HOST_PORT:-28080}"
NEXTCLOUD_URL_INTERNAL="${NEXTCLOUD_URL_INTERNAL:-http://nextcloud}"
TEST_USER="${TEST_USER:-e2euser}"
TEST_USER_PASSWORD="${TEST_USER_PASSWORD:-Tn8mY3qVrJ2x!E2e}"
APP_SECRET="${APP_SECRET:-$(head -c 24 /dev/urandom | base64 | tr -d '/+=' | head -c 32)}"

mkdir -p "$LOG_DIR"

log()  { printf '[install-e2e] %s\n' "$*"; }
fail() { log "FAIL: $*"; exit 1; }

compose() { docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE" "$@"; }
occ()     { compose exec -T -u www-data nextcloud php occ "$@"; }

cleanup() {
  local rc=$?
  log "cleanup (rc=$rc)"
  docker logs "$CONTAINER_NAME" >"$LOG_DIR/cassini-exapp.log" 2>&1 || true
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  compose logs nextcloud >"$LOG_DIR/nextcloud.log" 2>&1 || true
  compose down --volumes >/dev/null 2>&1 || true
  if [[ $rc -ne 0 ]]; then
    log "container log tail:"
    tail -n 80 "$LOG_DIR/cassini-exapp.log" 2>/dev/null | sed 's/^/    /' || true
    log "nextcloud log tail:"
    tail -n 80 "$LOG_DIR/nextcloud.log" 2>/dev/null | sed 's/^/    /' || true
  fi
}
trap cleanup EXIT

# --- 1. Bring up Nextcloud + db -------------------------------------------

log "starting Nextcloud + db on host port $NEXTCLOUD_HOST_PORT"
SPREED_PROFILE=default \
  compose up -d nextcloud db

SPREED_PROFILE=default PROJECT_NAME="$PROJECT_NAME" \
  "$HARNESS_DIR/bin/bootstrap.sh" >"$LOG_DIR/bootstrap.log" 2>&1 \
  || { tail "$LOG_DIR/bootstrap.log"; fail "bootstrap failed"; }

# --- 2. Install + enable app_api ------------------------------------------

log "installing + enabling app_api"
occ app:install app_api >/dev/null 2>&1 || true   # idempotent on rerun
occ app:enable  app_api >/dev/null

# --- 3. Register the manual_install daemon --------------------------------
#
# host must be a hostname Nextcloud can resolve and reach. The compose network
# gives every service docker-DNS resolution, so the cassini container name
# works directly. NOT "null" — AppAPI builds the heartbeat URL by concatenating
# daemon protocol + daemon host + app port, so a literal "null" produces
# http://null:8080.

log "registering daemon $DAEMON_NAME pointed at $CONTAINER_NAME"
occ app_api:daemon:unregister "$DAEMON_NAME" >/dev/null 2>&1 || true
occ app_api:daemon:register \
  "$DAEMON_NAME" \
  "Install-E2E manual install" \
  manual-install \
  http \
  "$CONTAINER_NAME" \
  "$NEXTCLOUD_URL_INTERNAL" >/dev/null

# --- 4. Run the Cassini ExApp container -----------------------------------

log "starting Cassini ExApp container ($IMAGE_REF)"
docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
docker run -d \
  --name "$CONTAINER_NAME" \
  --network "${PROJECT_NAME}_default" \
  -e APP_HOST=0.0.0.0 \
  -e APP_PORT=8080 \
  -e APP_ID="$APP_ID" \
  -e APP_VERSION="$APP_VERSION" \
  -e APP_SECRET="$APP_SECRET" \
  -e AA_VERSION=5.0.0 \
  -e CASSINI_APPAPI_REQUIRED=true \
  -e CASSINI_OPERATOR_BASE_PATH=/operator \
  -e NEXTCLOUD_URL="$NEXTCLOUD_URL_INTERNAL" \
  --entrypoint /usr/local/bin/cassini-operator \
  "$IMAGE_REF" >/dev/null

log "waiting for /heartbeat to answer 200 (no auth)"
for attempt in $(seq 1 30); do
  status=$(docker exec "$CONTAINER_NAME" \
    curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8080/heartbeat \
    || echo 000)
  if [[ "$status" == "200" ]]; then
    log "heartbeat ok after ${attempt}s"
    break
  fi
  [[ $attempt -eq 30 ]] && fail "heartbeat never reached 200 (last=$status)"
  sleep 1
done

# --- 5. Register the ExApp with full JSON (routes embedded) --------------
#
# AppAPI ignores --info-xml when --json-info is set, AND --json-info requires
# the routes array — register-via-XML mode is only triggered when --json-info
# is omitted. We mirror the info.xml routes here so the install path is
# deterministic regardless of where AppAPI reads from. Patterns follow the
# same escaping convention the proxy controller requires: no leading slash,
# internal slashes escaped as \/.

log "registering ExApp $APP_ID via app_api:app:register"
occ app_api:app:unregister "$APP_ID" --force >/dev/null 2>&1 || true

# jq-built JSON keeps escaping honest (we'd otherwise have to triple-escape
# backslashes through the shell, the docker exec, and occ).
JSON=$(jq -nc \
  --arg secret  "$APP_SECRET" \
  --arg appid   "$APP_ID" \
  --arg version "$APP_VERSION" \
  '{
     appid:               $appid,
     name:                "Cassini",
     daemon_config_name:  "'"$DAEMON_NAME"'",
     version:             $version,
     secret:              $secret,
     port:                8080,
     protocol:            "http",
     system_app:          0,
     routes: [
       {url: "^control-panel\\/?$",                 verb: "GET",      access_level: 2},
       {url: "^control-panel\\/.+$",                verb: "GET,HEAD", access_level: 2},
       {url: "^operator\\/jobs\\/?$",               verb: "GET,POST", access_level: 2},
       {url: "^operator\\/jobs\\/[^\\/]+\\/?$",     verb: "GET",      access_level: 2},
       {url: "^operator\\/jobs\\/[^\\/]+\\/stop\\/?$",  verb: "POST", access_level: 2},
       {url: "^operator\\/jobs\\/[^\\/]+\\/rerun\\/?$", verb: "POST", access_level: 2},
       {url: "^operator\\/events\\/?$",             verb: "GET",      access_level: 2},
       {url: "^viewer\\/?$",                        verb: "GET",      access_level: 1},
       {url: "^viewer\\/.+$",                       verb: "GET,HEAD", access_level: 1},
       {url: "^published\\/.+$",                    verb: "GET,HEAD", access_level: 1}
     ]
   }')

occ app_api:app:register "$APP_ID" "$DAEMON_NAME" \
  --json-info "$JSON" \
  --force-scopes \
  --wait-finish \
  >"$LOG_DIR/register.log" 2>&1 \
  || { tail "$LOG_DIR/register.log"; fail "app:register failed"; }
grep -q 'heartbeat check failed' "$LOG_DIR/register.log" \
  && fail "register reported heartbeat failure (see $LOG_DIR/register.log)"

# --- 6. Force PUT /enabled by cycling -------------------------------------
#
# `app_api:app:register` sets the DB enabled flag but never PUTs /enabled to
# the container. `app_api:app:enable` short-circuits when the DB flag is
# already set ("already enabled"). The reliable way to make AppAPI actually
# send the lifecycle callback is disable → enable.

log "cycling disable+enable to trigger PUT /enabled on the container"
occ app_api:app:disable "$APP_ID" >/dev/null
occ app_api:app:enable  "$APP_ID" >/dev/null

# Confirm the container received PUT /enabled?enabled=1.
state=$(docker exec "$CONTAINER_NAME" cat /var/lib/cassini-operator/app-state.json 2>/dev/null \
  || echo '{}')
echo "$state" | grep -q '"enabled":true' \
  || fail "container state not enabled after cycle: $state"

# --- 7. Create a regular test user (admin already exists) -----------------

log "creating regular user $TEST_USER"
OC_PASS="$TEST_USER_PASSWORD" \
  compose exec -T -e OC_PASS="$TEST_USER_PASSWORD" -u www-data nextcloud \
  php occ user:add --password-from-env --display-name="$TEST_USER" "$TEST_USER" \
  >/dev/null 2>&1 || true   # idempotent

# --- 7b. Assert proxied routes --------------------------------------------

PROXY="http://127.0.0.1:${NEXTCLOUD_HOST_PORT}/index.php/apps/app_api/proxy/${APP_ID}"

assert_status() {
  local who="$1" creds="$2" path="$3" expected="$4"
  local got
  if [[ -z "$creds" ]]; then
    got=$(curl -s -o /dev/null -w '%{http_code}' "$PROXY/$path")
  else
    got=$(curl -s -u "$creds" -o /dev/null -w '%{http_code}' "$PROXY/$path")
  fi
  if [[ "$got" == "$expected" ]]; then
    log "OK   $who $path -> $got"
  else
    fail "$who $path expected $expected got $got"
  fi
}

log "checking proxied route access for admin"
assert_status admin   "admin:admin"                       "control-panel/" 200
assert_status admin   "admin:admin"                       "operator/jobs"  200
assert_status admin   "admin:admin"                       "viewer/"        200

log "checking proxied route access for $TEST_USER (USER tier)"
assert_status "$TEST_USER" "$TEST_USER:$TEST_USER_PASSWORD" "viewer/"        200
assert_status "$TEST_USER" "$TEST_USER:$TEST_USER_PASSWORD" "control-panel/" 404
assert_status "$TEST_USER" "$TEST_USER:$TEST_USER_PASSWORD" "operator/jobs"  404

log "install-e2e passed"
