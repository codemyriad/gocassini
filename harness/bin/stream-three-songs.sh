#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

CALL_URL="${CALL_URL:-}"
DURATION="${DURATION:-}"
NAME_PREFIX="${NAME_PREFIX:-}"
GIULIA_NAME="${GIULIA_NAME:-Le Vibrazioni - Giulia}"
SPALMAN_NAME="${SPALMAN_NAME:-Elio e le Storie Tese - Spalman}"
FRANKIE_NAME="${FRANKIE_NAME:-Frankie Hi-NRG MC - Chiedi Chiedi}"
PREPARE="${PREPARE:-1}"
VIBRAZIONI_JOIN_DELAY="${VIBRAZIONI_JOIN_DELAY:-0}"
GIULIA_JOIN_DELAY="${GIULIA_JOIN_DELAY:-4}"
FRANKIE_JOIN_DELAY="${FRANKIE_JOIN_DELAY:-8}"
GIULIA_AUDIO_READY_AFTER="${GIULIA_AUDIO_READY_AFTER:-7}"
VIBRAZIONI_AUDIO_READY_AFTER="${VIBRAZIONI_AUDIO_READY_AFTER:-0}"
FRANKIE_AUDIO_READY_AFTER="${FRANKIE_AUDIO_READY_AFTER:-5}"
GIULIA_SYNC_SHIFT="${GIULIA_SYNC_SHIFT:-0}"
VIBRAZIONI_SYNC_SHIFT="${VIBRAZIONI_SYNC_SHIFT:-0}"
FRANKIE_SYNC_SHIFT="${FRANKIE_SYNC_SHIFT:-0}"

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
    --join-delay-giulia)
      GIULIA_JOIN_DELAY="$2"
      shift 2
      ;;
    --join-delay-vibrazioni)
      VIBRAZIONI_JOIN_DELAY="$2"
      shift 2
      ;;
    --join-delay-frankie)
      FRANKIE_JOIN_DELAY="$2"
      shift 2
      ;;
    --name-prefix)
      NAME_PREFIX="$2"
      shift 2
      ;;
    --name-giulia)
      GIULIA_NAME="$2"
      shift 2
      ;;
    --name-spalman)
      SPALMAN_NAME="$2"
      shift 2
      ;;
    --name-frankie)
      FRANKIE_NAME="$2"
      shift 2
      ;;
    --audio-ready-after-giulia)
      GIULIA_AUDIO_READY_AFTER="$2"
      shift 2
      ;;
    --audio-ready-after-vibrazioni)
      VIBRAZIONI_AUDIO_READY_AFTER="$2"
      shift 2
      ;;
    --audio-ready-after-frankie)
      FRANKIE_AUDIO_READY_AFTER="$2"
      shift 2
      ;;
    --sync-shift-giulia)
      GIULIA_SYNC_SHIFT="$2"
      shift 2
      ;;
    --sync-shift-vibrazioni)
      VIBRAZIONI_SYNC_SHIFT="$2"
      shift 2
      ;;
    --sync-shift-frankie)
      FRANKIE_SYNC_SHIFT="$2"
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

if [[ "$PREPARE" == "1" ]]; then
  "$SCRIPT_DIR/prepare-youtube-set.sh"
fi

WEBRTC_DIR="${WEBRTC_DIR:-$MEDIA_DIR/youtube/webrtc}"
GO_ROTATOR_DIR="${GO_ROTATOR_DIR:-$TEST_DIR/go-talk-rotator}"

GIULIA_VIDEO="$WEBRTC_DIR/giulia-aligned.ivf"
GIULIA_AUDIO="$WEBRTC_DIR/giulia-aligned.ogg"
VIBRAZIONI_VIDEO="$WEBRTC_DIR/vibrazioni-aligned.ivf"
VIBRAZIONI_AUDIO="$WEBRTC_DIR/vibrazioni-aligned.ogg"
FRANKIE_VIDEO="$WEBRTC_DIR/frankie-aligned.ivf"
FRANKIE_AUDIO="$WEBRTC_DIR/frankie-aligned.ogg"

