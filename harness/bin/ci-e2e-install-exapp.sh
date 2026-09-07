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
#   5. Register the ExApp via `app_api:app:register --json-info`, generating
#      the complete route allowlist from the current appinfo/info.xml.
#   6. Cycle disable→enable so AppAPI actually PUTs /enabled?enabled=1 to the
#      container (register alone only sets the Nextcloud-side flag).
#   7. Assert the proxied routes: admin sees operator + viewer, regular user
#      sees viewer but the operator JSON API stays ADMIN (403 for non-admins).
#
# Tear down on success and on failure. Logs land in $LOG_DIR.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$HARNESS_DIR/.." && pwd)"
COMPOSE_FILE="$HARNESS_DIR/compose.yml"
INFO_XML="$REPO_ROOT/appinfo/info.xml"
# shellcheck source=./lib/e2e-local.sh
source "$SCRIPT_DIR/lib/e2e-local.sh"
# shellcheck source=./lib-exapp-manifest.sh
source "$SCRIPT_DIR/lib-exapp-manifest.sh"
harness_e2e_local_stack_env core none none

: "${IMAGE_REF:?IMAGE_REF must be set (e.g. cassini-exapp:local or ghcr.io/codemyriad/gocassini:latest)}"
PROJECT_NAME="${PROJECT_NAME:-cassini-install-e2e-$$}"
DAEMON_NAME="${DAEMON_NAME:-manual_install}"
APP_ID="${APP_ID:-$(exapp_app_id "$INFO_XML")}"
APP_VERSION="${APP_VERSION:-$(exapp_app_version "$INFO_XML")}"
CONTAINER_NAME="${CONTAINER_NAME:-cassini-exapp-install-e2e}"
LOG_DIR="${LOG_DIR:-/tmp/cassini-install-e2e-$$}"
NEXTCLOUD_HOST_PORT="${NEXTCLOUD_HOST_PORT:-28080}"
NEXTCLOUD_URL_INTERNAL="${NEXTCLOUD_URL_INTERNAL:-http://nextcloud}"
TEST_USER="${TEST_USER:-e2euser}"
TEST_USER_PASSWORD="${TEST_USER_PASSWORD:-Tn8mY3qVrJ2x!E2e}"
APP_SECRET="${APP_SECRET:-$(head -c 24 /dev/urandom | base64 | tr -d '/+=' | head -c 32)}"
EXPECT_GPU_UNAVAILABLE="${CASSINI_EXPECT_GPU_UNAVAILABLE:-0}"

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

log "starting Nextcloud core stack on host port $NEXTCLOUD_HOST_PORT"
# Explicit stack topology: local HTTP, core services (db/nextcloud only —
# no media), no harness-installed ExApp (the
# manual AppAPI install below IS this test), no recording backend. The
# run-scoped PROJECT_NAME and NEXTCLOUD_HOST_PORT flow through
# `cassini dev stack up`; plain `up` (no --reset) because the PID-scoped
# project is fresh by construction.
export PROJECT_NAME NEXTCLOUD_HOST_PORT
"$REPO_ROOT/bin/cassini" dev stack up \
  --public-mode local-http \
  --services core \
  --cassini none \
  --recording-backend none \
  >"$LOG_DIR/stack-up.log" 2>&1 \
  || { tail -n 40 "$LOG_DIR/stack-up.log"; fail "cassini dev stack up failed"; }

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

# --- 5. Register the ExApp with manifest-derived JSON --------------------
#
# AppAPI ignores --info-xml when --json-info is set, AND --json-info requires
# the routes array. Generate that array from the current manifest so this
# manual-install test cannot hide route additions, removals, or access changes
# behind a stale handwritten copy.

ROUTES_JSON="$(exapp_routes_json "$INFO_XML")"
log "registering manual-install ExApp $APP_ID@$APP_VERSION with $(jq 'length' <<<"$ROUTES_JSON") manifest routes"
occ app_api:app:unregister "$APP_ID" --force >/dev/null 2>&1 || true

