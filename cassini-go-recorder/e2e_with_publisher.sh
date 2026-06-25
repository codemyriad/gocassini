#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"
HARNESS_DIR="$ROOT_DIR/../harness"
# shellcheck source=../harness/bin/common.sh
source "$HARNESS_DIR/bin/common.sh"

CALL_URL="${CALL_URL:-}"
CALL_NAME="${CALL_NAME:-Cassini Go E2E room}"
REC_DURATION="${REC_DURATION:-55}"
PUB_DURATION="${PUB_DURATION:-24}"
PUB_USERS="${PUB_USERS:-2}"
START_DELAY="${START_DELAY:-6}"
OUTPUT="${OUTPUT:-/tmp/gocassini-e2e.mkv}"
FINAL_OUTPUT="${FINAL_OUTPUT:-}"
if [[ -z "$FINAL_OUTPUT" ]]; then
  if [[ "$OUTPUT" == *.mkv ]]; then
    FINAL_OUTPUT="$OUTPUT"
  elif [[ "$OUTPUT" == *.csr ]]; then
    FINAL_OUTPUT="${OUTPUT%.csr}.mkv"
  else
    FINAL_OUTPUT="${OUTPUT}.mkv"
  fi
fi
NAME="${NAME:-GocassiniBot}"
MEDIA_PREFIX="${MEDIA_PREFIX:-}"
MEDIA_PREFIXES="${MEDIA_PREFIXES:-}"
NAMES="${NAMES:-}"
JOIN_DELAYS="${JOIN_DELAYS:-}"
AUDIO_READY_AFTERS="${AUDIO_READY_AFTERS:-}"
SYNC_SHIFTS="${SYNC_SHIFTS:-}"
BOT_DURATIONS="${BOT_DURATIONS:-}"
MUTE_ROTATION_START_FILE="${MUTE_ROTATION_START_FILE:-}"
CAPTURE_READY_PARTICIPANTS="${CAPTURE_READY_PARTICIPANTS:-0}"
CAPTURE_READY_TIMEOUT="${CAPTURE_READY_TIMEOUT:-35}"
CAPTURE_READY_STREAM_KIND="${CAPTURE_READY_STREAM_KIND:-video}"

REC_LOG="${REC_LOG:-/tmp/gocassini-e2e.log}"
PUB_LOG="${PUB_LOG:-/tmp/gocassini-publisher-e2e.log}"
CHECK_SESSION_ARTIFACT="${CHECK_SESSION_ARTIFACT:-1}"
CHECK_ARTIFACT_REMUX="${CHECK_ARTIFACT_REMUX:-1}"

if ! [[ "$CAPTURE_READY_PARTICIPANTS" =~ ^[0-9]+$ ]]; then
  echo "CAPTURE_READY_PARTICIPANTS must be a non-negative integer" >&2
  exit 2
fi
if ! [[ "$CAPTURE_READY_TIMEOUT" =~ ^[0-9]+$ ]]; then
  echo "CAPTURE_READY_TIMEOUT must be a non-negative integer number of seconds" >&2
  exit 2
fi
if [[ -z "$MUTE_ROTATION_START_FILE" && "$CAPTURE_READY_PARTICIPANTS" -gt 0 ]]; then
  MUTE_ROTATION_START_FILE="${FINAL_OUTPUT}.mute-start"
fi

rm -f "$OUTPUT" "$FINAL_OUTPUT" "$REC_LOG" "$PUB_LOG"
if [[ -n "$MUTE_ROTATION_START_FILE" ]]; then
  rm -f "$MUTE_ROTATION_START_FILE"
fi

if [[ -z "$CALL_URL" && -f "$HARNESS_DIR/runtime/last_call_url" ]]; then
  CALL_URL="$(cat "$HARNESS_DIR/runtime/last_call_url")"
fi

if [[ -z "$CALL_URL" ]]; then
  if [[ ! -x "$HARNESS_DIR/bin/create-room.sh" ]]; then
    echo "missing CALL_URL and local create-room helper is unavailable" >&2
    echo "set CALL_URL or run ./harness/bin/ci-e2e.sh" >&2
    exit 1
  fi

  echo "CALL_URL not provided, creating room in local test stack..."
  if ! CALL_URL="$("$HARNESS_DIR/bin/create-room.sh" --name "$CALL_NAME" | tail -n1)"; then
    echo "could not create local room. Start local stack with ./harness/bin/up.sh or use ./harness/bin/ci-e2e.sh" >&2
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
echo "MUTE_ROTATION_START_FILE=$MUTE_ROTATION_START_FILE"
echo "CAPTURE_READY_PARTICIPANTS=$CAPTURE_READY_PARTICIPANTS"
echo "CAPTURE_READY_TIMEOUT=$CAPTURE_READY_TIMEOUT"
echo "CAPTURE_READY_STREAM_KIND=$CAPTURE_READY_STREAM_KIND"
echo "OUTPUT=$OUTPUT"
echo "FINAL_OUTPUT=$FINAL_OUTPUT"
echo "SEGMENTS_DIR=$SEGMENTS_DIR"

