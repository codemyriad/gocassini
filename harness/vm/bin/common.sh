#!/usr/bin/env bash
set -euo pipefail

VM_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HARNESS_DIR="$(cd "$VM_DIR/.." && pwd)"
REPO_ROOT="$(cd "$HARNESS_DIR/.." && pwd)"
TEST_DIR="$VM_DIR"
COMPOSE_FILE="$VM_DIR/compose.yml"

PROJECT_NAME="${PROJECT_NAME:-spreedtest-vm}"
SPREED_PROFILE="${SPREED_PROFILE:-full}"

vm_route_source_ip() {
  ip -4 route get 1.1.1.1 2>/dev/null \
    | awk '{for (i = 1; i <= NF; i++) if ($i == "src") {print $(i + 1); exit}}'
}

default_harness_host() {
  if command -v systemd-detect-virt >/dev/null 2>&1 \
    && ! systemd-detect-virt --container --quiet \
    && systemd-detect-virt --vm --quiet; then
    local source_ip
    source_ip="$(vm_route_source_ip)"
    if [[ -n "$source_ip" && "$source_ip" != 127.* ]]; then
      printf '%s\n' "$source_ip"
      return
    fi
  fi
  printf '127.0.0.1\n'
}

CASSINI_HARNESS_HOST="${CASSINI_HARNESS_HOST:-$(default_harness_host)}"
HARNESS_SIGNALING_HOST="${HARNESS_SIGNALING_HOST:-}"
CASSINI_HARNESS_SIGNALING_HOST="${CASSINI_HARNESS_SIGNALING_HOST:-$HARNESS_SIGNALING_HOST}"

is_loopbackish_host() {
  case "$1" in
    127.0.0.1|localhost)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

render_harness_config() {
  "$TEST_DIR/bin/render-config.sh"
}

default_signaling_url() {
  if [[ -n "$CASSINI_HARNESS_SIGNALING_HOST" ]]; then
    echo "http://$CASSINI_HARNESS_SIGNALING_HOST:28082"
    return
  fi
  if ! is_loopbackish_host "$CASSINI_HARNESS_HOST"; then
    echo "http://$CASSINI_HARNESS_HOST:28082"
    return
  fi
  local gateway
  gateway="$(docker network inspect "${PROJECT_NAME}_default" -f '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null || true)"
  if [[ -n "$gateway" ]]; then
    echo "http://$gateway:28082"
    return
  fi
  echo "http://127.0.0.1:28082"
}

NEXTCLOUD_URL="${NEXTCLOUD_URL:-http://$CASSINI_HARNESS_HOST:28080}"
NEXTCLOUD_STATUS_URL="${NEXTCLOUD_STATUS_URL:-$NEXTCLOUD_URL/status.php}"
# Readiness checks use a VM/local URL that is already trusted even when an
# existing Nextcloud volume has not yet learned CASSINI_HARNESS_HOST. Bootstrap
# adds the public host before up.sh verifies NEXTCLOUD_STATUS_URL.
NEXTCLOUD_INTERNAL_URL="${NEXTCLOUD_INTERNAL_URL:-http://127.0.0.1:28080}"
NEXTCLOUD_INTERNAL_STATUS_URL="${NEXTCLOUD_INTERNAL_STATUS_URL:-$NEXTCLOUD_INTERNAL_URL/status.php}"

ADMIN_USER="${ADMIN_USER:-admin}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin}"
BOT_USER="${BOT_USER:-botuser}"
BOT_PASSWORD="${BOT_PASSWORD:-zN4vQ9mT2Kp7R1x!}"

# Keep empty by default here: bootstrap resolves an effective signaling URL
# after Docker networking is up.
SIGNALING_URL="${SIGNALING_URL:-}"
SIGNALING_SHARED_SECRET="${SIGNALING_SHARED_SECRET:-7f4dca67263621ba7f9f9917e13de95a201f6f360be0d303e3008c2e6c8ad37d}"
SIGNALING_INTERNAL_SECRET="${SIGNALING_INTERNAL_SECRET:-6f4dca67263621ba7f9f9917e13de95a201f6f360be0d303e3008c2e6c8ad37d}"
TURN_SERVER="${TURN_SERVER:-$CASSINI_HARNESS_HOST:13479}"
TURN_SHARED_SECRET="${TURN_SHARED_SECRET:-3c04d2fc2f7fe39d48eb4dc77f652c8c778a4ea178b0e486529b284afca7b648}"
export CASSINI_HARNESS_HOST CASSINI_HARNESS_SIGNALING_HOST NEXTCLOUD_URL NEXTCLOUD_STATUS_URL NEXTCLOUD_INTERNAL_URL NEXTCLOUD_INTERNAL_STATUS_URL TURN_SERVER
CASSINI_TALK_RECORDING_URL="${CASSINI_TALK_RECORDING_URL:-}"
CASSINI_TALK_RECORDING_SECRET="${CASSINI_TALK_RECORDING_SECRET:-9a2a9c0b7f4e43b7a2c6e19d6a4b8f8073b0174ee2f8425d99e8e33f7d60fb42}"

RUNTIME_DIR="$VM_DIR/runtime"
MEDIA_DIR="$HARNESS_DIR/media"
mkdir -p "$RUNTIME_DIR" "$MEDIA_DIR"

log() {
  printf '[test] %s\n' "$*"
}

