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
# No CASSINI_OPERATOR_BASE_PATH injection: a real AppAPI deploy never sets
# CASSINI_* vars, so this test relies on the /operator default baked into the
# runtime image and must catch the image ever losing it.
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
       {url: "^published\\/.+$",                    verb: "GET,HEAD", access_level: 1},
       {url: "^img\\/app\\.svg$",                   verb: "GET,HEAD", access_level: 1},
       {url: "^ui\\/viewer\\.js$",                  verb: "GET,HEAD", access_level: 1},
       {url: "^ui\\/viewer\\.css$",                 verb: "GET,HEAD", access_level: 1},
       {url: "^ui\\/control-panel\\.js$",           verb: "GET,HEAD", access_level: 2},
       {url: "^ui\\/control-panel\\.css$",          verb: "GET,HEAD", access_level: 2}
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

# --- 6b. Assert the Nextcloud navigation registration landed ---------------
#
# The /enabled handler registers the top-menu entries (viewer for users,
# control-panel for admins) in a goroutine after answering AppAPI, so poll
# briefly. GET /api/v1/ui/top-menu is AppAPI-authenticated with the same
# shared secret the app was registered with; 200 = entry exists, 404 = not.

AUTH_B64=$(printf ':%s' "$APP_SECRET" | base64 | tr -d '\n')
assert_top_menu_registered() {
  local name="$1" status=000
  for attempt in $(seq 1 30); do
    status=$(curl -s -o /dev/null -w '%{http_code}' \
      -H "AUTHORIZATION-APP-API: $AUTH_B64" \
      -H "EX-APP-ID: $APP_ID" \
      -H "EX-APP-VERSION: $APP_VERSION" \
      -H "OCS-APIRequest: true" \
      "http://127.0.0.1:${NEXTCLOUD_HOST_PORT}/ocs/v2.php/apps/app_api/api/v1/ui/top-menu?name=${name}")
    if [[ "$status" == "200" ]]; then
      log "OK   top-menu entry \"$name\" registered (after ${attempt}s)"
      return 0
    fi
    sleep 1
  done
  fail "top-menu entry \"$name\" not registered after 30s (last=$status)"
}

log "checking Nextcloud navigation (top-menu) registration"
assert_top_menu_registered viewer
assert_top_menu_registered control-panel

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
assert_status admin   "admin:admin"                       "ui/control-panel.js" 200
assert_status admin   "admin:admin"                       "ui/control-panel.css" 200

log "checking proxied route access for $TEST_USER (USER tier)"
assert_status "$TEST_USER" "$TEST_USER:$TEST_USER_PASSWORD" "viewer/"        200
assert_status "$TEST_USER" "$TEST_USER:$TEST_USER_PASSWORD" "control-panel/" 404
assert_status "$TEST_USER" "$TEST_USER:$TEST_USER_PASSWORD" "operator/jobs"  404
assert_status "$TEST_USER" "$TEST_USER:$TEST_USER_PASSWORD" "ui/viewer.js"   200
# ui/viewer.css must be proxy-reachable at USER tier: D-383 injects it into the
# viewer's shadow root via a runtime <link>, so the proxy has to serve it.
assert_status "$TEST_USER" "$TEST_USER:$TEST_USER_PASSWORD" "ui/viewer.css"  200
assert_status "$TEST_USER" "$TEST_USER:$TEST_USER_PASSWORD" "ui/control-panel.js" 404
assert_status "$TEST_USER" "$TEST_USER:$TEST_USER_PASSWORD" "ui/control-panel.css" 404
assert_status "$TEST_USER" "$TEST_USER:$TEST_USER_PASSWORD" "img/app.svg"    200

# --- 7c. Assert the admin UI actually loads (not just 200 + empty) --------
#
# A status-code-only check passes even when the proxy returns a placeholder
# or an error page styled to look benign. The contract a Nextcloud admin
# actually cares about is "I logged in as admin, opened the cassini admin
# UI, and saw the real control panel." Pin that by fetching the body and
# asserting it contains the SPA's title marker (cassini-control-panel/
# index.html). If the SPA ever stops shipping in the image, or the proxy
# silently degrades to an empty 200, this catches it.

assert_admin_ui_loads() {
  local body
  body=$(curl -sS -u "admin:admin" "$PROXY/control-panel/")
  if ! grep -qF "<title>Cassini Control Panel</title>" <<<"$body"; then
    log "admin /control-panel/ body did not contain the expected SPA title."
    log "first 200 chars of response:"
    log "${body:0:200}"
    fail "admin UI returned 200 but body is not the cassini SPA"
  fi
  log "OK   admin /control-panel/ returns the cassini SPA HTML"
}

