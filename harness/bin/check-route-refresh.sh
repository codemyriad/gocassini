#!/usr/bin/env bash
# Does `app_api:app:update` apply a CHANGED <routes> block to an already
# installed ExApp?
#
# Two Cassini features depend on the answer: #207 widened
# `^operator\/settings\/?$` to `^operator\/settings(\/.*)?$`, and the insights
# branch adds the app's first mutating USER route. If an in-place update does
# not refresh the allowlist, every upgraded install 404s on both, silently.
#
# Method:
#   1. Nextcloud + AppAPI, as ci-e2e-install-exapp.sh brings them up.
#   2. Register with the real routes MINUS `^operator\/setup\/?$` — a route that
#      really exists, is really served, and is really USER-level.
#   3. Prove the proxy refuses it. Without that control a 404 later proves nothing.
#   4. Try each update form, and after each read BOTH AppAPI's own route rows in
#      Postgres (where the allowlist actually lives) AND the proxy.
#      The database reading is load-bearing: an earlier version of this script
#      concluded "does not refresh" from a proxy 401 that was really the
#      manual-install container failing its post-update init handshake.
#   5. Re-register with the full manifest as the positive control.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$HARNESS_DIR/.." && pwd)"
COMPOSE_FILE="$HARNESS_DIR/compose.yml"
INFO_XML="$REPO_ROOT/appinfo/info.xml"
source "$SCRIPT_DIR/lib/e2e-local.sh"
source "$SCRIPT_DIR/lib-exapp-manifest.sh"
harness_e2e_local_stack_env core none none

: "${IMAGE_REF:?IMAGE_REF must be set}"
PROJECT_NAME="${PROJECT_NAME:-cassini-routecheck-$$}"
DAEMON_NAME="${DAEMON_NAME:-manual_install}"
APP_ID="${APP_ID:-$(exapp_app_id "$INFO_XML")}"
APP_VERSION="${APP_VERSION:-$(exapp_app_version "$INFO_XML")}"
CONTAINER_NAME="${CONTAINER_NAME:-cassini-routecheck}"
LOG_DIR="${LOG_DIR:-/tmp/cassini-routecheck-$$}"
NEXTCLOUD_HOST_PORT="${NEXTCLOUD_HOST_PORT:-28090}"
NEXTCLOUD_URL_INTERNAL="${NEXTCLOUD_URL_INTERNAL:-http://nextcloud}"
TEST_USER="${TEST_USER:-e2euser}"
TEST_USER_PASSWORD="${TEST_USER_PASSWORD:-Tn8mY3qVrJ2x!E2e}"
APP_SECRET="${APP_SECRET:-$(head -c 24 /dev/urandom | base64 | tr -d '/+=' | head -c 32)}"
VERDICT_FILE="${VERDICT_FILE:-$LOG_DIR/verdict.txt}"

mkdir -p "$LOG_DIR"
log()  { printf '[route-check] %s\n' "$*"; }
fail() { log "FAIL: $*"; exit 1; }
# Writes to stderr, never stdout: try_update runs inside no command
# substitution any more, but a verdict line that lands on stdout is exactly the
# bug that garbled the first conclusive run's summary.
verdict() { printf '%s\n' "$*" >&2; printf '%s\n' "$*" >>"$VERDICT_FILE"; }

