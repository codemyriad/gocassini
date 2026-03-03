#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

INPUT=""
REPORT=""
TOLERANCE="${TOLERANCE:-0.35}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --recording|--input)
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
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "$INPUT" ]]; then
  echo "missing --recording <final.mkv>" >&2
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

for cmd in jq ffprobe awk; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "$cmd is required" >&2
    exit 1
  fi
done

META="$(mktemp)"
EXPECTED_FILE="$(mktemp)"
cleanup() {
  rm -f "$META" "$EXPECTED_FILE"
}
trap cleanup EXIT

ffprobe -v error -show_streams -of json "$INPUT" >"$META"

python3 - "$REPORT" >"$EXPECTED_FILE" <<'PY'
import datetime as dt
import json
import math
import subprocess
import sys

report_path = sys.argv[1]
with open(report_path, "r", encoding="utf-8") as f:
    data = json.load(f)

sessions = [
    s for s in data.get("session_outputs", [])
    if isinstance(s, dict) and s.get("session_mkv_exists") is True
]
if not sessions:
    raise SystemExit(0)

def parse_ts(value: str) -> float:
    if value.endswith("Z"):
        value = value[:-1] + "+00:00"
    return dt.datetime.fromisoformat(value).timestamp()

def probe_min_stream_start(path: str) -> float:
    cmd = [
        "ffprobe",
        "-v",
        "error",
        "-show_entries",
        "stream=start_time",
        "-of",
        "default=noprint_wrappers=1:nokey=1",
        path,
    ]
    out = subprocess.check_output(cmd, text=True)
    values = []
    for line in out.splitlines():
        line = line.strip()
        if not line or line.upper() == "N/A":
            continue
        try:
            values.append(float(line))
        except ValueError:
            continue
    if not values:
        return 0.0
    return min(values)

base = min(parse_ts(s.get("started_at", "")) for s in sessions)
for s in sessions:
    sid = s.get("remote_session_id", "")
    started_at = s.get("started_at", "")
    session_mkv_path = s.get("session_mkv_path", "")
    if not sid or not started_at or not session_mkv_path:
        continue
    desired = parse_ts(started_at) - base
    source_start = probe_min_stream_start(session_mkv_path)
    offset = desired - source_start
    if math.isclose(offset, 0.0, abs_tol=1e-9):
        offset = 0.0
    print(f"{sid}\t{offset:.6f}")
PY

mapfile -t EXPECTED <"$EXPECTED_FILE"

if [[ "${#EXPECTED[@]}" -lt 2 ]]; then
  log "sync check skipped: need at least 2 composed sessions (found ${#EXPECTED[@]})"
  exit 0
fi

FAILURES=0
log "sync check: tolerance=${TOLERANCE}s"
for row in "${EXPECTED[@]}"; do
  IFS=$'\t' read -r SID EXPECTED_OFFSET <<<"$row"

  mapfile -t STREAM_INDEXES < <(jq -r --arg sid "$SID" '
    .streams[]
    | select((.tags.remote_session_id // .tags.REMOTE_SESSION_ID // "") == $sid)
    | .index
  ' "$META")

  OBSERVED_OFFSET=""
  for idx in "${STREAM_INDEXES[@]}"; do
    pts="$(ffprobe -v error \
      -select_streams "$idx" \
      -show_packets \
      -show_entries packet=pts_time \
      -of csv=p=0 \
      "$INPUT" | head -n1 || true)"
    if [[ -z "$pts" ]]; then
      continue
    fi
    if [[ -z "$OBSERVED_OFFSET" ]]; then
      OBSERVED_OFFSET="$pts"
      continue
    fi
    if awk -v a="$pts" -v b="$OBSERVED_OFFSET" 'BEGIN { exit !(a < b) }'; then
      OBSERVED_OFFSET="$pts"
    fi
  done

  if [[ -z "$OBSERVED_OFFSET" ]]; then
    echo "missing stream metadata for remote_session_id=$SID" >&2
    FAILURES=$((FAILURES + 1))
    continue
  fi

  DELTA="$(awk -v observed="$OBSERVED_OFFSET" -v expected="$EXPECTED_OFFSET" 'BEGIN { d=observed-expected; if (d<0) d=-d; printf "%.6f", d }')"
  WITHIN="$(awk -v delta="$DELTA" -v tol="$TOLERANCE" 'BEGIN { if (delta <= tol) print "yes"; else print "no" }')"

  log "  sid=${SID} expected=${EXPECTED_OFFSET}s observed=${OBSERVED_OFFSET}s delta=${DELTA}s"
  if [[ "$WITHIN" != "yes" ]]; then
    echo "sync drift beyond tolerance for sid=${SID}: expected=${EXPECTED_OFFSET}s observed=${OBSERVED_OFFSET}s (delta=${DELTA}s > ${TOLERANCE}s)" >&2
    FAILURES=$((FAILURES + 1))
  fi
done

if [[ "$FAILURES" -gt 0 ]]; then
  exit 1
fi

log "sync check passed"
