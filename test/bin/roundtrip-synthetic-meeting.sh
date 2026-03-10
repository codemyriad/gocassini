#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

CALL_URL="${CALL_URL:-}"
SCENARIO="${SCENARIO:-$TEST_DIR/scenarios/synthetic-pied-piper.v1.json}"
MEDIA_OUTPUT_DIR="${MEDIA_OUTPUT_DIR:-$MEDIA_DIR/processed/synthetic-pied-piper-v1}"
BACKEND="${BACKEND:-kokoro}"
OUTPUT_ROOT="${OUTPUT_ROOT:-/tmp/cassini-roundtrip-$(date -u +%Y%m%dT%H%M%S)}"
OUTPUT="${OUTPUT:-}"
ARCHIVE_OUTPUT="${ARCHIVE_OUTPUT:-}"
ARTIFACT_OUTPUT_ROOT="${ARTIFACT_OUTPUT_ROOT:-}"
RECORDER_DURATION="${RECORDER_DURATION:-}"
PUBLISH_DURATION="${PUBLISH_DURATION:-}"
START_DELAY="${START_DELAY:-6}"
RECORDER_NAME="${RECORDER_NAME:-GocassiniSyntheticRoundtrip}"
TRANSCRIBER_DEVICE="${TRANSCRIBER_DEVICE:-cpu}"
WHISPER_MODEL="${WHISPER_MODEL:-small.en}"
REQUEST_OFFER_INTERVAL="${REQUEST_OFFER_INTERVAL:-2}"
MAX_REQUEST_OFFER_ATTEMPTS="${MAX_REQUEST_OFFER_ATTEMPTS:-30}"
SKIP_PREPARE=0
FORCE=0

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
    --media-output-dir)
      MEDIA_OUTPUT_DIR="$2"
      shift 2
      ;;
    --backend)
      BACKEND="$2"
      shift 2
      ;;
    --output-root)
      OUTPUT_ROOT="$2"
      shift 2
      ;;
    --output)
      OUTPUT="$2"
      shift 2
      ;;
    --archive-output)
      ARCHIVE_OUTPUT="$2"
      shift 2
      ;;
    --artifact-output-root)
      ARTIFACT_OUTPUT_ROOT="$2"
      shift 2
      ;;
    --recorder-duration)
      RECORDER_DURATION="$2"
      shift 2
      ;;
    --publish-duration)
      PUBLISH_DURATION="$2"
      shift 2
      ;;
    --start-delay)
      START_DELAY="$2"
      shift 2
      ;;
    --transcriber-device)
      TRANSCRIBER_DEVICE="$2"
      shift 2
      ;;
    --whisper-model)
      WHISPER_MODEL="$2"
      shift 2
      ;;
    --skip-prepare)
      SKIP_PREPARE=1
      shift
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

if [[ -z "$CALL_URL" ]]; then
  if [[ -f "$RUNTIME_DIR/last_call_url" ]]; then
    CALL_URL="$(cat "$RUNTIME_DIR/last_call_url")"
  else
    echo "missing --call-url and no $RUNTIME_DIR/last_call_url found" >&2
    exit 1
  fi
fi

for tool in docker ffmpeg go npm; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "$tool is required" >&2
    exit 1
  fi
done
if ! docker info >/dev/null 2>&1; then
  echo "docker daemon is not reachable" >&2
  exit 1
fi

GO_RECORDER_DIR="$REPO_ROOT/cassini-go-recorder"
TRANSCRIBER_DIR="$REPO_ROOT/cassini-transcriber"
if [[ ! -f "$GO_RECORDER_DIR/cmd/gocassini/main.go" ]]; then
  echo "go recorder not found: $GO_RECORDER_DIR" >&2
  exit 1
fi
if [[ ! -x "$TRANSCRIBER_DIR/bin/process-meeting.sh" ]]; then
  echo "transcriber wrapper not found: $TRANSCRIBER_DIR/bin/process-meeting.sh" >&2
  exit 1
fi

if [[ "$SKIP_PREPARE" != "1" ]]; then
  PREP_ARGS=(
    "$SCRIPT_DIR/prepare-synthetic-meeting.sh"
    --scenario "$SCENARIO"
    --output-dir "$MEDIA_OUTPUT_DIR"
    --backend "$BACKEND"
  )
  if [[ "$FORCE" == "1" ]]; then
    PREP_ARGS+=(--force)
  fi
  if [[ -n "${PYTHON_BIN:-}" ]]; then
    env "PYTHON_BIN=$PYTHON_BIN" "${PREP_ARGS[@]}" >/dev/null
  else
    env "UV_PYTHON=${UV_PYTHON:-3.12}" "${PREP_ARGS[@]}" >/dev/null
  fi
