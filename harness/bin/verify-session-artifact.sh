#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

FINAL_OUTPUT="${FINAL_OUTPUT:-}"
REPORT="${REPORT:-}"
REQUIRE_STREAMS="${REQUIRE_STREAMS:-1}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --final-output)
      FINAL_OUTPUT="$2"
      shift 2
      ;;
    --report)
      REPORT="$2"
      shift 2
      ;;
    --require-streams)
      REQUIRE_STREAMS="$2"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "$FINAL_OUTPUT" ]]; then
  echo "missing --final-output <path>" >&2
  exit 1
fi

if [[ -z "$REPORT" ]]; then
  REPORT="${FINAL_OUTPUT}.json"
fi

SESSION_JSON="$(cassini_session_json_from_mkv "$FINAL_OUTPUT" || true)"
EVENTS_PATH="$(cassini_events_log_from_mkv "$FINAL_OUTPUT" || true)"
STREAMS_DIR="$(cassini_streams_dir_from_mkv "$FINAL_OUTPUT" || true)"

if [[ -z "$SESSION_JSON" || -z "$EVENTS_PATH" || -z "$STREAMS_DIR" ]]; then
  if [[ -f "$REPORT" ]]; then
    if ! command -v jq >/dev/null 2>&1; then
      echo "jq is required to read legacy report fallback: $REPORT" >&2
      exit 1
    fi
    SESSION_JSON="$(jq -r '.session_artifact.session_json // empty' "$REPORT")"
    EVENTS_PATH="$(jq -r '.session_artifact.events_ndjson // empty' "$REPORT")"
    STREAMS_DIR="$(jq -r '.session_artifact.streams_dir // empty' "$REPORT")"
    ENABLED="$(jq -r '.session_artifact.enabled // false' "$REPORT")"
    if [[ "$ENABLED" != "true" ]]; then
      echo "session artifact not enabled according to report; skipping"
      exit 0
    fi
  fi
fi

if [[ -z "$SESSION_JSON" || -z "$EVENTS_PATH" || -z "$STREAMS_DIR" ]]; then
  echo "could not derive session artifact paths from mkv metadata or legacy report" >&2
  exit 1
fi

if [[ ! -f "$SESSION_JSON" ]]; then
  echo "session json not found: $SESSION_JSON" >&2
  exit 1
fi
if [[ ! -f "$EVENTS_PATH" ]]; then
  echo "events log not found: $EVENTS_PATH" >&2
  exit 1
fi
if [[ ! -d "$STREAMS_DIR" ]]; then
  echo "streams directory not found: $STREAMS_DIR" >&2
  exit 1
fi

if ! rg -q '"type"[[:space:]]*:[[:space:]]*"session_started"' "$EVENTS_PATH"; then
  echo "session_started event not found in $EVENTS_PATH" >&2
  exit 1
fi

stream_count="$(find "$STREAMS_DIR" -type f -name '*.rtplog' | wc -l | tr -d ' ')"
if (( REQUIRE_STREAMS == 1 )) && [[ "$stream_count" -lt 1 ]]; then
  echo "no rtplog files found in $STREAMS_DIR" >&2
  exit 1
fi

if [[ "$stream_count" -gt 0 ]]; then
  while IFS= read -r stream; do
    index_path="${stream%.rtplog}.idx"
    if [[ ! -f "$index_path" ]]; then
      echo "missing index for stream: $stream" >&2
      exit 1
    fi

    if (( "$(stat -c '%s' "$index_path")" % 16 != 0 )); then
      echo "stream index has invalid entry width: $index_path" >&2
      exit 1
    fi
    if ! cmp -s <(printf 'RTPL0\0\0\0') <(head -c 8 "$stream"); then
      echo "invalid stream header magic: $stream" >&2
      exit 1
    fi
  done < <(find "$STREAMS_DIR" -type f -name '*.rtplog' -print)
fi

log "session artifact verification passed"
log "  session_json: $SESSION_JSON"
log "  events_log: $EVENTS_PATH"
log "  streams_dir: $STREAMS_DIR"
log "  stream_count: $stream_count"
