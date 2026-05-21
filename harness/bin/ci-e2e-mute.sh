#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

export PROJECT_NAME="${PROJECT_NAME:-gocassini-ci-mute}"
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

export REC_DURATION="${REC_DURATION:-36}"
export PUB_DURATION="${PUB_DURATION:-22}"
export PUB_USERS="${PUB_USERS:-3}"
export CALL_NAME="${CALL_NAME:-CI Gocassini mute room}"

# 3-publisher ICE convergence is sequential and can stack 4-8s per subscriber
# on shared CI runners; if the recorder window is too short, late subscribers
# never reach ICE state=connected and their tracks never enter the MKV
# metadata, tripping the EXPECTED_SESSIONS assertion below. Mirror the floor
# pattern used in ci-e2e.sh, but with headroom proportional to PUB_USERS.
if (( REC_DURATION < 60 )); then
  REC_DURATION=60
fi
if (( PUB_DURATION < 48 )); then
  PUB_DURATION=48
fi
export REC_DURATION PUB_DURATION

CI_OUTPUT_BASE="/tmp/gocassini-ci-mute-$(date -u +%Y%m%dT%H%M%S)-$$"
export OUTPUT="${OUTPUT:-$CI_OUTPUT_BASE.mkv}"
export FINAL_OUTPUT="${FINAL_OUTPUT:-$OUTPUT}"
export REC_LOG="${REC_LOG:-/tmp/gocassini-ci-recorder-mute.log}"
export PUB_LOG="${PUB_LOG:-/tmp/gocassini-ci-publisher-mute.log}"

cleanup() {
  log "Cleaning up local test stack"
  "$SCRIPT_DIR/down.sh" --volumes || true
}

trap cleanup EXIT INT TERM

log "Starting local Nextcloud Talk stack for CI (mute rotation)"
"$SCRIPT_DIR/up.sh"

log "Creating temporary room for CI capture"
CALL_URL="$(create_room_with_retry "$CALL_NAME")"
log "Test room URL: $CALL_URL"
export CALL_URL

log "Running recorder + rotating publishers (mute coverage)"
(
  cd "$REPO_ROOT/cassini-go-recorder"
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

# Final artifact-remux output can legitimately merge or collapse track layout.
# For CI we only require watchable A/V here and validate multi-source coverage via session artifacts below.
if (( VIDEO_COUNT < 1 )); then
  log "Expected at least 1 video stream in final output, got ${VIDEO_COUNT}"
  exit 1
fi
if (( AUDIO_COUNT < 1 )); then
  log "Expected at least 1 audio stream in final output, got ${AUDIO_COUNT}"
  exit 1
fi

EXPECTED_SESSIONS="$PUB_USERS"
if (( EXPECTED_SESSIONS < 1 )); then
  EXPECTED_SESSIONS=1
fi
PARTICIPANT_COUNT="$(cassini_unique_participant_count_from_mkv "$FINAL_OUTPUT")"
if (( PARTICIPANT_COUNT < EXPECTED_SESSIONS )); then
  log "Expected MKV metadata to expose at least ${EXPECTED_SESSIONS} unique participants, got ${PARTICIPANT_COUNT}"
  exit 1
fi

"$SCRIPT_DIR/verify-session-artifact.sh" --final-output "$FINAL_OUTPUT"

STREAMS_DIR="$(cassini_streams_dir_from_mkv "$FINAL_OUTPUT" || true)"
if [[ -z "$STREAMS_DIR" || ! -d "$STREAMS_DIR" ]]; then
  log "Could not derive session artifact streams directory from MKV metadata"
  exit 1
fi
ARTIFACT_STREAM_COUNT="$(find "$STREAMS_DIR" -type f -name '*.rtplog' | wc -l | tr -d ' ')"

# Artifact stream floor should reflect how many publishers we intentionally launched.
if (( ARTIFACT_STREAM_COUNT < EXPECTED_SESSIONS )); then
  log "Expected at least ${EXPECTED_SESSIONS} active artifact streams for mute rotation, got ${ARTIFACT_STREAM_COUNT}"
  exit 1
fi

log "PASS: mute coverage scenario complete"
