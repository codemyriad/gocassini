#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

OUTPUT_PREFIX="${OUTPUT_PREFIX:-$MEDIA_DIR/sample}"
DURATION="${DURATION:-15}"
FORCE=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix)
      OUTPUT_PREFIX="$2"
      shift 2
      ;;
    --duration)
      DURATION="$2"
      shift 2
      ;;
    --force)
      FORCE=1
      shift
      ;;
    *)
      # Backward-compatible positional prefix.
      OUTPUT_PREFIX="$1"
      shift
      ;;
  esac
done

if ! command -v ffmpeg >/dev/null 2>&1; then
  echo "ffmpeg is required to generate sample media" >&2
  exit 1
fi

mkdir -p "$(dirname "$OUTPUT_PREFIX")"

SAMPLE_MP4="${OUTPUT_PREFIX}.mp4"
SAMPLE_IVF="${OUTPUT_PREFIX}.ivf"
SAMPLE_OGG="${OUTPUT_PREFIX}.ogg"
SAMPLE_ULAW="${OUTPUT_PREFIX}.ulaw"

if [[ "$FORCE" -eq 0 && -f "$SAMPLE_MP4" && -f "$SAMPLE_IVF" && -f "$SAMPLE_OGG" && -f "$SAMPLE_ULAW" ]]; then
  log "Using cached sample assets: ${OUTPUT_PREFIX}.*"
  echo "$OUTPUT_PREFIX"
  exit 0
fi

log "Generating sample MP4: $SAMPLE_MP4"
ffmpeg -y -v error \
  -f lavfi -i "testsrc=size=1280x720:rate=30" \
  -f lavfi -i "sine=frequency=440:sample_rate=48000" \
  -t "$DURATION" \
  -c:v libx264 -pix_fmt yuv420p \
  -c:a aac -b:a 128k \
  "$SAMPLE_MP4"

log "Rendering IVF video: $SAMPLE_IVF"
# -g 15: a keyframe at least every 0.5s (30fps). The rotator drops video
# frames in real time until the subscriber binds, and the recorder can only
# decode from the next keyframe after binding -- with libvpx's default
# 128-frame interval that skews video start up to ~4.3s behind audio, which
# verify-av-drift.sh (elapsed-difference metric) reads as drift. Dense
# keyframes keep the structural skew well inside the 0.8s drift tolerance.
ffmpeg -y -v error \
  -i "$SAMPLE_MP4" \
  -an \
  -c:v libvpx \
  -b:v 1800k \
  -g 15 \
  -deadline realtime \
  -cpu-used 5 \
  -f ivf \
  "$SAMPLE_IVF"

log "Rendering OGG audio: $SAMPLE_OGG"
ffmpeg -y -v error \
  -i "$SAMPLE_MP4" \
  -vn \
  -c:a libopus \
  -b:a 128k \
  -ac 2 \
  -ar 48000 \
  -f ogg \
  "$SAMPLE_OGG"

log "Rendering ULAW audio: $SAMPLE_ULAW"
ffmpeg -y -v error \
  -i "$SAMPLE_MP4" \
  -vn \
  -ac 1 \
  -ar 8000 \
  -c:a pcm_mulaw \
  -f mulaw \
  "$SAMPLE_ULAW"

echo "$OUTPUT_PREFIX"
