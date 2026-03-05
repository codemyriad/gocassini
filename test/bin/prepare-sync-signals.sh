#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

USERS="${USERS:-3}"
DURATION="${DURATION:-36}"
TURN_SECONDS="${TURN_SECONDS:-2.8}"
FIRST_EVENT_SEC="${FIRST_EVENT_SEC:-4.0}"
CHIRP_SEC="${CHIRP_SEC:-0.35}"
NAME_PREFIX="${NAME_PREFIX:-SyncBot}"
NAMES="${NAMES:-}"
JOIN_DELAYS="${JOIN_DELAYS:-}"
ACTIVE_DURATIONS="${ACTIVE_DURATIONS:-}"
OUTPUT_DIR="${OUTPUT_DIR:-$MEDIA_DIR/sync-signals}"
MANIFEST="${MANIFEST:-$OUTPUT_DIR/signals.json}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --users)
      USERS="$2"
      shift 2
      ;;
    --duration)
      DURATION="$2"
      shift 2
      ;;
    --turn-seconds)
      TURN_SECONDS="$2"
      shift 2
      ;;
    --first-event-sec)
      FIRST_EVENT_SEC="$2"
      shift 2
      ;;
    --chirp-sec)
      CHIRP_SEC="$2"
      shift 2
      ;;
    --name-prefix)
      NAME_PREFIX="$2"
      shift 2
      ;;
    --names)
      NAMES="$2"
      shift 2
      ;;
    --join-delays)
      JOIN_DELAYS="$2"
      shift 2
      ;;
    --active-durations)
      ACTIVE_DURATIONS="$2"
      shift 2
      ;;
    --output-dir)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    --manifest)
      MANIFEST="$2"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if ! [[ "$USERS" =~ ^[0-9]+$ ]] || (( USERS < 1 || USERS > 3 )); then
  echo "--users must be an integer between 1 and 3" >&2
  exit 1
fi

for cmd in ffmpeg python3; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "$cmd is required" >&2
    exit 1
  fi
done

mkdir -p "$OUTPUT_DIR"
MANIFEST_DIR="$(dirname "$MANIFEST")"
mkdir -p "$MANIFEST_DIR"

frequencies=(523.25 659.25 783.99)
colors=("#cf3f3f" "#3fa25b" "#3f66c7")

period="$(awk -v users="$USERS" -v turn="$TURN_SECONDS" 'BEGIN { printf "%.6f", users*turn }')"

split_csv_into() {
  local csv="$1"
  local -n out_ref="$2"
  out_ref=()
  if [[ -z "$csv" ]]; then
    return 0
  fi
  IFS=',' read -r -a values <<<"$csv"
  for value in "${values[@]}"; do
    trimmed="$(echo "$value" | xargs)"
    if [[ -n "$trimmed" ]]; then
      out_ref+=("$trimmed")
    fi
  done
}

declare -a NAME_LIST=()
declare -a JOIN_DELAY_LIST=()
declare -a ACTIVE_DURATION_LIST=()
split_csv_into "$NAMES" NAME_LIST
split_csv_into "$JOIN_DELAYS" JOIN_DELAY_LIST
split_csv_into "$ACTIVE_DURATIONS" ACTIVE_DURATION_LIST

for pair in \
  "names:${#NAME_LIST[@]}" \
  "join-delays:${#JOIN_DELAY_LIST[@]}" \
  "active-durations:${#ACTIVE_DURATION_LIST[@]}"; do
  key="${pair%%:*}"
  count="${pair##*:}"
  if (( count > USERS )); then
    echo "--$key count ($count) exceeds --users ($USERS)" >&2
    exit 1
  fi
done

