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
MEDIA_PREFIXES="${MEDIA_PREFIXES:-}"
NAMES="${NAMES:-}"
JOIN_DELAYS="${JOIN_DELAYS:-}"
AUDIO_READY_AFTERS="${AUDIO_READY_AFTERS:-}"
SYNC_SHIFTS="${SYNC_SHIFTS:-}"
BOT_DURATIONS="${BOT_DURATIONS:-}"
AUTH_USERS="${AUTH_USERS:-}"
AUTH_PASSWORDS="${AUTH_PASSWORDS:-}"
MUTE_ROTATION_START_FILE="${MUTE_ROTATION_START_FILE:-}"
RECORD_BEFORE_MEDIA="${RECORD_BEFORE_MEDIA:-0}"
RECORDING_STARTER_INDEX="${RECORDING_STARTER_INDEX:-1}"
RECORDING_STARTER_AUTH_USER="${RECORDING_STARTER_AUTH_USER:-}"
RECORDING_STARTER_AUTH_PASSWORD="${RECORDING_STARTER_AUTH_PASSWORD:-}"
RECORDING_STARTER_NAME="${RECORDING_STARTER_NAME:-}"
RECORDING_TIMEOUT="${RECORDING_TIMEOUT:-90}"
PREPARE="${PREPARE:-1}"
ROTATE_SECONDS="${ROTATE_SECONDS:-5}"
declare -a MEDIA_PREFIX_LIST=()
declare -a NAME_LIST=()
declare -a JOIN_DELAY_LIST=()
declare -a AUDIO_READY_LIST=()
declare -a SYNC_SHIFT_LIST=()
declare -a BOT_DURATION_LIST=()
declare -a AUTH_USER_LIST=()
declare -a AUTH_PASSWORD_LIST=()

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
    --names)
      NAMES="$2"
      shift 2
      ;;
    --media)
      MEDIA_PREFIX_LIST+=("$2")
      shift 2
      ;;
    --media-prefix)
      MEDIA_PREFIX_LIST+=("$2")
      shift 2
      ;;
    --media-prefixes)
      MEDIA_PREFIXES="$2"
      shift 2
      ;;
    --rotate-seconds)
      ROTATE_SECONDS="$2"
      shift 2
      ;;
    --join-delays)
      JOIN_DELAYS="$2"
      shift 2
      ;;
    --audio-ready-afters)
      AUDIO_READY_AFTERS="$2"
      shift 2
      ;;
    --sync-shifts)
      SYNC_SHIFTS="$2"
      shift 2
      ;;
    --bot-durations)
      BOT_DURATIONS="$2"
      shift 2
      ;;
    --mute-start-file)
      MUTE_ROTATION_START_FILE="$2"
      shift 2
      ;;
    --auth-users)
      AUTH_USERS="$2"
      shift 2
      ;;
    --auth-passwords)
      AUTH_PASSWORDS="$2"
      shift 2
      ;;
    --record-before-media)
      RECORD_BEFORE_MEDIA=1
      shift
      ;;
    --recording-starter-index)
      RECORDING_STARTER_INDEX="$2"
      shift 2
      ;;
    --recording-starter-auth-user)
      RECORDING_STARTER_AUTH_USER="$2"
      shift 2
      ;;
    --recording-starter-auth-password)
      RECORDING_STARTER_AUTH_PASSWORD="$2"
      shift 2
      ;;
    --recording-starter-name)
      RECORDING_STARTER_NAME="$2"
      shift 2
      ;;
    --recording-timeout)
      RECORDING_TIMEOUT="$2"
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

if ! [[ "$USERS" =~ ^[0-9]+$ ]] || (( USERS < 1 )); then
  echo "--users must be a positive integer" >&2
  exit 1
fi

split_csv_into() {
  local csv="$1"
  local -n out_ref="$2"
  out_ref=()
  if [[ -z "$csv" ]]; then
    return 0
  fi
  IFS=',' read -r -a items <<<"$csv"
  for item in "${items[@]}"; do
    trimmed="$(echo "$item" | xargs)"
    if [[ -n "$trimmed" ]]; then
      out_ref+=("$trimmed")
    fi
  done
}

split_csv_into "$NAMES" NAME_LIST
split_csv_into "$JOIN_DELAYS" JOIN_DELAY_LIST
split_csv_into "$AUDIO_READY_AFTERS" AUDIO_READY_LIST
split_csv_into "$SYNC_SHIFTS" SYNC_SHIFT_LIST
split_csv_into "$BOT_DURATIONS" BOT_DURATION_LIST
split_csv_into "$AUTH_USERS" AUTH_USER_LIST
split_csv_into "$AUTH_PASSWORDS" AUTH_PASSWORD_LIST

