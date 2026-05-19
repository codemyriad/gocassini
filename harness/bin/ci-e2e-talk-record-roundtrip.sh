#!/usr/bin/env bash
# Full Talk-driven recording roundtrip: trigger via Talk's recording-backend
# HTTP protocol → gocassini joins the call → captures audio → transcribes →
# publishes → Levenshtein-check the transcript against the scenario's
# expected text.
#
# This is the e2e that exercises the WHOLE merged stack:
#   - PR #22 (Talk recording-backend HMAC adapter)
#   - PR #24 + #26 (v2 multi-transcript format + producer)
#   - PR #30 (ExApp install handshake)
#   - PR #32 (bundled v3 model image)
# The ci-e2e-v3-transcript-verify.sh smoke verifies cassini build + model
# loading; this script additionally exercises the Talk recording-backend
# trigger path that PR #22 introduces.
#
# Phases:
#   1. Bring up Nextcloud + Talk + signaling (compose full profile)
#   2. Install + enable Talk (spreed app)
#   3. Run cassini-exapp container with TALK_RECORDING_SECRET set
#   4. Configure Talk's recording_servers to point at gocassini-operator
#      (verify via /api/v1/welcome that Talk can reach the backend)
#   5. Create a Talk call; spawn audio bot(s) playing a known scenario
#   6. Trigger Talk's start-recording via OCS; wait for the lifecycle to
#      complete (Talk → HMAC POST /api/v1/room/{token} → operator's
#      record job → recorder joins call → captures → stop → upload to
#      Talk store → build → publish)
#   7. Read the published transcript from the gocassini container
#      (/srv/cassini-site/published/meeting-*/transcript.words.v1.json)
#   8. Levenshtein-check against the scenario's expected text
#
# Status: draft. Phases 1-4 are scripted; 5-8 need iteration. Each phase
# is gated so partial runs surface useful debug instead of cascading
# failures.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$HARNESS_DIR/.." && pwd)"
COMPOSE_FILE="$HARNESS_DIR/compose.yml"
INFO_XML="$REPO_ROOT/appinfo/info.xml"

# ---- Configuration --------------------------------------------------------

: "${IMAGE_REF:?IMAGE_REF must be set, e.g. ghcr.io/codemyriad/gocassini:latest}"

PROJECT_NAME="${PROJECT_NAME:-cassini-talk-rec-e2e-$$}"
APP_ID="${APP_ID:-gocassini}"
APP_VERSION="${APP_VERSION:-0.1.0}"
APP_SECRET="${APP_SECRET:-$(head -c 24 /dev/urandom | base64 | tr -d '/+=' | head -c 32)}"
TALK_RECORDING_SECRET="${TALK_RECORDING_SECRET:-$(head -c 24 /dev/urandom | base64 | tr -d '/+=' | head -c 32)}"
SCENARIO_NAME="${SCENARIO_NAME:-showcase-lantern-festival-v1}"
SCENARIO_PATH="${SCENARIO_PATH:-$HARNESS_DIR/scenarios/showcase-lantern-festival.v1.json}"
SCENARIO_MEDIA_DIR="${SCENARIO_MEDIA_DIR:-$HARNESS_DIR/media/processed/showcase-lantern-festival-v1}"
RECORD_DURATION_SECONDS="${RECORD_DURATION_SECONDS:-40}"
MIN_LEVENSHTEIN="${MIN_LEVENSHTEIN:-0.40}"

NEXTCLOUD_HOST_PORT="${NEXTCLOUD_HOST_PORT:-28080}"
CONTAINER_NAME="${CONTAINER_NAME:-cassini-talk-rec-e2e-app}"
LOG_DIR="${LOG_DIR:-/tmp/cassini-talk-rec-e2e-$$}"
mkdir -p "$LOG_DIR"

log()  { printf '[talk-rec-e2e] %s\n' "$*"; }
fail() { log "FAIL: $*"; exit 1; }
phase() { log "----- phase $1: $2 -----"; }

