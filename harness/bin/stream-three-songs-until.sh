#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

CALL_URL="${CALL_URL:-}"
UNTIL="${UNTIL:-08:00}"
SLEEP_SECONDS="${SLEEP_SECONDS:-3}"
PREPARE="${PREPARE:-1}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --call-url)
      CALL_URL="$2"
      shift 2
      ;;
    --until)
      UNTIL="$2"
      shift 2
      ;;
    --sleep-seconds)
      SLEEP_SECONDS="$2"
      shift 2
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

if ! END_TS="$(date -d "$UNTIL" '+%s' 2>/dev/null)"; then
  echo "invalid --until value: $UNTIL" >&2
  echo "examples: --until '08:00' or --until '2026-03-03 08:00'" >&2
  exit 1
fi

NOW_TS="$(date '+%s')"
if [[ "$END_TS" -le "$NOW_TS" && "$UNTIL" =~ ^[0-9]{1,2}:[0-9]{2}$ ]]; then
  END_TS="$(date -d "tomorrow $UNTIL" '+%s')"
fi

if [[ "$END_TS" -le "$NOW_TS" ]]; then
  echo "--until resolves to a past time: $UNTIL" >&2
  exit 1
fi

LOG_FILE="${RUNTIME_DIR}/stream-three-songs-until-$(date -u +%Y%m%d-%H%M%S).log"
ATTEMPT=0

log "Continuous run call URL: $CALL_URL"
log "Stopping at: $(date -d "@$END_TS" '+%Y-%m-%d %H:%M:%S %Z')"
log "Log file: $LOG_FILE"

while [[ "$(date '+%s')" -lt "$END_TS" ]]; do
  ATTEMPT=$((ATTEMPT + 1))
  START_LABEL="$(date '+%Y-%m-%dT%H:%M:%S%z')"
  log "Attempt $ATTEMPT start $START_LABEL"
  {
    echo "[attempt $ATTEMPT] start=$START_LABEL"
  } >>"$LOG_FILE"

  CMD=("$SCRIPT_DIR/stream-three-songs.sh" --call-url "$CALL_URL")
  if [[ "$PREPARE" == "0" ]]; then
    CMD+=(--skip-prepare)
  fi

  set +e
  "${CMD[@]}" >>"$LOG_FILE" 2>&1
  RC=$?
  set -e

  END_LABEL="$(date '+%Y-%m-%dT%H:%M:%S%z')"
  {
    echo "[attempt $ATTEMPT] end=$END_LABEL exit_code=$RC"
  } >>"$LOG_FILE"
  log "Attempt $ATTEMPT end $END_LABEL exit_code=$RC"

  if [[ "$(date '+%s')" -lt "$END_TS" ]]; then
    sleep "$SLEEP_SECONDS"
  fi
done

log "Reached stop time: $(date '+%Y-%m-%d %H:%M:%S %Z')"