for pair in \
  "names:${#NAME_LIST[@]}" \
  "join-delays:${#JOIN_DELAY_LIST[@]}" \
  "audio-ready-afters:${#AUDIO_READY_LIST[@]}" \
  "sync-shifts:${#SYNC_SHIFT_LIST[@]}" \
  "bot-durations:${#BOT_DURATION_LIST[@]}"; do
  key="${pair%%:*}"
  count="${pair##*:}"
  if (( count > USERS )); then
    echo "--$key count ($count) exceeds --users ($USERS)" >&2
    exit 1
  fi
done

if (( ${#AUTH_USER_LIST[@]} > 0 || ${#AUTH_PASSWORD_LIST[@]} > 0 )); then
  if (( ${#AUTH_USER_LIST[@]} != USERS )); then
    echo "--auth-users count (${#AUTH_USER_LIST[@]}) must match --users ($USERS)" >&2
    exit 1
  fi
  if (( ${#AUTH_PASSWORD_LIST[@]} != USERS )); then
    echo "--auth-passwords count (${#AUTH_PASSWORD_LIST[@]}) must match --users ($USERS)" >&2
    exit 1
  fi
fi
if [[ -n "$RECORDING_STARTER_AUTH_USER" || -n "$RECORDING_STARTER_AUTH_PASSWORD" ]]; then
  if [[ -z "$RECORDING_STARTER_AUTH_USER" || -z "$RECORDING_STARTER_AUTH_PASSWORD" ]]; then
    echo "--recording-starter-auth-user and --recording-starter-auth-password must be provided together" >&2
    exit 1
  fi
  if [[ "$RECORD_BEFORE_MEDIA" != "1" ]]; then
    echo "--recording-starter-auth-user requires --record-before-media" >&2
    exit 1
  fi
fi
if [[ "$RECORD_BEFORE_MEDIA" == "1" && "${#AUTH_USER_LIST[@]}" -eq 0 && -z "$RECORDING_STARTER_AUTH_USER" ]]; then
  echo "--record-before-media requires --auth-users/--auth-passwords or --recording-starter-auth-user/--recording-starter-auth-password" >&2
  exit 1
fi
if [[ "$RECORD_BEFORE_MEDIA" == "1" && -z "$RECORDING_STARTER_AUTH_USER" ]]; then
  if ! [[ "$RECORDING_STARTER_INDEX" =~ ^[0-9]+$ ]] || (( RECORDING_STARTER_INDEX < 1 || RECORDING_STARTER_INDEX > USERS )); then
    echo "--recording-starter-index must be between 1 and --users ($USERS)" >&2
    exit 1
  fi
fi

if [[ -n "$MEDIA_PREFIXES" ]]; then
  split_csv_into "$MEDIA_PREFIXES" csv_media
  MEDIA_PREFIX_LIST+=("${csv_media[@]}")
fi
if [[ "${#MEDIA_PREFIX_LIST[@]}" -eq 0 && -n "$MEDIA_PREFIX" ]]; then
  MEDIA_PREFIX_LIST=("$MEDIA_PREFIX")
fi
if [[ "${#MEDIA_PREFIX_LIST[@]}" -eq 0 && "$PREPARE" == "1" ]]; then
  MEDIA_PREFIX_LIST=("$("$SCRIPT_DIR/prepare-media.sh" | tail -n1)")
fi
if [[ "${#MEDIA_PREFIX_LIST[@]}" -eq 0 ]]; then
  MEDIA_PREFIX_LIST=("$MEDIA_DIR/sample")
fi
if [[ "${#MEDIA_PREFIX_LIST[@]}" -gt 1 && "${#MEDIA_PREFIX_LIST[@]}" -ne "$USERS" ]]; then
  echo "when multiple --media-prefix values are provided, count must match --users" >&2
  exit 1
fi

normalize_prefix() {
  local value="$1"
  if [[ "$value" == *.ivf ]]; then
    value="${value%.ivf}"
  elif [[ "$value" == *.ogg ]]; then
    value="${value%.ogg}"
  elif [[ "$value" == *.mp4 ]]; then
    value="${value%.mp4}"
  fi
  echo "$value"
}

resolve_media_prefix() {
  local user_index="$1"
  if [[ "${#MEDIA_PREFIX_LIST[@]}" -eq 1 ]]; then
    echo "$(normalize_prefix "${MEDIA_PREFIX_LIST[0]}")"
    return 0
  fi
  echo "$(normalize_prefix "${MEDIA_PREFIX_LIST[$((user_index - 1))]}")"
}

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
log "Media prefixes configured: ${#MEDIA_PREFIX_LIST[@]}"
log "Users: $USERS"
if [[ "${#NAME_LIST[@]}" -gt 0 ]]; then
  log "Custom names configured: ${#NAME_LIST[@]}"
fi
if [[ "${#AUTH_USER_LIST[@]}" -gt 0 ]]; then
  log "Authenticated users configured: ${#AUTH_USER_LIST[@]}"
fi
if [[ "$RECORD_BEFORE_MEDIA" == "1" ]]; then
  if [[ -n "$RECORDING_STARTER_AUTH_USER" ]]; then
    log "Recording gate enabled: external_starter=$RECORDING_STARTER_AUTH_USER timeout=${RECORDING_TIMEOUT}s"
  else
    log "Recording gate enabled: starter=$RECORDING_STARTER_INDEX timeout=${RECORDING_TIMEOUT}s"
  fi
fi

CMD_ARGS=(
  --call-url "$CALL_URL"
  --duration "$DURATION"
  --rotate-seconds "$ROTATE_SECONDS"
)
if [[ "$RECORD_BEFORE_MEDIA" == "1" ]]; then
  CMD_ARGS+=(--record-before-media)
  if [[ -n "$RECORDING_STARTER_AUTH_USER" ]]; then
    CMD_ARGS+=(--recording-starter-auth-user "$RECORDING_STARTER_AUTH_USER")
    CMD_ARGS+=(--recording-starter-auth-password "$RECORDING_STARTER_AUTH_PASSWORD")
    if [[ -n "$RECORDING_STARTER_NAME" ]]; then
      CMD_ARGS+=(--recording-starter-name "$RECORDING_STARTER_NAME")
    fi
  else
    CMD_ARGS+=(--recording-starter-index "$RECORDING_STARTER_INDEX")
  fi
  CMD_ARGS+=(--recording-timeout "$RECORDING_TIMEOUT")
fi
if [[ -n "$MUTE_ROTATION_START_FILE" ]]; then
  CMD_ARGS+=(--mute-start-file "$MUTE_ROTATION_START_FILE")
fi

for ((i = 1; i <= USERS; i++)); do
  media_prefix="$(resolve_media_prefix "$i")"
  video_file="${media_prefix}.ivf"
  audio_file="${media_prefix}.ogg"
  if [[ ! -f "$video_file" || ! -f "$audio_file" ]]; then
    echo "required media files not found for user $i: $video_file and $audio_file" >&2
    echo "run: $SCRIPT_DIR/prepare-media.sh --prefix $media_prefix" >&2
    exit 1
  fi
  log "User $i media: $media_prefix"

  if (( ${#NAME_LIST[@]} >= i )); then
    bot_name="${NAME_LIST[$((i - 1))]}"
  else
    bot_name="${NAME_PREFIX}${i}"
  fi
  if (( ${#JOIN_DELAY_LIST[@]} >= i )); then
    join_delay="${JOIN_DELAY_LIST[$((i - 1))]}"
  else
    join_delay="$((i - 1))"
  fi
  if (( ${#AUDIO_READY_LIST[@]} >= i )); then
    audio_ready="${AUDIO_READY_LIST[$((i - 1))]}"
  else
    audio_ready="0"
  fi
  if (( ${#SYNC_SHIFT_LIST[@]} >= i )); then
    sync_shift="${SYNC_SHIFT_LIST[$((i - 1))]}"
  else
    sync_shift="0"
  fi

  CMD_ARGS+=(--video "$video_file")
  CMD_ARGS+=(--audio "$audio_file")
  CMD_ARGS+=(--name "$bot_name")
  if (( ${#AUTH_USER_LIST[@]} >= i )); then
    CMD_ARGS+=(--auth-user "${AUTH_USER_LIST[$((i - 1))]}")
    CMD_ARGS+=(--auth-password "${AUTH_PASSWORD_LIST[$((i - 1))]}")
  fi
  CMD_ARGS+=(--join-delay "$join_delay")
  CMD_ARGS+=(--audio-ready-after "$audio_ready")
  CMD_ARGS+=(--sync-shift "$sync_shift")
  if (( ${#BOT_DURATION_LIST[@]} >= i )); then
    CMD_ARGS+=(--bot-duration "${BOT_DURATION_LIST[$((i - 1))]}")
  fi
done

(
  cd "$GO_ROTATOR_DIR"
  go run . "${CMD_ARGS[@]}"
)