capture_events_files() {
  local session_root="$SEGMENTS_PARENT/sessions"
  [[ -d "$session_root" ]] || return 0
  find "$session_root" \
    -maxdepth 2 \
    -type f \
    -name events.ndjson \
    -path "$session_root/${SEGMENTS_BASE}_*/events.ndjson" \
    -print
}

capture_ready_count() {
  local kind="$1"
  local -a event_files=()
  mapfile -t event_files < <(capture_events_files)
  if (( ${#event_files[@]} == 0 )); then
    echo 0
    return 0
  fi
  jq -r --arg kind "$kind" '
    select(.type == "stream_opened" and ((.kind // "") == $kind))
    | (.participant_id // .participant_name // .remote_session_id // empty)
  ' "${event_files[@]}" 2>/dev/null \
    | sort -u \
    | awk 'NF{n++} END {print n+0}'
}

wait_for_capture_ready() {
  local expected="$1"
  local timeout_s="$2"
  local gate_file="$3"
  local kind="$4"
  local end_time=$((SECONDS + timeout_s))
  local last_count=-1

  if (( expected < 1 )); then
    return 0
  fi
  if ! command -v jq >/dev/null 2>&1; then
    echo "[FAIL] jq is required for CAPTURE_READY_PARTICIPANTS readiness gating" >&2
    return 1
  fi
  if [[ -z "$gate_file" ]]; then
    echo "[FAIL] MUTE_ROTATION_START_FILE is required for capture readiness gating" >&2
    return 1
  fi

  mkdir -p "$(dirname "$gate_file")"
  echo "waiting for recorder capture readiness: expected=${expected} kind=${kind} timeout=${timeout_s}s"
  while (( SECONDS <= end_time )); do
    count="$(capture_ready_count "$kind" 2>/dev/null || true)"
    if ! [[ "$count" =~ ^[0-9]+$ ]]; then
      count=0
    fi
    if [[ "$count" != "$last_count" ]]; then
      echo "capture readiness: ${count}/${expected} distinct participants have ${kind} stream_opened"
      last_count="$count"
    fi
    if (( count >= expected )); then
      : >"$gate_file"
      echo "capture readiness reached; opened mute rotation gate: $gate_file"
      return 0
    fi
    sleep 1
  done

  echo "[FAIL] recorder capture readiness timed out: got ${last_count}/${expected} distinct participants with ${kind} stream_opened" >&2
  echo "--- session artifact events tail ---" >&2
  while IFS= read -r events_path; do
    echo "events: $events_path" >&2
    tail -n 40 "$events_path" >&2 || true
  done < <(capture_events_files)
  return 1
}

stop_background_processes() {
  local pid
  for pid in "${PUB_PID:-}" "${REC_PID:-}"; do
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
    fi
  done
}

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
  cd "$HARNESS_DIR"
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
  if [[ -n "$MUTE_ROTATION_START_FILE" ]]; then
    STREAM_ARGS+=(--mute-start-file "$MUTE_ROTATION_START_FILE")
  fi
  ./bin/stream-video.sh "${STREAM_ARGS[@]}"
) >"$PUB_LOG" 2>&1 &
PUB_PID=$!

if (( CAPTURE_READY_PARTICIPANTS > 0 )); then
  if ! wait_for_capture_ready "$CAPTURE_READY_PARTICIPANTS" "$CAPTURE_READY_TIMEOUT" "$MUTE_ROTATION_START_FILE" "$CAPTURE_READY_STREAM_KIND"; then
    echo "--- key recorder lines ---"
    rg -n "talk bootstrap|subscribing to remote session|remote track:|ICE state=|duration reached|run error|composed final multi-track output|kept intermediate files" "$REC_LOG" -S || true
    stop_background_processes
    wait "$PUB_PID" 2>/dev/null || true
    wait "$REC_PID" 2>/dev/null || true
    exit 1
  fi
fi

wait "$PUB_PID" || true

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

echo "--- key recorder lines ---"
# Match every "ICE state=" transition (not just =connected): the recorder logs
# each subscriber ICE transition, so surfacing failed/disconnected/checking states
# reveals which sessions never reached ICE-connected (and thus captured no media)
# when the multi-participant assertion in ci-e2e-mute.sh comes up short (flake D-052).
rg -n "talk bootstrap|subscribing to remote session|remote track:|ICE state=|duration reached|run error|composed final multi-track output|kept intermediate files" "$REC_LOG" -S || true

if [[ "$CHECK_SESSION_ARTIFACT" == "1" ]]; then
  "$TEST_DIR/bin/verify-session-artifact.sh" --final-output "$FINAL_OUTPUT"
fi

if [[ "$CHECK_ARTIFACT_REMUX" == "1" ]]; then
  SESSION_JSON="$(cassini_session_json_from_mkv "$FINAL_OUTPUT" || true)"
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
    echo "[WARN] session artifact json could not be derived from mkv metadata; skipping artifact remux check"
  fi
fi

echo "PASS"