log "checking that admin UI actually loads (body contains SPA title)"
assert_admin_ui_loads

# --- 7d. Embedded viewer wiring (D-381) -----------------------------------
#
# The viewer renders INSIDE Nextcloud on AppAPI's embedded page
# (/index.php/apps/app_api/embedded/<app>/viewer), which carries a permissive
# nonce CSP — not an iframe of proxied HTML. This asserts the wiring HTTP-level
# (HONEST LIMIT: proves registered+served+page-references-nonce'd-script+
# catalog-reachable, NOT pixel render — there is no headless browser in repo):
#   1. Seed a catalog so /published/catalog.json is reachable.
#   2. The embedded page (as the test user) is 200 AND references the
#      registered, nonce'd <script src=".../proxy/<app>/ui/viewer.js">.
#   3. The proxied catalog is 200 + a valid cassini.viewer.catalog.v1.

# SiteRoot inside the container: the operator serves /published/ from
# CASSINI_OPERATOR_SITE_ROOT. This e2e runs the container without
# APP_PERSISTENT_STORAGE, so the baked image default applies unchanged; read it
# from the container env rather than hardcoding.
SITE_ROOT=$(docker exec "$CONTAINER_NAME" printenv CASSINI_OPERATOR_SITE_ROOT 2>/dev/null || true)
SITE_ROOT="${SITE_ROOT:-/srv/cassini-site/published}"

log "seeding catalog at ${SITE_ROOT}/catalog.json inside the container"
docker exec "$CONTAINER_NAME" sh -c \
  'mkdir -p "$1" && printf "%s" "{\"version\":\"cassini.viewer.catalog.v1\",\"meetings\":[]}" > "$1/catalog.json"' \
  _ "$SITE_ROOT" \
  || fail "could not seed catalog into the container at $SITE_ROOT"

# A plain Basic GET on the embedded page can 302 to the login flow, so use a
# cookie-jar login (like d263-nextcloud-lifecycle.sh) for the test user.
NC_BASE="http://127.0.0.1:${NEXTCLOUD_HOST_PORT}"
# The embedded page is a regular authenticated Nextcloud route (served by the
# app_api PHP app, not the ExApp proxy). Nextcloud's BasicAuth middleware
# authenticates GETs per-request, so reuse the same HTTP Basic creds the proxy
# route checks above already use successfully (a cookie-jar login flow is
# brittle and was not establishing a session). The viewer entry is USER tier,
# so the regular e2euser can open it.
EMBEDDED_URL="$NC_BASE/index.php/apps/app_api/embedded/${APP_ID}/viewer"
log "checking embedded viewer page $EMBEDDED_URL"
embedded_status=$(curl -sS -u "$TEST_USER:$TEST_USER_PASSWORD" \
  -o "$LOG_DIR/embedded-viewer.html" -w '%{http_code}' "$EMBEDDED_URL")
if [[ "$embedded_status" != "200" ]]; then
  log "first 300 chars of embedded response:"
  log "$(head -c 300 "$LOG_DIR/embedded-viewer.html" 2>/dev/null)"
  fail "embedded viewer page expected 200 got $embedded_status"
fi

# The registered ui/script is injected as a Nextcloud-nonce'd
# <script ... nonce="…" src=".../proxy/<app>/ui/viewer.js">. Assert the nonce is
# ON THE SAME <script> tag that loads viewer.js — under CSP strict-dynamic only
# a nonce'd script runs; a raw host-allowlisted src is ignored, so a nonce
# elsewhere on the page is not sufficient. Attribute order is not guaranteed, so
# isolate the viewer.js <script> tag and require nonce= within it.
#
# D-383: the stylesheet is NO LONGER a global ui/style <link> on the page — the
# IIFE injects it into the SPA's shadow root at runtime — so we do NOT assert a
# page-level ui/viewer.css <link> here. That the stylesheet is served + proxy-
# reachable is covered by the ui/viewer.css route check in section 6 above.
embedded_body=$(cat "$LOG_DIR/embedded-viewer.html")
viewer_script_tag=$(grep -oE "<script[^>]*proxy/${APP_ID}/ui/viewer\.js[^>]*>" <<<"$embedded_body" | head -1)
if [[ -z "$viewer_script_tag" ]]; then
  log "embedded page did not reference the registered ui/viewer.js; first 400 chars:"
  log "$(head -c 400 "$LOG_DIR/embedded-viewer.html")"
  fail "embedded viewer page does not reference the proxied ui/viewer.js bundle"
