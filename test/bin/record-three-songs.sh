#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

CALL_URL="${CALL_URL:-}"
DURATION="${DURATION:-}"
RECORDER_DURATION="${RECORDER_DURATION:-}"
OUTPUT="${OUTPUT:-}"
ARCHIVE_OUTPUT="${ARCHIVE_OUTPUT:-}"
SKIP_PREPARE="${SKIP_PREPARE:-0}"
SKIP_SYNC_CHECK="${SKIP_SYNC_CHECK:-0}"
CHECK_SESSION_ARTIFACT="${CHECK_SESSION_ARTIFACT:-1}"
RECORDER_NAME="${RECORDER_NAME:-GocassiniThreeSongs}"
REQUEST_OFFER_INTERVAL="${REQUEST_OFFER_INTERVAL:-2}"
MAX_REQUEST_OFFER_ATTEMPTS="${MAX_REQUEST_OFFER_ATTEMPTS:-30}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --call-url)
      CALL_URL="$2"
      shift 2
      ;;
    --duration)
      DURATION="$2"
      shift 2
      ;;
    --recorder-duration)
      RECORDER_DURATION="$2"
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
    --skip-prepare)
      SKIP_PREPARE=1
      shift
      ;;
    --skip-sync-check)
      SKIP_SYNC_CHECK=1
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

if [[ -z "$OUTPUT" ]]; then
  OUTPUT="/tmp/three-songs-recording-$(date -u +%Y%m%d-%H%M%S).mkv"
fi
if [[ -z "$ARCHIVE_OUTPUT" ]]; then
  ARCHIVE_OUTPUT="${OUTPUT%.*}.csr"
fi

if [[ -z "$RECORDER_DURATION" ]]; then
  if [[ -n "$DURATION" ]]; then
    RECORDER_DURATION="$(awk -v d="$DURATION" 'BEGIN { printf "%.0f", d + 50 }')"
  else
    RECORDER_DURATION="420"
  fi
fi

GO_RECORDER_DIR="$REPO_ROOT/cassini-go-recorder"
if [[ ! -d "$GO_RECORDER_DIR" || ! -f "$GO_RECORDER_DIR/cmd/gocassini/main.go" ]]; then
  echo "go recorder not found: $GO_RECORDER_DIR" >&2
  exit 1
fi
if ! command -v go >/dev/null 2>&1; then
  echo "go is required to run gocassini" >&2
  exit 1
fi

REC_LOG="${OUTPUT}.recorder.log"
PUB_LOG="${OUTPUT}.publisher.log"
REPORT_JSON="${OUTPUT}.json"

mkdir -p "$(dirname "$OUTPUT")"
mkdir -p "$(dirname "$ARCHIVE_OUTPUT")"

log "Starting recorder"
log "  call: $CALL_URL"
log "  archive output: $ARCHIVE_OUTPUT"
log "  output: $OUTPUT"
log "  recorder duration: ${RECORDER_DURATION}s"
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

sleep 6

STREAM_ARGS=(--call-url "$CALL_URL")
if [[ "$SKIP_PREPARE" == "1" ]]; then
  STREAM_ARGS+=(--skip-prepare)
fi
if [[ -n "$DURATION" ]]; then
  STREAM_ARGS+=(--duration "$DURATION")
fi

set +e
"$SCRIPT_DIR/stream-three-songs.sh" "${STREAM_ARGS[@]}" >"$PUB_LOG" 2>&1
PUB_RC=$?
wait "$REC_PID"
REC_RC=$?
set -e

if [[ "$PUB_RC" -ne 0 ]]; then
  echo "publisher failed (rc=$PUB_RC), log: $PUB_LOG" >&2
  exit "$PUB_RC"
fi
if [[ "$REC_RC" -ne 0 ]]; then
  echo "recorder failed (rc=$REC_RC), log: $REC_LOG" >&2
  exit "$REC_RC"
fi

if [[ ! -f "$OUTPUT" ]]; then
  echo "recording output not found: $OUTPUT" >&2
  exit 1
fi
if [[ ! -f "$ARCHIVE_OUTPUT" ]]; then
  echo "recording archive output not found: $ARCHIVE_OUTPUT" >&2
  exit 1
fi

if [[ "$SKIP_SYNC_CHECK" != "1" && -f "$REPORT_JSON" ]]; then
  "$SCRIPT_DIR/verify-sync-from-report.sh" \
    --recording "$OUTPUT" \
    --report "$REPORT_JSON"
fi
if [[ "$CHECK_SESSION_ARTIFACT" == "1" && -f "$REPORT_JSON" ]]; then
  "$SCRIPT_DIR/verify-session-artifact.sh" \
    --final-output "$OUTPUT" \
    --report "$REPORT_JSON"
fi

log "Recorder log: $REC_LOG"
log "Publisher log: $PUB_LOG"
if [[ -f "$REPORT_JSON" ]]; then
  log "Recorder report: $REPORT_JSON"
fi
log "Archive recording: $ARCHIVE_OUTPUT"
log "Raw recording: $OUTPUT"
