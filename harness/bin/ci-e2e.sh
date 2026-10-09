#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./lib/e2e-local.sh
source "$SCRIPT_DIR/lib/e2e-local.sh"
harness_e2e_local_stack_env full legacy none
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

export PROJECT_NAME="${PROJECT_NAME:-gocassini-ci}"
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

export REC_DURATION="${REC_DURATION:-22}"
export PUB_DURATION="${PUB_DURATION:-18}"
export PUB_USERS="${PUB_USERS:-1}"
export CALL_NAME="${CALL_NAME:-CI Gocassini room}"

# The drift check below requires an A/V pair with >= 15s of measured overlap
# (--min-elapsed 15). Publishing is bounded by BOTH knobs: the bot stops at
# min(PUB_DURATION since stream-video.sh launch, end of its media fixture),
# and the bots spend ~17s joining before media flows, plus ~4s is lost to the
# video start offset and stream tails. REC_DURATION must outlast publisher
# start (START_DELAY=6) + PUB_DURATION with slack for the recorder tail.
if (( REC_DURATION < 60 )); then
  REC_DURATION=60
fi
if (( PUB_DURATION < 45 )); then
  PUB_DURATION=45
fi
export REC_DURATION PUB_DURATION

# Without an explicit fixture, stream-video.sh falls back to prepare-media.sh's
# default 15s sample, and the bot stops publishing when the fixture runs out --
# capping the measurable pair at ~10.9s no matter how large PUB_DURATION is
# (this is exactly what kept the pre-D-510 drift check silently skipping).
# Prepare a fixture sized to the full publish window, same pattern as
# ci-e2e-mute.sh.
PREPARE_E2E_MEDIA=0
if [[ -z "${MEDIA_PREFIX:-}" && -z "${MEDIA_PREFIXES:-}" ]]; then
  export E2E_MEDIA_PREFIX="${E2E_MEDIA_PREFIX:-$MEDIA_DIR/sample-e2e-drift}"
  export E2E_MEDIA_DURATION="${E2E_MEDIA_DURATION:-$PUB_DURATION}"
  PREPARE_E2E_MEDIA=1
fi

CI_OUTPUT_BASE="/tmp/gocassini-ci-$(date -u +%Y%m%dT%H%M%S)-$$"
export OUTPUT="${OUTPUT:-$CI_OUTPUT_BASE.mkv}"
export FINAL_OUTPUT="${FINAL_OUTPUT:-$OUTPUT}"
export REC_LOG="${REC_LOG:-/tmp/gocassini-ci-recorder.log}"
export PUB_LOG="${PUB_LOG:-/tmp/gocassini-ci-publisher.log}"

# Explicit stack topology for this e2e leg: local HTTP, full media services
# (nats/janus/signaling/coturn), no installed ExApp, legacy recording backend.
STACK_TOPOLOGY=(
  --public-mode local-http
  --services full
  --cassini none
  --recording-backend legacy
)

cleanup() {
  if [[ -n "${JANUS_MONITOR_PID:-}" ]] && kill -0 "$JANUS_MONITOR_PID" 2>/dev/null; then
    kill "$JANUS_MONITOR_PID"
    wait "$JANUS_MONITOR_PID" || true
  fi
  if [[ -n "${TONE_MEDIA_DIR:-}" ]]; then rm -rf "$TONE_MEDIA_DIR"; fi
  log "Cleaning up local test stack"
  "$REPO_ROOT/bin/cassini" dev stack down --volumes "${STACK_TOPOLOGY[@]}" || true
}

trap cleanup EXIT INT TERM

if [[ "$PREPARE_E2E_MEDIA" == "1" ]]; then
  log "Preparing e2e media fixture (${E2E_MEDIA_DURATION}s): $E2E_MEDIA_PREFIX"
  "$SCRIPT_DIR/prepare-media.sh" \
    --prefix "$E2E_MEDIA_PREFIX" \
    --duration "$E2E_MEDIA_DURATION" \
    --force
  export MEDIA_PREFIX="$E2E_MEDIA_PREFIX"
fi

log "Starting local Nextcloud Talk stack for CI"
# --reset: e2e wants a deterministic fresh stack; a leaked project from an
# earlier aborted run must not fail the bring-up guard.
"$REPO_ROOT/bin/cassini" dev stack up "${STACK_TOPOLOGY[@]}" --reset

log "Creating temporary room for CI capture"
CALL_URL="$(create_room_with_retry "$CALL_NAME")"
log "Test room URL: $CALL_URL"
export CALL_URL

log "Running recorder + publisher end-to-end"
(
  cd "$REPO_ROOT/cassini-go-recorder"
  RETAIN_VIDEO=true ./e2e_with_publisher.sh
)

# Tolerance budget: video decodes only from the first fixture keyframe after
# the subscriber binds (<= 0.5s with prepare-media.sh's -g 15) while audio
# decodes immediately, so up to ~0.5s of the elapsed difference is structural
# start skew, not drift; the rest covers bind/pacing jitter with margin. Real
# failures in this class (frozen or starved video) show up as tens of seconds.
"$SCRIPT_DIR/verify-av-drift.sh" \
  --input "$FINAL_OUTPUT" \
  --tolerance 1.5 \
  --min-elapsed 15

