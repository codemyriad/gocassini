# shellcheck shell=bash  # sourced by the harness scripts; it has no shebang of its own
# Shared safe harness basics. Sourcing this file only defines paths,
# helpers, and non-topology defaults; it must not validate or exit because e2e
# scripts source it before choosing/sanitizing their stack topology.

if [[ "${CASSINI_HARNESS_LIB_BASE_SOURCED:-0}" == "1" ]]; then
  return 0
fi
CASSINI_HARNESS_LIB_BASE_SOURCED=1

HARNESS_BIN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(cd "$HARNESS_BIN_DIR/.." && pwd)"
REPO_ROOT="$(cd "$TEST_DIR/.." && pwd)"
COMPOSE_FILE="$TEST_DIR/compose.yml"

PROJECT_NAME="${PROJECT_NAME:-spreedtest}"
HARNESS_SIGNALING_HOST="${HARNESS_SIGNALING_HOST:-}"

harness_route_source_ip() {
  ip -4 route get 1.1.1.1 2>/dev/null \
    | awk '{for (i = 1; i <= NF; i++) if ($i == "src") {print $(i + 1); exit}}'
}

default_harness_host() {
  if command -v systemd-detect-virt >/dev/null 2>&1 \
    && ! systemd-detect-virt --container --quiet \
    && systemd-detect-virt --vm --quiet; then
    local source_ip
    source_ip="$(harness_route_source_ip)"
    if [[ -n "$source_ip" && "$source_ip" != 127.* ]]; then
      printf '%s\n' "$source_ip"
      return
    fi
  fi
  printf '127.0.0.1\n'
}

harness_url_hostport() {
  local value="$1"
  value="${value#http://}"
  value="${value#https://}"
  value="${value%%/*}"
  value="${value%/}"
  printf '%s\n' "$value"
}

harness_url_host() {
  local hostport
  hostport="$(harness_url_hostport "$1")"
  if [[ "$hostport" == \[*\]* ]]; then
    hostport="${hostport#\[}"
    hostport="${hostport%%\]*}"
  else
    hostport="${hostport%%:*}"
  fi
  printf '%s\n' "$hostport"
}

harness_url_scheme() {
  local value="$1"
  if [[ "$value" == *"://"* ]]; then
    printf '%s\n' "${value%%://*}"
  else
    printf 'http\n'
  fi
}

harness_url_origin() {
  local value="$1"
  local scheme hostport
  scheme="$(harness_url_scheme "$value")"
  hostport="$(harness_url_hostport "$value")"
  if [[ -n "$hostport" ]]; then
    printf '%s://%s\n' "$scheme" "$hostport"
  fi
}

harness_is_builtin_host() {
  case "$1" in
    ""|127.0.0.1|localhost|nextcloud|host.docker.internal|reverse-proxy)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

harness_add_unique() {
  local __array_name="$1"
  local value="$2"
  [[ -n "$value" ]] || return 0
  local existing
  local current_count=0
  local -a current_values=()
  # Bash 3.2 (the macOS system Bash) treats an expansion of a declared but
  # empty array as an unbound variable under `set -u`. Read its length first
  # and only expand it when it actually contains values.
  eval "current_count=\${#${__array_name}[@]}"
  if (( current_count > 0 )); then
    eval "current_values=(\"\${${__array_name}[@]}\")"
    for existing in "${current_values[@]}"; do
      if [[ "$existing" == "$value" ]]; then
        return 0
      fi
    done
  fi
  eval "${__array_name}+=(\"\$value\")"
}

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
