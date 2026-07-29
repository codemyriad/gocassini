# Harness stack environment resolution and validation.
#
# Sourcing this file is safe: it defines functions only (plus base helpers).
# Call harness_stack_env_resolve for defaults and harness_stack_env_validate /
# harness_stack_init when a real stack operation must fail loud.

if [[ "${CASSINI_HARNESS_LIB_STACK_ENV_SOURCED:-0}" == "1" ]]; then
  return 0
fi
CASSINI_HARNESS_LIB_STACK_ENV_SOURCED=1

# shellcheck source=./base.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/base.sh"

harness_stack_env_resolve() {
  PROJECT_NAME="${PROJECT_NAME:-spreedtest}"
  HARNESS_SIGNALING_HOST="${HARNESS_SIGNALING_HOST:-}"

  CASSINI_HARNESS_SERVICE_MODE="${CASSINI_HARNESS_SERVICE_MODE:-legacy-default}"
  case "$CASSINI_HARNESS_SERVICE_MODE" in
    legacy-default|legacy|default)
      CASSINI_HARNESS_SERVICE_MODE="legacy-default"
      SPREED_PROFILE="${SPREED_PROFILE:-full}"
      ;;
    core|appapi)
      SPREED_PROFILE="default"
      ;;
    full|full-remote)
      SPREED_PROFILE="full"
      ;;
    *)
      # Leave invalid values intact for explicit validation to report. Keep a
      # compatibility profile so safe helper sourcing can continue.
      SPREED_PROFILE="${SPREED_PROFILE:-full}"
      ;;
  esac

  CASSINI_HARNESS_PUBLIC_MODE="${CASSINI_HARNESS_PUBLIC_MODE:-local-http}"
  CASSINI_HARNESS_HOST="${CASSINI_HARNESS_HOST:-$(default_harness_host)}"
  CASSINI_HARNESS_PUBLIC_URL="${CASSINI_HARNESS_PUBLIC_URL:-}"
  CASSINI_HARNESS_PUBLIC_HOST="${CASSINI_HARNESS_PUBLIC_HOST:-}"
  CASSINI_HARNESS_MEDIA_HOST="${CASSINI_HARNESS_MEDIA_HOST:-}"
  CASSINI_HARNESS_SIGNALING_PUBLIC_URL="${CASSINI_HARNESS_SIGNALING_PUBLIC_URL:-}"

  if [[ "$CASSINI_HARNESS_PUBLIC_MODE" == "remote-https" ]]; then
    if [[ -z "$CASSINI_HARNESS_PUBLIC_URL" && -n "$CASSINI_HARNESS_PUBLIC_HOST" ]]; then
      CASSINI_HARNESS_PUBLIC_URL="https://${CASSINI_HARNESS_PUBLIC_HOST}"
    fi
    CASSINI_HARNESS_PUBLIC_URL="${CASSINI_HARNESS_PUBLIC_URL%/}"
    if [[ -z "$CASSINI_HARNESS_PUBLIC_HOST" && -n "$CASSINI_HARNESS_PUBLIC_URL" ]]; then
      CASSINI_HARNESS_PUBLIC_HOST="$(harness_url_host "$CASSINI_HARNESS_PUBLIC_URL")"
    fi
    if [[ -z "$CASSINI_HARNESS_MEDIA_HOST" ]] && ! harness_is_builtin_host "$CASSINI_HARNESS_HOST"; then
      CASSINI_HARNESS_MEDIA_HOST="$CASSINI_HARNESS_HOST"
    fi
  fi

  CASSINI_HARNESS_PUBLIC_HOSTPORT=""
  if [[ -n "$CASSINI_HARNESS_PUBLIC_URL" ]]; then
    CASSINI_HARNESS_PUBLIC_HOSTPORT="$(harness_url_hostport "$CASSINI_HARNESS_PUBLIC_URL")"
  fi

  export CASSINI_HARNESS_SERVICE_MODE CASSINI_HARNESS_PUBLIC_MODE CASSINI_HARNESS_HOST
  export CASSINI_HARNESS_PUBLIC_URL CASSINI_HARNESS_PUBLIC_HOST CASSINI_HARNESS_PUBLIC_HOSTPORT
  export CASSINI_HARNESS_MEDIA_HOST CASSINI_HARNESS_SIGNALING_PUBLIC_URL

  # Nextcloud server image for the compose stack. Empty selects compose.yml's
  # pinned default; CI matrix legs override this.
  export NEXTCLOUD_IMAGE="${NEXTCLOUD_IMAGE:-}"

  # Follows NEXTCLOUD_HOST_PORT so run-scoped projects get matching helper URLs.
  NEXTCLOUD_URL="${NEXTCLOUD_URL:-http://127.0.0.1:${NEXTCLOUD_HOST_PORT:-28080}}"
  NEXTCLOUD_STATUS_URL="${NEXTCLOUD_STATUS_URL:-$NEXTCLOUD_URL/status.php}"
  NEXTCLOUD_PUBLIC_URL="${NEXTCLOUD_PUBLIC_URL:-${CASSINI_HARNESS_PUBLIC_URL:-$NEXTCLOUD_URL}}"
  NEXTCLOUD_PUBLIC_URL="${NEXTCLOUD_PUBLIC_URL%/}"

  ADMIN_USER="${ADMIN_USER:-admin}"
  ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin}"
  BOT_USER="${BOT_USER:-botuser}"
  BOT_PASSWORD="${BOT_PASSWORD:-zN4vQ9mT2Kp7R1x!}"

  # DEV-ONLY harness defaults below are committed test values. They are public
  # and must never be reused for real Nextcloud, Talk, signaling, TURN, or
  # Cassini deployments.
  SIGNALING_URL="${SIGNALING_URL:-${CASSINI_HARNESS_SIGNALING_PUBLIC_URL:-}}"
  if [[ -z "$SIGNALING_URL" && -n "$CASSINI_HARNESS_PUBLIC_HOSTPORT" ]]; then
    SIGNALING_URL="https://${CASSINI_HARNESS_PUBLIC_HOST}:8443"
  fi
  SIGNALING_SHARED_SECRET="${SIGNALING_SHARED_SECRET:-7f4dca67263621ba7f9f9917e13de95a201f6f360be0d303e3008c2e6c8ad37d}"
  SIGNALING_INTERNAL_SECRET="${SIGNALING_INTERNAL_SECRET:-${CASSINI_TALK_SIGNALING_INTERNAL_SECRET:-6f4dca67263621ba7f9f9917e13de95a201f6f360be0d303e3008c2e6c8ad37d}}"
  TURN_SERVER="${TURN_SERVER:-${CASSINI_HARNESS_MEDIA_HOST:+$CASSINI_HARNESS_MEDIA_HOST:13479}}"
  TURN_SERVER="${TURN_SERVER:-127.0.0.1:13479}"
  TURN_SHARED_SECRET="${TURN_SHARED_SECRET:-3c04d2fc2f7fe39d48eb4dc77f652c8c778a4ea178b0e486529b284afca7b648}"
  CASSINI_TALK_RECORDING_URL="${CASSINI_TALK_RECORDING_URL:-}"
  CASSINI_TALK_RECORDING_SECRET="${CASSINI_TALK_RECORDING_SECRET:-9a2a9c0b7f4e43b7a2c6e19d6a4b8f8073b0174ee2f8425d99e8e33f7d60fb42}"
  CASSINI_TALK_SIGNALING_INTERNAL_SECRET="${CASSINI_TALK_SIGNALING_INTERNAL_SECRET:-$SIGNALING_INTERNAL_SECRET}"
  # The local harness is an access-control testbed, so installed ExApps opt in
  # by default. Production remains opt-in because the operator itself still
  # defaults this variable to false when no deploy value is supplied.
  CASSINI_NC_ACCESS_CONTROL="${CASSINI_NC_ACCESS_CONTROL:-true}"
  export CASSINI_TALK_RECORDING_SECRET CASSINI_TALK_SIGNALING_INTERNAL_SECRET
  export CASSINI_NC_ACCESS_CONTROL

  RUNTIME_DIR="$TEST_DIR/runtime"
  MEDIA_DIR="$TEST_DIR/media"
  mkdir -p "$RUNTIME_DIR" "$MEDIA_DIR"
}

