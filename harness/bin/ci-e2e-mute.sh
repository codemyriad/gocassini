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

export PUB_USERS="${PUB_USERS:-3}"

# Flake D-052 mitigation: serialize the publishers' ICE negotiations.
#
# With the rotator default join_delay=i-1 the three publishers join ~0/1/2s
# apart, so all three ICE negotiations hit janus near-simultaneously. Under CI
# CPU load only some reach ICE-connected inside REC_DURATION, and the recorder's
# onRemoteTrack (and thus media capture) only fires after ICE+track. A session
# that never ICE-connects contributes zero captured media, collapsing the unique
# participant / rtplog count below the >=PUB_USERS assertion (seen as "got 1/2").
#
# JOIN_DELAYS staggers the joins ~6s apart so the negotiations are spread out and
# each publisher — especially the last joiner, the one that flaked at "got 2/3"
# under load — gets a clean, uncontended ICE window. e2e_with_publisher.sh plumbs
# JOIN_DELAYS -> stream-video.sh --join-delays -> rotator --join-delay (per bot).
export JOIN_DELAYS="${JOIN_DELAYS:-0,6,12}"

# REC_DURATION must outlast the last joiner's (+12s) join + a generous ICE budget
# plus its whole publish window. With START_DELAY=6 the +12s bot starts joining at
# ~t=18s and, under CI load, may take up to CAPTURE_READY_TIMEOUT (60s, i.e. until
# ~t=66) to reach recorder-side video capture; the recorder must keep recording
# past that and through the mute rotation, so give it 85s.
export REC_DURATION="${REC_DURATION:-85}"

# Per-bot publish windows are sized so every bot is still sending media deep into
# the recorder's capture window — past the worst-case readiness deadline (~t=66)
# and through the mute rotation. The rotator exits on media EOF even if
# --bot-duration is longer, so MUTE_MEDIA_DURATION below is sized >= the longest
# bot duration. join_delay is slept *before* the per-bot duration timer starts, so
# each bot streams for roughly [START_DELAY+join+connect, +duration]. With
# START_DELAY=6 and joins at +0/+6/+12 these durations land all three publishing
# together from ~t=20s until ~t=80s, inside REC_DURATION=85.
#   bot1: join@6  stream@~8  +72 -> ~80
#   bot2: join@12 stream@~14 +66 -> ~80
#   bot3: join@18 stream@~20 +60 -> ~80
# BOT_DURATIONS (plumbed via e2e_with_publisher.sh -> stream-video.sh
# --bot-durations -> rotator --bot-duration) takes precedence over PUB_DURATION
# per bot; PUB_DURATION stays as a fallback for any bot without an explicit value.
export BOT_DURATIONS="${BOT_DURATIONS:-72,66,60}"
export PUB_DURATION="${PUB_DURATION:-72}"
export CALL_NAME="${CALL_NAME:-CI Gocassini mute room}"

PREPARE_MUTE_MEDIA=0
if [[ -z "${MEDIA_PREFIX:-}" && -z "${MEDIA_PREFIXES:-}" ]]; then
  export MUTE_MEDIA_PREFIX="${MUTE_MEDIA_PREFIX:-$MEDIA_DIR/sample-mute}"
  export MUTE_MEDIA_DURATION="${MUTE_MEDIA_DURATION:-78}"
  PREPARE_MUTE_MEDIA=1
fi

CI_OUTPUT_BASE="/tmp/gocassini-ci-mute-$(date -u +%Y%m%dT%H%M%S)-$$"
export OUTPUT="${OUTPUT:-$CI_OUTPUT_BASE.mkv}"
export FINAL_OUTPUT="${FINAL_OUTPUT:-$OUTPUT}"
export REC_LOG="${REC_LOG:-/tmp/gocassini-ci-recorder-mute.log}"
export PUB_LOG="${PUB_LOG:-/tmp/gocassini-ci-publisher-mute.log}"

# Deterministic mute-start barrier:
#
# The mute scenario asserts that all three participants were captured, but the
# flaky path was recorder-side subscriber bring-up: a publisher could be
# "connected and streaming" while the recorder had not yet opened a capture
# stream for that participant. Start publishers muted, wait for recorder-side
# video stream_opened events for every expected participant, then let the
# rotator begin unmuting audio. If readiness never arrives, fail this one run
# with the subscriber/stream diagnostics instead of retrying the whole job.
export MUTE_ROTATION_START_FILE="${MUTE_ROTATION_START_FILE:-$CI_OUTPUT_BASE.mute-start}"
export CAPTURE_READY_PARTICIPANTS="${CAPTURE_READY_PARTICIPANTS:-$PUB_USERS}"
# 60s (was 35): under CI load the last joiner's ICE sometimes needed >35s to reach
# recorder-side video capture, flaking at "got 2/3". This is a MAX wait — fast runs
# open the gate and proceed immediately — so a generous ceiling cuts the flake
# without slowing the common case. REC_DURATION above is sized to outlast it.
export CAPTURE_READY_TIMEOUT="${CAPTURE_READY_TIMEOUT:-60}"
export CAPTURE_READY_STREAM_KIND="${CAPTURE_READY_STREAM_KIND:-video}"