compose() { docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE" "$@"; }
occ()     { compose exec -T -u www-data nextcloud php occ "$@"; }

# Bot streamer needs Nextcloud reachable from the host shell.
NC_URL_HOST="http://127.0.0.1:${NEXTCLOUD_HOST_PORT}"
# Inside the compose network, services use docker DNS by hostname.
NC_URL_INTERNAL="${NC_URL_INTERNAL:-http://nextcloud}"
# Talk dials the recording-backend URL configured below; from inside
# Nextcloud's container that has to resolve to gocassini-exapp.
TALK_BACKEND_URL_INTERNAL="${TALK_BACKEND_URL_INTERNAL:-http://${CONTAINER_NAME}:8080/api/v1}"

cleanup() {
  local rc=$?
  log "cleanup (rc=$rc)"
  docker logs "$CONTAINER_NAME" >"$LOG_DIR/cassini-exapp.log" 2>&1 || true
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  compose logs nextcloud >"$LOG_DIR/nextcloud.log" 2>&1 || true
  compose logs signaling >"$LOG_DIR/signaling.log" 2>&1 || true
  compose down --volumes >/dev/null 2>&1 || true
  if [[ $rc -ne 0 ]]; then
    log "logs in $LOG_DIR"
    for f in cassini-exapp nextcloud signaling; do
      if [[ -s "$LOG_DIR/$f.log" ]]; then
        log "--- last 60 lines of $f ---"
        tail -n 60 "$LOG_DIR/$f.log" | sed 's/^/    /' || true
      fi
    done
  fi
}
trap cleanup EXIT

# ============================================================================
# Phase 1: bring up Nextcloud + Talk signaling stack (compose full profile)
# ============================================================================
phase 1 "Bring up Nextcloud + signaling/janus/coturn (compose full profile)"

SPREED_PROFILE=full PROJECT_NAME="$PROJECT_NAME" compose up -d >/dev/null

# wait_for_nextcloud is defined in common.sh, but we sourced compose helpers
# above instead. Poll directly:
log "waiting for Nextcloud status.php (up to 7 min)"
DEADLINE=$(( SECONDS + 420 ))
until curl -sf "$NC_URL_HOST/status.php" >/dev/null 2>&1; do
  if (( SECONDS > DEADLINE )); then
    fail "Nextcloud did not become reachable at $NC_URL_HOST"
  fi
  sleep 5
done
log "OK Nextcloud reachable at $NC_URL_HOST"

SPREED_PROFILE=full PROJECT_NAME="$PROJECT_NAME" \
  "$HARNESS_DIR/bin/bootstrap.sh" >"$LOG_DIR/bootstrap.log" 2>&1 \
  || { tail -n 40 "$LOG_DIR/bootstrap.log"; fail "bootstrap failed"; }
log "OK bootstrap (Talk + signaling configured)"

# ============================================================================
# Phase 2: install + enable AppAPI (needed for ExApp install)
# ============================================================================
phase 2 "Install + enable AppAPI"

occ app:install app_api >>"$LOG_DIR/occ.log" 2>&1 || true
occ app:enable  app_api >>"$LOG_DIR/occ.log" 2>&1
log "OK app_api enabled"

# Register a manual-install daemon pointing at our container.
occ app_api:daemon:register \
    manual_install \
    "Local manual install" \
    manual-install \
    http "$CONTAINER_NAME" "$NC_URL_INTERNAL" \
    >>"$LOG_DIR/occ.log" 2>&1 || log "WARN daemon may already be registered"

# ============================================================================
# Phase 3: run cassini-exapp container on the compose network
# ============================================================================
phase 3 "Start cassini-exapp ($IMAGE_REF) on the compose network"

docker pull "$IMAGE_REF" >>"$LOG_DIR/docker.log" 2>&1 || true

# AppAPI lifecycle env (so the operator's middleware works) plus the
# Talk-recording-backend shared secret (so the HMAC adapter accepts
# Talk's POSTs).
docker run -d --rm \
  --name "$CONTAINER_NAME" \
  --network "${PROJECT_NAME}_default" \
  -e "APP_HOST=0.0.0.0" \
  -e "APP_PORT=8080" \
  -e "APP_ID=$APP_ID" \
  -e "APP_VERSION=$APP_VERSION" \
  -e "APP_SECRET=$APP_SECRET" \
  -e "AA_VERSION=4.1.0" \
  -e "NEXTCLOUD_URL=$NC_URL_INTERNAL" \
  -e "TALK_RECORDING_SECRET=$TALK_RECORDING_SECRET" \
  -e "CASSINI_OPERATOR_BIND_ADDR=0.0.0.0:8080" \
  -e "CASSINI_APPAPI_REQUIRED=true" \
  "$IMAGE_REF" \
  >>"$LOG_DIR/docker.log" 2>&1

# Wait for /heartbeat to answer.
log "waiting for cassini-exapp /heartbeat"
DEADLINE=$(( SECONDS + 60 ))
until docker exec "$CONTAINER_NAME" \
    sh -c 'wget -qO- http://127.0.0.1:8080/heartbeat' >/dev/null 2>&1; do
  if (( SECONDS > DEADLINE )); then
    fail "cassini-exapp /heartbeat did not respond"
  fi
  sleep 2
done
log "OK cassini-exapp heartbeat 200"

# ============================================================================
# Phase 4: configure Talk's recording_servers + verify /api/v1/welcome
# ============================================================================
phase 4 "Configure Talk recording_servers + verify welcome handshake"

# Talk reads recording_servers as JSON from app config.
RECORDING_CFG=$(printf '{"servers":[{"server":"%s","verify":false}],"secret":"%s"}' \
  "$TALK_BACKEND_URL_INTERNAL" "$TALK_RECORDING_SECRET")
occ config:app:set spreed recording_servers --value "$RECORDING_CFG" \
  >>"$LOG_DIR/occ.log" 2>&1

# Talk welcome: GET /api/v1/welcome should answer {"version":1} from inside
# the Nextcloud container (Talk's reachable target).
WELCOME=$(compose exec -T nextcloud sh -c \
  "curl -sf $TALK_BACKEND_URL_INTERNAL/welcome" 2>/dev/null || true)
if [[ "$WELCOME" != *'"version":1'* ]]; then
  fail "Talk welcome from Nextcloud → gocassini failed (got: ${WELCOME:-<empty>})"
fi
log "OK Talk reaches gocassini /api/v1/welcome"

# ============================================================================
# Phase 5 (DRAFT): create a Talk room and stream scenario audio in
# ============================================================================
phase 5 "Create Talk room + start scenario audio (DRAFT)"

if [[ ! -d "$SCENARIO_MEDIA_DIR" ]]; then
  fail "scenario media not found: $SCENARIO_MEDIA_DIR
  Run: harness/bin/prepare-synthetic-meeting.sh \\
       --scenario $SCENARIO_PATH \\
       --output-dir $SCENARIO_MEDIA_DIR"
fi

# Create the Talk room as admin via OCS.
ROOM_NAME="cassini-talk-rec-e2e-$(date +%s)"
ROOM_RESP=$(curl -sf -u admin:admin \
  -H "OCS-APIRequest: true" \
  -H "Content-Type: application/json" \
  -d "{\"roomType\":2,\"roomName\":\"$ROOM_NAME\"}" \
  "$NC_URL_HOST/ocs/v2.php/apps/spreed/api/v4/room" \
  || fail "OCS create room failed")
ROOM_TOKEN=$(printf '%s' "$ROOM_RESP" | python3 -c \
  "import json,sys; print(json.load(sys.stdin)['ocs']['data']['token'])" \
  || fail "could not parse room token")
log "room token: $ROOM_TOKEN"

CALL_URL="$NC_URL_HOST/call/$ROOM_TOKEN"

log "TODO: stream audio bots into $CALL_URL using scenario $SCENARIO_NAME"
log "  (drafted; needs verification that bot streamer can reach signaling"
log "   on the docker-compose network from outside the network)"

# ============================================================================
# Phase 6 (DRAFT): trigger Talk recording via OCS
# ============================================================================
phase 6 "Trigger Talk recording via OCS (DRAFT)"

# Status: 1 = audio+video, 2 = audio only.
log "TODO: POST $NC_URL_HOST/ocs/v2.php/apps/spreed/api/v1/recording/$ROOM_TOKEN"
log "      status=2 (audio only)"
log "  Talk will then POST /api/v1/room/$ROOM_TOKEN at gocassini with HMAC"
log "  headers Talk-Recording-Backend / Talk-Recording-Random /"
log "  Talk-Recording-Checksum. Operator validates, accepts the job, and"
log "  spawns 'cassini record --call $CALL_URL'."

# ============================================================================
# Phase 7 (DRAFT): wait for the publish + read transcript
# ============================================================================
phase 7 "Wait for publish + read transcript (DRAFT)"

log "TODO: poll cassini-exapp /srv/cassini-site/published/ for the new bundle"
log "      then docker cp transcript.words.v1.json out"

# ============================================================================
# Phase 8 (DRAFT): Levenshtein check
# ============================================================================
phase 8 "Levenshtein check vs scenario expected text (DRAFT)"

log "TODO: concat scenario turns → expected text;"
log "      compute Levenshtein ratio vs got;"
log "      assert >= $MIN_LEVENSHTEIN"

log "DRAFT END — phases 1-4 are wired and assert; 5-8 are placeholders"
log "Next iteration: get phase 5 (bot audio into Talk call from this script"
log "context) and phase 6 (OCS recording trigger) working, then 7/8 follow"
