# shellcheck shell=bash  # sourced by the harness scripts; it has no shebang of its own
# Local e2e topology sanitizer. This file is intentionally safe to source
# before common.sh/stack-env.sh; it masks ambient remote harness variables so
# local e2e scripts remain deterministic from developer shells and persistent
# runners.

if [[ "${CASSINI_HARNESS_LIB_E2E_LOCAL_SOURCED:-0}" == "1" ]]; then
  return 0
fi
CASSINI_HARNESS_LIB_E2E_LOCAL_SOURCED=1

harness_e2e_local_stack_env() {
  local service_mode="${1:-full}"
  local recording_backend="${2:-legacy}"
  local cassini_mode="${3:-none}"

  export CASSINI_HARNESS_PUBLIC_MODE=local-http
  export CASSINI_HARNESS_PUBLIC_URL=
  export CASSINI_HARNESS_PUBLIC_HOST=
  export CASSINI_HARNESS_MEDIA_HOST=
  export CASSINI_HARNESS_SIGNALING_PUBLIC_URL=

  export CASSINI_HARNESS_SERVICE_MODE="$service_mode"
  export CASSINI_HARNESS_CASSINI_MODE="$cassini_mode"
  export CASSINI_HARNESS_RECORDING_BACKEND="$recording_backend"

  case "$service_mode" in
    core|appapi)
      export SPREED_PROFILE=default
      ;;
    full|full-remote)
      export SPREED_PROFILE=full
      ;;
    legacy-default|legacy|default)
      export SPREED_PROFILE="${SPREED_PROFILE:-full}"
      ;;
  esac
}