harness_stack_env_validate() {
  case "${CASSINI_HARNESS_SERVICE_MODE:-legacy-default}" in
    legacy-default|core|appapi|full|full-remote) ;;
    *) echo "Invalid CASSINI_HARNESS_SERVICE_MODE: ${CASSINI_HARNESS_SERVICE_MODE:-}" >&2; return 2 ;;
  esac

  case "${CASSINI_HARNESS_PUBLIC_MODE:-local-http}" in
    local-http|lan-http|remote-https) ;;
    *) echo "Invalid CASSINI_HARNESS_PUBLIC_MODE: ${CASSINI_HARNESS_PUBLIC_MODE:-}" >&2; return 2 ;;
  esac

  if [[ "${CASSINI_HARNESS_SERVICE_MODE:-}" == "full-remote" && "${CASSINI_HARNESS_PUBLIC_MODE:-}" != "remote-https" ]]; then
    echo "CASSINI_HARNESS_SERVICE_MODE=full-remote requires CASSINI_HARNESS_PUBLIC_MODE=remote-https" >&2
    return 2
  fi

  case "${CASSINI_HARNESS_PUBLIC_MODE:-local-http}" in
    local-http)
      if [[ -n "${CASSINI_HARNESS_PUBLIC_URL:-}" \
        || -n "${CASSINI_HARNESS_PUBLIC_HOST:-}" \
        || -n "${CASSINI_HARNESS_MEDIA_HOST:-}" \
        || -n "${CASSINI_HARNESS_SIGNALING_PUBLIC_URL:-}" ]]; then
        echo "Remote harness env vars require CASSINI_HARNESS_PUBLIC_MODE=remote-https" >&2
        return 2
      fi
      ;;
    lan-http)
      if [[ -z "${CASSINI_HARNESS_PUBLIC_URL:-}" || "$(harness_url_scheme "${CASSINI_HARNESS_PUBLIC_URL:-}")" != "http" ]]; then
        echo "CASSINI_HARNESS_PUBLIC_MODE=lan-http requires an http CASSINI_HARNESS_PUBLIC_URL" >&2
        return 2
      fi
      if harness_media_selected; then
        if [[ -z "${CASSINI_HARNESS_MEDIA_HOST:-}" ]] || harness_is_builtin_host "${CASSINI_HARNESS_MEDIA_HOST:-}"; then
          echo "CASSINI_HARNESS_PUBLIC_MODE=lan-http media mode requires a non-loopback CASSINI_HARNESS_MEDIA_HOST" >&2
          return 2
        fi
        if [[ -z "${CASSINI_HARNESS_SIGNALING_PUBLIC_URL:-}" \
          || "$(harness_url_scheme "${CASSINI_HARNESS_SIGNALING_PUBLIC_URL:-}")" != "http" ]] \
          || harness_is_builtin_host "$(harness_url_host "${CASSINI_HARNESS_SIGNALING_PUBLIC_URL:-}")"; then
          echo "CASSINI_HARNESS_PUBLIC_MODE=lan-http media mode requires an http CASSINI_HARNESS_SIGNALING_PUBLIC_URL with a non-loopback host" >&2
          return 2
        fi
      fi
      if [[ "${CASSINI_HARNESS_RECORDING_BACKEND:-legacy}" == "installed-exapp" \
        && -z "${CASSINI_TALK_BACKEND_URL:-}" ]]; then
        echo "CASSINI_HARNESS_PUBLIC_MODE=lan-http installed-ExApp recording requires CASSINI_TALK_BACKEND_URL" >&2
        return 2
      fi
      ;;
    remote-https)
      if [[ "$(harness_url_scheme "${CASSINI_HARNESS_PUBLIC_URL:-}")" != "https" ]]; then
        echo "CASSINI_HARNESS_PUBLIC_MODE=remote-https requires an https CASSINI_HARNESS_PUBLIC_URL" >&2
        return 2
      fi
      if [[ -z "${CASSINI_HARNESS_PUBLIC_URL:-}" || -z "${CASSINI_HARNESS_PUBLIC_HOST:-}" || -z "${CASSINI_HARNESS_MEDIA_HOST:-}" ]]; then
        echo "CASSINI_HARNESS_PUBLIC_MODE=remote-https requires public URL/host and media host" >&2
        return 2
      fi
      ;;
  esac
}

harness_stack_init() {
  harness_stack_env_resolve
  harness_stack_env_validate
}

default_signaling_url() {
  if [[ -n "$HARNESS_SIGNALING_HOST" ]]; then
    echo "http://$HARNESS_SIGNALING_HOST:28082"
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

harness_remote_config_requested() {
  [[ "${CASSINI_HARNESS_PUBLIC_MODE:-local-http}" == "remote-https" || "${CASSINI_HARNESS_SERVICE_MODE:-legacy-default}" == "full-remote" ]]
}

harness_media_selected() {
  [[ "${CASSINI_HARNESS_SERVICE_MODE:-legacy-default}" == "full" \
    || "${CASSINI_HARNESS_SERVICE_MODE:-legacy-default}" == "full-remote" \
    || "${SPREED_PROFILE:-}" == "full" ]]
}
