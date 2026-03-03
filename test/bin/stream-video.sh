#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

CALL_URL="${CALL_URL:-}"
USERS="${USERS:-1}"
DURATION="${DURATION:-20}"
NAME_PREFIX="${NAME_PREFIX:-HarnessBot}"
MEDIA_PREFIX="${MEDIA_PREFIX:-}"
PREPARE="${PREPARE:-1}"
ROTATE_SECONDS="${ROTATE_SECONDS:-5}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --call-url)
      CALL_URL="$2"
      shift 2
      ;;
    --users)
      USERS="$2"
      shift 2
      ;;
    --duration)
      DURATION="$2"
      shift 2
      ;;
    --name-prefix)
      NAME_PREFIX="$2"
      shift 2
      ;;
    --media)
      MEDIA_PREFIX="$2"
      shift 2
      ;;
    --media-prefix)
      MEDIA_PREFIX="$2"
      shift 2
      ;;
    --rotate-seconds)
      ROTATE_SECONDS="$2"
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

if ! [[ "$USERS" =~ ^[0-9]+$ ]] || (( USERS < 1 || USERS > 3 )); then
  echo "--users must be an integer between 1 and 3" >&2
  exit 1
fi

if [[ "$PREPARE" == "1" && -z "$MEDIA_PREFIX" ]]; then
  MEDIA_PREFIX="$("$SCRIPT_DIR/prepare-media.sh" | tail -n1)"
fi
if [[ -z "$MEDIA_PREFIX" ]]; then
  MEDIA_PREFIX="$MEDIA_DIR/sample"
fi
if [[ "$MEDIA_PREFIX" == *.ivf ]]; then
  MEDIA_PREFIX="${MEDIA_PREFIX%.ivf}"
elif [[ "$MEDIA_PREFIX" == *.ogg ]]; then
  MEDIA_PREFIX="${MEDIA_PREFIX%.ogg}"
elif [[ "$MEDIA_PREFIX" == *.mp4 ]]; then
  MEDIA_PREFIX="${MEDIA_PREFIX%.mp4}"
fi

VIDEO_FILE="${MEDIA_PREFIX}.ivf"
AUDIO_FILE="${MEDIA_PREFIX}.ogg"
if [[ ! -f "$VIDEO_FILE" || ! -f "$AUDIO_FILE" ]]; then
  echo "required media files not found: $VIDEO_FILE and $AUDIO_FILE" >&2
  echo "run: $SCRIPT_DIR/prepare-media.sh --prefix $MEDIA_PREFIX" >&2
  exit 1
fi

GO_ROTATOR_DIR="${GO_ROTATOR_DIR:-$TEST_DIR/go-talk-rotator}"
if [[ ! -d "$GO_ROTATOR_DIR" || ! -f "$GO_ROTATOR_DIR/main.go" ]]; then
  echo "go rotator not found: $GO_ROTATOR_DIR" >&2
  exit 1
fi
if ! command -v go >/dev/null 2>&1; then
  echo "go is required to run the Go rotator" >&2
  exit 1
fi

log "Call URL: $CALL_URL"
log "Media prefix: $MEDIA_PREFIX"
log "Users: $USERS"

CMD_ARGS=(
  --call-url "$CALL_URL"
  --duration "$DURATION"
  --rotate-seconds "$ROTATE_SECONDS"
)

for ((i = 1; i <= USERS; i++)); do
  CMD_ARGS+=(--video "$VIDEO_FILE")
  CMD_ARGS+=(--audio "$AUDIO_FILE")
  CMD_ARGS+=(--name "${NAME_PREFIX}${i}")
  CMD_ARGS+=(--join-delay "$((i - 1))")
  CMD_ARGS+=(--audio-ready-after 0)
  CMD_ARGS+=(--sync-shift 0)
done

(
  cd "$GO_ROTATOR_DIR"
  go run . "${CMD_ARGS[@]}"
)
