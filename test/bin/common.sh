#!/usr/bin/env bash
set -euo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "$TEST_DIR/.." && pwd)"
COMPOSE_FILE="$TEST_DIR/compose.yml"

PROJECT_NAME="${PROJECT_NAME:-spreedtest}"
SPREED_PROFILE="${SPREED_PROFILE:-full}"

default_signaling_url() {
  local gateway
  gateway="$(docker network inspect "${PROJECT_NAME}_default" -f '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null || true)"
  if [[ -n "$gateway" ]]; then
    echo "http://$gateway:28082"
    return
  fi
  echo "http://127.0.0.1:28082"
}

NEXTCLOUD_URL="${NEXTCLOUD_URL:-http://127.0.0.1:28080}"
NEXTCLOUD_STATUS_URL="${NEXTCLOUD_STATUS_URL:-$NEXTCLOUD_URL/status.php}"

ADMIN_USER="${ADMIN_USER:-admin}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin}"
BOT_USER="${BOT_USER:-botuser}"
BOT_PASSWORD="${BOT_PASSWORD:-zN4vQ9mT2Kp7R1x!}"

# Keep empty by default here: bootstrap resolves an effective signaling URL
# after Docker networking is up.
SIGNALING_URL="${SIGNALING_URL:-}"
SIGNALING_SHARED_SECRET="${SIGNALING_SHARED_SECRET:-7f4dca67263621ba7f9f9917e13de95a201f6f360be0d303e3008c2e6c8ad37d}"
TURN_SERVER="${TURN_SERVER:-127.0.0.1:13479}"
TURN_SHARED_SECRET="${TURN_SHARED_SECRET:-3c04d2fc2f7fe39d48eb4dc77f652c8c778a4ea178b0e486529b284afca7b648}"

RUNTIME_DIR="$TEST_DIR/runtime"
MEDIA_DIR="$TEST_DIR/media"
mkdir -p "$RUNTIME_DIR" "$MEDIA_DIR"

log() {
  printf '[test] %s\n' "$*"
}

compose() {
  local profile_args=()
  if [[ "$SPREED_PROFILE" == "full" ]]; then
    profile_args+=(--profile full)
  fi
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

occ_has() {
  local command_name="$1"
  occ list --raw 2>/dev/null | grep -Fxq "$command_name"
}

wait_for_nextcloud() {
  local timeout_s="${1:-360}"
  local end_time=$((SECONDS + timeout_s))

  log "Waiting for Nextcloud at $NEXTCLOUD_STATUS_URL (timeout ${timeout_s}s)"
  until (( SECONDS >= end_time )); do
    if curl -fsS "$NEXTCLOUD_STATUS_URL" | grep -q '"installed":true'; then
      log "Nextcloud is ready"
      return 0
    fi
    sleep 2
  done

  log "Nextcloud did not become ready within ${timeout_s}s"
  return 1
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
