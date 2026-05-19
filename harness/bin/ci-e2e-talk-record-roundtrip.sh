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
# Override the default entrypoint (exapp-start.sh) which expects HaRP env
# vars (HP_FRP_ADDRESS etc) that we don't have outside a real AppAPI deploy.
# Run cassini-operator directly — same pattern as ci-e2e-install-exapp.sh.
docker run -d \
  --name "$CONTAINER_NAME" \
  --network "${PROJECT_NAME}_default" \
  -e "APP_HOST=0.0.0.0" \
  -e "APP_PORT=8080" \
  -e "APP_ID=$APP_ID" \
  -e "APP_VERSION=$APP_VERSION" \
  -e "APP_SECRET=$APP_SECRET" \
  -e "AA_VERSION=5.0.0" \
  -e "NEXTCLOUD_URL=$NC_URL_INTERNAL" \
  -e "TALK_RECORDING_SECRET=$TALK_RECORDING_SECRET" \
  -e "CASSINI_OPERATOR_BIND_ADDR=0.0.0.0:8080" \
  -e "CASSINI_OPERATOR_BASE_PATH=/operator" \
  -e "CASSINI_APPAPI_REQUIRED=true" \
  --entrypoint /usr/local/bin/cassini-operator \
  "$IMAGE_REF" \
  >>"$LOG_DIR/docker.log" 2>&1

# Wait for /heartbeat to answer. Image ships curl, not wget.
log "waiting for cassini-exapp /heartbeat"
DEADLINE=$(( SECONDS + 60 ))
HB_OK=0
while (( SECONDS < DEADLINE )); do
  status=$(docker exec "$CONTAINER_NAME" \
    curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8080/heartbeat \
    2>/dev/null || echo 000)
  if [[ "$status" == "200" ]]; then HB_OK=1; break; fi
  sleep 2
done
[[ $HB_OK -eq 1 ]] || fail "cassini-exapp /heartbeat never reached 200"
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
# Phase 5: create Talk room + start scenario audio bots
# ============================================================================
phase 5 "Create Talk room + start scenario audio bots"

if [[ ! -d "$SCENARIO_MEDIA_DIR" ]]; then
  fail "scenario media not found: $SCENARIO_MEDIA_DIR
  Run: harness/bin/prepare-synthetic-meeting.sh \\
       --scenario $SCENARIO_PATH \\
       --output-dir $SCENARIO_MEDIA_DIR"
fi

# Create a Talk public room as admin via OCS (roomType=3 = public).
ROOM_NAME="cassini-talk-rec-e2e-$(date +%s)"
ROOM_RESP=$(curl -sf -u admin:admin \
  -H "OCS-APIRequest: true" \
  -H "Accept: application/json" \
  -H "Content-Type: application/json" \
  -d "{\"roomType\":3,\"roomName\":\"$ROOM_NAME\"}" \
  "$NC_URL_HOST/ocs/v2.php/apps/spreed/api/v4/room" \
  || fail "OCS create room failed")
ROOM_TOKEN=$(printf '%s' "$ROOM_RESP" | python3 -c \
  "import json,sys; print(json.load(sys.stdin)['ocs']['data']['token'])" \
  || fail "could not parse room token from: $ROOM_RESP")
CALL_URL="$NC_URL_HOST/call/$ROOM_TOKEN"
log "room token: $ROOM_TOKEN"
log "call URL:   $CALL_URL"

# Stream scenario audio into the call. The synthetic-meeting streamer
# launches go-talk-rotator bots (one per scenario participant) that
# join the call as guests and play their pre-rendered OGG track.
# Bots reach Talk's signaling via the host-network services on
# 127.0.0.1, same as ci-e2e-rejoin.sh's stream-video.sh.
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