# The default recorder policy needs its own gate: no A/V pairs is expected,
# so inspect positive audio, zero video, and the SFU's outbound counters.
log "Checking default audio-only capture with camera publishers"
# Distinct frequencies identify each speaker through the mute rotations. Keep
# their cameras live and run long enough to compare early and late turns.
TONE_MEDIA_DIR="$(mktemp -d /tmp/cassini-capture-tones-XXXXXX)"
TONE_MEDIA_PREFIXES=""
for frequency in 440 880; do
  prefix="$TONE_MEDIA_DIR/$frequency"
  ffmpeg -y -v error -f lavfi -i "color=size=64x64:rate=30:duration=145" \
    -c:v libvpx -g 15 -deadline realtime -cpu-used 5 -f ivf "$prefix.ivf"
  ffmpeg -y -v error -f lavfi -i "sine=frequency=$frequency:sample_rate=48000:duration=145" \
    -c:a libopus -b:a 32k -application voip -frame_duration 20 -ac 1 "$prefix.ogg"
  TONE_MEDIA_PREFIXES="${TONE_MEDIA_PREFIXES:+$TONE_MEDIA_PREFIXES,}$prefix"
done
# A private one-to-one room needs both invited accounts authenticated.
AUDIO_AUTH_USERS="${AUTH_USERS:-}"
AUDIO_AUTH_PASSWORDS="${AUTH_PASSWORDS:-}"
if [[ "${ROOM_TYPE:-}" == "1" ]]; then
  AUDIO_AUTH_USERS="$BOT_USER,$ADMIN_USER"
  AUDIO_AUTH_PASSWORDS="$BOT_PASSWORD,$ADMIN_PASSWORD"
fi
AUDIO_OUTPUT="${FINAL_OUTPUT%.mkv}-audio-only.mkv"
JANUS_EVIDENCE="${AUDIO_OUTPUT%.mkv}-janus.json"
JANUS_READY="${JANUS_EVIDENCE}.ready"
python3 "$SCRIPT_DIR/monitor-audio-only-janus.py" --output "$JANUS_EVIDENCE" --ready-file "$JANUS_READY" &
JANUS_MONITOR_PID=$!
for ((i=0; i<40; i++)); do
  [[ -f "$JANUS_READY" ]] && break
  kill -0 "$JANUS_MONITOR_PID" 2>/dev/null || { echo "Janus monitor failed to start" >&2; exit 1; }
  sleep 0.25
done
[[ -f "$JANUS_READY" ]] || { echo "Janus monitor not ready" >&2; exit 1; }
(
  cd "$REPO_ROOT/cassini-go-recorder"
  RETAIN_VIDEO=false OUTPUT="$AUDIO_OUTPUT" FINAL_OUTPUT="$AUDIO_OUTPUT" \
    REC_LOG="${REC_LOG%.log}-audio-only.log" PUB_LOG="${PUB_LOG%.log}-audio-only.log" \
    REC_DURATION=160 PUB_DURATION=145 PUB_USERS=2 JOIN_DELAYS=0,6 \
    AUTH_USERS="$AUDIO_AUTH_USERS" AUTH_PASSWORDS="$AUDIO_AUTH_PASSWORDS" \
    AUDIO_TRACK_AFTERS=20,0 VIDEO_TRACK_AFTERS=0,20 \
    MEDIA_PREFIX="" MEDIA_PREFIXES="$TONE_MEDIA_PREFIXES" ./e2e_with_publisher.sh
)
rg -q 'adding delayed audio track' "${PUB_LOG%.log}-audio-only.log"
rg -q 'adding delayed video track' "${PUB_LOG%.log}-audio-only.log"
kill "$JANUS_MONITOR_PID"
wait "$JANUS_MONITOR_PID"
JANUS_MONITOR_PID=""
python3 - "$AUDIO_OUTPUT" <<'PY_AUDIO'
import json, pathlib, subprocess, sys
output = pathlib.Path(sys.argv[1])
streams = json.loads(subprocess.check_output(["ffprobe", "-v", "error", "-show_streams", "-of", "json", str(output)]))["streams"]
assert sum(s["codec_type"] == "audio" for s in streams) >= 2, "missing participant audio"
assert not any(s["codec_type"] == "video" for s in streams), "persisted video"
sessions = list((output.parent / "sessions").glob(output.stem + "_*/session.json"))
assert len(sessions) == 1, "missing/ambiguous capture artifact"
source = json.loads(sessions[0].read_text())
assert source["capture_mode"] == "audio-only"
assert source["logical_tracks"] and all(t["kind"] == "audio" for t in source["logical_tracks"])
assert len(source["packet_streams"]) >= 2
assert all(s["codec"].startswith("audio/") for s in source["packet_streams"])
assert len(list(sessions[0].parent.rglob("*.rtplog"))) == len(source["packet_streams"])
assert not any(p.suffix in {".ivf", ".h264", ".h265"} for p in sessions[0].parent.rglob("*"))
print("PASS: audio-only source and final MKV retain separate audio tracks, zero video")
PY_AUDIO
# Exercise the real portable build with transcription disabled and no models.
CASSINI_CACHE_ROOT="$TONE_MEDIA_DIR/empty-cache" CASSINI_DISALLOW_MODEL_DOWNLOAD=1 \
  "$REPO_ROOT/bin/cassini" build "$AUDIO_OUTPUT" --transcription off \
  --out "${AUDIO_OUTPUT%.mkv}.opus"
python3 "$SCRIPT_DIR/verify-capture-tone-timing.py" --source "$AUDIO_OUTPUT" \
  --published "${AUDIO_OUTPUT%.mkv}.opus" --publisher-log "${PUB_LOG%.log}-audio-only.log" \
  | tee "${AUDIO_OUTPUT%.mkv}-timing.json"
rm -rf "$TONE_MEDIA_DIR"
log "CI integration run complete"
