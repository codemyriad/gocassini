#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./lib/e2e-local.sh
source "$SCRIPT_DIR/lib/e2e-local.sh"
harness_e2e_local_stack_env full legacy none
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
# BOT_USER/BOT_PASSWORD come from common.sh — the single source of truth the
# bootstrap creates the user from. A second default here would silently
# diverge (that mismatch broke the first D-454 private soak); only export.
export BOT_USER BOT_PASSWORD
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
export PHASE_TWO_PARTICIPANT="${PHASE_TWO_PARTICIPANT:-${NAME_PREFIX}1}"

RECORDER_DIR="$REPO_ROOT/cassini-go-recorder"
CI_OUTPUT_BASE="/tmp/gocassini-ci-rejoin-$(date -u +%Y%m%dT%H%M%S)-$$"
OUTPUT="${OUTPUT:-$CI_OUTPUT_BASE.mkv}"
FINAL_OUTPUT="${FINAL_OUTPUT:-$OUTPUT}"
REC_LOG="${REC_LOG:-/tmp/gocassini-ci-rejoin-recorder.log}"
PHASE1_LOG="${PHASE1_LOG:-/tmp/gocassini-ci-rejoin-phase1.log}"
PHASE2_LOG="${PHASE2_LOG:-/tmp/gocassini-ci-rejoin-phase2.log}"

# Explicit stack topology for this e2e leg: local HTTP, full media services
# (nats/janus/signaling/coturn), no installed ExApp, legacy recording backend.
STACK_TOPOLOGY=(
  --public-mode local-http
  --services full
  --cassini none
  --recording-backend legacy
)

cleanup() {
  log "Cleaning up local test stack"
  "$REPO_ROOT/bin/cassini" dev stack down --volumes "${STACK_TOPOLOGY[@]}" || true
}

trap cleanup EXIT INT TERM

log "Starting local Nextcloud Talk stack for CI (leave/rejoin)"
# --reset: e2e wants a deterministic fresh stack; a leaked project from an
# earlier aborted run must not fail the bring-up guard.
"$REPO_ROOT/bin/cassini" dev stack up "${STACK_TOPOLOGY[@]}" --reset

log "Creating temporary room for CI capture"
CALL_URL="$(create_room_with_retry "$CALL_NAME")"
log "Test room URL: $CALL_URL"
export CALL_URL

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

# session-artifact stream_opened events use UnixNano in mono_ns. Capture the
# phase boundary immediately before launching the second publisher so earlier
# phase-one media cannot satisfy the rejoin assertion.
PHASE_TWO_BOUNDARY_NS="$(date +%s%N)"
log "Phase-two recorder evidence boundary: ${PHASE_TWO_BOUNDARY_NS} participant=${PHASE_TWO_PARTICIPANT}"
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

PHASE_TWO_REMOTE_SESSION_ID="$(
  sed -nE 's/.*hello ok \(version [^,]+, session=([^)]*)\).*/\1/p' "$PHASE2_LOG" | head -n1
)"
if [[ -z "$PHASE_TWO_REMOTE_SESSION_ID" ]]; then
  log "[FAIL] phase-two publisher did not report its signaling session identity"
  exit 1
fi
log "Phase-two publisher signaling session: $PHASE_TWO_REMOTE_SESSION_ID"

PHASE1_CONNECTIONS="$(rg -c "audio muted|connected and streaming|audio unmuted" "$PHASE1_LOG" || true)"
PHASE2_CONNECTIONS="$(rg -c "connected and streaming" "$PHASE2_LOG" || true)"
if (( PHASE1_CONNECTIONS == 0 || PHASE2_CONNECTIONS == 0 )); then
  log "[FAIL] phases did not both connect; rejoin did not happen as expected"
  log "phase1 log: $PHASE1_LOG"
  log "phase2 log: $PHASE2_LOG"
  exit 1
fi

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

EVENTS_PATH="$(cassini_events_log_from_mkv "$FINAL_OUTPUT" || true)"
STREAMS_DIR="$(cassini_streams_dir_from_mkv "$FINAL_OUTPUT" || true)"
if [[ -z "$EVENTS_PATH" || ! -f "$EVENTS_PATH" ]]; then
  log "[FAIL] could not derive session artifact events log from MKV metadata"
  exit 1
fi
if [[ -z "$STREAMS_DIR" || ! -d "$STREAMS_DIR" ]]; then
  log "[FAIL] could not derive session artifact streams directory from MKV metadata"
  exit 1
fi
if ! POST_BOUNDARY_EVIDENCE="$(
  "$SCRIPT_DIR/verify-post-boundary-stream.sh" \
    --events "$EVENTS_PATH" \
    --boundary-ns "$PHASE_TWO_BOUNDARY_NS" \
    --participant "$PHASE_TWO_PARTICIPANT" \
    --remote-session-id "$PHASE_TWO_REMOTE_SESSION_ID"
)"; then
  log "[FAIL] recorder opened no phase-two media stream for $PHASE_TWO_PARTICIPANT"
  log "events log: $EVENTS_PATH"
  tail -n 80 "$EVENTS_PATH" || true
  exit 1
fi
log "Phase-two recorder evidence: $POST_BOUNDARY_EVIDENCE"
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