JSON=$(jq -nc \
  --arg secret  "$APP_SECRET" \
  --arg appid   "$APP_ID" \
  --arg daemon  "$DAEMON_NAME" \
  --arg version "$APP_VERSION" \
  --argjson routes "$ROUTES_JSON" \
  '{
     appid:               $appid,
     name:                "Cassini",
     daemon_config_name:  $daemon,
     version:             $version,
     secret:              $secret,
     port:                8080,
     protocol:            "http",
     system_app:          0,
     routes:              $routes
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
# The /enabled handler registers the single "Cassini" top-menu entry (viewer,
# all users) in a goroutine after answering AppAPI, so poll
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

# --- 7. Create a regular test user (admin already exists) -----------------

log "creating regular user $TEST_USER"
OC_PASS="$TEST_USER_PASSWORD" \
  compose exec -T -e OC_PASS="$TEST_USER_PASSWORD" -u www-data nextcloud \
  php occ user:add --password-from-env --display-name="$TEST_USER" "$TEST_USER" \
  >/dev/null 2>&1 || true   # idempotent

# --- 7a. The recordings substrate: what a stock install actually yields ----
#
# This job is the closest thing in the repo to a real production install: a
# manual AppAPI install against a plain Nextcloud, with NO Team folders and NO
# Everyone Group — which is exactly what Nextcloud AIO ships. So it can prove
# BOTH halves of D-585 with one boot.
#
# Everything here runs against a stock install: no extra --env, nothing but the
# variables AppAPI injects.

SUBSTRATE_PROXY="http://127.0.0.1:${NEXTCLOUD_HOST_PORT}/index.php/apps/app_api/proxy/${APP_ID}"

cycle_exapp() {
  occ app_api:app:disable "$APP_ID" >/dev/null 2>&1 || true
  occ app_api:app:enable  "$APP_ID" >/dev/null 2>&1 || true
}

# await_substrate <expected-state> — provisioning is dispatched in a goroutine
# AFTER the lifecycle handler has already answered, so the enabled edge returning
# is not the same as provisioning having finished. Poll rather than sleep on a
# guess; a fixed sleep is a race in both directions (flaky when slow, wasted
# minutes when fast).
await_substrate() {
  local want="$1" seen=""
  for _ in $(seq 1 40); do
    seen=$(substrate_field state)
    [[ "$seen" == "$want" ]] && return 0
    sleep 1
  done
  log "recordings_access: $(substrate_json)"
  log "provisioning log:"
  docker logs "$CONTAINER_NAME" 2>&1 | grep 'nc provision' | tail -5 | sed 's/^/    /'
  fail "substrate state settled at '${seen:-<unreadable>}', expected '$want'"
}

# require_app_enabled <id> — occ app:enable exits 0 even where the app did not
# actually become enabled, and a silent miss here is indistinguishable from the
# failure this section is testing for.
require_app_enabled() {
  local app="$1"
  for _ in $(seq 1 20); do
    if occ app:list 2>/dev/null | sed -n '/^Enabled:/,/^Disabled:/p' | grep -q "  - ${app}:"; then
      return 0
    fi
    sleep 1
  done
  fail "Nextcloud app $app did not become enabled"
}

substrate_json() {
  curl -sS -u "admin:admin" "$SUBSTRATE_PROXY/operator/status" 2>/dev/null \
    | jq -c '.recordings_access' 2>/dev/null || echo '{}'
}

substrate_field() {
  substrate_json | jq -r ".$1 // \"\"" 2>/dev/null || echo ""
}

# (a) POSITIVE: with the two prerequisites — the ONLY manual step a production
#     admin performs — the whole substrate must appear with no further
#     configuration. Acceptance criterion 2.
#
#     They are enabled explicitly rather than assumed: whether a given Nextcloud
#     image ships either of them is not this test's contract, and asserting on
#     the base image's app list would make the result depend on an upstream
#     packaging decision.
log "ensuring the two native prerequisites are enabled"
for app in groupfolders group_everyone; do
  occ app:install "$app" >/dev/null 2>&1 || true
  occ app:enable  "$app" >/dev/null 2>&1 \
    || fail "could not enable required Nextcloud app $app"
  require_app_enabled "$app"
done
cycle_exapp
await_substrate provisioned

substrate_status_body="$LOG_DIR/operator-status-provisioned.json"
substrate_status=$(curl -sS -u "admin:admin" -o "$substrate_status_body" -w '%{http_code}' "$SUBSTRATE_PROXY/operator/status")
if [[ "$EXPECT_GPU_UNAVAILABLE" == "1" ]]; then
  # A GPU-less substrate is ready: it transcribes on the CPU (D-702). Assert the
  # device explicitly so a runner that quietly has a GPU cannot pass as this mode.
  if [[ "$substrate_status" != "200" ]] || ! jq -e '
    .ok == true
    and .stt.device == "cpu"
    and .stt.device_usable == true
    and (.stt.detail | type == "string" and length > 0)
    and .db.ok == true
    and .storage.work_root.ok == true
    and .storage.site_root.ok == true
    and .recordings_access.ok == true
  ' "$substrate_status_body" >/dev/null; then
    log "recordings_access: $(substrate_json)"
    fail "a provisioned GPU-less substrate must answer 200 with a usable CPU device, got HTTP $substrate_status"
  fi
elif [[ "$substrate_status" != "200" ]] || ! jq -e '.ok == true' "$substrate_status_body" >/dev/null; then
  log "recordings_access: $(substrate_json)"
  fail "a ready provisioned substrate must answer 200/ok, got HTTP $substrate_status"
fi
# The sink must be the RESOLVED one. An ExApp that sets no CASSINI_PUBLISH_SINK
# resolves to nextcloud-files, and reporting the raw (empty) config as `local`
# would say no substrate is expected for a deployment that plainly expects one.
sink=$(substrate_field publish_sink)
[[ "$sink" == "nextcloud-files" ]] \
  || fail "expected the resolved sink nextcloud-files, got '$sink'"
admin_user=$(substrate_field admin_user)
[[ -n "$admin_user" ]] || fail "the resolved administrator is not reported"
enabled_prereqs=$(substrate_json | jq '[.prerequisites[] | select(.state == "enabled")] | length' 2>/dev/null || echo 0)
[[ "$enabled_prereqs" == "2" ]] \
  || fail "expected both prerequisites reported enabled, got $enabled_prereqs"
log "OK   /status: provisioned, expected GPU readiness, sink=$sink, admin_user=$admin_user, 2 prerequisites enabled"

# The USER-readable half of the same verdict. This is the only route that lets
# someone who is NOT an administrator find out that an install was never
# finished — before it, the viewer's `HTTP 502` was the whole message — so both
# halves of its contract are asserted here, through the proxy, as a non-admin:
# it answers, and it answers with nothing else. The unit tests pin the shape;
# only this pins that Nextcloud lets a USER-tier account reach it at all.
setup_json="$LOG_DIR/operator-setup.json"
curl -sS -u "$TEST_USER:$TEST_USER_PASSWORD" "$SUBSTRATE_PROXY/operator/setup" -o "$setup_json" \
  || fail "could not read operator/setup as $TEST_USER"
jq -e '.ok == true and .state == "provisioned"' "$setup_json" >/dev/null 2>&1 \
  || fail "operator/setup as $TEST_USER should mirror the provisioned verdict, got: $(cat "$setup_json")"
setup_keys=$(jq -r 'keys | join(",")' "$setup_json" 2>/dev/null || echo "")
[[ "$setup_keys" == "ok,state" ]] \
  || fail "operator/setup must expose ok+state only — a non-admin has no business with the step, the administrator or the paths; got keys: $setup_keys"
log "OK   operator/setup: readable by $TEST_USER, verdict only (keys: $setup_keys)"

# A fresh install must produce a Team folder whose recordings can be DELETED and
# MOVED across directories. This is D-612's acceptance, asserted live so the flag cannot silently
# come back: Cassini used to create the folder with Group Folders'
# acl_default_no_permission, which on v21+ pins the base permission at READ and
# makes canDeleteTree false for EVERY path and EVERY account — the service
# account and instance administrators included. Nothing in the product deletes a
# recording today, so no other check here would notice.
#
# Probed as the service account, because that is the identity that would have to
# do it, and through a throwaway file so the assertion never depends on a
# recording existing. Run from inside the ExApp container using the same
# act-as-user credential the operator itself uses — the account's password is
# generated and never stored, so Basic auth is not available to us.
d612_probe=$(docker exec "$CONTAINER_NAME" sh -c '
  AUTH=$(printf "cassini:%s" "$APP_SECRET" | base64 -w0)
  ROOT="$NEXTCLOUD_URL/remote.php/dav/files/cassini/Cassini/Recordings"
  SRC="$ROOT/d612-probe.txt"
  DST="$ROOT/meetings/d612-probe-moved.txt"
  put=$(curl -sS -o /dev/null -w "%{http_code}" -X PUT --data-binary d612 \
    -H "AUTHORIZATION-APP-API: $AUTH" -H "EX-APP-ID: $APP_ID" -H "EX-APP-VERSION: $APP_VERSION" "$SRC")
  # Cross-directory MOVE, not a same-directory rename: only the former exercises
  # canDeleteTree on the source, which is what the flag zeroes.
  mv=$(curl -sS -o /dev/null -w "%{http_code}" -X MOVE -H "Destination: $DST" -H "Overwrite: T" \
    -H "AUTHORIZATION-APP-API: $AUTH" -H "EX-APP-ID: $APP_ID" -H "EX-APP-VERSION: $APP_VERSION" "$SRC")
  del=$(curl -sS -o /dev/null -w "%{http_code}" -X DELETE \
    -H "AUTHORIZATION-APP-API: $AUTH" -H "EX-APP-ID: $APP_ID" -H "EX-APP-VERSION: $APP_VERSION" "$DST")
  # Best effort: if the MOVE did not happen the source is still there.
  curl -sS -o /dev/null -X DELETE \
    -H "AUTHORIZATION-APP-API: $AUTH" -H "EX-APP-ID: $APP_ID" -H "EX-APP-VERSION: $APP_VERSION" "$SRC" || true
  printf "%s %s %s" "$put" "$mv" "$del"
' 2>/dev/null || echo "000 000 000")
read -r d612_put d612_mv d612_del <<<"$d612_probe"
[[ "$d612_put" == "201" || "$d612_put" == "204" ]] \
  || fail "the service account could not write into the Team folder (PUT -> $d612_put); the substrate is not usable"
# 201 created at the destination, 204 overwrote an existing one.
[[ "$d612_mv" == "201" || "$d612_mv" == "204" ]] \
  || fail "the service account cannot MOVE across directories in the Team folder (MOVE -> $d612_mv). That is D-612: the folder carries acl_default_no_permission, which zeroes canDeleteTree for every account. This is the operation D-594 needs."
[[ "$d612_del" == "204" ]] \
  || fail "the service account cannot DELETE inside the Team folder (DELETE -> $d612_del). That is D-612: no recording in the folder can ever be deleted, by anyone including administrators."
log "OK   d612: the service account can write, cross-directory move and delete in the Team folder (PUT -> $d612_put, MOVE -> $d612_mv, DELETE -> $d612_del)"

# The service account, created because the app was installed — no occ recipe.
occ user:info cassini >/dev/null 2>&1 \
  || fail "the cassini service account was not created by the install"
log "OK   the cassini service account exists"

# The Team-folder topology, read over the same HTTP surface the operator speaks.
gf_json="$LOG_DIR/groupfolders.json"
curl -sS -u "admin:admin" -H 'OCS-APIRequest: true' \
  "http://127.0.0.1:${NEXTCLOUD_HOST_PORT}/index.php/apps/groupfolders/folders?format=json" \
  -o "$gf_json" || fail "could not list Team folders"
if ! jq -e '[.ocs.data[]? | select(.mount_point == "Cassini")] | length == 1' "$gf_json" >/dev/null 2>&1; then
  log "groupfolders: $(head -c 400 "$gf_json")"
  fail "expected exactly one Cassini Team folder"
fi
# everyone READ is the audience; the narrow owner group ALL is the only write
# path. Asserted by value so a permissions regression cannot pass.
jq -e '.ocs.data[] | select(.mount_point == "Cassini") | select(.acl == true)' "$gf_json" >/dev/null 2>&1 \
  || fail "the Cassini Team folder does not have advanced ACL enabled"
jq -e '.ocs.data[] | select(.mount_point == "Cassini") | select(.groups.everyone == 1)' "$gf_json" >/dev/null 2>&1 \
  || fail "the Cassini Team folder does not grant the everyone group read (1)"
jq -e '.ocs.data[] | select(.mount_point == "Cassini") | select(.groups.cassini == 31)' "$gf_json" >/dev/null 2>&1 \
  || fail "the Cassini Team folder does not grant the cassini owner group all (31)"
jq -e '.ocs.data[] | select(.mount_point == "Cassini") | .manage[] | select(.type == "user" and .id == "cassini")' "$gf_json" >/dev/null 2>&1 \
  || fail "cassini is not the ACL manager of the Cassini Team folder"
log "OK   Cassini Team folder: acl=true, everyone:1, cassini:31, manager=cassini"

# The flag must be ABSENT (D-612). It used to be asserted present, as the
# default-deny floor; on Group Folders v21+ it instead pins the base permission
# at READ, which makes every recording in the folder permanently undeletable and
# un-moveable by every account, administrators included. The floor it was meant
# to provide comes from the explicit root ACL asserted above, and the DELETE
# probe earlier in this script is the behavioural half of the same claim.
#
# Still via occ: the flag is not exposed by the HTTP index. Note the key is
# `mountPoint` here and `mount_point` over HTTP — the two surfaces genuinely
# disagree, and using the HTTP spelling against occ silently selects nothing,
# which would make this assertion vacuously true.
occ groupfolders:list --output=json_pretty > "$LOG_DIR/groupfolders-occ.json" 2>/dev/null \
  || fail "occ groupfolders:list failed"
jq -e '[.[] | select(.mountPoint == "Cassini")] | length == 1' \
   "$LOG_DIR/groupfolders-occ.json" >/dev/null 2>&1 \
  || fail "expected exactly one Cassini Team folder in occ groupfolders:list"
if ! jq -e '[.[] | select(.mountPoint == "Cassini") | select(.acl_default_no_permission == false)] | length == 1' \
     "$LOG_DIR/groupfolders-occ.json" >/dev/null 2>&1; then
  log "groupfolders (occ): $(head -c 400 "$LOG_DIR/groupfolders-occ.json")"
  fail "the Cassini Team folder carries acl_default_no_permission — recordings in it can never be deleted or moved (D-612)"
fi
log "OK   the Cassini Team folder has no default-deny flag (D-612)"

# THE DISCRIMINATOR. From `cassini` a private home directory and a mounted Team
# folder are indistinguishable — both answer 207 to its own PROPFIND. From a
# THIRD, unrelated account they are 404 vs 207. This is the only assertion here
# that can tell "the recordings tree exists" from "the recordings tree exists
# where nobody else can reach it", which is the failure D-585 exists to remove.
propfind_status=$(curl -sS -X PROPFIND -u "$TEST_USER:$TEST_USER_PASSWORD" -H 'Depth: 1' \
  -o /dev/null -w '%{http_code}' \
  "http://127.0.0.1:${NEXTCLOUD_HOST_PORT}/remote.php/dav/files/$TEST_USER/Cassini/Recordings/meetings")
if [[ "$propfind_status" != "207" ]]; then
  fail "PROPFIND of Cassini/Recordings/meetings as $TEST_USER expected 207 (a mounted Team folder), got $propfind_status — the tree is in the owner's private home"
fi
log "OK   $TEST_USER sees Cassini/Recordings/meetings: it is a Team folder, not a private home"


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
assert_status admin   "admin:admin"                       "operator/jobs"  200
assert_status admin   "admin:admin"                       "viewer/"        200

log "checking proxied route access for $TEST_USER (USER tier)"
assert_status "$TEST_USER" "$TEST_USER:$TEST_USER_PASSWORD" "viewer/"        200
# The operator JSON API stays ADMIN — a non-admin is refused (D-420: the shell
# entry is USER, but the operator surface's API remains the real boundary).
assert_status "$TEST_USER" "$TEST_USER:$TEST_USER_PASSWORD" "operator/jobs"  404
# ...but "is Cassini set up" is USER, and has to be, or the only answer a
# non-admin could get about an unfinished install is the viewer's own HTTP 502.
# That it carries the verdict and none of the diagnosis is asserted in 7a.
assert_status "$TEST_USER" "$TEST_USER:$TEST_USER_PASSWORD" "operator/setup"  200
assert_status "$TEST_USER" "$TEST_USER:$TEST_USER_PASSWORD" "ui/viewer.js"   200
# ui/viewer.css must be proxy-reachable at USER tier: D-383 injects it into the
# viewer's shadow root via a runtime <link>, so the proxy has to serve it.
assert_status "$TEST_USER" "$TEST_USER:$TEST_USER_PASSWORD" "ui/viewer.css"  200
assert_status "$TEST_USER" "$TEST_USER:$TEST_USER_PASSWORD" "img/app.svg"    200

# --- 7c. Assert the admin UI actually loads (not just 200 + empty) --------
#
# A status-code-only check passes even when the proxy returns a placeholder
# or an error page styled to look benign. The contract a Nextcloud admin
# actually cares about is "I logged in as admin, opened the cassini admin
# app, and saw the real Cassini SPA." Pin that by fetching the body and
# asserting it contains the SPA's title marker (cassini-app/index.html). If the
# SPA ever stops shipping in the image, or the proxy silently degrades to an
# empty 200, this catches it. (D-420: one unified SPA at /viewer for everyone;
# the operator surface is gated client-side inside it.)

assert_app_ui_loads() {
  local body
  body=$(curl -sS -u "admin:admin" "$PROXY/viewer/")
  if ! grep -qF "<title>Cassini</title>" <<<"$body"; then
    log "admin /viewer/ body did not contain the expected SPA title."
    log "first 200 chars of response:"
    log "${body:0:200}"
    fail "admin UI returned 200 but body is not the cassini SPA"
  fi
  log "OK   admin /viewer/ returns the cassini SPA HTML"
}

log "checking that the Cassini SPA actually loads (body contains SPA title)"
assert_app_ui_loads

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

# Seeded with a meeting on purpose. The local site root is no longer the
# archive — recordings live in Nextcloud Files — so this entry must NOT reach
# the caller. If it does, the operator is serving its own volume again.
log "seeding a decoy catalog at ${SITE_ROOT}/catalog.json inside the container"
docker exec "$CONTAINER_NAME" sh -c \
  'mkdir -p "$1" && printf "%s" "{\"version\":\"cassini.viewer.catalog.v1\",\"meetings\":[{\"id\":\"decoy\",\"title\":\"Decoy\",\"dateLabel\":\"2026-01-01\",\"audioPath\":\"./meetings/decoy.opus\"}]}" > "$1/catalog.json"' \
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

# (D-420: the former 7e "embedded control-panel wiring" check is gone — there is
# no separate control-panel entry/embedded page now. The operator surface lives
# inside the single Cassini app at /viewer, gated client-side; its JSON API
# stays ADMIN, covered by the operator/jobs 200-admin / 404-user checks above.)

# $TEST_USER is a plain logged-in account that has been granted nothing: no
# recording lists it as a participant, so it is the non-participant case. Before
# D-554 this asserted the opposite — that any logged-in user gets the shared
# catalog — which is precisely the org-wide archive D-521 exists to retire.
log "checking proxied catalog $PROXY/published/catalog.json as a non-participant"
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
# A valid, EMPTY catalog. Two claims in one assertion: the caller is granted
# nothing so they see nothing, and the decoy seeded into the container's own
# volume is not what gets served.
catalog_count=$(jq -r '.meetings | length' "$LOG_DIR/published-catalog.json" 2>/dev/null || echo "")
if [[ "$catalog_count" != "0" ]]; then
  log "catalog body:"
  log "$(head -c 300 "$LOG_DIR/published-catalog.json" 2>/dev/null)"
  fail "a non-participant received $catalog_count catalog entries, expected 0"
fi
if jq -e '.meetings[]? | select(.id == "decoy")' "$LOG_DIR/published-catalog.json" >/dev/null 2>&1; then
  fail "the container-local catalog was served; Nextcloud Files is supposed to be authoritative"
fi
log "OK   a non-participant gets a valid but empty catalog, and the local decoy is not served"

# ...and playback is denied the same way: a miss and a denial are the same 404,
# so a recording a caller may not read never even reveals that it exists.
log "checking proxied playback $PROXY/published/meetings/decoy.opus as a non-participant"
playback_status=$(curl -sS -u "$TEST_USER:$TEST_USER_PASSWORD" \
  -o /dev/null -w '%{http_code}' "$PROXY/published/meetings/decoy.opus")
if [[ "$playback_status" != "404" ]]; then
  fail "playback for a non-participant expected 404 got $playback_status"
fi
log "OK   proxied playback 404s for a non-participant"

# NOT ASSERTED HERE: the negative half (disable a prerequisite → the install
# reports unavailable/app_missing:<id> instead of coming up healthy).
#
# It is real and it is verified — by unit tests over the provisioner
# (TestProvisionNamesTheMissingNativeApp asserts the state, the step, the per-app
# list, the log naming `occ app:install`, and that nothing downstream is
# attempted), and by hand against a live Nextcloud 34 (transcript in
# _ivans-notes/development/549-install-substrate-preflight/implementation.md).
#
# It is not asserted in THIS job because toggling a Nextcloud app mid-run is
# nondeterministic here: `occ` and php-fpm do not share an APCu segment, so
# `occ app:disable`/`app:enable` updates the database and the CLI cache while the
# web workers keep serving a stale enabled-apps list. Observed in both
# directions across runs, with `occ app:list` disagreeing with
# `GET /ocs/v2.php/cloud/apps?filter=enabled` for longer than 40s of polling.
# Asserting on it measures Nextcloud's cache invalidation, not this code, and
# makes the repo's most expensive job flaky for a reason unrelated to the change
# under test.
#
# To reinstate: restart the `nextcloud` compose service between the toggle and
# the poll so the web workers rebuild their app list.

log "install-e2e passed"
