#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"
TEST_DIR="$ROOT_DIR/../test"

CALL_URL="${CALL_URL:-}"
CALL_NAME="${CALL_NAME:-Cassini Go E2E room}"
REC_DURATION="${REC_DURATION:-55}"
PUB_DURATION="${PUB_DURATION:-24}"
PUB_USERS="${PUB_USERS:-2}"
START_DELAY="${START_DELAY:-6}"
OUTPUT="${OUTPUT:-/tmp/gocassini-e2e.csr}"
FINAL_OUTPUT="${FINAL_OUTPUT:-${OUTPUT%.csr}.mkv}"
CHECK_COMPOSED_AUDIO_TAIL="${CHECK_COMPOSED_AUDIO_TAIL:-0}"
NAME="${NAME:-GocassiniBot}"

REC_LOG="${REC_LOG:-/tmp/gocassini-e2e.log}"
PUB_LOG="${PUB_LOG:-/tmp/gocassini-publisher-e2e.log}"
CHECK_SESSION_ARTIFACT="${CHECK_SESSION_ARTIFACT:-1}"

rm -f "$OUTPUT" "$FINAL_OUTPUT" "$REC_LOG" "$PUB_LOG"

if [[ -z "$CALL_URL" && -f "$TEST_DIR/runtime/last_call_url" ]]; then
  CALL_URL="$(cat "$TEST_DIR/runtime/last_call_url")"
fi

if [[ -z "$CALL_URL" ]]; then
  if [[ ! -x "$TEST_DIR/bin/create-room.sh" ]]; then
    echo "missing CALL_URL and local create-room helper is unavailable" >&2
    echo "set CALL_URL or run ./test/bin/ci-e2e.sh" >&2
    exit 1
  fi

  echo "CALL_URL not provided, creating room in local test stack..."
  if ! CALL_URL="$("$TEST_DIR/bin/create-room.sh" --name "$CALL_NAME" | tail -n1)"; then
    echo "could not create local room. Start local stack with ./test/bin/up.sh or use ./test/bin/ci-e2e.sh" >&2
    exit 1
  fi
fi

if [[ -z "${SEGMENTS_DIR:-}" ]]; then
  SEGMENTS_PARENT="$(dirname "$FINAL_OUTPUT")"
  SEGMENTS_BASE="$(basename "${FINAL_OUTPUT%.*}")"
  mkdir -p "$SEGMENTS_PARENT"
  SEGMENTS_DIR="$(mktemp -d "${SEGMENTS_PARENT}/${SEGMENTS_BASE}-segments-XXXXXX")"
else
  rm -rf "$SEGMENTS_DIR"
  mkdir -p "$SEGMENTS_DIR"
fi

echo "=== Cassini Go Recorder E2E ==="
echo "CALL_URL=$CALL_URL"
echo "REC_DURATION=$REC_DURATION"
echo "PUB_DURATION=$PUB_DURATION"
echo "PUB_USERS=$PUB_USERS"
echo "OUTPUT=$OUTPUT"
echo "FINAL_OUTPUT=$FINAL_OUTPUT"
echo "SEGMENTS_DIR=$SEGMENTS_DIR"

(
  cd "$ROOT_DIR"
  go run ./cmd/gocassini \
    --mode talk \
    --call-url "$CALL_URL" \
    --name "$NAME" \
    --duration "$REC_DURATION" \
    --output "$OUTPUT" \
    --final-output "$FINAL_OUTPUT" \
    --segments-dir "$SEGMENTS_DIR" \
    --request-offer-interval 2 \
    --max-request-offer-attempts 30
) >"$REC_LOG" 2>&1 &
REC_PID=$!

sleep "$START_DELAY"

(
  cd "$TEST_DIR"
  ./bin/stream-video.sh \
    --call-url "$CALL_URL" \
    --users "$PUB_USERS" \
    --duration "$PUB_DURATION" \
    --name-prefix "CassiniGoE2E"
) >"$PUB_LOG" 2>&1 || true

wait "$REC_PID" || true

if [[ ! -f "$OUTPUT" ]]; then
  echo "[FAIL] output not found: $OUTPUT"
  echo "--- recorder log tail ---"
  tail -n 120 "$REC_LOG" || true
  exit 1
fi

SIZE="$(stat -c '%s' "$OUTPUT")"
echo "output size bytes: $SIZE"
if [[ "$SIZE" -le 14 ]]; then
  echo "[FAIL] output contains header only (<=14 bytes)"
  echo "--- recorder log tail ---"
  tail -n 160 "$REC_LOG" || true
  exit 1
fi

echo "--- archive summary ---"
(
  cd "$ROOT_DIR"
  go run ./cmd/gocassini-inspect "$OUTPUT"
)

if [[ ! -f "$FINAL_OUTPUT" ]]; then
  echo "[FAIL] final mkv not found: $FINAL_OUTPUT"
  echo "--- recorder log tail ---"
  tail -n 160 "$REC_LOG" || true
  exit 1
fi

FINAL_SIZE="$(stat -c '%s' "$FINAL_OUTPUT")"
echo "final mkv size bytes: $FINAL_SIZE"
if [[ "$FINAL_SIZE" -le 1024 ]]; then
  echo "[FAIL] final mkv unexpectedly small"
  exit 1
fi

if [[ ! -d "$SEGMENTS_DIR" ]]; then
  echo "[FAIL] segments dir not found: $SEGMENTS_DIR"
  exit 1
fi

echo "segments dir: $SEGMENTS_DIR"
ls -lah "$SEGMENTS_DIR" | sed -n '1,80p'

echo "--- final mkv streams ---"
ffprobe -v error \
  -show_entries stream=index,codec_type,codec_name,start_time,duration \
  -of compact=p=0:nk=1 "$FINAL_OUTPUT" | sed -n '1,120p'

if [[ "$CHECK_COMPOSED_AUDIO_TAIL" == "1" ]]; then
  if (( PUB_USERS < 3 )); then
    echo "[WARN] CHECK_COMPOSED_AUDIO_TAIL=1 requires PUB_USERS>=3 (current: $PUB_USERS). Skipping."
  else
    COMPOSED_OUTPUT="${FINAL_OUTPUT%.mkv}.composed.mp4"
    echo "--- composing review MP4 + checking tail audio ---"
    "$TEST_DIR/bin/compose-recording.sh" \
      --input "$FINAL_OUTPUT" \
      --output "$COMPOSED_OUTPUT" \
      --publisher-log "$PUB_LOG"
    "$TEST_DIR/bin/verify-audio-tail.sh" --input "$COMPOSED_OUTPUT"
    echo "composed output: $COMPOSED_OUTPUT"
  fi
fi

echo "--- key recorder lines ---"
rg -n "talk bootstrap|subscribing to remote session|remote track:|ICE state=connected|duration reached|run error|composed final multi-track output|kept intermediate files" "$REC_LOG" -S || true

if [[ "$CHECK_SESSION_ARTIFACT" == "1" ]]; then
  "$TEST_DIR/bin/verify-session-artifact.sh" \
    --final-output "$FINAL_OUTPUT" \
    --report "${FINAL_OUTPUT}.json"
fi

echo "PASS"
