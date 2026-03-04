#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

export PROJECT_NAME="${PROJECT_NAME:-gocassini-ci-rejoin}"
export SPREED_PROFILE="${SPREED_PROFILE:-full}"
export NEXTCLOUD_URL="${NEXTCLOUD_URL:-http://127.0.0.1:28080}"
export NEXTCLOUD_STATUS_URL="${NEXTCLOUD_STATUS_URL:-$NEXTCLOUD_URL/status.php}"
export SIGNALING_URL="${SIGNALING_URL:-}"

export ADMIN_USER="${ADMIN_USER:-admin}"
export ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin}"
export BOT_USER="${BOT_USER:-ci-botuser}"
export BOT_PASSWORD="${BOT_PASSWORD:-ci-bot-password}"
export SIGNALING_SHARED_SECRET="${SIGNALING_SHARED_SECRET:-7f4dca67263621ba7f9f9917e13de95a201f6f360be0d303e3008c2e6c8ad37d}"
export TURN_SERVER="${TURN_SERVER:-127.0.0.1:13479}"
export TURN_SHARED_SECRET="${TURN_SHARED_SECRET:-3c04d2fc2f7fe39d48eb4dc77f652c8c778a4ea178b0e486529b284afca7b648}"

export CALL_NAME="${CALL_NAME:-CI Gocassini rejoin room}"
export REC_DURATION="${REC_DURATION:-40}"
export PHASE_ONE_DURATION="${PHASE_ONE_DURATION:-10}"
export PHASE_TWO_DURATION="${PHASE_TWO_DURATION:-12}"
export PHASE_GAP_SECONDS="${PHASE_GAP_SECONDS:-2}"
export START_DELAY="${START_DELAY:-6}"
export NAME_PREFIX="${NAME_PREFIX:-CassiniGoRejoin}"

RECORDER_DIR="$REPO_ROOT/cassini-go-recorder"
CI_OUTPUT_BASE="/tmp/gocassini-ci-rejoin-$(date -u +%Y%m%dT%H%M%S)-$$"
OUTPUT="${OUTPUT:-$CI_OUTPUT_BASE.requested-output}"
FINAL_OUTPUT="${FINAL_OUTPUT:-$CI_OUTPUT_BASE.mkv}"
REC_LOG="${REC_LOG:-/tmp/gocassini-ci-rejoin-recorder.log}"
PHASE1_LOG="${PHASE1_LOG:-/tmp/gocassini-ci-rejoin-phase1.log}"
PHASE2_LOG="${PHASE2_LOG:-/tmp/gocassini-ci-rejoin-phase2.log}"
REPORT_JSON="${FINAL_OUTPUT}.json"

cleanup() {
  log "Cleaning up local test stack"
  "$SCRIPT_DIR/down.sh" --volumes || true
}

trap cleanup EXIT INT TERM

log "Starting local Nextcloud Talk stack for CI (leave/rejoin)"
"$SCRIPT_DIR/up.sh"

log "Creating temporary room for CI capture"
CALL_URL="$(create_room_with_retry "$CALL_NAME")"
log "Test room URL: $CALL_URL"
export CALL_URL

rm -f "$OUTPUT" "$FINAL_OUTPUT" "$REC_LOG" "$PHASE1_LOG" "$PHASE2_LOG" "$REPORT_JSON"
mkdir -p "$(dirname "$OUTPUT")"

(
  cd "$RECORDER_DIR"
  go run ./cmd/gocassini \
    --mode talk \
    --call-url "$CALL_URL" \
    --name "$NAME_PREFIX" \
    --duration "$REC_DURATION" \
    --output "$OUTPUT" \
    --final-output "$FINAL_OUTPUT" \
    --request-offer-interval 2 \
    --max-request-offer-attempts 30
) >"$REC_LOG" 2>&1 &
REC_PID=$!

sleep "$START_DELAY"

(
  cd "$SCRIPT_DIR/../"
  ./bin/stream-video.sh \
    --call-url "$CALL_URL" \
    --users 1 \
    --duration "$PHASE_ONE_DURATION" \
    --name-prefix "$NAME_PREFIX"
) >"$PHASE1_LOG" 2>&1 || true

sleep "$PHASE_GAP_SECONDS"

(
  cd "$SCRIPT_DIR/../"
  ./bin/stream-video.sh \
    --call-url "$CALL_URL" \
    --users 1 \
    --duration "$PHASE_TWO_DURATION" \
    --name-prefix "$NAME_PREFIX"
) >"$PHASE2_LOG" 2>&1 || true

