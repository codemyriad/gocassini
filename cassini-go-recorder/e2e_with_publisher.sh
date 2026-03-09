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
OUTPUT="${OUTPUT:-/tmp/gocassini-e2e.requested-output}"
FINAL_OUTPUT="${FINAL_OUTPUT:-${OUTPUT%.csr}.mkv}"
CHECK_COMPOSED_AUDIO_TAIL="${CHECK_COMPOSED_AUDIO_TAIL:-0}"
NAME="${NAME:-GocassiniBot}"
MEDIA_PREFIX="${MEDIA_PREFIX:-}"
MEDIA_PREFIXES="${MEDIA_PREFIXES:-}"
NAMES="${NAMES:-}"
JOIN_DELAYS="${JOIN_DELAYS:-}"
AUDIO_READY_AFTERS="${AUDIO_READY_AFTERS:-}"
SYNC_SHIFTS="${SYNC_SHIFTS:-}"
BOT_DURATIONS="${BOT_DURATIONS:-}"

REC_LOG="${REC_LOG:-/tmp/gocassini-e2e.log}"
PUB_LOG="${PUB_LOG:-/tmp/gocassini-publisher-e2e.log}"
CHECK_SESSION_ARTIFACT="${CHECK_SESSION_ARTIFACT:-1}"
CHECK_ARTIFACT_REMUX="${CHECK_ARTIFACT_REMUX:-1}"

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
echo "MEDIA_PREFIX=$MEDIA_PREFIX"
echo "MEDIA_PREFIXES=$MEDIA_PREFIXES"
echo "NAMES=$NAMES"
echo "JOIN_DELAYS=$JOIN_DELAYS"
echo "AUDIO_READY_AFTERS=$AUDIO_READY_AFTERS"
echo "SYNC_SHIFTS=$SYNC_SHIFTS"
echo "BOT_DURATIONS=$BOT_DURATIONS"
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
  STREAM_ARGS=(
    --call-url "$CALL_URL"
    --users "$PUB_USERS"
    --duration "$PUB_DURATION"
    --name-prefix "CassiniGoE2E"
  )
  if [[ -n "$MEDIA_PREFIX" ]]; then
    STREAM_ARGS+=(--media-prefix "$MEDIA_PREFIX")
  fi
  if [[ -n "$MEDIA_PREFIXES" ]]; then
    IFS=',' read -r -a MEDIA_ITEMS <<<"$MEDIA_PREFIXES"
    for prefix in "${MEDIA_ITEMS[@]}"; do
      trimmed="$(echo "$prefix" | xargs)"
      if [[ -n "$trimmed" ]]; then
        STREAM_ARGS+=(--media-prefix "$trimmed")
      fi
    done
  fi
  if [[ -n "$NAMES" ]]; then
    STREAM_ARGS+=(--names "$NAMES")
  fi
  if [[ -n "$JOIN_DELAYS" ]]; then
    STREAM_ARGS+=(--join-delays "$JOIN_DELAYS")
  fi
  if [[ -n "$AUDIO_READY_AFTERS" ]]; then
    STREAM_ARGS+=(--audio-ready-afters "$AUDIO_READY_AFTERS")
  fi
  if [[ -n "$SYNC_SHIFTS" ]]; then
    STREAM_ARGS+=(--sync-shifts "$SYNC_SHIFTS")
  fi
  if [[ -n "$BOT_DURATIONS" ]]; then
    STREAM_ARGS+=(--bot-durations "$BOT_DURATIONS")
  fi
  ./bin/stream-video.sh "${STREAM_ARGS[@]}"
) >"$PUB_LOG" 2>&1 || true

wait "$REC_PID" || true

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

if [[ "$CHECK_ARTIFACT_REMUX" == "1" ]]; then
  if command -v jq >/dev/null 2>&1; then
    SESSION_JSON="$(jq -r '.session_artifact.session_json // empty' "${FINAL_OUTPUT}.json")"
    if [[ -n "$SESSION_JSON" && -f "$SESSION_JSON" ]]; then
      REMUX_OUTPUT="${FINAL_OUTPUT%.mkv}.artifact-remux.mkv"
      (
        cd "$ROOT_DIR"
        go run ./cmd/gocassini-remux \
          --session "$SESSION_JSON" \
          --output "$REMUX_OUTPUT"
      )
      if [[ ! -s "$REMUX_OUTPUT" ]]; then
        echo "[FAIL] artifact remux output missing or empty: $REMUX_OUTPUT"
        exit 1
      fi
      echo "--- artifact remux streams ---"
      ffprobe -v error \
        -show_entries stream=index,codec_type,codec_name,start_time,duration \
        -of compact=p=0:nk=1 "$REMUX_OUTPUT" | sed -n '1,120p'
    else
      echo "[WARN] session artifact json missing in report; skipping artifact remux check"
    fi
  else
    echo "[WARN] jq not available; skipping artifact remux check"
  fi
fi

echo "PASS"