prefixes=()
for ((i = 1; i <= USERS; i++)); do
  freq="${frequencies[$((i - 1))]}"
  color="${colors[$((i - 1))]}"
  join_delay="0"
  if (( ${#JOIN_DELAY_LIST[@]} >= i )); then
    join_delay="${JOIN_DELAY_LIST[$((i - 1))]}"
  fi
  phase="$(awk -v first="$FIRST_EVENT_SEC" -v join="$join_delay" 'BEGIN { printf "%.6f", first + join }')"

  prefix="$OUTPUT_DIR/user${i}"
  video_file="${prefix}.ivf"
  audio_file="${prefix}.ogg"

  log "Generating sync media user=$i freq=${freq}Hz color=${color} phase=${phase}s"

  ffmpeg -y -v error \
    -f lavfi -i "color=c=${color}:size=960x540:rate=30:duration=${DURATION}" \
    -vf "drawbox=x='mod(t*120,iw-120)':y='mod(t*80,ih-90)':w=120:h=90:color=white@0.25:t=fill,drawbox=x=0:y=0:w=iw:h=ih:color=white@0.85:t=fill:enable='lt(mod(t+${period}-${phase},${period}),${CHIRP_SEC})'" \
    -an \
    -c:v libvpx \
    -b:v 1300k \
    -deadline realtime \
    -cpu-used 5 \
    -f ivf \
    "$video_file"

  ffmpeg -y -v error \
    -f lavfi -i "aevalsrc=0.8*sin(2*PI*${freq}*t)*if(lt(mod(t+${period}-${phase}\,${period})\,${CHIRP_SEC})\,1\,0):s=48000:d=${DURATION}" \
    -c:a libopus \
    -b:a 96k \
    -ac 1 \
    -ar 48000 \
    -f ogg \
    "$audio_file"

  prefixes+=("$prefix")
done

prefixes_csv="$(IFS=,; echo "${prefixes[*]}")"
names_csv="$(IFS=,; echo "${NAME_LIST[*]}")"
join_delays_csv="$(IFS=,; echo "${JOIN_DELAY_LIST[*]}")"
active_durations_csv="$(IFS=,; echo "${ACTIVE_DURATION_LIST[*]}")"

python3 - "$MANIFEST" "$USERS" "$DURATION" "$TURN_SECONDS" "$FIRST_EVENT_SEC" "$CHIRP_SEC" "$NAME_PREFIX" "$prefixes_csv" "$names_csv" "$join_delays_csv" "$active_durations_csv" <<'PY'
import json
import sys

manifest_path = sys.argv[1]
users = int(sys.argv[2])
duration = float(sys.argv[3])
turn = float(sys.argv[4])
first = float(sys.argv[5])
chirp = float(sys.argv[6])
name_prefix = sys.argv[7]
prefixes = [item for item in sys.argv[8].split(",") if item]
names = [item for item in sys.argv[9].split(",") if item]
join_delays = [float(item) for item in sys.argv[10].split(",") if item]
active_durations = [float(item) for item in sys.argv[11].split(",") if item]

freqs = [523.25, 659.25, 783.99]
colors = ["#cf3f3f", "#3fa25b", "#3f66c7"]
period = users * turn

participants = []
all_events = []
for idx in range(users):
    join_delay = join_delays[idx] if idx < len(join_delays) else 0.0
    phase = first + join_delay
    active_end = duration
    if idx < len(active_durations) and active_durations[idx] > 0:
        active_end = min(duration, join_delay + active_durations[idx])

    events = []
    t = phase
    while t + chirp <= active_end:
        rounded = round(t, 3)
        events.append(rounded)
        all_events.append(rounded)
        t += period

    participants.append(
        {
            "id": f"syncbot{idx+1}",
            "name": names[idx] if idx < len(names) else f"{name_prefix}{idx+1}",
            "frequency_hz": freqs[idx],
            "color": colors[idx],
            "phase_sec": round(phase, 3),
            "period_sec": round(period, 3),
            "join_delay_sec": round(join_delay, 3),
            "active_end_sec": round(active_end, 3),
            "event_times_sec": events,
            "media_prefix": prefixes[idx] if idx < len(prefixes) else "",
        }
    )

payload = {
    "version": 1,
    "duration_sec": duration,
    "turn_seconds": turn,
    "first_event_sec": first,
    "chirp_sec": chirp,
    "participants": participants,
    "all_event_times_sec": sorted(set(all_events)),
}

with open(manifest_path, "w", encoding="utf-8") as fh:
    json.dump(payload, fh, indent=2)

print(manifest_path)
PY

echo "$prefixes_csv" >"$OUTPUT_DIR/media_prefixes.csv"
log "sync manifest: $MANIFEST"
log "media prefixes csv: $OUTPUT_DIR/media_prefixes.csv"