wait "$REC_PID" || true

if [[ ! -f "$FINAL_OUTPUT" ]]; then
  log "[FAIL] final mkv not found: $FINAL_OUTPUT"
  log "--- recorder log tail ---"
  tail -n 120 "$REC_LOG" || true
  exit 1
fi

if [[ ! -f "$REPORT_JSON" ]]; then
  log "[FAIL] missing run report: $REPORT_JSON"
  exit 1
fi

if (( $(stat -c '%s' "$FINAL_OUTPUT") <= 1024 )); then
  log "[FAIL] final mkv unexpectedly small"
  exit 1
fi

PHASE_LOGS=( "$PHASE1_LOG" "$PHASE2_LOG" )
for log_file in "${PHASE_LOGS[@]}"; do
  if ! rg -q "\[bot" "$log_file"; then
    log "[FAIL] no bot output in ${log_file}; publisher phase likely did not start"
    exit 1
  fi
done

PHASE1_CONNECTIONS="$(rg -c "audio muted|connected and streaming|audio unmuted" "$PHASE1_LOG" || true)"
PHASE2_CONNECTIONS="$(rg -c "connected and streaming" "$PHASE2_LOG" || true)"
if (( PHASE1_CONNECTIONS == 0 || PHASE2_CONNECTIONS == 0 )); then
  log "[FAIL] phases did not both connect; rejoin did not happen as expected"
  log "phase1 log: $PHASE1_LOG"
  log "phase2 log: $PHASE2_LOG"
  exit 1
fi

SESSION_COUNT="$(python3 - "$REPORT_JSON" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
session_outputs = data.get("session_outputs", [])
active = [
    s for s in session_outputs
    if int(s.get("audio_packets", 0)) > 0 or int(s.get("video_packets", 0)) > 0
]
print(len(active))
PY
)"

if (( SESSION_COUNT < 2 )); then
  log "[FAIL] expected at least 2 captured sessions for rejoin scenario, got ${SESSION_COUNT}"
  exit 1
fi

REJOIN_SEP_SECONDS="$(python3 - "$REPORT_JSON" <<'PY'
import datetime
import json
import sys

data = json.load(open(sys.argv[1]))
starts = []
for session in data.get("session_outputs", []):
    if not (int(session.get("audio_packets", 0)) > 0 or int(session.get("video_packets", 0)) > 0):
        continue
    started_at = session.get("started_at")
    if not started_at:
        continue
    dt = datetime.datetime.fromisoformat(started_at.replace("Z", "+00:00"))
    starts.append(dt.timestamp())

if len(starts) < 2:
    sys.exit(1)

starts.sort()
print(f"{starts[1] - starts[0]:.3f}")
PY
)"

if [[ -z "$REJOIN_SEP_SECONDS" ]]; then
  log "[FAIL] could not compute session start separation"
  exit 1
fi
if ! awk -v sep="$REJOIN_SEP_SECONDS" -v gap="$PHASE_GAP_SECONDS" 'BEGIN { exit (sep >= (gap - 0.5) ? 0 : 1) }'; then
  log "[FAIL] rejoin gap not reflected in session timing (sep=${REJOIN_SEP_SECONDS}s, expected >= $((PHASE_GAP_SECONDS - 1))s)"
  exit 1
fi

if ! ffprobe -v error -select_streams v -show_streams "$FINAL_OUTPUT" | rg -q "codec_type=video" ; then
  log "[FAIL] final output should contain video tracks"
  exit 1
fi

VIDEO_TRACKS="$(ffprobe -v error -show_entries stream=codec_type -of csv=p=0:nk=1 "$FINAL_OUTPUT" | awk -F',' '$1=="video"{n++} END {print n+0}')"
AUDIO_TRACKS="$(ffprobe -v error -show_entries stream=codec_type -of csv=p=0:nk=1 "$FINAL_OUTPUT" | awk -F',' '$1=="audio"{n++} END {print n+0}')"
if (( VIDEO_TRACKS < 1 )); then
  log "[FAIL] expected at least 1 video track after rejoin, got ${VIDEO_TRACKS}"
  exit 1
fi
if (( AUDIO_TRACKS < 1 )); then
  log "[FAIL] expected at least 1 audio track after rejoin, got ${AUDIO_TRACKS}"
  exit 1
fi

if [[ ! -f "$PHASE1_LOG" || ! -f "$PHASE2_LOG" ]]; then
  log "[FAIL] missing phase logs"
  exit 1
fi

log "PASS: leave/rejoin scenario complete"