fi

MANIFEST_PATH="$MEDIA_OUTPUT_DIR/manifest.json"
if [[ ! -f "$MANIFEST_PATH" ]]; then
  echo "missing manifest: $MANIFEST_PATH" >&2
  exit 1
fi

configure_python_runner "$TEST_DIR/requirements-synthetic.txt"
readarray -t MANIFEST_INFO < <("${PYTHON_RUNNER[@]}" - "$MANIFEST_PATH" "${PUBLISH_DURATION:-}" <<'PY'
import json
import math
import sys

manifest = json.load(open(sys.argv[1], "r", encoding="utf-8"))
publish_override = (sys.argv[2] or "").strip()
publish_duration = int(math.ceil(float(publish_override or manifest["duration_seconds"])))
print(str(publish_duration))
print(str(manifest.get("scenario_id") or "synthetic-meeting"))
print(str(len(manifest.get("participants") or [])))
PY
)

PUBLISH_DURATION="${MANIFEST_INFO[0]}"
SCENARIO_ID="${MANIFEST_INFO[1]}"
PARTICIPANT_COUNT="${MANIFEST_INFO[2]}"

mkdir -p "$OUTPUT_ROOT"
STAMP="$(date -u +%Y%m%dT%H%M%S)"
DEFAULT_BASE="${SCENARIO_ID}--${STAMP}"
if [[ -z "$OUTPUT" ]]; then
  OUTPUT="$OUTPUT_ROOT/${DEFAULT_BASE}.mkv"
fi
if [[ -z "$ARCHIVE_OUTPUT" ]]; then
  ARCHIVE_OUTPUT="$OUTPUT"
fi
if [[ -z "$ARTIFACT_OUTPUT_ROOT" ]]; then
  ARTIFACT_OUTPUT_ROOT="$OUTPUT_ROOT/transcriber"
fi
mkdir -p "$(dirname "$OUTPUT")" "$ARTIFACT_OUTPUT_ROOT"
if [[ "$ARCHIVE_OUTPUT" != "$OUTPUT" ]]; then
  mkdir -p "$(dirname "$ARCHIVE_OUTPUT")"
fi

if [[ -z "$RECORDER_DURATION" ]]; then
  RECORDER_DURATION="$(awk -v publish="$PUBLISH_DURATION" -v start="$START_DELAY" 'BEGIN { printf "%.0f", publish + start + 35 }')"
fi

REC_LOG="$OUTPUT_ROOT/recorder.log"
PUB_LOG="$OUTPUT_ROOT/publisher.log"
INSPECT_LOG="$OUTPUT_ROOT/inspect.log"
TRANSCRIBE_LOG="$OUTPUT_ROOT/transcriber.log"
MEETING_ID="$(basename "$OUTPUT" .mkv)"
ARTIFACT_DIR="$ARTIFACT_OUTPUT_ROOT/${MEETING_ID}.artifact"
RENDERED_DIR="$ARTIFACT_OUTPUT_ROOT/${MEETING_ID}.rendered"
RENDERED_INDEX="$RENDERED_DIR/index.html"
RENDERED_CATALOG="$RENDERED_DIR/catalog.json"
RENDERED_MEETING_DIR="$RENDERED_DIR/meetings/$MEETING_ID"

rm -f "$OUTPUT" "$REC_LOG" "$PUB_LOG" "$INSPECT_LOG" "$TRANSCRIBE_LOG"
if [[ "$ARCHIVE_OUTPUT" != "$OUTPUT" ]]; then
  rm -f "$ARCHIVE_OUTPUT"
fi
rm -rf "$ARTIFACT_DIR" "$RENDERED_DIR"

