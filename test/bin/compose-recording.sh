#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

INPUT=""
OUTPUT=""
PUBLISHER_LOG="${PUBLISHER_LOG:-}"
GIULIA_PATTERN="${GIULIA_PATTERN:-giulia}"
SPALMAN_PATTERN="${SPALMAN_PATTERN:-spalman|elio}"
FRANKIE_PATTERN="${FRANKIE_PATTERN:-frankie|chiedi}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --input)
      INPUT="$2"
      shift 2
      ;;
    --output)
      OUTPUT="$2"
      shift 2
      ;;
    --publisher-log)
      PUBLISHER_LOG="$2"
      shift 2
      ;;
    --pattern-giulia)
      GIULIA_PATTERN="$2"
      shift 2
      ;;
    --pattern-spalman)
      SPALMAN_PATTERN="$2"
      shift 2
      ;;
    --pattern-frankie)
      FRANKIE_PATTERN="$2"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "$INPUT" ]]; then
  echo "missing --input <recording.mkv>" >&2
  exit 1
fi
if [[ ! -f "$INPUT" ]]; then
  echo "input not found: $INPUT" >&2
  exit 1
fi
if [[ -z "$OUTPUT" ]]; then
  stem="${INPUT%.*}"
  OUTPUT="${stem}.composed.mp4"
fi
if [[ -z "$PUBLISHER_LOG" ]]; then
  candidate="${INPUT}.publisher.log"
  if [[ -f "$candidate" ]]; then
    PUBLISHER_LOG="$candidate"
  fi
fi

for cmd in ffprobe ffmpeg jq; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "$cmd is required" >&2
    exit 1
  fi
done

META="$(mktemp)"
cleanup() {
  rm -f "$META"
}
trap cleanup EXIT

ffprobe -v error -show_streams -show_format -of json "$INPUT" >"$META"

mapfile -t VIDEO_STREAMS < <(jq -r '.streams[] | select(.codec_type=="video") | .index' "$META")
mapfile -t AUDIO_STREAMS < <(jq -r '.streams[] | select(.codec_type=="audio") | .index' "$META")

if [[ "${#VIDEO_STREAMS[@]}" -lt 3 ]]; then
  echo "expected at least 3 video streams in recording, found ${#VIDEO_STREAMS[@]}" >&2
  exit 1
fi
if [[ "${#AUDIO_STREAMS[@]}" -lt 3 ]]; then
  echo "expected at least 3 audio streams in recording, found ${#AUDIO_STREAMS[@]}" >&2
  exit 1
fi

