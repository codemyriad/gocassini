#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

USERS="${USERS:-3}"
DURATION="${DURATION:-90}"
TURN_SECONDS="${TURN_SECONDS:-3.1}"
FIRST_EVENT_SEC="${FIRST_EVENT_SEC:-4.0}"
CHIRP_SEC="${CHIRP_SEC:-0.35}"
JOIN_DELAYS="${JOIN_DELAYS:-0,8,20}"
ACTIVE_DURATIONS="${ACTIVE_DURATIONS:-90,38,24}"
MAX_RSS_MB="${MAX_RSS_MB:-4096}"
RUN_ENVELOPE_CHECK="${RUN_ENVELOPE_CHECK:-0}"

RUN_DIR="/tmp/gocassini-sync-composition-$(date -u +%Y%m%dT%H%M%S)-$$"
mkdir -p "$RUN_DIR"

MANIFEST="$RUN_DIR/signals.json"
SOURCE_MKV="$RUN_DIR/synthetic.multitrack.mkv"
COMPOSED_MP4="$RUN_DIR/synthetic.composed.mp4"

log "Preparing deterministic sync signals fixture in $RUN_DIR"
OUTPUT_DIR="$RUN_DIR" \
MANIFEST="$MANIFEST" \
USERS="$USERS" \
DURATION="$DURATION" \
TURN_SECONDS="$TURN_SECONDS" \
FIRST_EVENT_SEC="$FIRST_EVENT_SEC" \
CHIRP_SEC="$CHIRP_SEC" \
JOIN_DELAYS="$JOIN_DELAYS" \
ACTIVE_DURATIONS="$ACTIVE_DURATIONS" \
"$SCRIPT_DIR/prepare-sync-signals.sh"

ffmpeg_inputs=()
ffmpeg_maps=()

for ((i = 1; i <= USERS; i++)); do
  video_input="$RUN_DIR/user${i}.ivf"
  audio_input="$RUN_DIR/user${i}.ogg"
  if [[ ! -f "$video_input" || ! -f "$audio_input" ]]; then
    echo "missing fixture media for user ${i}" >&2
    exit 1
  fi
  ffmpeg_inputs+=(-i "$video_input" -i "$audio_input")
  ffmpeg_maps+=(-map "$((2 * (i - 1))):v:0" -map "$((2 * (i - 1) + 1)):a:0")
done

log "Muxing synthetic multitrack MKV"
ffmpeg -y -v error \
  "${ffmpeg_inputs[@]}" \
  "${ffmpeg_maps[@]}" \
  -c copy \
  "$SOURCE_MKV"

log "Verifying synthetic source MKV sync markers"
python3 "$SCRIPT_DIR/verify-sync-signals.py" \
  --input "$SOURCE_MKV" \
  --manifest "$MANIFEST" \
  --mode multitrack

log "Composing synthetic multitrack MKV"
"$SCRIPT_DIR/compose-recording.sh" \
  --input "$SOURCE_MKV" \
  --output "$COMPOSED_MP4" \
  --preview \
  --max-rss-mb "$MAX_RSS_MB"

log "Verifying composed sync markers"
python3 "$SCRIPT_DIR/verify-sync-signals.py" \
  --input "$COMPOSED_MP4" \
  --manifest "$MANIFEST" \
  --mode composed

if [[ "$RUN_ENVELOPE_CHECK" == "1" ]]; then
  log "Running envelope correlation sync check"
  uv run --script "$SCRIPT_DIR/verify-composed-sync.py" \
    --source "$SOURCE_MKV" \
    --composed "$COMPOSED_MP4" \
    --max-abs-lag-sec 0.75 \
    --max-drift-sec 0.75 \
    --min-corr 0.30
fi

log "Sync composition gate passed"
log "Artifacts: source=$SOURCE_MKV composed=$COMPOSED_MP4 manifest=$MANIFEST"