cleanup() {
  log "Cleaning up local test stack"
  "$SCRIPT_DIR/down.sh" --volumes || true
}

trap cleanup EXIT INT TERM

if [[ "$PREPARE_MUTE_MEDIA" == "1" ]]; then
  log "Preparing mute media fixture (${MUTE_MEDIA_DURATION}s): $MUTE_MEDIA_PREFIX"
  "$SCRIPT_DIR/prepare-media.sh" \
    --prefix "$MUTE_MEDIA_PREFIX" \
    --duration "$MUTE_MEDIA_DURATION" \
    --force
  export MEDIA_PREFIX="$MUTE_MEDIA_PREFIX"
fi

log "Starting local Nextcloud Talk stack for CI (mute rotation)"
"$SCRIPT_DIR/up.sh"

log "Creating temporary room for CI capture"
CALL_URL="$(create_room_with_retry "$CALL_NAME")"
log "Test room URL: $CALL_URL"
export CALL_URL

log "Running recorder + rotating publishers (mute coverage)"
# A transient WebRTC ICE flake fails one capture attempt but a fresh negotiation
# usually connects; a real regression fails all attempts (run_with_retries). The
# D-444 readiness gate inside e2e_with_publisher.sh turns a missed participant
# into a clean non-zero exit, which this retries.
run_recorder_publisher() { ( cd "$REPO_ROOT/cassini-go-recorder" && ./e2e_with_publisher.sh ); }
run_with_retries run_recorder_publisher

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

# Per-bot publisher-health gate.
#
# A single "media stats:" line proves only that *one* publisher reached its media
# pipeline; it cannot tell a sporadic load flake (some publisher dropped out) from
# a systematic regression. Require every launched bot to log its own media stats so
# a publisher-side dropout is attributed to the specific bot instead of silently
# passing on another bot's output.
#
# Bot names mirror e2e_with_publisher.sh, which calls stream-video.sh with
# --name-prefix "CassiniGoE2E" and no --names, so bots are CassiniGoE2E1..N.
PUB_NAME_PREFIX="${PUB_NAME_PREFIX:-CassiniGoE2E}"
missing_pub_health=()
for ((bot_idx = 1; bot_idx <= PUB_USERS; bot_idx++)); do
  bot_name="${PUB_NAME_PREFIX}${bot_idx}"
  # Match the rotator's per-bot prefix "[bot NN <name>] ... media stats:".
  if ! rg -q "\[bot [0-9]+ ${bot_name}\] media stats: " "$PUB_LOG"; then
    missing_pub_health+=("$bot_name")
  fi
done
if (( ${#missing_pub_health[@]} > 0 )); then
  log "Publisher media stats missing for bot(s): ${missing_pub_health[*]} (expected all ${PUB_USERS} publishers to send media)"
  exit 1
fi

# Per-run capture-funnel summary (always emitted before the count assertions).
#
# Surfaces the subscriber funnel from the recorder log so a systematic
# "always got 1" regression is distinct from a sporadic CI-load flake:
#   subscribers attempted -> ICE-connected -> tracks captured -> rtplogs written.
# Counts are diagnostic-only and never fail the run; the assertions below own
# pass/fail. Recorder log strings: "subscribing to remote session <sid>",
# "subscriber <sid> ICE state=connected", "remote track: sid=<sid> ...".
SUBS_ATTEMPTED=0
SUBS_ICE_CONNECTED=0
TRACKS_CAPTURED=0
SUMMARY_RTPLOGS=0
if [[ -f "$REC_LOG" ]]; then
  # rg exits 1 on no match; under `set -euo pipefail` an unguarded pipeline
  # would abort the script — and this summary must survive the very failure it
  # diagnoses (zero subscribers/tracks). Guard each so it reports 0, not aborts.
  SUBS_ATTEMPTED="$(rg -c "subscribing to remote session " "$REC_LOG" || echo 0)"
  # Distinct sessions that reached ICE-connected (a session can log the
  # transition more than once across renegotiations).
  SUBS_ICE_CONNECTED="$(rg -o "subscriber \S+ ICE state=connected" "$REC_LOG" 2>/dev/null \
    | awk '{print $2}' | sort -u | awk 'NF{n++} END {print n+0}' || true)"
  # Distinct sessions that produced at least one remote track.
  TRACKS_CAPTURED="$(rg -o "remote track: sid=\S+" "$REC_LOG" 2>/dev/null \
    | sed 's/^remote track: sid=//' | sort -u | awk 'NF{n++} END {print n+0}' || true)"
fi
SUMMARY_STREAMS_DIR="$(cassini_streams_dir_from_mkv "$FINAL_OUTPUT" 2>/dev/null || true)"
if [[ -n "$SUMMARY_STREAMS_DIR" && -d "$SUMMARY_STREAMS_DIR" ]]; then
  SUMMARY_RTPLOGS="$(find "$SUMMARY_STREAMS_DIR" -type f -name '*.rtplog' | wc -l | tr -d ' ')"
fi
log "mute capture summary: publishers=${PUB_USERS} subscribers_attempted=${SUBS_ATTEMPTED} ice_connected=${SUBS_ICE_CONNECTED} tracks_captured=${TRACKS_CAPTURED} rtplogs=${SUMMARY_RTPLOGS}"

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