pick_stream() {
  local codec_type="$1"
  local pattern="$2"
  local fallback="$3"
  local picked
  picked="$(jq -r \
    --arg codec_type "$codec_type" \
    --arg pattern "$pattern" \
    '[.streams[]
      | select(.codec_type == $codec_type)
      | select((((.tags.PARTICIPANT_NAME // .tags.participant_name // .tags.title // "") | tostring) | test($pattern; "i")))
      | .index][0] // empty' "$META")"
  if [[ -n "$picked" ]]; then
    echo "$picked"
  else
    echo "$fallback"
  fi
}

V_SPALMAN="$(pick_stream "video" "$SPALMAN_PATTERN" "${VIDEO_STREAMS[0]}")"
V_GIULIA="$(pick_stream "video" "$GIULIA_PATTERN" "${VIDEO_STREAMS[1]}")"
V_FRANKIE="$(pick_stream "video" "$FRANKIE_PATTERN" "${VIDEO_STREAMS[2]}")"

A_SPALMAN="$(pick_stream "audio" "$SPALMAN_PATTERN" "${AUDIO_STREAMS[0]}")"
A_GIULIA="$(pick_stream "audio" "$GIULIA_PATTERN" "${AUDIO_STREAMS[1]}")"
A_FRANKIE="$(pick_stream "audio" "$FRANKIE_PATTERN" "${AUDIO_STREAMS[2]}")"

log "Composing recording from streams:"
log "  video: spalman=$V_SPALMAN giulia=$V_GIULIA frankie=$V_FRANKIE"
log "  audio: spalman=$A_SPALMAN giulia=$A_GIULIA frankie=$A_FRANKIE"

TARGET_DURATION="$(jq -r '
  (
    [ .streams[] | (.duration? // empty) | tonumber? ] | max
  ) // (
    .format.duration? | tonumber?
  ) // 0
' "$META")"
if ! awk -v d="$TARGET_DURATION" 'BEGIN { exit !(d > 0) }'; then
  echo "could not determine positive target duration from input metadata" >&2
  exit 1
fi
TARGET_DURATION_PADDED="$(awk -v d="$TARGET_DURATION" 'BEGIN { printf "%.3f", d + 1.0 }')"
log "  target duration: ${TARGET_DURATION_PADDED}s"

name_to_key() {
  local raw="$1"
  if echo "$raw" | grep -Eiq "$SPALMAN_PATTERN"; then
    echo "spalman"
    return
  fi
  if echo "$raw" | grep -Eiq "$GIULIA_PATTERN"; then
    echo "giulia"
    return
  fi
  if echo "$raw" | grep -Eiq "$FRANKIE_PATTERN"; then
    echo "frankie"
    return
  fi
  echo ""
}

declare -a AUDIO_ROTATION_KEYS=()
declare -a AUDIO_ROTATION_STARTS=()
declare -a AUDIO_ROTATION_ENDS=()
ROTATION_MODE="fallback-amix"
if [[ -n "$PUBLISHER_LOG" && -f "$PUBLISHER_LOG" ]]; then
  mapfile -t AUDIBLE_LINES < <(rg -N '^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} \[manager\] audible=' "$PUBLISHER_LOG" || true)
  if [[ "${#AUDIBLE_LINES[@]}" -gt 0 ]]; then
    declare -a EVENT_TIMES=()
    declare -a EVENT_KEYS=()
    HAVE_MEDIA_T=1
    for line in "${AUDIBLE_LINES[@]}"; do
      media_t="$(printf '%s\n' "$line" | sed -n 's/.* media_t=\([0-9.][0-9.]*\).*/\1/p')"
      if [[ -z "$media_t" ]]; then
        HAVE_MEDIA_T=0
        break
      fi
    done

    if [[ "$HAVE_MEDIA_T" == "1" ]]; then
      for line in "${AUDIBLE_LINES[@]}"; do
        media_t="$(printf '%s\n' "$line" | sed -n 's/.* media_t=\([0-9.][0-9.]*\).*/\1/p')"
        if [[ -z "$media_t" ]]; then
          continue
        fi
        name="${line#*audible=}"
        name="${name%% active=*}"
        key="$(name_to_key "$name")"
        if [[ -z "$key" ]]; then
          continue
        fi
        EVENT_TIMES+=("$media_t")
        EVENT_KEYS+=("$key")
      done

      for ((i = 0; i < ${#EVENT_TIMES[@]}; i++)); do
        start="${EVENT_TIMES[$i]}"
        if (( i + 1 < ${#EVENT_TIMES[@]} )); then
          end="${EVENT_TIMES[$((i + 1))]}"
        else
          end="$TARGET_DURATION_PADDED"
        fi
        dur="$(awk -v a="$start" -v b="$end" 'BEGIN { printf "%.6f", b - a }')"
        if ! awk -v d="$dur" 'BEGIN { exit !(d > 0.001) }'; then
          continue
        fi
        AUDIO_ROTATION_KEYS+=("${EVENT_KEYS[$i]}")
        AUDIO_ROTATION_STARTS+=("$start")
        AUDIO_ROTATION_ENDS+=("$end")
      done
    else
      BASE_TS="$(rg -N -m1 '^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}' "$PUBLISHER_LOG" | awk '{print $1" "$2}')"
      BASE_EPOCH="$(date -ud "${BASE_TS//\//-}" +%s 2>/dev/null || true)"
      if [[ -n "$BASE_EPOCH" ]]; then
        for line in "${AUDIBLE_LINES[@]}"; do
          ts="${line:0:19}"
          epoch="$(date -ud "${ts//\//-}" +%s 2>/dev/null || true)"
          if [[ -z "$epoch" ]]; then
            continue
          fi
          rel="$((epoch - BASE_EPOCH))"
          if (( rel < 0 )); then
            continue
          fi
          name="${line#*audible=}"
          name="${name%% active=*}"
          key="$(name_to_key "$name")"
          if [[ -z "$key" ]]; then
            continue
          fi
          EVENT_TIMES+=("$rel")
          EVENT_KEYS+=("$key")
        done

        if [[ "${#EVENT_TIMES[@]}" -gt 0 ]]; then
          END_TS="$(rg -N -m1 '^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} \[manager\] all bots completed' "$PUBLISHER_LOG" | awk '{print $1" "$2}')"
          if [[ -z "$END_TS" ]]; then
            last_idx=$(( ${#AUDIBLE_LINES[@]} - 1 ))
            END_TS="${AUDIBLE_LINES[$last_idx]:0:19}"
          fi
          END_EPOCH="$(date -ud "${END_TS//\//-}" +%s 2>/dev/null || true)"
          if [[ -n "$END_EPOCH" && "$END_EPOCH" -gt "$BASE_EPOCH" ]]; then
            WALL_TOTAL="$((END_EPOCH - BASE_EPOCH))"
            SCALE="$(awk -v t="$TARGET_DURATION_PADDED" -v w="$WALL_TOTAL" 'BEGIN { if (w <= 0) print 1.0; else printf "%.9f", t / w }')"
            cursor="0.000000"
            for ((i = 0; i < ${#EVENT_TIMES[@]}; i++)); do
              start="${EVENT_TIMES[$i]}"
              if (( i + 1 < ${#EVENT_TIMES[@]} )); then
                end="${EVENT_TIMES[$((i + 1))]}"
              else
                end="$WALL_TOTAL"
              fi
              raw_dur="$(awk -v a="$start" -v b="$end" 'BEGIN { printf "%.6f", b - a }')"
              if ! awk -v d="$raw_dur" 'BEGIN { exit !(d > 0.001) }'; then
                continue
              fi
              dur="$(awk -v d="$raw_dur" -v s="$SCALE" 'BEGIN { printf "%.6f", d * s }')"
              seg_start="$cursor"
              seg_end="$(awk -v c="$cursor" -v d="$dur" 'BEGIN { printf "%.6f", c + d }')"
              AUDIO_ROTATION_KEYS+=("${EVENT_KEYS[$i]}")
              AUDIO_ROTATION_STARTS+=("$seg_start")
              AUDIO_ROTATION_ENDS+=("$seg_end")
              cursor="$seg_end"
            done
          fi
        fi
      fi
    fi
  fi
fi

if [[ "${#AUDIO_ROTATION_KEYS[@]}" -gt 0 ]]; then
  ROTATION_MODE="publisher-log-splice"
fi
log "  audio mode: ${ROTATION_MODE}"

if [[ "$ROTATION_MODE" == "publisher-log-splice" ]]; then
  off_spalman="0.000"
  off_giulia="0.000"
  off_frankie="0.000"
  seg_count=0
  seg_filter=""
  seg_labels=""

  for ((i = 0; i < ${#AUDIO_ROTATION_KEYS[@]}; i++)); do
    key="${AUDIO_ROTATION_KEYS[$i]}"
    start="${AUDIO_ROTATION_STARTS[$i]}"
    end="${AUDIO_ROTATION_ENDS[$i]}"
    dur="$(awk -v a="$start" -v b="$end" 'BEGIN { printf "%.3f", b - a }')"
    if ! awk -v d="$dur" 'BEGIN { exit !(d > 0.001) }'; then
      continue
    fi

    case "$key" in
      spalman)
        src="$A_SPALMAN"
        seg_start="$off_spalman"
        seg_end="$(awk -v s="$off_spalman" -v d="$dur" 'BEGIN { printf "%.3f", s + d }')"
        off_spalman="$seg_end"
        ;;
      giulia)
        src="$A_GIULIA"
        seg_start="$off_giulia"
        seg_end="$(awk -v s="$off_giulia" -v d="$dur" 'BEGIN { printf "%.3f", s + d }')"
        off_giulia="$seg_end"
        ;;
      frankie)
        src="$A_FRANKIE"
        seg_start="$off_frankie"
        seg_end="$(awk -v s="$off_frankie" -v d="$dur" 'BEGIN { printf "%.3f", s + d }')"
        off_frankie="$seg_end"
        ;;
      *)
        continue
        ;;
    esac

    label="aseg${seg_count}"
    seg_filter+="[0:${src}]atrim=start=${seg_start}:end=${seg_end},asetpts=PTS-STARTPTS[${label}];"$'\n'
    seg_labels+="[${label}]"
    seg_count=$((seg_count + 1))
  done

  if [[ "$seg_count" -gt 0 ]]; then
    AUDIO_FILTER="
${seg_filter}${seg_labels}concat=n=${seg_count}:v=0:a=1,aresample=async=1:first_pts=0,alimiter=limit=0.95[aout]
"
  else
    log "  no valid rotation segments parsed from publisher log, falling back to amix"
    AUDIO_FILTER="
[0:${A_SPALMAN}][0:${A_GIULIA}][0:${A_FRANKIE}]amix=inputs=3:dropout_transition=0:normalize=0,aresample=async=1:first_pts=0,alimiter=limit=0.95[aout]
"
  fi
else
  AUDIO_FILTER="
[0:${A_SPALMAN}][0:${A_GIULIA}][0:${A_FRANKIE}]amix=inputs=3:dropout_transition=0:normalize=0,aresample=async=1:first_pts=0,alimiter=limit=0.95[aout]
"
fi

FILTER_COMPLEX="
[0:${V_SPALMAN}]scale=640:360:force_original_aspect_ratio=decrease,pad=640:360:(ow-iw)/2:(oh-ih)/2,setsar=1[v_spalman];
[0:${V_GIULIA}]scale=640:360:force_original_aspect_ratio=decrease,pad=640:360:(ow-iw)/2:(oh-ih)/2,setsar=1[v_giulia];
[0:${V_FRANKIE}]scale=640:360:force_original_aspect_ratio=decrease,pad=640:360:(ow-iw)/2:(oh-ih)/2,setsar=1[v_frankie];
color=c=black:s=1280x720:r=30[canvas];
[canvas][v_spalman]overlay=0:0[tmp1];
[tmp1][v_giulia]overlay=640:0[tmp2];
[tmp2][v_frankie]overlay=320:360[vout];
${AUDIO_FILTER}
"

mkdir -p "$(dirname "$OUTPUT")"

ffmpeg -y -v error \
  -i "$INPUT" \
  -filter_complex "$FILTER_COMPLEX" \
  -map "[vout]" \
  -map "[aout]" \
  -t "$TARGET_DURATION_PADDED" \
  -c:v libx264 -preset veryfast -crf 22 \
  -c:a aac -b:a 192k \
  -movflags +faststart \
  "$OUTPUT"

log "Composed file: $OUTPUT"
