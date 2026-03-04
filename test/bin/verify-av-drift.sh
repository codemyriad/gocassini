#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

INPUT=""
REPORT=""
TOLERANCE="${TOLERANCE:-0.80}"
MIN_ELAPSED="${MIN_ELAPSED:-15}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --input|--recording|--final-output)
      INPUT="$2"
      shift 2
      ;;
    --report)
      REPORT="$2"
      shift 2
      ;;
    --tolerance)
      TOLERANCE="$2"
      shift 2
      ;;
    --min-elapsed)
      MIN_ELAPSED="$2"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "$INPUT" ]]; then
  echo "missing --input <final.mkv>" >&2
  exit 1
fi
if [[ ! -f "$INPUT" ]]; then
  echo "recording not found: $INPUT" >&2
  exit 1
fi
if [[ -z "$REPORT" ]]; then
  REPORT="${INPUT}.json"
fi
if [[ ! -f "$REPORT" ]]; then
  echo "report not found: $REPORT" >&2
  exit 1
fi

for cmd in jq ffprobe awk python3; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "$cmd is required" >&2
    exit 1
  fi
done

PAIR_FILE="$(mktemp)"
cleanup() {
  rm -f "$PAIR_FILE"
}
trap cleanup EXIT

python3 - "$REPORT" >"$PAIR_FILE" <<'PY'
import json
import re
import sys

report = json.load(open(sys.argv[1], "r", encoding="utf-8"))
plans = ((report.get("artifact_remux") or {}).get("stream_plans") or [])
by_group = {}

def group_key(ltid: str) -> str:
    if ":audio:" in ltid:
        return ltid.replace(":audio:", ":media:", 1)
    if ":video:" in ltid:
        return ltid.replace(":video:", ":media:", 1)
    # Fallback for future LTID formats.
    return re.sub(r":(audio|video):", ":media:", ltid, count=1)

for plan in plans:
    if not isinstance(plan, dict):
        continue
    ltid = str(plan.get("ltid") or "").strip()
    kind = str(plan.get("kind") or "").strip().lower()
    stream_id = str(plan.get("stream_id") or "").strip()
    if not ltid or not kind or not stream_id:
        continue
    group = group_key(ltid)
    entry = by_group.setdefault(group, {})
    entry[kind] = stream_id
    entry.setdefault("ltid_audio", "")
    entry.setdefault("ltid_video", "")
    entry[f"ltid_{kind}"] = ltid

for group, entry in sorted(by_group.items()):
    if "audio" in entry and "video" in entry:
        ltid_label = entry.get("ltid_video") or entry.get("ltid_audio") or group
        print(f"{ltid_label}\t{entry['audio']}\t{entry['video']}")
PY

mapfile -t PAIRS <"$PAIR_FILE"
if [[ "${#PAIRS[@]}" -eq 0 ]]; then
  log "av drift check skipped: no audio/video pairs found in artifact_remux.stream_plans"
  exit 0
fi

META_JSON="$(mktemp)"
trap 'rm -f "$PAIR_FILE" "$META_JSON"' EXIT
ffprobe -v error -show_streams -of json "$INPUT" >"$META_JSON"

stream_index_for_id() {
  local stream_id="$1"
  jq -r --arg sid "$stream_id" '
    .streams[]
    | select((.tags.stream_id // .tags.STREAM_ID // "") == $sid)
    | .index
  ' "$META_JSON" | head -n1
}

first_last_audio() {
  local index="$1"
  ffprobe -v error \
    -select_streams "$index" \
    -show_entries packet=pts_time \
    -of csv=p=0 \
    "$INPUT" \
    | awk 'NR==1{first=$1} {last=$1} END {if (NR>0) printf "%.6f %.6f\n", first, last}'
}

first_last_video() {
  local index="$1"
  ffprobe -v error \
    -select_streams "$index" \
    -show_entries frame=best_effort_timestamp_time \
    -of csv=p=0 \
    "$INPUT" \
    | awk 'NR==1{first=$1} {last=$1} END {if (NR>0) printf "%.6f %.6f\n", first, last}'
}

FAILURES=0
log "av drift check: tolerance=${TOLERANCE}s min_elapsed=${MIN_ELAPSED}s pairs=${#PAIRS[@]}"
for row in "${PAIRS[@]}"; do
  IFS=$'\t' read -r LTID AUDIO_ID VIDEO_ID <<<"$row"

  AUDIO_IDX="$(stream_index_for_id "$AUDIO_ID" || true)"
  VIDEO_IDX="$(stream_index_for_id "$VIDEO_ID" || true)"
  if [[ -z "$AUDIO_IDX" || -z "$VIDEO_IDX" ]]; then
    echo "missing stream index for ltid=${LTID} audio_id=${AUDIO_ID} video_id=${VIDEO_ID}" >&2
    FAILURES=$((FAILURES + 1))
    continue
  fi

  AUDIO_FIRST_LAST="$(first_last_audio "$AUDIO_IDX" || true)"
  VIDEO_FIRST_LAST="$(first_last_video "$VIDEO_IDX" || true)"
  if [[ -z "$AUDIO_FIRST_LAST" || -z "$VIDEO_FIRST_LAST" ]]; then
    echo "missing timestamps for ltid=${LTID} audio_idx=${AUDIO_IDX} video_idx=${VIDEO_IDX}" >&2
    FAILURES=$((FAILURES + 1))
    continue
  fi

  AUDIO_FIRST="$(awk '{print $1}' <<<"$AUDIO_FIRST_LAST")"
  AUDIO_LAST="$(awk '{print $2}' <<<"$AUDIO_FIRST_LAST")"
  VIDEO_FIRST="$(awk '{print $1}' <<<"$VIDEO_FIRST_LAST")"
  VIDEO_LAST="$(awk '{print $2}' <<<"$VIDEO_FIRST_LAST")"

  AUDIO_ELAPSED="$(awk -v a="$AUDIO_LAST" -v b="$AUDIO_FIRST" 'BEGIN {printf "%.6f", a-b}')"
  VIDEO_ELAPSED="$(awk -v a="$VIDEO_LAST" -v b="$VIDEO_FIRST" 'BEGIN {printf "%.6f", a-b}')"
  MIN_PAIR_ELAPSED="$(awk -v a="$AUDIO_ELAPSED" -v b="$VIDEO_ELAPSED" 'BEGIN {if (a<b) print a; else print b}')"
  if awk -v m="$MIN_PAIR_ELAPSED" -v min="$MIN_ELAPSED" 'BEGIN { exit !(m < min) }'; then
    log "  ltid=${LTID} skipped (short pair elapsed=${MIN_PAIR_ELAPSED}s)"
    continue
  fi

  DRIFT="$(awk -v a="$VIDEO_ELAPSED" -v b="$AUDIO_ELAPSED" 'BEGIN {d=a-b; if (d<0) d=-d; printf "%.6f", d}')"
  WITHIN="$(awk -v d="$DRIFT" -v t="$TOLERANCE" 'BEGIN { if (d<=t) print "yes"; else print "no" }')"

  log "  ltid=${LTID} audio_elapsed=${AUDIO_ELAPSED}s video_elapsed=${VIDEO_ELAPSED}s abs_drift=${DRIFT}s"
  if [[ "$WITHIN" != "yes" ]]; then
    echo "av drift beyond tolerance for ltid=${LTID}: drift=${DRIFT}s > ${TOLERANCE}s" >&2
    FAILURES=$((FAILURES + 1))
  fi
done

if [[ "$FAILURES" -gt 0 ]]; then
  exit 1
fi

log "av drift check passed"
