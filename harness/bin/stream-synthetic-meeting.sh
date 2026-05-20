#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

CALL_URL="${CALL_URL:-}"
SCENARIO="${SCENARIO:-$TEST_DIR/scenarios/synthetic-pied-piper.v1.json}"
OUTPUT_DIR="${OUTPUT_DIR:-$MEDIA_DIR/processed/synthetic-pied-piper-v1}"
BACKEND="${BACKEND:-kokoro}"
# Honor PREPARE from the env so callers pointing at pre-rendered LFS-vendored
# fixtures can skip the ffmpeg+Kokoro generation step entirely. Defaults to 1
# for backwards compatibility with manual invocations.
PREPARE="${PREPARE:-1}"
FORCE=0
OVERRIDE_DURATION="${DURATION:-}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --call-url)
      CALL_URL="$2"
      shift 2
      ;;
    --scenario)
      SCENARIO="$2"
      shift 2
      ;;
    --output-dir)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    --backend)
      BACKEND="$2"
      shift 2
      ;;
    --duration)
      OVERRIDE_DURATION="$2"
      shift 2
      ;;
    --force)
      FORCE=1
      shift
      ;;
    --skip-prepare)
      PREPARE=0
      shift
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "$CALL_URL" ]]; then
  if [[ -f "$RUNTIME_DIR/last_call_url" ]]; then
    CALL_URL="$(cat "$RUNTIME_DIR/last_call_url")"
  else
    echo "missing --call-url and no $RUNTIME_DIR/last_call_url found" >&2
    exit 1
  fi
fi

REQUIREMENTS_FILE="$TEST_DIR/requirements-synthetic.txt"
if [[ "$BACKEND" == "kokoro" ]]; then
  REQUIREMENTS_FILE="$TEST_DIR/requirements-tts.txt"
fi
configure_python_runner "$REQUIREMENTS_FILE"

if [[ "$PREPARE" == "1" ]]; then
  PREP_CMD=(
    "$SCRIPT_DIR/prepare-synthetic-meeting.sh"
    --scenario "$SCENARIO"
    --output-dir "$OUTPUT_DIR"
    --backend "$BACKEND"
  )
  if [[ "$FORCE" == "1" ]]; then
    PREP_CMD+=(--force)
  fi
  if [[ -n "${PYTHON_BIN:-}" ]]; then
    PREP_CMD=(env "PYTHON_BIN=$PYTHON_BIN" "${PREP_CMD[@]}")
  else
    PREP_CMD=(env "UV_PYTHON=${UV_PYTHON:-3.12}" "${PREP_CMD[@]}")
  fi
  "${PREP_CMD[@]}" >/dev/null
else
  MANIFEST_PATH="$OUTPUT_DIR/manifest.json"
  if [[ ! -f "$MANIFEST_PATH" ]]; then
    echo "missing manifest: $MANIFEST_PATH" >&2
    echo "run: $SCRIPT_DIR/prepare-synthetic-meeting.sh --scenario \"$SCENARIO\" --output-dir \"$OUTPUT_DIR\"" >&2
    exit 1
  fi
fi

MANIFEST_PATH="$OUTPUT_DIR/manifest.json"
if [[ ! -f "$MANIFEST_PATH" ]]; then
  echo "missing manifest after preparation: $MANIFEST_PATH" >&2
  exit 1
fi

readarray -t STREAM_ARGS < <("${PYTHON_RUNNER[@]}" - "$MANIFEST_PATH" "$OVERRIDE_DURATION" <<'PY'
import json
import math
import sys

manifest = json.load(open(sys.argv[1], "r", encoding="utf-8"))
override_duration = (sys.argv[2] or "").strip()
participants = manifest["participants"]
duration = override_duration or str(int(math.ceil(float(manifest["duration_seconds"]))))
print("--users")
print(str(len(participants)))
print("--duration")
print(duration)
print("--media-prefixes")
print(",".join(p["media_prefix"] for p in participants))
print("--names")
print(",".join(p["display_name"] for p in participants))
print("--join-delays")
print(",".join(str(p["join_delay_seconds"]) for p in participants))
print("--sync-shifts")
print(",".join(str(p["join_delay_seconds"]) for p in participants))
PY
)

"$SCRIPT_DIR/stream-video.sh" --call-url "$CALL_URL" "${STREAM_ARGS[@]}"