configure_python_runner() {
  local requirements_file="${1:-}"
  local uv_python="${UV_PYTHON:-3.12}"

  PYTHON_RUNNER=()
  if [[ -n "${PYTHON_BIN:-}" ]]; then
    if ! "$PYTHON_BIN" --version >/dev/null 2>&1; then
      echo "python interpreter is not runnable: $PYTHON_BIN" >&2
      return 1
    fi
    PYTHON_RUNNER=("$PYTHON_BIN")
    return 0
  fi

  if command -v uv >/dev/null 2>&1; then
    PYTHON_RUNNER=(uv run --python "$uv_python")
    if [[ -n "$requirements_file" ]]; then
      PYTHON_RUNNER+=(--with-requirements "$requirements_file")
    fi
    PYTHON_RUNNER+=(python)
    return 0
  fi

  if command -v python3 >/dev/null 2>&1; then
    PYTHON_RUNNER=(python3)
    return 0
  fi

  echo "missing python runtime: set PYTHON_BIN or install uv/python3" >&2
  return 1
}

cassini_session_id_from_mkv() {
  local input="$1"
  ffprobe -v error -show_entries format_tags -of default=nw=1 "$input" 2>/dev/null \
    | awk -F= '/^TAG:(SESSION_ID|session_id)=/ {print $2; exit}'
}

cassini_session_dir_from_mkv() {
  local input="$1"
  local session_id
  session_id="$(cassini_session_id_from_mkv "$input")"
  if [[ -z "$session_id" ]]; then
    return 1
  fi
  printf '%s/sessions/%s\n' "$(dirname "$input")" "$session_id"
}

cassini_session_json_from_mkv() {
  local session_dir
  session_dir="$(cassini_session_dir_from_mkv "$1")" || return 1
  printf '%s/session.json\n' "$session_dir"
}

cassini_events_log_from_mkv() {
  local session_dir
  session_dir="$(cassini_session_dir_from_mkv "$1")" || return 1
  printf '%s/events.ndjson\n' "$session_dir"
}

cassini_streams_dir_from_mkv() {
  local session_dir
  session_dir="$(cassini_session_dir_from_mkv "$1")" || return 1
  printf '%s/streams\n' "$session_dir"
}

cassini_unique_participant_count_from_mkv() {
  local input="$1"
  ffprobe -v error -show_entries stream_tags -of default=nw=1 "$input" 2>/dev/null \
    | awk -F= '/^TAG:(PARTICIPANT_ID|participant_id)=/ {print $2}' \
    | sort -u \
    | awk 'NF{count++} END {print count+0}'
}

compose() {
  local profile_args=()
  if [[ "$SPREED_PROFILE" == "full" ]]; then
    profile_args+=(--profile full)
  fi
  render_harness_config
  docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE" "${profile_args[@]}" "$@"
}

occ() {
  local env_args=()
  if [[ -n "${OC_PASS:-}" ]]; then
    env_args+=(-e "OC_PASS=$OC_PASS")
  fi
  if [[ -n "${NC_PASS:-}" ]]; then
    env_args+=(-e "NC_PASS=$NC_PASS")
  fi
  compose exec -T "${env_args[@]}" -u www-data nextcloud php occ "$@"
}

occ_ignore_failure() {
  local had_errexit=0
  if [[ $- == *e* ]]; then
    had_errexit=1
    set +e
  fi
  occ "$@"
  if (( had_errexit )); then
    set -e
  fi
  return 0
}

occ_has() {
  local command_name="$1"
  occ list --raw 2>/dev/null | awk '{print $1}' | grep -Fxq "$command_name"
}

wait_for_nextcloud_url() {
  local status_url="$1"
  local timeout_s="${2:-360}"
  local label="${3:-Nextcloud}"
  local end_time=$((SECONDS + timeout_s))

  log "Waiting for $label at $status_url (timeout ${timeout_s}s)"
  until (( SECONDS >= end_time )); do
    if curl -fsS "$status_url" | grep -q '"installed":true'; then
      log "$label is ready"
      return 0
    fi
    sleep 2
  done

  log "$label did not become ready within ${timeout_s}s"
  return 1
}

wait_for_nextcloud() {
  wait_for_nextcloud_url "$NEXTCLOUD_INTERNAL_STATUS_URL" "${1:-360}" "Nextcloud"
}

wait_for_public_nextcloud() {
  wait_for_nextcloud_url "$NEXTCLOUD_STATUS_URL" "${1:-60}" "public Nextcloud"
}

create_room_with_retry() {
  local room_name="${1:-Local room}"
  local max_attempts="${2:-10}"
  local attempt=1
  local output
  local delay_s=1
  local did_rebootstrap=0

  while ((attempt <= max_attempts)); do
    if output="$("$TEST_DIR/bin/create-room.sh" --name "$room_name" 2>&1)"; then
      printf '%s\n' "$(echo "$output" | tail -n1)"
      return 0
    fi

    log "room creation attempt $attempt/$max_attempts failed" >&2
    if ((did_rebootstrap == 0)) && [[ "$output" == *"statuscode': 996"* || "$output" == *'"statuscode": 996'* ]]; then
      log "room creation hit statuscode 996; re-running bootstrap once before next retry" >&2
      if "$TEST_DIR/bin/bootstrap.sh" >/dev/null 2>&1; then
        log "bootstrap retry completed" >&2
      else
        log "bootstrap retry failed; continuing with room retries" >&2
      fi
      did_rebootstrap=1
    fi
    if ((attempt < max_attempts)); then
      log "retrying room creation in ${delay_s}s..." >&2
      sleep "$delay_s"
      delay_s=$((delay_s * 2))
      if ((delay_s > 30)); then
        delay_s=30
      fi
    fi
    attempt=$((attempt + 1))
  done

  log "create-room failed after ${max_attempts} attempts" >&2
  log "$output" >&2
  return 1
}
