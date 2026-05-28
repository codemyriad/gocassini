#!/usr/bin/env bash
# Production-path E2E for Cassini ExApp: install + record + viewer.
#
# Covers (extends ci-e2e-install-prod-path.sh; D-290 Slices 2 + 3):
#   - All of the install harness's coverage (Slice 1).
#   - Nextcloud Talk record-button trigger routed through the AppAPI
#     proxy + HaRP to the ExApp container's operator (Slice 2).
#     bootstrap.sh wires spreed's recording_servers at the proxy URL;
#     PUBLIC routes in info.xml (api/v1/welcome + api/v1/room/<token>)
#     let Talk's HMAC POST land on the operator with no NC-session auth.
#   - Operator accepts the Talk-triggered job (`record started id=`
#     in the ExApp container log) and the recorder bot joins the call,
#     captures audio, transcribes, publishes.
#   - Published transcript artifact appears on disk inside the ExApp
#     container at the expected canonical path.
#   - A non-admin Nextcloud user (alice) loads the viewer SPA through
#     the AppAPI proxy (USER access), reads the published meeting's
#     transcript JSON through /published, and is correctly REFUSED on
#     ADMIN-only routes like /operator/jobs (Slice 3; subsumes X2 I7b).
#   - Anonymous (unauthenticated) GET on viewer/ is rejected — proves
#     AppAPI's USER access level is enforced, not just declared.
#
# Does NOT cover:
#   - Levenshtein quality of the transcript text. Independent test
#     `ci-e2e-v3-transcript-verify.sh` gates that against bundled
#     LibriSpeech (kept per R6.2).
#   - Admin control-panel routes. Control-panel is being rewritten in
#     a separate unit of work (R7.5).
#   - The legacy AppAPI manual-install daemon path.
#   - Direct container middleware tests that bypass HaRP.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$HARNESS_DIR/.." && pwd)"
COMPOSE_FILE="$HARNESS_DIR/compose.yml"
INFO_XML="$REPO_ROOT/appinfo/info.xml"

: "${IMAGE_REF:?IMAGE_REF must be set to a locally available ExApp image ref loaded by CI}"

PROJECT_NAME="${PROJECT_NAME:-cassini-prod-record-e2e}"
APP_ID="${APP_ID:-gocassini}"
DAEMON_NAME="${DAEMON_NAME:-harp_local}"
IMAGE_AS_PRODUCTION="${IMAGE_AS_PRODUCTION:-ghcr.io/codemyriad/gocassini:latest}"
APP_PORT="${APP_PORT:-8080}"
NEXTCLOUD_HOST_PORT="${NEXTCLOUD_HOST_PORT:-28080}"
NEXTCLOUD_URL="${NEXTCLOUD_URL:-http://127.0.0.1:${NEXTCLOUD_HOST_PORT}}"
NEXTCLOUD_STATUS_URL="${NEXTCLOUD_STATUS_URL:-${NEXTCLOUD_URL}/status.php}"
HARP_SHARED_KEY="${HARP_SHARED_KEY:-dogfood-shared-key-not-secret}"
CASSINI_TALK_RECORDING_SECRET="${CASSINI_TALK_RECORDING_SECRET:-}"
# Tell bootstrap.sh to point spreed.recording_servers at the AppAPI proxy
# URL (PR #40 ee88fd2 pattern). This is the seam Slice 2 exercises that
# Slice 1 doesn't: Talk -> reverse-proxy -> AppAPI -> HaRP -> operator.
export CASSINI_TALK_RECORDING_URL="${CASSINI_TALK_RECORDING_URL:-http://reverse-proxy/index.php/apps/app_api/proxy/${APP_ID}}"
REGISTER_TIMEOUT_SECONDS="${REGISTER_TIMEOUT_SECONDS:-300}"
REGISTER_SUCCESS_GRACE_SECONDS="${REGISTER_SUCCESS_GRACE_SECONDS:-10}"
LOG_DIR="${LOG_DIR:-/tmp/${PROJECT_NAME}}"
HARP_BACKEND_PORT=""