# Wait for at least one bot to actually be in the call before triggering
# recording — Talk rejects recording start with 400 when there's no active
# call. Poll Talk's "active call" state via OCS.
log "waiting for at least one participant in call (up to 90s)"
DEADLINE=$(( SECONDS + 90 ))
PARTICIPANTS=0
while (( SECONDS < DEADLINE )); do
  PEEK=$(curl -sf -u admin:admin \
    -H "OCS-APIRequest: true" \
    -H "Accept: application/json" \
    "$NC_URL_HOST/ocs/v2.php/apps/spreed/api/v4/room/$ROOM_TOKEN" \
    2>/dev/null || true)
  if [[ -n "$PEEK" ]]; then
    PARTICIPANTS=$(printf '%s' "$PEEK" | python3 -c \
      "import json,sys; d=json.load(sys.stdin)['ocs']['data']; print(d.get('activeSince') and d.get('hasCall') and 1 or 0)" \
      2>/dev/null || echo 0)
  fi
  if [[ "$PARTICIPANTS" == "1" ]]; then break; fi
  sleep 3
done
if [[ "$PARTICIPANTS" != "1" ]]; then
  log "WARN: no active call detected after 90s; bot log tail:"
  tail -n 30 "$BOT_LOG" | sed 's/^/    /' || true
  # Continue anyway — Talk's reject behavior is informative
fi
log "OK active call detected (or proceeding anyway)"

# ============================================================================
# Phase 6: trigger Talk recording via OCS; Talk fires HMAC POST at gocassini
# ============================================================================
phase 6 "Trigger Talk recording via OCS POST /apps/spreed/api/v1/recording"

# Status: 1 = audio+video, 2 = audio only. Audio-only is enough for the
# transcript check and produces a smaller MKV.
TRIGGER_RESP=$(curl -sf -u admin:admin \
  -H "OCS-APIRequest: true" \
  -H "Accept: application/json" \
  -H "Content-Type: application/json" \
  -d '{"status":2}' \
  "$NC_URL_HOST/ocs/v2.php/apps/spreed/api/v1/recording/$ROOM_TOKEN" \
  || fail "OCS recording start failed; Talk side rejected the request")
log "OCS recording trigger response: ${TRIGGER_RESP:0:200}"

# Within a few seconds Talk should call /api/v1/room/$ROOM_TOKEN on
# gocassini with the HMAC headers. Confirm by tailing the operator log.
log "waiting for operator to log Talk-started callback (up to 30s)"
DEADLINE=$(( SECONDS + 30 ))
until docker logs "$CONTAINER_NAME" 2>&1 | grep -q "accepted id="; do
  if (( SECONDS > DEADLINE )); then
    log "operator log tail:"
    docker logs "$CONTAINER_NAME" 2>&1 | tail -n 40 | sed 's/^/    /'
    fail "operator never accepted the Talk-triggered record job"
  fi
  sleep 2
done
JOB_ID=$(docker logs "$CONTAINER_NAME" 2>&1 \
  | grep -oE 'accepted id=[A-Z0-9]+' | head -n1 | cut -d= -f2)
log "OK operator accepted Talk-triggered job: $JOB_ID"

# ============================================================================
# Phase 7: wait for the recording lifecycle to complete; pull transcript
# ============================================================================
phase 7 "Wait for record → upload → build → publish; pull transcript"

# Total budget: bot audio plays for $RECORD_DURATION_SECONDS seconds, then
# recorder stops on empty room (default 8s grace), then upload to Talk,
# then build (transcribe v3 int8 on 30s of audio takes ~10-20s on CPU),
# then publish. Generous timeout:
PUBLISH_DEADLINE=$(( SECONDS + RECORD_DURATION_SECONDS + 180 ))
log "waiting for /srv/cassini-site/published/meeting-* (up to $((PUBLISH_DEADLINE - SECONDS))s)"
PUBLISHED_DIR=""
while (( SECONDS < PUBLISH_DEADLINE )); do
  CANDIDATE=$(docker exec "$CONTAINER_NAME" \
    sh -c 'ls -d /srv/cassini-site/published/meeting-* 2>/dev/null | head -n1' \
    || true)
  if [[ -n "$CANDIDATE" ]] && \
     docker exec "$CONTAINER_NAME" test -s "$CANDIDATE/transcript.words.v1.json"; then
    PUBLISHED_DIR="$CANDIDATE"
    break
  fi
  sleep 4