for f in \
  "$GIULIA_VIDEO" "$GIULIA_AUDIO" \
  "$VIBRAZIONI_VIDEO" "$VIBRAZIONI_AUDIO" \
  "$FRANKIE_VIDEO" "$FRANKIE_AUDIO"; do
  if [[ ! -f "$f" ]]; then
    echo "required prepared media missing: $f" >&2
    echo "run: $SCRIPT_DIR/prepare-youtube-set.sh" >&2
    exit 1
  fi
done

if [[ ! -d "$GO_ROTATOR_DIR" || ! -f "$GO_ROTATOR_DIR/main.go" ]]; then
  echo "go rotator not found: $GO_ROTATOR_DIR" >&2
  exit 1
fi
if ! command -v go >/dev/null 2>&1; then
  echo "go is required to run the Go rotator" >&2
  exit 1
fi

log "Using Go rotator: $GO_ROTATOR_DIR"
log "Call URL: $CALL_URL"
log "Launching 3 clients with full-length aligned media and rotating mute (Giulia, Vibrazioni, Frankie)"
log "Join delays: Giulia=${GIULIA_JOIN_DELAY}s Vibrazioni=${VIBRAZIONI_JOIN_DELAY}s Frankie=${FRANKIE_JOIN_DELAY}s"
log "Audio-ready delays: Giulia=${GIULIA_AUDIO_READY_AFTER}s Vibrazioni=${VIBRAZIONI_AUDIO_READY_AFTER}s Frankie=${FRANKIE_AUDIO_READY_AFTER}s"
log "Sync shifts: Giulia=${GIULIA_SYNC_SHIFT}s Vibrazioni=${VIBRAZIONI_SYNC_SHIFT}s Frankie=${FRANKIE_SYNC_SHIFT}s"

BOT_GIULIA_NAME="$GIULIA_NAME"
BOT_SPALMAN_NAME="$SPALMAN_NAME"
BOT_FRANKIE_NAME="$FRANKIE_NAME"
if [[ -n "$NAME_PREFIX" ]]; then
  BOT_GIULIA_NAME="${NAME_PREFIX} - ${GIULIA_NAME}"
  BOT_SPALMAN_NAME="${NAME_PREFIX} - ${SPALMAN_NAME}"
  BOT_FRANKIE_NAME="${NAME_PREFIX} - ${FRANKIE_NAME}"
fi

CMD_ARGS=(
  --call-url "$CALL_URL"
  --video "$GIULIA_VIDEO"
  --audio "$GIULIA_AUDIO"
  --video "$VIBRAZIONI_VIDEO"
  --audio "$VIBRAZIONI_AUDIO"
  --video "$FRANKIE_VIDEO"
  --audio "$FRANKIE_AUDIO"
  --name "$BOT_GIULIA_NAME"
  --name "$BOT_SPALMAN_NAME"
  --name "$BOT_FRANKIE_NAME"
  --join-delay "$GIULIA_JOIN_DELAY"
  --join-delay "$VIBRAZIONI_JOIN_DELAY"
  --join-delay "$FRANKIE_JOIN_DELAY"
  --audio-ready-after "$GIULIA_AUDIO_READY_AFTER"
  --audio-ready-after "$VIBRAZIONI_AUDIO_READY_AFTER"
  --audio-ready-after "$FRANKIE_AUDIO_READY_AFTER"
  --sync-shift "$GIULIA_SYNC_SHIFT"
  --sync-shift "$VIBRAZIONI_SYNC_SHIFT"
  --sync-shift "$FRANKIE_SYNC_SHIFT"
  --rotate-seconds 5
)

if [[ -n "$DURATION" ]]; then
  CMD_ARGS+=(--duration "$DURATION")
  log "Using explicit duration override for all bots: ${DURATION}s"
else
  log "Using full media lengths (run until EOF)"
fi

(
  cd "$GO_ROTATOR_DIR"
  go run . "${CMD_ARGS[@]}"
)
