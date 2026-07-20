#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

INPUT=""
REPORT=""
TOLERANCE="${TOLERANCE:-0.80}"
MIN_ELAPSED="${MIN_ELAPSED:-15}"
# Minimum A/V pairs that must actually be COMPARED for this run to pass.
#
# "Compared", not "found", deliberately. Asserting on pairs found would still
# exit 0 for a run where every pair is shorter than MIN_ELAPSED and is skipped
# without a single drift measurement -- a check that ran zero times reporting
# success. Both are the same false-green class.
#
# Default 1: a run must prove it measured something. A leg that legitimately
# has no A/V pair to compare (video-less rejoin) opts down with 0, visibly.
REQUIRE_PAIRS="${REQUIRE_PAIRS:-1}"

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
    --require-pairs)
      REQUIRE_PAIRS="$2"
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

for cmd in ffprobe awk python3; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "$cmd is required" >&2
    exit 1
  fi
done

PAIR_FILE="$(mktemp)"
META_JSON="$(mktemp)"
cleanup() {
  rm -f "$PAIR_FILE" "$META_JSON"
}
trap cleanup EXIT

ffprobe -v error -show_streams -of json "$INPUT" >"$META_JSON"

python3 - "$META_JSON" >"$PAIR_FILE" <<'PY'
import json
import re
import sys

meta = json.load(open(sys.argv[1], "r", encoding="utf-8"))
by_group = {}

def group_key(ltid: str) -> str:
    if ":audio:" in ltid:
        return ltid.replace(":audio:", ":media:", 1)
    if ":video:" in ltid:
        return ltid.replace(":video:", ":media:", 1)
    # Fallback for future LTID formats.
    return re.sub(r":(audio|video):", ":media:", ltid, count=1)

for stream in meta.get("streams", []):
    if not isinstance(stream, dict):
        continue
    kind = str(stream.get("codec_type") or "").strip().lower()
    tags = stream.get("tags") or {}
    ltid = str(tags.get("LTID") or tags.get("ltid") or "").strip()
    stream_id = str(tags.get("STREAM_ID") or tags.get("stream_id") or "").strip()
    index = stream.get("index")
    if not ltid or not kind or not stream_id or kind not in {"audio", "video"}:
        continue
    group = group_key(ltid)
    entry = by_group.setdefault(group, {})
    entry[kind] = {"stream_id": stream_id, "index": int(index)}
    entry.setdefault("ltid_audio", "")
    entry.setdefault("ltid_video", "")
    entry[f"ltid_{kind}"] = ltid

for group, entry in sorted(by_group.items()):
    if "audio" in entry and "video" in entry:
        ltid_label = entry.get("ltid_video") or entry.get("ltid_audio") or group
        print(
            f"{ltid_label}\t"
            f"{entry['audio']['stream_id']}\t{entry['audio']['index']}\t"
            f"{entry['video']['stream_id']}\t{entry['video']['index']}"
        )
PY

mapfile -t PAIRS <"$PAIR_FILE"
if [[ "${#PAIRS[@]}" -eq 0 ]]; then
  # Skipping is legitimate -- drift is meaningless without both clocks -- but
  # whether it is ACCEPTABLE is the caller's call, not this script's.
  if (( REQUIRE_PAIRS > 0 )); then
    echo "no paired LTID audio/video streams found in MKV metadata, but --require-pairs=${REQUIRE_PAIRS} demands at least ${REQUIRE_PAIRS} compared pair(s)" >&2
    echo "this run expected both audio and video; finding no pair to compare is a capture/remux failure, not a reason to skip" >&2
    exit 1
  fi
  log "av drift check skipped: no paired LTID audio/video streams found in MKV metadata (--require-pairs=0)"
  exit 0
fi

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
PAIRS_COMPARED=0
log "av drift check: tolerance=${TOLERANCE}s min_elapsed=${MIN_ELAPSED}s pairs=${#PAIRS[@]} require_pairs=${REQUIRE_PAIRS}"
for row in "${PAIRS[@]}"; do
  IFS=$'\t' read -r LTID AUDIO_ID AUDIO_IDX VIDEO_ID VIDEO_IDX <<<"$row"

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

  # Past every `continue` above: this pair is really being measured, so it is
  # the only place the counter may advance.
  PAIRS_COMPARED=$((PAIRS_COMPARED + 1))

  DRIFT="$(awk -v a="$VIDEO_ELAPSED" -v b="$AUDIO_ELAPSED" 'BEGIN {d=a-b; if (d<0) d=-d; printf "%.6f", d}')"
  WITHIN="$(awk -v d="$DRIFT" -v t="$TOLERANCE" 'BEGIN { if (d<=t) print "yes"; else print "no" }')"

  log "  ltid=${LTID} audio_elapsed=${AUDIO_ELAPSED}s video_elapsed=${VIDEO_ELAPSED}s abs_drift=${DRIFT}s audio=[${AUDIO_FIRST},${AUDIO_LAST}] video=[${VIDEO_FIRST},${VIDEO_LAST}]"
  if [[ "$WITHIN" != "yes" ]]; then
    echo "av drift beyond tolerance for ltid=${LTID}: drift=${DRIFT}s > ${TOLERANCE}s" >&2
    FAILURES=$((FAILURES + 1))
  fi
done

if [[ "$FAILURES" -gt 0 ]]; then
  exit 1
fi

# Closes the second false-green: pairs existed, but every one was skipped as
# too short, so the check "passed" without measuring anything.
if (( PAIRS_COMPARED < REQUIRE_PAIRS )); then
  echo "av drift compared ${PAIRS_COMPARED} pair(s), but --require-pairs=${REQUIRE_PAIRS} demands at least ${REQUIRE_PAIRS}" >&2
  echo "pairs found=${#PAIRS[@]} -- a pair that was found but skipped (shorter than min_elapsed=${MIN_ELAPSED}s) was never measured, so this run proves nothing about drift" >&2
  exit 1
fi

log "av drift check passed (pairs_compared=${PAIRS_COMPARED})"
