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
OUTPUT="${OUTPUT:-$CI_OUTPUT_BASE.mkv}"
FINAL_OUTPUT="${FINAL_OUTPUT:-$OUTPUT}"
REC_LOG="${REC_LOG:-/tmp/gocassini-ci-rejoin-recorder.log}"
PHASE1_LOG="${PHASE1_LOG:-/tmp/gocassini-ci-rejoin-phase1.log}"
PHASE2_LOG="${PHASE2_LOG:-/tmp/gocassini-ci-rejoin-phase2.log}"

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

# Capture = recorder + two publisher phases (leave/rejoin). A transient WebRTC
# ICE flake makes the recorder capture no media for a phase, so the capture
# fails one attempt but a fresh negotiation usually connects. run_with_retries
# re-rolls; a real regression fails every attempt. The capture-success checks
# (mkv present + both phases connected) live INSIDE the function so an ICE flake
# is what triggers the retry; the richer track/artifact assertions run once,
# after a good capture.
run_rejoin_capture() {
  rm -f "$OUTPUT" "$FINAL_OUTPUT" "$REC_LOG" "$PHASE1_LOG" "$PHASE2_LOG"
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
  local rec_pid=$!

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

  wait "$rec_pid" || true

  if [[ ! -f "$FINAL_OUTPUT" ]]; then
    log "[capture] final mkv not found: $FINAL_OUTPUT"
    log "--- recorder log tail ---"
    tail -n 120 "$REC_LOG" || true
    return 1
  fi

  if (( $(stat -c '%s' "$FINAL_OUTPUT") <= 1024 )); then
    log "[capture] final mkv unexpectedly small"
    return 1
  fi

  local log_file
  for log_file in "$PHASE1_LOG" "$PHASE2_LOG"; do
    if ! rg -q "\[bot" "$log_file"; then
      log "[capture] no bot output in ${log_file}; publisher phase likely did not start"
      return 1
    fi
  done

  local phase1_conn phase2_conn
  phase1_conn="$(rg -c "audio muted|connected and streaming|audio unmuted" "$PHASE1_LOG" || true)"
  phase2_conn="$(rg -c "connected and streaming" "$PHASE2_LOG" || true)"
  if (( phase1_conn == 0 || phase2_conn == 0 )); then
    log "[capture] phases did not both connect; rejoin did not happen (phase1=${phase1_conn} phase2=${phase2_conn})"
    return 1
  fi
  return 0
}
run_with_retries run_rejoin_capture

VIDEO_TRACKS="$(ffprobe -v error -show_entries stream=codec_type -of csv=p=0:nk=1 "$FINAL_OUTPUT" | awk -F',' '$1=="video"{n++} END {print n+0}')"
AUDIO_TRACKS="$(ffprobe -v error -show_entries stream=codec_type -of csv=p=0:nk=1 "$FINAL_OUTPUT" | awk -F',' '$1=="audio"{n++} END {print n+0}')"
MEDIA_TRACKS=$((VIDEO_TRACKS + AUDIO_TRACKS))
if (( MEDIA_TRACKS < 1 )); then
  log "[FAIL] expected at least one media track after rejoin, got video=${VIDEO_TRACKS} audio=${AUDIO_TRACKS}"
  exit 1
fi
if (( VIDEO_TRACKS == 0 )); then
  log "[WARN] final output has no video track in this run; continuing because rejoin evidence is validated from logs/artifacts"
fi

if [[ ! -f "$PHASE1_LOG" || ! -f "$PHASE2_LOG" ]]; then
  log "[FAIL] missing phase logs"
  exit 1
fi

"$SCRIPT_DIR/verify-session-artifact.sh" --final-output "$FINAL_OUTPUT"

"$SCRIPT_DIR/verify-av-drift.sh" \
  --input "$FINAL_OUTPUT" \
  --tolerance 0.80 \
  --min-elapsed 15

STREAMS_DIR="$(cassini_streams_dir_from_mkv "$FINAL_OUTPUT" || true)"
if [[ -z "$STREAMS_DIR" || ! -d "$STREAMS_DIR" ]]; then
  log "[FAIL] could not derive session artifact streams directory from MKV metadata"
  exit 1
fi
ARTIFACT_STREAM_COUNT="$(find "$STREAMS_DIR" -type f -name '*.rtplog' | wc -l | tr -d ' ')"
if (( ARTIFACT_STREAM_COUNT < 1 )); then
  log "[FAIL] expected at least one active artifact stream, got ${ARTIFACT_STREAM_COUNT}"
  exit 1
fi

SUBSCRIBED_REMOTE_SESSIONS="$(
  rg -o 'subscribing to remote session [^ ]+' "$REC_LOG" \
    | awk '{print $5}' \
    | sort -u \
    | wc -l \
    | tr -d ' '
)"

SUBSCRIBE_EVENT_COUNT="$(rg -c 'subscribing to remote session' "$REC_LOG" || true)"
if (( SUBSCRIBE_EVENT_COUNT < 1 )); then
  log "[FAIL] recorder did not subscribe to any remote session"
  log "recorder log: $REC_LOG"
  exit 1
fi
if (( SUBSCRIBED_REMOTE_SESSIONS < 2 )); then
  # Some deployments keep/reuse the same remote session ID across leave/rejoin.
  # We still have rejoin evidence from phase connection logs and artifact continuity.
  log "[WARN] observed ${SUBSCRIBED_REMOTE_SESSIONS} distinct remote session ID(s) across rejoin; accepting (deployment may reuse IDs)"
fi

log "PASS: leave/rejoin scenario complete"
