#!/usr/bin/env bash
set -euo pipefail

INPUT=""
TAIL_SEC="${TAIL_SEC:-12}"
MAX_SHORTFALL_SEC="${MAX_SHORTFALL_SEC:-5}"
MIN_MEAN_DB="${MIN_MEAN_DB:--55}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --input)
      INPUT="$2"
      shift 2
      ;;
    --tail-sec)
      TAIL_SEC="$2"
      shift 2
      ;;
    --max-shortfall-sec)
      MAX_SHORTFALL_SEC="$2"
      shift 2
      ;;
    --min-mean-db)
      MIN_MEAN_DB="$2"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "$INPUT" ]]; then
  echo "missing --input <video-file>" >&2
  exit 1
fi
if [[ ! -f "$INPUT" ]]; then
  echo "input not found: $INPUT" >&2
  exit 1
fi

for cmd in ffprobe ffmpeg awk; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "$cmd is required" >&2
    exit 1
  fi
done

VIDEO_DURATION="$(ffprobe -v error -select_streams v:0 -show_entries stream=duration -of default=noprint_wrappers=1:nokey=1 "$INPUT" | head -n1)"
AUDIO_DURATION="$(ffprobe -v error -select_streams a:0 -show_entries stream=duration -of default=noprint_wrappers=1:nokey=1 "$INPUT" | head -n1)"
if [[ -z "$VIDEO_DURATION" || -z "$AUDIO_DURATION" || "$VIDEO_DURATION" == "N/A" || "$AUDIO_DURATION" == "N/A" ]]; then
  echo "could not determine stream durations for $INPUT" >&2
  exit 1
fi

SHORTFALL="$(awk -v v="$VIDEO_DURATION" -v a="$AUDIO_DURATION" 'BEGIN { d=v-a; if (d < 0) d=0; printf "%.6f", d }')"
if ! awk -v s="$SHORTFALL" -v max="$MAX_SHORTFALL_SEC" 'BEGIN { exit !(s <= max) }'; then
  echo "audio ends too early: video=${VIDEO_DURATION}s audio=${AUDIO_DURATION}s shortfall=${SHORTFALL}s (max=${MAX_SHORTFALL_SEC}s)" >&2
  exit 1
fi

START_AT="$(awk -v v="$VIDEO_DURATION" -v t="$TAIL_SEC" 'BEGIN { s=v-t; if (s < 0) s=0; printf "%.6f", s }')"
TAIL_LOG="$(mktemp)"
cleanup() {
  rm -f "$TAIL_LOG"
}
trap cleanup EXIT

ffmpeg -v info -i "$INPUT" \
  -vn \
  -af "atrim=start=${START_AT}:end=${VIDEO_DURATION},volumedetect" \
  -f null - > /dev/null 2>"$TAIL_LOG" || true

SAMPLES="$(sed -n 's/.*n_samples: \([0-9]\+\).*/\1/p' "$TAIL_LOG" | tail -n1)"
MEAN_DB="$(sed -n 's/.*mean_volume: \([-0-9.]\+\) dB.*/\1/p' "$TAIL_LOG" | tail -n1)"

if [[ -z "$SAMPLES" || "$SAMPLES" == "0" || -z "$MEAN_DB" ]]; then
  echo "no decodable tail audio samples found in last ${TAIL_SEC}s for $INPUT" >&2
  exit 1
fi
if ! awk -v m="$MEAN_DB" -v min="$MIN_MEAN_DB" 'BEGIN { exit !(m > min) }'; then
  echo "tail audio too quiet: mean_volume=${MEAN_DB}dB threshold=${MIN_MEAN_DB}dB" >&2
  exit 1
fi

echo "tail audio ok: video=${VIDEO_DURATION}s audio=${AUDIO_DURATION}s shortfall=${SHORTFALL}s mean_volume=${MEAN_DB}dB"