done
[[ -n "$PUBLISHED_DIR" ]] || fail "published bundle never appeared"
log "OK published bundle: $PUBLISHED_DIR"

# Pull the transcript out so phase 8 can read it.
TRANSCRIPT_HOST="$LOG_DIR/transcript.words.v1.json"
docker cp "$CONTAINER_NAME:$PUBLISHED_DIR/transcript.words.v1.json" "$TRANSCRIPT_HOST"
log "OK transcript pulled: $TRANSCRIPT_HOST ($(wc -c <"$TRANSCRIPT_HOST") bytes)"

# Bot streamer should have exited by now (or we kill it).
wait "$BOT_PID" 2>/dev/null || true

# ============================================================================
# Phase 8: Levenshtein-check transcript vs scenario expected text
# ============================================================================
phase 8 "Levenshtein-check transcript vs scenario expected text"

EXPECTED_TEXT=$(python3 - "$SCENARIO_PATH" <<'PY'
import json, sys
with open(sys.argv[1]) as f:
    data = json.load(f)
print(" ".join(t["text"] for t in data.get("turns", [])))
PY
)
if [[ -z "$EXPECTED_TEXT" ]]; then
  fail "could not derive expected text from $SCENARIO_PATH"
fi

python3 - "$TRANSCRIPT_HOST" "$EXPECTED_TEXT" "$MIN_LEVENSHTEIN" <<'PY'
import json, re, sys, unicodedata

transcript_path, expected, min_ratio = sys.argv[1], sys.argv[2], float(sys.argv[3])

def normalise(text: str) -> str:
    text = unicodedata.normalize("NFKD", text)
    text = "".join(c for c in text if not unicodedata.combining(c))
    text = text.lower()
    text = re.sub(r"[^a-z0-9 ]+", " ", text)
    text = re.sub(r"\s+", " ", text).strip()
    return text

def lev(a: str, b: str) -> int:
    if len(a) < len(b):
        a, b = b, a
    if not b:
        return len(a)
    prev = list(range(len(b) + 1))
    for i, ca in enumerate(a, 1):
        curr = [i]
        for j, cb in enumerate(b, 1):
            curr.append(min(curr[j-1]+1, prev[j]+1, prev[j-1] + (0 if ca == cb else 1)))
        prev = curr
    return prev[-1]

with open(transcript_path) as f:
    data = json.load(f)

words = []
for segment in data.get("segments", []):
    for w in segment.get("words", []):
        if (w.get("text") or "").strip():
            words.append(w["text"])
got = normalise(" ".join(words))
want = normalise(expected)

if not got:
    print("[talk-rec-e2e] FAIL transcript empty after normalisation")
    sys.exit(1)

distance = lev(got, want)
ratio = 1.0 - distance / max(len(got), len(want))
print(f"[talk-rec-e2e] expected ({len(want)} chars): {want[:120]!r}...")
print(f"[talk-rec-e2e] got      ({len(got)} chars): {got[:120]!r}...")
print(f"[talk-rec-e2e] edit-distance={distance} ratio={ratio:.4f} threshold={min_ratio:.2f}")
if ratio < min_ratio:
    print(f"[talk-rec-e2e] FAIL Levenshtein ratio {ratio:.4f} < threshold {min_ratio:.2f}")
    sys.exit(1)
print(f"[talk-rec-e2e] OK   Levenshtein ratio {ratio:.4f} >= threshold {min_ratio:.2f}")
PY

log "FULL TALK-DRIVEN E2E PASSED — Talk record-button → gocassini → transcript"