REC_PID=""
cleanup() {
  if [[ -n "$REC_PID" ]] && kill -0 "$REC_PID" >/dev/null 2>&1; then
    kill "$REC_PID" >/dev/null 2>&1 || true
    wait "$REC_PID" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

recording_composed_successfully() {
  [[ -s "$OUTPUT" && -f "$REC_LOG" ]] || return 1
  grep -Eq 'composed final multi-track output|composed final output from session artifact' "$REC_LOG"
}

log "Running synthetic meeting roundtrip"
log "  call: $CALL_URL"
log "  scenario: $SCENARIO"
log "  participants: $PARTICIPANT_COUNT"
log "  media dir: $MEDIA_OUTPUT_DIR"
log "  meeting output: $OUTPUT"
log "  artifact output root: $ARTIFACT_OUTPUT_ROOT"
log "  publish duration: ${PUBLISH_DURATION}s"
log "  recorder duration: ${RECORDER_DURATION}s"
log "  whisper model: $WHISPER_MODEL"

(
  cd "$GO_RECORDER_DIR"
  go run ./cmd/gocassini \
    --mode talk \
    --call-url "$CALL_URL" \
    --name "$RECORDER_NAME" \
    --duration "$RECORDER_DURATION" \
    --output "$ARCHIVE_OUTPUT" \
    --final-output "$OUTPUT" \
    --request-offer-interval "$REQUEST_OFFER_INTERVAL" \
    --max-request-offer-attempts "$MAX_REQUEST_OFFER_ATTEMPTS"
) >"$REC_LOG" 2>&1 &
REC_PID=$!

sleep "$START_DELAY"

STREAM_ARGS=(
  "$SCRIPT_DIR/stream-synthetic-meeting.sh"
  --call-url "$CALL_URL"
  --scenario "$SCENARIO"
  --output-dir "$MEDIA_OUTPUT_DIR"
  --backend "$BACKEND"
  --duration "$PUBLISH_DURATION"
)
if [[ "$SKIP_PREPARE" == "1" ]]; then
  STREAM_ARGS+=(--skip-prepare)
fi
if [[ "$FORCE" == "1" && "$SKIP_PREPARE" != "1" ]]; then
  STREAM_ARGS+=(--force)
fi

set +e
if [[ -n "${PYTHON_BIN:-}" ]]; then
  env "PYTHON_BIN=$PYTHON_BIN" "${STREAM_ARGS[@]}" >"$PUB_LOG" 2>&1
else
  env "UV_PYTHON=${UV_PYTHON:-3.12}" "${STREAM_ARGS[@]}" >"$PUB_LOG" 2>&1
fi
PUB_RC=$?
if [[ "$PUB_RC" -ne 0 ]] && kill -0 "$REC_PID" >/dev/null 2>&1; then
  kill "$REC_PID" >/dev/null 2>&1 || true
fi
wait "$REC_PID"
REC_RC=$?
set -e
REC_PID=""

if [[ "$REC_RC" -ne 0 ]] && recording_composed_successfully; then
  log "Recorder exited rc=$REC_RC after producing the final MKV; continuing"
  REC_RC=0
fi
if [[ "$PUB_RC" -ne 0 ]] && recording_composed_successfully; then
  log "Publisher exited rc=$PUB_RC after recorder produced the final MKV; continuing"
  PUB_RC=0
fi

if [[ "$PUB_RC" -ne 0 ]]; then
  echo "publisher failed (rc=$PUB_RC), log: $PUB_LOG" >&2
  exit "$PUB_RC"
fi
if [[ "$REC_RC" -ne 0 ]]; then
  echo "recorder failed (rc=$REC_RC), log: $REC_LOG" >&2
  exit "$REC_RC"
fi
if [[ ! -s "$OUTPUT" ]]; then
  echo "recording output missing or empty: $OUTPUT" >&2
  exit 1
fi
SESSION_ID="$(cassini_session_id_from_mkv "$OUTPUT" || true)"
if [[ -z "$SESSION_ID" ]]; then
  echo "meeting mkv is missing embedded SESSION_ID metadata: $OUTPUT" >&2
  exit 1
fi

(
  cd "$GO_RECORDER_DIR"
  go run ./cmd/gocassini-inspect "$OUTPUT"
) >"$INSPECT_LOG" 2>&1

(
  cd "$TRANSCRIBER_DIR"
  ./bin/process-meeting.sh \
    --input "$OUTPUT" \
    --output-root "$ARTIFACT_OUTPUT_ROOT" \
    --bundle-viewer \
    --device "$TRANSCRIBER_DEVICE" \
    --whisper-model "$WHISPER_MODEL"
) >"$TRANSCRIBE_LOG" 2>&1

for required in \
  "$ARTIFACT_DIR/meeting.webm" \
  "$ARTIFACT_DIR/transcript.words.v1.json" \
  "$ARTIFACT_DIR/captions.vtt" \
  "$ARTIFACT_DIR/manifest.json" \
  "$RENDERED_CATALOG" \
  "$RENDERED_MEETING_DIR/manifest.json" \
  "$RENDERED_INDEX"; do
  if [[ ! -s "$required" ]]; then
    echo "expected output missing or empty: $required" >&2
    exit 1
  fi
done

log "Recorder log: $REC_LOG"
log "Publisher log: $PUB_LOG"
log "Inspect log: $INSPECT_LOG"
log "Transcriber log: $TRANSCRIBE_LOG"
log "Meeting MKV: $OUTPUT"
log "Meeting session id: $SESSION_ID"
log "Artifact dir: $ARTIFACT_DIR"
log "Viewer landing page: $RENDERED_INDEX"
log "Viewer catalog: $RENDERED_CATALOG"
log "Viewer meeting dir: $RENDERED_MEETING_DIR"
