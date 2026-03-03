#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

RAW_DIR="${RAW_DIR:-$MEDIA_DIR/youtube/raw}"
SYNC_DIR="${SYNC_DIR:-$MEDIA_DIR/youtube/aligned}"
WEBRTC_DIR="${WEBRTC_DIR:-$MEDIA_DIR/youtube/webrtc}"
TARGET_HEIGHT="${TARGET_HEIGHT:-720}"
FORCE=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target-height)
      TARGET_HEIGHT="$2"
      shift 2
      ;;
    --force)
      FORCE=1
      shift
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if ! command -v ffmpeg >/dev/null 2>&1; then
  echo "ffmpeg is required for preparing aligned clips" >&2
  exit 1
fi

if ! command -v uvx >/dev/null 2>&1; then
  echo "uvx is required (used to run yt-dlp)" >&2
  exit 1
fi

mkdir -p "$RAW_DIR" "$SYNC_DIR"
mkdir -p "$WEBRTC_DIR"

# Sources
GIULIA_URL="https://www.youtube.com/watch?v=gvhycbsyX8k"
VIBRAZIONI_URL="https://www.youtube.com/watch?v=KBkThh0azrQ"
FRANKIE_URL="https://www.youtube.com/watch?v=ktNAzte2MmI"

# Alignment from provided anchors, without trimming:
# - Giulia 2:09 == Vibrazioni 2:16  -> Vibrazioni should start 7s earlier than Giulia
# - Giulia 0:42 == Frankie 0:44     -> Frankie should start 2s earlier than Giulia
#
# We cannot use negative delays, so we shift all tracks by +7s:
# - Vibrazioni delay: 0s
# - Frankie delay: 5s
# - Giulia delay: 7s
GIULIA_DELAY="7"
VIBRAZIONI_DELAY="0"
FRANKIE_DELAY="5"

download_if_missing() {
  local key="$1"
  local url="$2"
  local target_mp4="$RAW_DIR/${key}.mp4"
  if [[ "$FORCE" -eq 0 && -f "$target_mp4" ]]; then
    log "Using cached source: $target_mp4"
    return
  fi

  log "Downloading $key via uvx yt-dlp"
  rm -f "$RAW_DIR/${key}.mp4" "$RAW_DIR/${key}.mkv" "$RAW_DIR/${key}.webm"
  uvx yt-dlp \
    --no-playlist \
    --format "bv*[height<=${TARGET_HEIGHT}]+ba/b[height<=${TARGET_HEIGHT}]/best" \
    --merge-output-format mp4 \
    --remux-video mp4 \
    --output "$RAW_DIR/${key}.%(ext)s" \
    "$url"

  if [[ ! -f "$target_mp4" ]]; then
    echo "expected downloaded file not found: $target_mp4" >&2
    exit 1
  fi
}

make_aligned_clip() {
  local key="$1"
  local delay="$2"
  local source="$RAW_DIR/${key}.mp4"
  local output="$SYNC_DIR/${key}-aligned.mp4"
  local delay_ms
  delay_ms="$(awk -v v="$delay" 'BEGIN { printf "%d", (v * 1000) + 0.5 }')"

  if [[ ! -f "$source" ]]; then
    echo "source missing: $source" >&2
    exit 1
  fi

  if [[ "$FORCE" -eq 0 && -f "$output" ]]; then
    log "Using cached aligned clip: $output"
    return
  fi

  log "Rendering aligned clip: $output (start-delay=${delay}s)"
  ffmpeg -y -v error \
    -i "$source" \
    -vf "tpad=start_duration=${delay}:start_mode=add,scale='min(1280,iw)':'-2',fps=30,format=yuv420p" \
    -af "adelay=${delay_ms}|${delay_ms}" \
    -c:v libx264 -preset veryfast -crf 23 \
    -c:a aac -b:a 160k \
    -movflags +faststart \
    "$output"
}

make_webrtc_assets() {
  local key="$1"
  local aligned="$SYNC_DIR/${key}-aligned.mp4"
  local out_video="$WEBRTC_DIR/${key}-aligned.ivf"
  local out_audio_ogg="$WEBRTC_DIR/${key}-aligned.ogg"
  local out_audio_ulaw="$WEBRTC_DIR/${key}-aligned.ulaw"

  if [[ ! -f "$aligned" ]]; then
    echo "aligned source missing: $aligned" >&2
    exit 1
  fi

  if [[ "$FORCE" -eq 0 && -f "$out_video" ]]; then
    log "Using cached WebRTC video: $out_video"
  else
    log "Rendering WebRTC video: $out_video"
    ffmpeg -y -v error \
      -i "$aligned" \
      -an \
      -c:v libvpx \
      -b:v 2500k \
      -deadline realtime \
      -cpu-used 5 \
      -f ivf \
      "$out_video"
  fi

  if [[ "$FORCE" -eq 0 && -f "$out_audio_ogg" ]]; then
    log "Using cached WebRTC audio (ogg): $out_audio_ogg"
  else
    log "Rendering WebRTC audio (ogg): $out_audio_ogg"
    ffmpeg -y -v error \
      -i "$aligned" \
      -vn \
      -c:a libopus \
      -b:a 160k \
      -ac 2 \
      -ar 48000 \
      -f ogg \
      "$out_audio_ogg"
  fi

  if [[ "$FORCE" -eq 0 && -f "$out_audio_ulaw" ]]; then
    log "Using cached WebRTC audio (ulaw): $out_audio_ulaw"
  else
    log "Rendering WebRTC audio (ulaw): $out_audio_ulaw"
    ffmpeg -y -v error \
      -i "$aligned" \
      -vn \
      -ac 1 \
      -ar 8000 \
      -c:a pcm_mulaw \
      -f mulaw \
      "$out_audio_ulaw"
  fi
}

download_if_missing "giulia" "$GIULIA_URL"
download_if_missing "vibrazioni" "$VIBRAZIONI_URL"
download_if_missing "frankie" "$FRANKIE_URL"

make_aligned_clip "giulia" "$GIULIA_DELAY"
make_aligned_clip "vibrazioni" "$VIBRAZIONI_DELAY"
make_aligned_clip "frankie" "$FRANKIE_DELAY"

make_webrtc_assets "giulia"
make_webrtc_assets "vibrazioni"
make_webrtc_assets "frankie"

log "Prepared aligned clips:"
printf '%s\n' \
  "$SYNC_DIR/giulia-aligned.mp4" \
  "$SYNC_DIR/vibrazioni-aligned.mp4" \
  "$SYNC_DIR/frankie-aligned.mp4" \
  "$WEBRTC_DIR/giulia-aligned.ivf" \
  "$WEBRTC_DIR/giulia-aligned.ogg" \
  "$WEBRTC_DIR/giulia-aligned.ulaw" \
  "$WEBRTC_DIR/vibrazioni-aligned.ivf" \
  "$WEBRTC_DIR/vibrazioni-aligned.ogg" \
  "$WEBRTC_DIR/vibrazioni-aligned.ulaw" \
  "$WEBRTC_DIR/frankie-aligned.ivf" \
  "$WEBRTC_DIR/frankie-aligned.ogg" \
  "$WEBRTC_DIR/frankie-aligned.ulaw"