SCENARIO_NAME="${SCENARIO_NAME:-showcase-lantern-festival-v1}"
SCENARIO_PATH="${SCENARIO_PATH:-$HARNESS_DIR/scenarios/showcase-lantern-festival.v1.json}"
SCENARIO_MEDIA_DIR="${SCENARIO_MEDIA_DIR:-$HARNESS_DIR/media/processed/$SCENARIO_NAME}"
RECORD_DURATION_SECONDS="${RECORD_DURATION_SECONDS:-40}"
# Budget = transcribe (parakeet CPU is roughly 5x realtime on
# ubuntu-latest) + build + publish + slack. The legacy Talk-roundtrip
# uses RECORD_DURATION + 180s and 75s recordings; we match that
# proportionally with a generous floor for the 40s record path.
PUBLISH_TIMEOUT_SECONDS="${PUBLISH_TIMEOUT_SECONDS:-360}"

# Signaling host port — must match harness/compose.yml's `full` profile
# mapping. Same as ci-e2e.sh / mute / rejoin.
export SIGNALING_URL="${SIGNALING_URL:-http://127.0.0.1:28082}"
export SIGNALING_SHARED_SECRET="${SIGNALING_SHARED_SECRET:-7f4dca67263621ba7f9f9917e13de95a201f6f360be0d303e3008c2e6c8ad37d}"
export TURN_SERVER="${TURN_SERVER:-127.0.0.1:13479}"
export TURN_SHARED_SECRET="${TURN_SHARED_SECRET:-3c04d2fc2f7fe39d48eb4dc77f652c8c778a4ea178b0e486529b284afca7b648}"

export NEXTCLOUD_HOST_PORT NEXTCLOUD_URL NEXTCLOUD_STATUS_URL

mkdir -p "$LOG_DIR"

