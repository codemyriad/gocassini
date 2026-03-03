#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

export PROJECT_NAME="${PROJECT_NAME:-gocassini-ci-mute}"
export SPREED_PROFILE="${SPREED_PROFILE:-full}"
export NEXTCLOUD_URL="${NEXTCLOUD_URL:-http://127.0.0.1:18080}"
export NEXTCLOUD_STATUS_URL="${NEXTCLOUD_STATUS_URL:-$NEXTCLOUD_URL/status.php}"
export SIGNALING_URL="${SIGNALING_URL:-http://127.0.0.1:18082}"

export ADMIN_USER="${ADMIN_USER:-admin}"
export ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin}"
export BOT_USER="${BOT_USER:-ci-botuser}"
export BOT_PASSWORD="${BOT_PASSWORD:-ci-bot-password}"
export SIGNALING_SHARED_SECRET="${SIGNALING_SHARED_SECRET:-7f4dca67263621ba7f9f9917e13de95a201f6f360be0d303e3008c2e6c8ad37d}"
export TURN_SERVER="${TURN_SERVER:-127.0.0.1:13479}"
export TURN_SHARED_SECRET="${TURN_SHARED_SECRET:-3c04d2fc2f7fe39d48eb4dc77f652c8c778a4ea178b0e486529b284afca7b648}"

export REC_DURATION="${REC_DURATION:-36}"
export PUB_DURATION="${PUB_DURATION:-22}"
export PUB_USERS="${PUB_USERS:-3}"
export CALL_NAME="${CALL_NAME:-CI Gocassini mute room}"

CI_OUTPUT_BASE="/tmp/gocassini-ci-mute-$(date -u +%Y%m%dT%H%M%S)-$$"
export OUTPUT="${OUTPUT:-$CI_OUTPUT_BASE.csr}"
export FINAL_OUTPUT="${FINAL_OUTPUT:-$CI_OUTPUT_BASE.mkv}"
export REC_LOG="${REC_LOG:-/tmp/gocassini-ci-recorder-mute.log}"
export PUB_LOG="${PUB_LOG:-/tmp/gocassini-ci-publisher-mute.log}"
export REPORT_JSON="${REPORT_JSON:-$OUTPUT.json}"

cleanup() {
  log "Cleaning up local test stack"
  "$SCRIPT_DIR/down.sh" --volumes || true
}

trap cleanup EXIT INT TERM

log "Starting local Nextcloud Talk stack for CI (mute rotation)"
"$SCRIPT_DIR/up.sh"

log "Creating temporary room for CI capture"
CALL_URL="$("$SCRIPT_DIR/create-room.sh" --name "$CALL_NAME" | tail -n1)"
log "Test room URL: $CALL_URL"
export CALL_URL

log "Running recorder + rotating publishers (mute coverage)"
(
  cd "$SCRIPT_DIR/../cassini-go-recorder"
  ./e2e_with_publisher.sh
)
ci_rc=$?
if [[ "$ci_rc" -ne 0 ]]; then
  log "ci-e2e base publisher run failed with rc=${ci_rc}"
  exit "$ci_rc"
fi

if [[ ! -f "$PUB_LOG" ]]; then
  log "Publisher log missing: $PUB_LOG"
  exit 1
fi

if ! rg -q "\[manager\] audible=" "$PUB_LOG"; then
  log "No publisher audible rotation events found; mute coverage was not exercised"
  exit 1
fi

if ! rg -q "media stats: " "$PUB_LOG"; then
  log "No publisher media stats found; media pipeline may not have started"
  exit 1
fi

if [[ ! -f "$FINAL_OUTPUT" ]]; then
  log "Missing final output: $FINAL_OUTPUT"
  exit 1
fi

VIDEO_COUNT="$(ffprobe -v error -show_entries stream=codec_type -of csv=p=0:nk=1 "$FINAL_OUTPUT" | awk -F',' '$1=="video"{n++} END {print n+0}')"
AUDIO_COUNT="$(ffprobe -v error -show_entries stream=codec_type -of csv=p=0:nk=1 "$FINAL_OUTPUT" | awk -F',' '$1=="audio"{n++} END {print n+0}')"

if (( VIDEO_COUNT < 3 )); then
  log "Expected at least 3 video streams from mute scenario, got ${VIDEO_COUNT}"
  exit 1
fi
if (( AUDIO_COUNT < 3 )); then
  log "Expected at least 3 audio streams from mute scenario, got ${AUDIO_COUNT}"
  exit 1
fi

if [[ -f "$REPORT_JSON" ]]; then
  SESSION_COUNT="$(python3 - "$REPORT_JSON" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
session_outputs = data.get("session_outputs", [])
active = [
    s for s in session_outputs
    if s.get("audio_exists") or s.get("video_exists") or s.get("h264_video_exists")
]
print(len(active))
PY
)"
  if (( SESSION_COUNT < 3 )); then
    log "Expected report to include at least 3 sessions, got ${SESSION_COUNT}"
    exit 1
  fi
fi

log "PASS: mute coverage scenario complete"
