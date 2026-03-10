#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

SCENARIO="${SCENARIO:-$TEST_DIR/scenarios/synthetic-pied-piper.v1.json}"
OUTPUT_DIR="${OUTPUT_DIR:-$MEDIA_DIR/processed/synthetic-pied-piper-v1}"
BACKEND="${BACKEND:-kokoro}"
FORCE=0

while [[ $# -gt 0 ]]; do
  case "$1" in
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
  echo "ffmpeg is required" >&2
  exit 1
fi

REQUIREMENTS_FILE="$TEST_DIR/requirements-synthetic.txt"
if [[ "$BACKEND" == "kokoro" ]]; then
  REQUIREMENTS_FILE="$TEST_DIR/requirements-tts.txt"
fi
configure_python_runner "$REQUIREMENTS_FILE"

mkdir -p "$OUTPUT_DIR"

CMD=(
  "${PYTHON_RUNNER[@]}" "$SCRIPT_DIR/prepare-synthetic-meeting.py"
  --scenario "$SCENARIO"
  --output-dir "$OUTPUT_DIR"
  --backend "$BACKEND"
)
if [[ "$FORCE" == "1" ]]; then
  CMD+=(--force)
fi

log "Generating synthetic meeting media"
log "  scenario: $SCENARIO"
log "  output dir: $OUTPUT_DIR"
log "  backend: $BACKEND"
if [[ "${PYTHON_RUNNER[0]}" == "uv" ]]; then
  log "  python: uv ${UV_PYTHON:-3.12}"
elif [[ -n "${PYTHON_BIN:-}" ]]; then
  log "  python: $PYTHON_BIN"
else
  log "  python: python3"
fi
MANIFEST_PATH="$("${CMD[@]}" | tail -n1)"

"${PYTHON_RUNNER[@]}" - "$MANIFEST_PATH" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1], "r", encoding="utf-8"))
print(",".join(p["media_prefix"] for p in data["participants"]))
PY