log()  { printf '[prod-record-e2e] %s\n' "$*"; }
fail() { log "FAIL: $*"; exit 1; }
phase(){ log "----- phase $1: $2 -----"; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

require_cmd curl
require_cmd docker
require_cmd jq
require_cmd openssl
require_cmd python3

if [[ -z "$CASSINI_TALK_RECORDING_SECRET" ]]; then
  CASSINI_TALK_RECORDING_SECRET="$(openssl rand -hex 32)"
fi
export CASSINI_TALK_RECORDING_SECRET

COMPOSE=(docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE")
compose() { "${COMPOSE[@]}" "$@"; }
occ()     { compose exec -T -u www-data nextcloud php occ "$@"; }

project_network() { printf '%s_default\n' "$PROJECT_NAME"; }

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

BOT_PID=""

capture_logs() {
  compose ps >"$LOG_DIR/compose-ps.txt" 2>&1 || true
  compose logs --no-color nextcloud >"$LOG_DIR/nextcloud.log" 2>&1 || true
  compose logs --no-color appapi-harp >"$LOG_DIR/appapi-harp.log" 2>&1 || true
  compose logs --no-color reverse-proxy >"$LOG_DIR/reverse-proxy.log" 2>&1 || true
  # NC application log is inside the container, not in docker stdout.
  # Without this we can't see PHP exceptions, spreed errors, etc.
  compose exec -T nextcloud cat /var/www/html/data/nextcloud.log \
    >"$LOG_DIR/nextcloud-app.log" 2>&1 || true
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
  if [[ -n "$BOT_PID" ]]; then
    kill -TERM "$BOT_PID" >/dev/null 2>&1 || true
    wait "$BOT_PID" 2>/dev/null || true
  fi
  capture_logs
  remove_non_compose_network_containers
  compose --profile full down --volumes --remove-orphans >/dev/null 2>&1 || true
  if [[ $rc -ne 0 ]]; then
    log "logs saved under $LOG_DIR"
    log "appapi-harp log tail:"
    tail -n 80 "$LOG_DIR/appapi-harp.log" 2>/dev/null | sed 's/^/    /' || true
    log "ExApp (gocassini operator) container log tail:"
    tail -n 120 "$LOG_DIR/container-nc_app_gocassini.log" 2>/dev/null | sed 's/^/    /' || true
    log "nextcloud apache log tail:"
    tail -n 60 "$LOG_DIR/nextcloud.log" 2>/dev/null | sed 's/^/    /' || true
    log "nextcloud app log tail (PHP exceptions, spreed errors):"
    tail -n 80 "$LOG_DIR/nextcloud-app.log" 2>/dev/null | sed 's/^/    /' || true
    log "bot streamer log tail:"
    tail -n 60 "$LOG_DIR/bots.log" 2>/dev/null | sed 's/^/    /' || true
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

find_exapp_container() {
  local network cid
  network="$(project_network)"
  cid="$(docker ps -q --filter "network=$network" --filter "ancestor=$IMAGE_AS_PRODUCTION" | head -n1)"
  if [[ -n "$cid" ]]; then printf '%s\n' "$cid"; return 0; fi
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

# Wait for NC first-run install BEFORE we perturb it (docker-socket
# group grant + restart). On slower CI runners NC's restart races with
# its own first-run install, leaving status.php stuck at installed:false.
wait_for_nextcloud_installed() {
  local end_time=$((SECONDS + 420))
  log "waiting for Nextcloud first-run install (installed:true at $NEXTCLOUD_STATUS_URL)"
  while (( SECONDS < end_time )); do
    if curl -fsS "$NEXTCLOUD_STATUS_URL" 2>/dev/null | grep -q '"installed":true'; then
      log "OK Nextcloud first-run install complete"
      return 0
    fi
    sleep 2
  done
  fail "Nextcloud did not reach installed:true within 420s"
}

assert_app_enabled() {
  log "asserting AppAPI reports $APP_ID as [enabled]"
  occ app_api:app:list >"$LOG_DIR/app-list.txt"
  grep -Eq "(^|[[:space:]])${APP_ID}[[:space:]].*\\[enabled\\]" "$LOG_DIR/app-list.txt" \
    || fail "app_api:app:list did not report $APP_ID as [enabled]; see $LOG_DIR/app-list.txt"
  log "OK AppAPI reports $APP_ID [enabled]"
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

assert_proxy_route_through_harp() {
  log "asserting AppAPI proxy welcome route reaches Cassini through HaRP"
  local url code body
  url="${NEXTCLOUD_URL}/index.php/apps/app_api/proxy/${APP_ID}/api/v1/welcome"
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

run_app_register() {
  local pid deadline grace_deadline rc
  rm -f "$LOG_DIR/register.log"
  setsid docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE" \
    exec -T -u www-data nextcloud php occ \
    app_api:app:register "$APP_ID" "$DAEMON_NAME" \
      --info-xml /tmp/gocassini-info.xml \
      --env "CASSINI_TALK_RECORDING_SECRET=$CASSINI_TALK_RECORDING_SECRET" \
      --env "CASSINI_TALK_BACKEND_URL=http://nextcloud" \
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
  wait "$pid"; rc=$?
  set -e
  if [[ $rc -ne 0 ]]; then
    tail -n 160 "$LOG_DIR/register.log" >&2 || true
    fail "app:register failed"
  fi
}

# ============================================================================
# Setup: install Cassini via the prod-path (mirrors Slice 1)
# ============================================================================
phase 1 "Setup compose stack with SPREED_PROFILE=full"

log "cleaning previous prod-path record stack state"
remove_non_compose_network_containers
compose --profile full down --volumes --remove-orphans >/dev/null 2>&1 || true

log "tagging loaded image $IMAGE_REF as $IMAGE_AS_PRODUCTION for info.xml install"
docker image inspect "$IMAGE_REF" >/dev/null
docker tag "$IMAGE_REF" "$IMAGE_AS_PRODUCTION"

log "starting Nextcloud + AppAPI HaRP prod-path stack with full Talk topology"
compose --profile full up -d nextcloud db appapi-harp reverse-proxy nats janus signaling coturn

wait_for_nextcloud_installed

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

phase 2 "Bootstrap Nextcloud + Talk + signaling"

log "bootstrapping Nextcloud (SPREED_PROFILE=full, recording_servers via proxy)"
SPREED_PROFILE=full PROJECT_NAME="$PROJECT_NAME" \
  CASSINI_TALK_RECORDING_URL="$CASSINI_TALK_RECORDING_URL" \
  CASSINI_TALK_RECORDING_SECRET="$CASSINI_TALK_RECORDING_SECRET" \
  "$SCRIPT_DIR/bootstrap.sh" >"$LOG_DIR/bootstrap.log" 2>&1 \
  || { tail -n 120 "$LOG_DIR/bootstrap.log" >&2; fail "bootstrap failed"; }

log "installing + enabling app_api"
occ app:install app_api >/dev/null 2>&1 || true
occ app:enable app_api >/dev/null

# Set NC's self-perceived URL to the in-network nextcloud service so the
# recorder can dial NC directly (Docker DNS, port 80). Without this NC
# reports `http://127.0.0.1:28080` (the host port-forward) in
# Talk-Recording-Backend headers, which is unreachable from inside the
# AppAPI-deployed ExApp container. We aim straight at the nextcloud
# service rather than the reverse-proxy because the recorder's OCS
# join flow (participants/active, recording/backend) is sensitive to
# proxy header handling on the spreed side and returns 500s when those
# requests arrive via a proxy that NC's trusted_proxies hasn't been
# fine-tuned to expect. Host-side curl tests still hit NC via the
# published port forward; nothing user-facing changes.
log "setting Nextcloud overwrite.cli.url to http://nextcloud"
occ config:system:set overwrite.cli.url --value="http://nextcloud" >/dev/null
occ config:system:set trusted_proxies 0 --value="reverse-proxy" >/dev/null

log "patching AppAPI CSP for ExApp proxy responses"
compose exec -T nextcloud php <"$SCRIPT_DIR/patch-csp.php" >"$LOG_DIR/patch-csp.log" 2>&1
compose restart nextcloud >/dev/null
wait_for_nextcloud_after_restart

log "creating standard user alice for viewer-role assertions"
ALICE_PASSWORD="${ALICE_PASSWORD:-Tn8mY3qVrJ2x!E2e}"
export OC_PASS="$ALICE_PASSWORD"
compose exec -T -e OC_PASS -u www-data nextcloud php occ user:add \
  --password-from-env --display-name=Alice alice >/dev/null 2>&1 || true
unset OC_PASS

phase 3 "Register Cassini via AppAPI HaRP daemon"

log "registering HaRP deploy daemon"
occ app_api:daemon:unregister docker_local >/dev/null 2>&1 || true
occ app_api:daemon:unregister "$DAEMON_NAME" >/dev/null 2>&1 || true
occ app_api:daemon:register \
  "$DAEMON_NAME" \
  "HaRP (local prod-path record CI)" \
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
assert_proxy_route_through_harp

# ============================================================================
# Phase 4: create Talk room + start scenario audio bots
# ============================================================================
phase 4 "Create Talk room + start scenario audio bots"

if [[ ! -d "$SCENARIO_MEDIA_DIR" ]]; then
  fail "scenario media not found at $SCENARIO_MEDIA_DIR
  Run: harness/bin/prepare-synthetic-meeting.sh \\
       --scenario $SCENARIO_PATH \\
       --output-dir $SCENARIO_MEDIA_DIR"
fi

ROOM_NAME="cassini-prod-record-e2e-$(date +%s)"
ROOM_RESP=$(curl -sf -u admin:admin \
  -H "OCS-APIRequest: true" \
  -H "Accept: application/json" \
  -H "Content-Type: application/json" \
  -d "{\"roomType\":3,\"roomName\":\"$ROOM_NAME\"}" \
  "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v4/room" \
  || fail "OCS create room failed")
ROOM_TOKEN=$(printf '%s' "$ROOM_RESP" \
  | python3 -c "import json,sys; print(json.load(sys.stdin)['ocs']['data']['token'])" \
  || fail "could not parse room token from: $ROOM_RESP")
CALL_URL="$NEXTCLOUD_URL/call/$ROOM_TOKEN"
log "room token: $ROOM_TOKEN"
log "call URL:   $CALL_URL"

BOT_LOG="$LOG_DIR/bots.log"
(
  CALL_URL="$CALL_URL" \
  SCENARIO="$SCENARIO_PATH" \
  OUTPUT_DIR="$SCENARIO_MEDIA_DIR" \
  PREPARE=0 \
  "$HARNESS_DIR/bin/stream-synthetic-meeting.sh" \
    --call-url "$CALL_URL"
) >"$BOT_LOG" 2>&1 &
BOT_PID=$!
log "bot streamer pid=$BOT_PID (log: $BOT_LOG)"

log "waiting for active call (hasCall=true) (up to 90s)"
DEADLINE=$(( SECONDS + 90 ))
HAS_CALL=0
while (( SECONDS < DEADLINE )); do
  PEEK=$(curl -sf -u admin:admin \
    -H "OCS-APIRequest: true" \
    -H "Accept: application/json" \
    "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v4/room/$ROOM_TOKEN" \
    2>/dev/null || true)
  if [[ -n "$PEEK" ]]; then
    HAS_CALL=$(printf '%s' "$PEEK" \
      | python3 -c "import json,sys; d=json.load(sys.stdin)['ocs']['data']; print(1 if d.get('hasCall') else 0)" \
      2>/dev/null || echo 0)
  fi
  [[ "$HAS_CALL" == "1" ]] && break
  sleep 3
done
if [[ "$HAS_CALL" != "1" ]]; then
  log "WARN: no active call detected after 90s; bot log tail:"
  tail -n 30 "$BOT_LOG" | sed 's/^/    /' || true
fi
log "OK proceeding (hasCall=$HAS_CALL)"

# ============================================================================
# Phase 5: trigger Talk recording via OCS → AppAPI proxy → HaRP → operator
# ============================================================================
phase 5 "Trigger Talk recording via OCS (proxy path)"

# status=2 is audio-only — enough for the transcript check and smaller MKV.
TRIGGER_RESP=$(curl -sf -u admin:admin \
  -H "OCS-APIRequest: true" \
  -H "Accept: application/json" \
  -H "Content-Type: application/json" \
  -d '{"status":2}' \
  "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v1/recording/$ROOM_TOKEN" \
  || fail "OCS recording start failed; Talk side rejected the request (likely recording_servers misconfigured or signaling down)")
log "OCS recording trigger response: ${TRIGGER_RESP:0:200}"

EXAPP_CID="$(find_exapp_container || true)"
[[ -n "$EXAPP_CID" ]] || fail "cannot find installed ExApp container after install"

log "waiting for operator to log 'record started id=' (up to 60s)"
DEADLINE=$(( SECONDS + 60 ))
JOB_ID=""
while (( SECONDS < DEADLINE )); do
  if docker logs "$EXAPP_CID" 2>&1 | grep -qE "record started id="; then
    JOB_ID=$(docker logs "$EXAPP_CID" 2>&1 \
      | grep -oE 'record started id=[A-Z0-9]+' | head -n1 | sed 's/.*=//')
    break
  fi
  sleep 2
done
if [[ -z "$JOB_ID" ]]; then
  log "operator log tail:"
  docker logs "$EXAPP_CID" 2>&1 | tail -n 60 | sed 's/^/    /'
  fail "operator never accepted the Talk-triggered record job (proxy/HMAC path broken?)"
fi
log "OK operator accepted Talk-triggered job: $JOB_ID"

log "recording for ${RECORD_DURATION_SECONDS}s before triggering Talk stop"
sleep "$RECORD_DURATION_SECONDS"

log "triggering Talk recording stop via OCS DELETE"
STOP_RESP=$(curl -sf -u admin:admin -X DELETE \
  -H "OCS-APIRequest: true" \
  -H "Accept: application/json" \
  "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v1/recording/$ROOM_TOKEN" \
  || true)
log "OCS recording stop response: ${STOP_RESP:0:200}"

# ============================================================================
# Phase 6: wait for record → upload → build → publish; assert transcript
# ============================================================================
phase 6 "Wait for transcript to land in the published bundle"

PUBLISH_DEADLINE=$(( SECONDS + PUBLISH_TIMEOUT_SECONDS ))
PUBLISHED_DIR=""
while (( SECONDS < PUBLISH_DEADLINE )); do
  CANDIDATE=$(docker exec "$EXAPP_CID" \
    sh -c 'ls -d /srv/cassini-site/published/meetings/*/ 2>/dev/null | head -n1 | sed "s|/$||"' \
    || true)
  if [[ -n "$CANDIDATE" ]] && \
     docker exec "$EXAPP_CID" test -s "$CANDIDATE/transcript.words.v1.json"; then
    PUBLISHED_DIR="$CANDIDATE"
    break
  fi
  sleep 4
done
[[ -n "$PUBLISHED_DIR" ]] || fail "published bundle with transcript never appeared within ${PUBLISH_TIMEOUT_SECONDS}s"
log "OK published bundle: $PUBLISHED_DIR"

TRANSCRIPT_HOST="$LOG_DIR/transcript.words.v1.json"
docker cp "$EXAPP_CID:$PUBLISHED_DIR/transcript.words.v1.json" "$TRANSCRIPT_HOST"
SIZE=$(wc -c <"$TRANSCRIPT_HOST")
[[ "$SIZE" -gt 0 ]] || fail "transcript file is empty: $TRANSCRIPT_HOST"
WORD_COUNT=$(python3 -c "import json,sys; d=json.load(open('$TRANSCRIPT_HOST')); print(len(d.get('words', [])))")
[[ "$WORD_COUNT" -gt 0 ]] || fail "transcript has zero words"
log "OK transcript pulled: $TRANSCRIPT_HOST (${SIZE} bytes, ${WORD_COUNT} words)"

# Bot streamer should have exited by now (or we kill it in cleanup).
wait "$BOT_PID" 2>/dev/null || true
BOT_PID=""

# ============================================================================
# Phase 7: viewer route exercised by a regular user (Slice 3)
# ============================================================================
phase 7 "Viewer route via proxy as non-admin user (alice)"

VIEWER_URL="${NEXTCLOUD_URL}/index.php/apps/app_api/proxy/${APP_ID}/viewer/"
OPERATOR_URL="${NEXTCLOUD_URL}/index.php/apps/app_api/proxy/${APP_ID}/operator/jobs"
MEETING_ID="$(basename "$PUBLISHED_DIR")"
PUBLISHED_URL="${NEXTCLOUD_URL}/index.php/apps/app_api/proxy/${APP_ID}/published/meetings/${MEETING_ID}/transcript.words.v1.json"

# 1) Alice (USER access) loads the viewer SPA through the proxy.
log "asserting alice can GET viewer/ through the AppAPI proxy"
ALICE_VIEWER_BODY="$LOG_DIR/viewer-alice.html"
CODE="$(curl -sS -u "alice:$ALICE_PASSWORD" \
  -o "$ALICE_VIEWER_BODY" -w '%{http_code}' "$VIEWER_URL" || echo 000)"
[[ "$CODE" == "200" ]] \
  || fail "alice GET viewer/ expected 200, got $CODE (body in $ALICE_VIEWER_BODY)"
[[ -s "$ALICE_VIEWER_BODY" ]] \
  || fail "alice GET viewer/ returned empty body"
log "OK alice loaded viewer/ (HTTP 200, $(wc -c <"$ALICE_VIEWER_BODY") bytes)"

# 2) Anonymous (no auth) MUST NOT see the viewer. AppAPI's USER access
# level requires NC session/basic-auth; without it the proxy refuses.
log "asserting anonymous GET viewer/ is rejected"
ANON_BODY="$LOG_DIR/viewer-anon.body"
ANON_CODE="$(curl -sS -o "$ANON_BODY" -w '%{http_code}' "$VIEWER_URL" || echo 000)"
[[ "$ANON_CODE" != "200" ]] \
  || fail "anonymous GET viewer/ unexpectedly returned 200 (USER access not enforced)"
log "OK anonymous GET viewer/ rejected (HTTP $ANON_CODE)"

# 3) Alice (non-admin) MUST NOT reach ADMIN-only operator routes (X2 I7b).
# AppAPI's ExAppProxyController returns 404 when the access_level check
# fails — matches the legacy ci-e2e-install-exapp.sh assertion.
log "asserting alice GET operator/jobs returns 404 (ADMIN-only route)"
ALICE_OP_BODY="$LOG_DIR/operator-jobs-alice.body"
ALICE_OP_CODE="$(curl -sS -u "alice:$ALICE_PASSWORD" \
  -o "$ALICE_OP_BODY" -w '%{http_code}' "$OPERATOR_URL" || echo 000)"
[[ "$ALICE_OP_CODE" == "404" ]] \
  || fail "alice GET operator/jobs expected 404, got $ALICE_OP_CODE (ADMIN gate not enforced)"
log "OK alice GET operator/jobs returns 404"

# 4) Alice reads the published meeting's transcript through the proxy.
# /published is USER access in info.xml, so this is the user-visible end
# of the pipeline Slice 2 produced.
log "asserting alice can read published transcript for meeting $MEETING_ID"
ALICE_TRANSCRIPT="$LOG_DIR/published-transcript-alice.json"
CODE="$(curl -sS -u "alice:$ALICE_PASSWORD" \
  -o "$ALICE_TRANSCRIPT" -w '%{http_code}' "$PUBLISHED_URL" || echo 000)"
[[ "$CODE" == "200" ]] \
  || fail "alice GET published transcript expected 200, got $CODE (see $ALICE_TRANSCRIPT)"
ALICE_WORDS="$(python3 -c "import json; print(len(json.load(open('$ALICE_TRANSCRIPT')).get('words', [])))" 2>/dev/null || echo 0)"
[[ "$ALICE_WORDS" -gt 0 ]] \
  || fail "alice's published transcript has zero words (expected >0)"
log "OK alice read published transcript: $ALICE_WORDS words"

log "prod-path record + viewer e2e passed"