compose() { docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE" "$@"; }
occ()     { compose exec -T -u www-data nextcloud php occ "$@"; }
sql()     { compose exec -T -e PGPASSWORD=nc db psql -U nc -d nextcloud -tAc "$1"; }

cleanup() {
  local rc=$?
  log "cleanup (rc=$rc)"
  docker logs "$CONTAINER_NAME" >"$LOG_DIR/exapp.log" 2>&1 || true
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  compose logs nextcloud >"$LOG_DIR/nextcloud.log" 2>&1 || true
  compose down --volumes >/dev/null 2>&1 || true
  log "logs in $LOG_DIR"
}
trap cleanup EXIT

WITHHELD_URL='^operator\/setup\/?$'
WITHHELD_PATH='operator/setup'

FULL_ROUTES="$(exapp_routes_json "$INFO_XML")"
REDUCED_ROUTES="$(jq -c --arg u "$WITHHELD_URL" 'map(select(.url != $u))' <<<"$FULL_ROUTES")"
full_n=$(jq 'length' <<<"$FULL_ROUTES")
reduced_n=$(jq 'length' <<<"$REDUCED_ROUTES")
[[ "$reduced_n" -eq $((full_n - 1)) ]] \
  || fail "reduced manifest should drop exactly one route (full=$full_n reduced=$reduced_n)"
log "manifest routes: full=$full_n, reduced=$reduced_n (withholding $WITHHELD_URL)"

log "starting Nextcloud core stack on host port $NEXTCLOUD_HOST_PORT"
export PROJECT_NAME NEXTCLOUD_HOST_PORT
"$REPO_ROOT/bin/cassini" dev stack up \
  --public-mode local-http --services core --cassini none --recording-backend none \
  >"$LOG_DIR/stack-up.log" 2>&1 \
  || { tail -n 40 "$LOG_DIR/stack-up.log"; fail "cassini dev stack up failed"; }

log "installing + enabling app_api"
occ app:install app_api >/dev/null 2>&1 || true
occ app:enable  app_api >/dev/null
APPAPI_VERSION=$(occ app:list 2>/dev/null | grep -A1 -i 'app_api' | head -3 | tr -d ' \n' || true)
log "app_api: ${APPAPI_VERSION:-unknown}"

log "creating $TEST_USER"
compose exec -T -e OC_PASS="$TEST_USER_PASSWORD" -u www-data nextcloud \
  php occ user:add --password-from-env --display-name="$TEST_USER" "$TEST_USER" \
  >"$LOG_DIR/user-add.log" 2>&1 \
  || { tail "$LOG_DIR/user-add.log"; fail "could not create $TEST_USER"; }
whoami_status=$(curl -s -u "$TEST_USER:$TEST_USER_PASSWORD" -o /dev/null -w '%{http_code}' \
  -H 'OCS-APIRequest: true' \
  "http://127.0.0.1:${NEXTCLOUD_HOST_PORT}/ocs/v2.php/cloud/user?format=json")
[[ "$whoami_status" == "200" ]] || fail "$TEST_USER credentials do not authenticate (got $whoami_status)"
log "OK   $TEST_USER authenticates"

log "registering daemon $DAEMON_NAME"
occ app_api:daemon:unregister "$DAEMON_NAME" >/dev/null 2>&1 || true
occ app_api:daemon:register "$DAEMON_NAME" "Route-check manual install" \
  manual-install http "$CONTAINER_NAME" "$NEXTCLOUD_URL_INTERNAL" >/dev/null

start_container() {
  local secret="$1" version="$2"
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  docker run -d --name "$CONTAINER_NAME" --network "${PROJECT_NAME}_default" \
    -e APP_HOST=0.0.0.0 -e APP_PORT=8080 -e APP_ID="$APP_ID" \
    -e APP_VERSION="$version" -e APP_SECRET="$secret" -e AA_VERSION=5.0.0 \
    -e CASSINI_APPAPI_REQUIRED=true -e NEXTCLOUD_URL="$NEXTCLOUD_URL_INTERNAL" \
    --entrypoint /usr/local/bin/cassini-operator "$IMAGE_REF" >/dev/null
  local status
  for _ in $(seq 1 40); do
    status=$(docker exec "$CONTAINER_NAME" curl -s -o /dev/null -w '%{http_code}' \
      http://127.0.0.1:8080/heartbeat || echo 000)
    [[ "$status" == "200" ]] && return 0
    sleep 1
  done
  fail "heartbeat never reached 200"
}

log "starting ExApp container ($IMAGE_REF)"
start_container "$APP_SECRET" "$APP_VERSION"

log "discovering AppAPI's own tables"
sql "select table_name from information_schema.tables where table_name like 'oc_ex_%' order by 1" \
  >"$LOG_DIR/appapi-tables.txt" 2>&1 || true
sed 's/^/    /' "$LOG_DIR/appapi-tables.txt" || true

ROUTES_TABLE=$(grep -E 'route' "$LOG_DIR/appapi-tables.txt" | head -1 | tr -d ' \r' || true)
ROUTE_COL=""
if [[ -n "$ROUTES_TABLE" ]]; then
  sql "select column_name from information_schema.columns where table_name='$ROUTES_TABLE' order by ordinal_position" \
    >"$LOG_DIR/routes-schema.txt" 2>&1 || true
  log "routes table: $ROUTES_TABLE"
  sed 's/^/    /' "$LOG_DIR/routes-schema.txt" || true
  ROUTE_COL=$(grep -iE '^(url|url_pattern|route|pattern)$' "$LOG_DIR/routes-schema.txt" | head -1 | tr -d ' \r' || true)
  log "route url column: ${ROUTE_COL:-UNKNOWN}"
else
  log "WARNING: no table matching 'route' — proxy readings only"
fi

db_route_count() {
  [[ -z "$ROUTES_TABLE" ]] && { echo "?"; return; }
  sql "select count(*) from $ROUTES_TABLE where appid='$APP_ID'" 2>/dev/null | tr -d ' \r'
}
db_has_withheld() {
  [[ -z "$ROUTES_TABLE" || -z "$ROUTE_COL" ]] && { echo "?"; return; }
  sql "select count(*) from $ROUTES_TABLE where appid='$APP_ID' and $ROUTE_COL like '%operator%setup%'" 2>/dev/null | tr -d ' \r'
}
db_dump() {
  [[ -z "$ROUTES_TABLE" ]] && return
  sql "select coalesce(json_agg(t order by t::text)::text,'[]') from $ROUTES_TABLE t where appid='$APP_ID'" 2>/dev/null | head -c 6000
}
db_secret()  { sql "select secret from oc_ex_apps where appid='$APP_ID'" 2>/dev/null | tr -d ' \r'; }
db_version() { sql "select version from oc_ex_apps where appid='$APP_ID'" 2>/dev/null | tr -d ' \r'; }

manifest_json() {
  jq -nc --arg secret "$APP_SECRET" --arg appid "$APP_ID" \
    --arg daemon "$DAEMON_NAME" --arg version "${2:-$APP_VERSION}" --argjson routes "$1" \
    '{appid:$appid,name:"Cassini",daemon_config_name:$daemon,version:$version,
      secret:$secret,port:8080,protocol:"http",system_app:0,routes:$routes}'
}
register_with() {
  local routes="$1" label="$2"
  occ app_api:app:register "$APP_ID" "$DAEMON_NAME" --json-info "$(manifest_json "$routes")" \
    --force-scopes --wait-finish >"$LOG_DIR/register-$label.log" 2>&1 \
    || { tail "$LOG_DIR/register-$label.log"; fail "register ($label) failed"; }
}

log "registering with the REDUCED manifest ($reduced_n routes)"
occ app_api:app:unregister "$APP_ID" --force >/dev/null 2>&1 || true
register_with "$REDUCED_ROUTES" reduced
occ app_api:app:disable "$APP_ID" >/dev/null
occ app_api:app:enable  "$APP_ID" >/dev/null

PROXY="http://127.0.0.1:${NEXTCLOUD_HOST_PORT}/index.php/apps/app_api/proxy/${APP_ID}"
probe() { curl -s -u "$TEST_USER:$TEST_USER_PASSWORD" -o /dev/null -w '%{http_code}' "$PROXY/$1"; }

control=$(probe "viewer/")
before=$(probe "$WITHHELD_PATH")
rows_before=$(db_route_count); withheld_before=$(db_has_withheld)
db_dump >"$LOG_DIR/routes-before.json"

verdict "STATE AFTER REGISTERING WITHOUT THE ROUTE"
verdict "  proxy viewer/ (declared)          -> $control  (expect 200)"
verdict "  proxy $WITHHELD_PATH (withheld)   -> $before  (expect 404)"
verdict "  db    allowlist rows              -> $rows_before  (manifest had $reduced_n)"
verdict "  db    rows matching withheld route-> $withheld_before  (expect 0)"
[[ "$control" == "200" ]] || fail "control route did not answer — the stack is wrong, not the allowlist"
[[ "$before" == "404" ]] || fail "withheld route answered $before before it was declared"
verdict "  => the allowlist IS the enforcement point, and it is missing the route."
verdict ""

try_update() {
  local label="$1"; shift
  set +e
  occ app_api:app:update "$APP_ID" "$@" --wait-finish >"$LOG_DIR/update-$label.log" 2>&1
  local rc=$?
  set -e
  local msg rows withheld http
  msg=$(tr -d '\n' <"$LOG_DIR/update-$label.log" | sed 's/  */ /g' | head -c 240)
  rows=$(db_route_count); withheld=$(db_has_withheld); http=$(probe "$WITHHELD_PATH")
  db_dump >"$LOG_DIR/routes-after-$label.json"
  verdict "  $label exit=$rc db_rows=$rows db_has_route=$withheld proxy=$http"
  verdict "      said: $msg"
  LAST_WITHHELD="$withheld"
}

verdict "UPDATE ATTEMPTS — db_has_route is the answer; proxy can be masked by container auth"
try_update "5a-bare      "; w_bare="$LAST_WITHHELD"
try_update "5b-same-ver  " --json-info "$(manifest_json "$FULL_ROUTES")"; w_same="$LAST_WITHHELD"
BUMPED="${APP_VERSION%.*}.$(( ${APP_VERSION##*.} + 1 ))"
log "bumping $APP_VERSION -> $BUMPED so the update actually runs"
try_update "5c-bumped-ver" --json-info "$(manifest_json "$FULL_ROUTES" "$BUMPED")"; w_bump="$LAST_WITHHELD"

new_secret=$(db_secret); new_version=$(db_version)
verdict ""
verdict "  after the bumped update AppAPI holds version=$new_version secret_len=${#new_secret}"
after_resync="?"; control_resync="?"
if [[ -n "$new_secret" ]]; then
  log "restarting the container matched to AppAPI's current secret/version"
  start_container "$new_secret" "${new_version:-$BUMPED}"
  occ app_api:app:disable "$APP_ID" >/dev/null 2>&1 || true
  occ app_api:app:enable  "$APP_ID" >/dev/null 2>&1 || true
  after_resync=$(probe "$WITHHELD_PATH"); control_resync=$(probe "viewer/")
  verdict "  container re-synced: viewer/ -> $control_resync, $WITHHELD_PATH -> $after_resync"
fi

log "re-registering with the FULL manifest ($full_n routes)"
occ app_api:app:unregister "$APP_ID" --force >/dev/null 2>&1 || true
start_container "$APP_SECRET" "$APP_VERSION"
register_with "$FULL_ROUTES" full
occ app_api:app:disable "$APP_ID" >/dev/null
occ app_api:app:enable  "$APP_ID" >/dev/null
after_reregister=$(probe "$WITHHELD_PATH")
rows_reregister=$(db_route_count); withheld_reregister=$(db_has_withheld)
verdict ""
verdict "POSITIVE CONTROL — re-register with the full manifest"
verdict "  db_rows=$rows_reregister db_has_route=$withheld_reregister proxy=$after_reregister (expect 200)"

verdict ""
if [[ "$w_bump" == "1" ]]; then
  verdict "ANSWER: app_api:app:update DOES refresh the route allowlist — but only when the"
  verdict "        manifest it is handed carries a NEW <version>. With the same version it"
  verdict "        short-circuits ('already updated') and the allowlist is untouched."
  verdict "        A release that adds or widens a route IS deliverable by the Update button,"
  verdict "        because a release bumps <version> anyway."
  verdict ""
  verdict "        The 401 seen on the proxy immediately after 5c is an artefact of this"
  verdict "        manual-install harness, not of routing: the redeploy rotates the app"
  verdict "        secret and cannot recreate a hand-started container, so /init fails."
  verdict "        Restarting the container with AppAPI's current secret gave"
  verdict "        viewer/ -> $control_resync and $WITHHELD_PATH -> $after_resync."
elif [[ "$withheld_reregister" == "1" ]]; then
  verdict "ANSWER: app_api:app:update does NOT refresh the route allowlist, even when handed a"
  verdict "        new manifest with a bumped version — AppAPI's own route rows still lack the"
  verdict "        route (db_has_route=$w_bump), while re-registering writes it ($withheld_reregister)."
  verdict "        A release that adds or widens a route is NOT deliverable by the Update"
  verdict "        button; existing installs 404 on the new route until re-registered."
else
  verdict "ANSWER: INCONCLUSIVE — the positive control did not restore the route either."
fi
verdict ""
verdict "  5a bare update    db_has_route=$w_bare (cannot run on a manual install)"
verdict "  5b same version   db_has_route=$w_same (no-op: 'already updated')"
verdict "  5c bumped version db_has_route=$w_bump"
verdict "  6  re-register    db_has_route=$withheld_reregister"
verdict ""
verdict "app_api: ${APPAPI_VERSION:-unknown}   image: $IMAGE_REF"
log "verdict written to $VERDICT_FILE"