fi
if ! grep -qE 'nonce="[^"]+"' <<<"$viewer_script_tag"; then
  log "viewer.js script tag was: $viewer_script_tag"
  fail "the ui/viewer.js <script> tag is not nonce'd (CSP strict-dynamic would block the viewer)"
fi
log "OK   embedded viewer page 200 with a nonce'd proxy ui/viewer.js (CSS injected into the shadow root)"

# --- 7e. Embedded control-panel wiring (D-382) ----------------------------
#
# Same shape as the viewer check above, for the admin control panel: it now
# renders directly on AppAPI's nonce'd embedded page from a self-mounting IIFE
# + stylesheet (the D-382 embedded build), not an iframe of proxied SPA HTML
# (which AppAPI's default-src 'none' CSP blocked, leaving the panel blank).
# The control-panel entry is ADMIN tier, so use the admin Basic creds (the
# regular e2euser would 404 on the embedded route). HONEST LIMIT: this proves
# registered+served+page-references-nonce'd-script, NOT pixel render (no
# headless browser in repo).
CP_EMBEDDED_URL="$NC_BASE/index.php/apps/app_api/embedded/${APP_ID}/control-panel"
log "checking embedded control-panel page $CP_EMBEDDED_URL"
cp_embedded_status=$(curl -sS -u "admin:admin" \
  -o "$LOG_DIR/embedded-control-panel.html" -w '%{http_code}' "$CP_EMBEDDED_URL")
if [[ "$cp_embedded_status" != "200" ]]; then
  log "first 300 chars of embedded response:"
  log "$(head -c 300 "$LOG_DIR/embedded-control-panel.html" 2>/dev/null)"
  fail "embedded control-panel page expected 200 got $cp_embedded_status"
fi

# Assert the nonce is ON THE SAME <script> tag that loads control-panel.js —
# under CSP strict-dynamic only a nonce'd script runs; a raw host-allowlisted
# src is ignored, so a nonce elsewhere on the page is not sufficient. Attribute
# order is not guaranteed, so isolate the control-panel.js <script> tag and
# require nonce= within it.
#
# D-383: as with the viewer, the stylesheet is injected into the panel's shadow
# root at runtime, not a page-level ui/style <link>, so we do NOT assert a
# page-level ui/control-panel.css <link>. The ui/control-panel.css route check in
# section 6 (admin 200 / user 404) covers that the stylesheet is served + gated.
cp_embedded_body=$(cat "$LOG_DIR/embedded-control-panel.html")
cp_script_tag=$(grep -oE "<script[^>]*proxy/${APP_ID}/ui/control-panel\.js[^>]*>" <<<"$cp_embedded_body" | head -1)
if [[ -z "$cp_script_tag" ]]; then
  log "embedded page did not reference the registered ui/control-panel.js; first 400 chars:"
  log "$(head -c 400 "$LOG_DIR/embedded-control-panel.html")"
  fail "embedded control-panel page does not reference the proxied ui/control-panel.js bundle"
fi
if ! grep -qE 'nonce="[^"]+"' <<<"$cp_script_tag"; then
  log "control-panel.js script tag was: $cp_script_tag"
  fail "the ui/control-panel.js <script> tag is not nonce'd (CSP strict-dynamic would block the panel)"
fi
log "OK   embedded control-panel page 200 with a nonce'd proxy ui/control-panel.js (CSS injected into the shadow root)"

log "checking proxied catalog $PROXY/published/catalog.json"
catalog_status=$(curl -sS -u "$TEST_USER:$TEST_USER_PASSWORD" \
  -o "$LOG_DIR/published-catalog.json" -w '%{http_code}' "$PROXY/published/catalog.json")
if [[ "$catalog_status" != "200" ]]; then
  fail "proxied catalog expected 200 got $catalog_status"
fi
catalog_version=$(jq -r '.version' "$LOG_DIR/published-catalog.json" 2>/dev/null || echo "")
if [[ "$catalog_version" != "cassini.viewer.catalog.v1" ]]; then
  log "catalog body:"
  log "$(head -c 300 "$LOG_DIR/published-catalog.json" 2>/dev/null)"
  fail "proxied catalog is not a valid cassini.viewer.catalog.v1 (version=$catalog_version)"
fi
log "OK   proxied /published/catalog.json 200 and is a valid cassini.viewer.catalog.v1"

log "install-e2e passed"
