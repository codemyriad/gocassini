#!/usr/bin/env bash
set -euo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "$TEST_DIR/.." && pwd)"
COMPOSE_FILE="$TEST_DIR/compose.yml"

PROJECT_NAME="${PROJECT_NAME:-spreedtest}"
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
    echo "Invalid CASSINI_HARNESS_SERVICE_MODE: $CASSINI_HARNESS_SERVICE_MODE" >&2
    exit 2
    ;;
esac
CASSINI_HARNESS_PUBLIC_MODE="${CASSINI_HARNESS_PUBLIC_MODE:-local-http}"
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

CASSINI_HARNESS_HOST="${CASSINI_HARNESS_HOST:-$(default_harness_host)}"
_harness_public_url_was_set=false
_harness_public_host_was_set=false
_harness_media_host_was_set=false
_harness_signaling_public_url_was_set=false
[[ -v CASSINI_HARNESS_PUBLIC_URL ]] && _harness_public_url_was_set=true
[[ -v CASSINI_HARNESS_PUBLIC_HOST ]] && _harness_public_host_was_set=true
[[ -v CASSINI_HARNESS_MEDIA_HOST ]] && _harness_media_host_was_set=true
[[ -v CASSINI_HARNESS_SIGNALING_PUBLIC_URL ]] && _harness_signaling_public_url_was_set=true

CASSINI_HARNESS_PUBLIC_URL="${CASSINI_HARNESS_PUBLIC_URL:-}"
CASSINI_HARNESS_PUBLIC_HOST="${CASSINI_HARNESS_PUBLIC_HOST:-}"
CASSINI_HARNESS_MEDIA_HOST="${CASSINI_HARNESS_MEDIA_HOST:-}"
CASSINI_HARNESS_SIGNALING_PUBLIC_URL="${CASSINI_HARNESS_SIGNALING_PUBLIC_URL:-}"

if [[ "$CASSINI_HARNESS_SERVICE_MODE" == "full-remote" && "$CASSINI_HARNESS_PUBLIC_MODE" != "remote-https" ]]; then
  echo "CASSINI_HARNESS_SERVICE_MODE=full-remote requires CASSINI_HARNESS_PUBLIC_MODE=remote-https" >&2
  exit 2
fi

case "$CASSINI_HARNESS_PUBLIC_MODE" in
  local-http)
    if [[ "$_harness_public_url_was_set" == "true" \
      || "$_harness_public_host_was_set" == "true" \
      || "$_harness_media_host_was_set" == "true" \
      || "$_harness_signaling_public_url_was_set" == "true" ]]; then
      echo "Remote harness env vars require CASSINI_HARNESS_PUBLIC_MODE=remote-https" >&2
      exit 2
    fi
    ;;
  lan-http)
    ;;
  remote-https)
    if [[ -z "$CASSINI_HARNESS_PUBLIC_URL" && -n "$CASSINI_HARNESS_PUBLIC_HOST" ]]; then
      CASSINI_HARNESS_PUBLIC_URL="https://${CASSINI_HARNESS_PUBLIC_HOST}"
    fi
    CASSINI_HARNESS_PUBLIC_URL="${CASSINI_HARNESS_PUBLIC_URL%/}"
    if [[ -z "$CASSINI_HARNESS_PUBLIC_HOST" && -n "$CASSINI_HARNESS_PUBLIC_URL" ]]; then
      CASSINI_HARNESS_PUBLIC_HOST="$(harness_url_host "$CASSINI_HARNESS_PUBLIC_URL")"
    fi
    if [[ "$(harness_url_scheme "$CASSINI_HARNESS_PUBLIC_URL")" != "https" ]]; then
      echo "CASSINI_HARNESS_PUBLIC_MODE=remote-https requires an https CASSINI_HARNESS_PUBLIC_URL" >&2
      exit 2
    fi
    if [[ -z "$CASSINI_HARNESS_MEDIA_HOST" ]] && ! harness_is_builtin_host "$CASSINI_HARNESS_HOST"; then
      CASSINI_HARNESS_MEDIA_HOST="$CASSINI_HARNESS_HOST"
    fi
    if [[ -z "$CASSINI_HARNESS_PUBLIC_URL" || -z "$CASSINI_HARNESS_PUBLIC_HOST" || -z "$CASSINI_HARNESS_MEDIA_HOST" ]]; then
      echo "CASSINI_HARNESS_PUBLIC_MODE=remote-https requires public URL/host and media host" >&2
      exit 2
    fi
    ;;
  *)
    echo "Invalid CASSINI_HARNESS_PUBLIC_MODE: $CASSINI_HARNESS_PUBLIC_MODE" >&2
    exit 2
    ;;
esac

CASSINI_HARNESS_PUBLIC_HOSTPORT=""
if [[ -n "$CASSINI_HARNESS_PUBLIC_URL" ]]; then
  CASSINI_HARNESS_PUBLIC_HOSTPORT="$(harness_url_hostport "$CASSINI_HARNESS_PUBLIC_URL")"
fi
export CASSINI_HARNESS_SERVICE_MODE CASSINI_HARNESS_PUBLIC_MODE CASSINI_HARNESS_HOST CASSINI_HARNESS_PUBLIC_URL CASSINI_HARNESS_PUBLIC_HOST CASSINI_HARNESS_PUBLIC_HOSTPORT CASSINI_HARNESS_MEDIA_HOST CASSINI_HARNESS_SIGNALING_PUBLIC_URL

# Nextcloud server image for the compose stack. Empty selects the pinned
# default in compose.yml; CI's NC-compatibility matrix leg overrides it
# (e.g. NEXTCLOUD_IMAGE=nextcloud:33). Exported because docker compose
# resolves ${NEXTCLOUD_IMAGE:-...} from its own process environment.
export NEXTCLOUD_IMAGE="${NEXTCLOUD_IMAGE:-}"

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

# Follows NEXTCLOUD_HOST_PORT (compose.yml's published-port variable) so
# scripts that re-scope the host port — ci-e2e-talk-record-roundtrip.sh
# exports a per-run port to dodge stale-run collisions (gh issue #51) —
# get a matching URL in every callee that sources this file.
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
# Keep empty by default here: bootstrap resolves an effective signaling URL
# after Docker networking is up. In remote HTTPS mode, default to the
# Tailscale-Serve/Caddy-facing signaling URL so the browser gets WSS instead
# of mixed-content ws:// signaling.
SIGNALING_URL="${SIGNALING_URL:-${CASSINI_HARNESS_SIGNALING_PUBLIC_URL:-}}"
if [[ -z "$SIGNALING_URL" && -n "$CASSINI_HARNESS_PUBLIC_HOSTPORT" ]]; then
  SIGNALING_URL="https://${CASSINI_HARNESS_PUBLIC_HOST}:8443"
fi
SIGNALING_SHARED_SECRET="${SIGNALING_SHARED_SECRET:-7f4dca67263621ba7f9f9917e13de95a201f6f360be0d303e3008c2e6c8ad37d}"
SIGNALING_INTERNAL_SECRET="${SIGNALING_INTERNAL_SECRET:-6f4dca67263621ba7f9f9917e13de95a201f6f360be0d303e3008c2e6c8ad37d}"
TURN_SERVER="${TURN_SERVER:-${CASSINI_HARNESS_MEDIA_HOST:+$CASSINI_HARNESS_MEDIA_HOST:13479}}"
TURN_SERVER="${TURN_SERVER:-127.0.0.1:13479}"
TURN_SHARED_SECRET="${TURN_SHARED_SECRET:-3c04d2fc2f7fe39d48eb4dc77f652c8c778a4ea178b0e486529b284afca7b648}"
CASSINI_TALK_RECORDING_URL="${CASSINI_TALK_RECORDING_URL:-}"
# DEV-ONLY fallback for the local harness loop. This value is committed and
# public in repo history; never use it (or any committed value) for a real
# deployment — generate one with `openssl rand -hex 32` instead.
CASSINI_TALK_RECORDING_SECRET="${CASSINI_TALK_RECORDING_SECRET:-9a2a9c0b7f4e43b7a2c6e19d6a4b8f8073b0174ee2f8425d99e8e33f7d60fb42}"
CASSINI_TALK_SIGNALING_INTERNAL_SECRET="${CASSINI_TALK_SIGNALING_INTERNAL_SECRET:-$SIGNALING_INTERNAL_SECRET}"
export CASSINI_TALK_RECORDING_SECRET CASSINI_TALK_SIGNALING_INTERNAL_SECRET

RUNTIME_DIR="$TEST_DIR/runtime"
MEDIA_DIR="$TEST_DIR/media"
mkdir -p "$RUNTIME_DIR" "$MEDIA_DIR"

harness_remote_config_requested() {
  [[ "${CASSINI_HARNESS_PUBLIC_MODE:-local-http}" == "remote-https" || "${CASSINI_HARNESS_SERVICE_MODE:-legacy-default}" == "full-remote" ]]
}

harness_add_unique() {
  local __array_name="$1"
  local value="$2"
  [[ -n "$value" ]] || return 0
  local existing
  local -a current_values=()
  eval "current_values=(\"\${${__array_name}[@]}\")"
  for existing in "${current_values[@]}"; do
    if [[ "$existing" == "$value" ]]; then
      return 0
    fi
  done
  eval "${__array_name}+=(\"\$value\")"
}

harness_render_full_profile_configs() {
  local allowall="${1:-false}"
  local generated_dir="$RUNTIME_DIR/generated"
  local generated_janus_dir="$generated_dir/janus"
  local nextcloud_port="${NEXTCLOUD_HOST_PORT:-28080}"
  local signaling_conf="$generated_dir/signaling.conf"
  local janus_conf="$generated_janus_dir/janus.jcfg"
  local turn_conf="$generated_dir/turnserver.conf"
  local proxy_conf="$generated_dir/signaling-public-proxy.conf"
  local proxy_cert="$generated_dir/signaling-public-proxy.crt"
  local proxy_key="$generated_dir/signaling-public-proxy.key"
  local proxy_cert_conf="$generated_dir/signaling-public-proxy.openssl.cnf"
  local media_host="${CASSINI_HARNESS_MEDIA_HOST:-127.0.0.1}"
  local public_host="${CASSINI_HARNESS_PUBLIC_HOST:-localhost}"

  mkdir -p "$generated_dir" "$generated_janus_dir"

  local -a backend_urls=()
  harness_add_unique backend_urls "http://127.0.0.1:${nextcloud_port}"
  harness_add_unique backend_urls "http://localhost:${nextcloud_port}"
  harness_add_unique backend_urls "http://host.docker.internal:${nextcloud_port}"

  if ! harness_is_builtin_host "$CASSINI_HARNESS_HOST"; then
    harness_add_unique backend_urls "http://$(harness_url_host "$CASSINI_HARNESS_HOST"):${nextcloud_port}"
  fi

  if command -v hostname >/dev/null 2>&1; then
    local host_ip
    for host_ip in $(hostname -I 2>/dev/null || true); do
      harness_add_unique backend_urls "http://${host_ip}:${nextcloud_port}"
    done
  fi

  if [[ -n "$CASSINI_HARNESS_PUBLIC_URL" ]]; then
    harness_add_unique backend_urls "$(harness_url_origin "$CASSINI_HARNESS_PUBLIC_URL")"
  fi

  local backend_names=""
  local i
  for i in "${!backend_urls[@]}"; do
    if [[ -n "$backend_names" ]]; then
      backend_names+=", "
    fi
    backend_names+="backend-$((i + 1))"
  done

  {
    cat <<EOF_CONF
[http]
listen = 0.0.0.0:28082

[app]
debug = false

[sessions]
hashkey = 39a5433df8f8334f6eff8a67f6afcfc594a2599f5389b4fef316ec30277fb910

[clients]
internalsecret = $SIGNALING_INTERNAL_SECRET

[backend]
backends = $backend_names
allowall = $allowall
EOF_CONF
    if [[ "$allowall" == "true" ]]; then
      cat <<EOF_CONF
# Dev harness only. Allows callbacks that present Docker-internal backend
# names such as reverse-proxy while still using the shared backend secret.
secret = $SIGNALING_SHARED_SECRET
EOF_CONF
    fi
    cat <<EOF_CONF
timeout = 10
connectionsperhost = 16

EOF_CONF
    for i in "${!backend_urls[@]}"; do
      cat <<EOF_CONF
[backend-$((i + 1))]
url = ${backend_urls[$i]}
secret = $SIGNALING_SHARED_SECRET

EOF_CONF
    done
    cat <<EOF_CONF
[nats]
url = nats://127.0.0.1:14222

[mcu]
type = janus
url = ws://127.0.0.1:28188
adminkey = 01e2fcd0d226d7f4cf34a8a61397f110693f05042e57ab68e94f8476a4b8f22a

[turn]
apikey = 127.0.0.1
secret = $TURN_SHARED_SECRET
servers = turn:$TURN_SERVER?transport=udp,turn:$TURN_SERVER?transport=tcp
EOF_CONF
  } >"$signaling_conf"

  cat >"$janus_conf" <<EOF_CONF
general: {
  debug_level = 4
  admin_secret = "01e2fcd0d226d7f4cf34a8a61397f110693f05042e57ab68e94f8476a4b8f22a"
  plugins_folder = "/usr/local/lib/janus/plugins"
  transports_folder = "/usr/local/lib/janus/transports"
  events_folder = "/usr/local/lib/janus/events"
}

nat: {
  # Rendered by harness/bin/common.sh for remote browser access.
  nat_1_1_mapping = "$media_host"
}

media: {
  rtp_port_range = "20000-20100"
}

admin: {
  admin_port = 17088
  admin_secret = "01e2fcd0d226d7f4cf34a8a61397f110693f05042e57ab68e94f8476a4b8f22a"
}
EOF_CONF

  cat >"$turn_conf" <<EOF_CONF
# Rendered local coturn config for the test harness.
listening-port=13479
tls-listening-port=0

external-ip=$media_host
relay-ip=$media_host

min-port=49160
max-port=49200

use-auth-secret
static-auth-secret=$TURN_SHARED_SECRET
realm=localhost

fingerprint
no-cli
no-tls
no-dtls

log-file=stdout
verbose
EOF_CONF

  cat >"$proxy_conf" <<'EOF_CONF'
map $http_upgrade $connection_upgrade {
  default upgrade;
  '' close;
}

server {
  listen 443 ssl;
  server_name _;

  ssl_certificate /etc/nginx/certs/signaling.crt;
  ssl_certificate_key /etc/nginx/certs/signaling.key;

  location / {
    proxy_pass http://nextcloud:80;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Host $host;
    proxy_set_header X-Forwarded-Proto https;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
  }
}

server {
  listen 8443 ssl;
  server_name _;

  ssl_certificate /etc/nginx/certs/signaling.crt;
  ssl_certificate_key /etc/nginx/certs/signaling.key;

  # Split-horizon helper for Nextcloud server-side HPB notifications.
  # The Mac browser reaches the real signaling server through Tailscale Serve,
  # but containers on hardened Linux hosts may be unable to hairpin to host
  # ports. Talk treats non-2xx or unreachable backend notifications as fatal
  # for room creation, so the Docker-network alias answers the server-side
  # compatibility/notification endpoint while browser WSS still uses the real
  # host signaling service.
  location /api/v1/ {
    add_header X-Spreed-Signaling-Features "audio-video-permissions, federation, incall-all, hello-v2, switchto" always;
    add_header Content-Type application/json always;
    return 200 '{"nextcloud-spreed-signaling":"Welcome","version":"2.1.1~docker"}';
  }

  location / {
    return 404;
  }
}
EOF_CONF

  cat >"$proxy_cert_conf" <<EOF_CONF
[req]
prompt = no
distinguished_name = req_distinguished_name
x509_extensions = v3_req

[req_distinguished_name]
CN = $public_host

[v3_req]
subjectAltName = DNS:$public_host
basicConstraints = CA:FALSE
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
EOF_CONF
  if command -v openssl >/dev/null 2>&1; then
    openssl req -x509 -nodes -newkey rsa:2048 -days 14 \
      -keyout "$proxy_key" \
      -out "$proxy_cert" \
      -config "$proxy_cert_conf" >/dev/null 2>&1
  elif command -v docker >/dev/null 2>&1; then
    docker run --rm --user "$(id -u):$(id -g)" \
      -v "$generated_dir:/out" \
      alpine/openssl req -x509 -nodes -newkey rsa:2048 -days 14 \
      -keyout "/out/$(basename "$proxy_key")" \
      -out "/out/$(basename "$proxy_cert")" \
      -config "/out/$(basename "$proxy_cert_conf")" >/dev/null 2>&1
  else
    echo "openssl or Docker is required to generate the remote signaling proxy certificate" >&2
    return 1
  fi
  chmod 0600 "$proxy_key"

  export SIGNALING_CONF="$signaling_conf"
  export JANUS_CONF="$janus_conf"
  export TURN_CONF="$turn_conf"
  export SIGNALING_PUBLIC_PROXY_CONF="$proxy_conf"
  export SIGNALING_PUBLIC_PROXY_CERT="$proxy_cert"
  export SIGNALING_PUBLIC_PROXY_KEY="$proxy_key"
  log "Rendered full-profile harness config (public: ${CASSINI_HARNESS_PUBLIC_URL:-local}, media: $media_host, signaling allowall: $allowall)"
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
  if harness_remote_config_requested; then
    profile_args+=(--profile remote)
  fi
  docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE" "${profile_args[@]}" "$@"
}

harness_require_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    echo "Docker is required for the Cassini dev stack" >&2
    return 1
  fi
  if ! docker compose version >/dev/null 2>&1; then
    echo "Docker Compose v2 is required for the Cassini dev stack" >&2
    return 1
  fi
}

harness_project_containers() {
  docker ps -a --filter "label=com.docker.compose.project=$PROJECT_NAME" --format '{{.Names}}' 2>/dev/null | sort -u
}

harness_project_volumes() {
  docker volume ls --filter "label=com.docker.compose.project=$PROJECT_NAME" --format '{{.Name}}' 2>/dev/null | sort -u
}

harness_project_networks() {
  docker network ls --filter "label=com.docker.compose.project=$PROJECT_NAME" --format '{{.Name}}' 2>/dev/null | sort -u
}

harness_project_resources() {
  local item
  while IFS= read -r item; do [[ -n "$item" ]] && printf 'container:%s\n' "$item"; done < <(harness_project_containers)
  while IFS= read -r item; do [[ -n "$item" ]] && printf 'volume:%s\n' "$item"; done < <(harness_project_volumes)
  while IFS= read -r item; do [[ -n "$item" ]] && printf 'network:%s\n' "$item"; done < <(harness_project_networks)
}

harness_installed_exapp_resources() {
  docker ps -a --format '{{.Names}}' 2>/dev/null \
    | grep -E '^(cassini-exapp|nc_app_gocassini)$' \
    | sort -u \
    | sed 's/^/container:/' || true
  docker volume ls --format '{{.Name}}' 2>/dev/null \
    | grep -E '^(cassini-exapp-state|cassini-exapp-site|nc_app_gocassini_data)$' \
    | sort -u \
    | sed 's/^/volume:/' || true
}

harness_remove_installed_exapp_resources() {
  docker rm -f cassini-exapp nc_app_gocassini >/dev/null 2>&1 || true
  docker volume rm cassini-exapp-state cassini-exapp-site nc_app_gocassini_data >/dev/null 2>&1 || true
}

harness_resource_list_nonempty() {
  local output="$1"
  [[ -n "${output//$'\n'/}" ]]
}

harness_existing_resource_report() {
  local resources="$1"
  if harness_resource_list_nonempty "$resources"; then
    printf '%s\n' "$resources" | sed 's/^/  - /' >&2
  fi
}

harness_compose_services_for_mode() {
  case "${CASSINI_HARNESS_SERVICE_MODE:-legacy-default}" in
    core)
      printf '%s\n' db nextcloud
      ;;
    appapi)
      printf '%s\n' db nextcloud appapi-harp reverse-proxy
      ;;
    full)
      printf '%s\n' db nextcloud appapi-harp reverse-proxy nats janus signaling coturn
      ;;
    full-remote)
      printf '%s\n' db nextcloud appapi-harp reverse-proxy nats janus signaling coturn signaling-public-proxy
      ;;
    legacy-default)
      return 1
      ;;
    *)
      echo "Invalid CASSINI_HARNESS_SERVICE_MODE: ${CASSINI_HARNESS_SERVICE_MODE:-}" >&2
      return 2
      ;;
  esac
}

harness_compose_service_args() {
  local services
  if services="$(harness_compose_services_for_mode)"; then
    printf '%s\n' "$services"
  fi
}

harness_desired_compose_services() {
  if harness_compose_services_for_mode 2>/dev/null | sort -u; then
    return 0
  fi
  compose config --services 2>/dev/null | sort -u
}

harness_existing_compose_services() {
  compose ps -a --services 2>/dev/null | sort -u
}

harness_running_compose_services() {
  compose ps --services --filter status=running 2>/dev/null | sort -u
}

harness_diff_lines() {
  local left="$1"
  local right="$2"
  local mode="$3"
  local left_file right_file
  left_file="$(mktemp)"
  right_file="$(mktemp)"
  printf '%s\n' "$left" | sed '/^$/d' | sort -u >"$left_file"
  printf '%s\n' "$right" | sed '/^$/d' | sort -u >"$right_file"
  case "$mode" in
    left) comm -23 "$left_file" "$right_file" ;;
    right) comm -13 "$left_file" "$right_file" ;;
    *) return 2 ;;
  esac
  rm -f "$left_file" "$right_file"
}

harness_validate_resume_resources() {
  local desired existing running missing extra
  desired="$(harness_desired_compose_services)"
  existing="$(harness_existing_compose_services)"
  running="$(harness_running_compose_services)"

  if ! harness_resource_list_nonempty "$existing"; then
    echo "No existing compose containers found for project '$PROJECT_NAME'; cannot --resume." >&2
    echo "Use 'cassini dev stack up' for a fresh stack or 'cassini dev stack up --reset' to recreate." >&2
    return 1
  fi
  if harness_resource_list_nonempty "$running"; then
    echo "Cannot --resume because these services are already running:" >&2
    printf '%s\n' "$running" | sed 's/^/  - /' >&2
    echo "Use 'cassini dev stack status' or 'cassini dev stack stop' first." >&2
    return 1
  fi

  missing="$(harness_diff_lines "$desired" "$existing" left)"
  extra="$(harness_diff_lines "$desired" "$existing" right)"
  if harness_resource_list_nonempty "$missing" || harness_resource_list_nonempty "$extra"; then
    echo "Cannot --resume because existing services do not match the resolved config." >&2
    if harness_resource_list_nonempty "$missing"; then
      echo "Missing services:" >&2
      printf '%s\n' "$missing" | sed 's/^/  - /' >&2
    fi
    if harness_resource_list_nonempty "$extra"; then
      echo "Extra services:" >&2
      printf '%s\n' "$extra" | sed 's/^/  - /' >&2
    fi
    echo "Use 'cassini dev stack up --reset' to recreate resources for the resolved config." >&2
    return 1
  fi
}

harness_check_existing_resources_for_up() {
  harness_require_docker

  local mode="${CASSINI_HARNESS_EXISTING:-fail}"
  local compose_resources exapp_resources resources
  compose_resources="$(harness_project_resources)"
  exapp_resources=""
  if [[ "${CASSINI_HARNESS_CASSINI_MODE:-none}" == "installed-exapp" ]]; then
    exapp_resources="$(harness_installed_exapp_resources)"
  fi
  resources="$(printf '%s\n%s\n' "$compose_resources" "$exapp_resources" | sed '/^$/d')"

  case "$mode" in
    fail)
      if harness_resource_list_nonempty "$resources"; then
        echo "Cassini harness resources already exist for project '$PROJECT_NAME'." >&2
        harness_existing_resource_report "$resources"
        echo "Default stack startup is non-destructive." >&2
        echo "Use one of:" >&2
        echo "  cassini dev stack up --resume   # start matching stopped resources" >&2
        echo "  cassini dev stack up --reset    # remove and recreate resolved resources" >&2
        echo "  cassini dev stack stop --full   # remove all harness-owned resources" >&2
        return 1
      fi
      ;;
    resume)
      harness_validate_resume_resources
      ;;
    reset)
      log "Resetting Docker Compose resources for project '$PROJECT_NAME'"
      compose down --volumes --remove-orphans
      if [[ "${CASSINI_HARNESS_CASSINI_MODE:-none}" == "installed-exapp" ]]; then
        harness_remove_installed_exapp_resources
      fi
      ;;
    *)
      echo "Unknown CASSINI_HARNESS_EXISTING mode: $mode" >&2
      return 1
      ;;
  esac
}

harness_render_stack_configs() {
  if [[ "$SPREED_PROFILE" == "full" ]] && harness_remote_config_requested; then
    harness_render_full_profile_configs false
  fi
}

harness_start_compose_stack() {
  local -a services=()
  local service
  while IFS= read -r service; do
    [[ -n "$service" ]] && services+=("$service")
  done < <(harness_compose_service_args)

  if [[ "${CASSINI_HARNESS_EXISTING:-fail}" == "resume" ]]; then
    log "Resuming Docker Compose stack (profile: $SPREED_PROFILE, services: ${services[*]:-legacy-default})"
    if ((${#services[@]} > 0)); then
      compose start "${services[@]}"
    else
      compose start
    fi
  else
    log "Starting Docker Compose stack (profile: $SPREED_PROFILE, services: ${services[*]:-legacy-default})"
    if ((${#services[@]} > 0)); then
      compose up -d "${services[@]}"
    else
      compose up -d
    fi
  fi
}

harness_full_down() {
  harness_require_docker
  local -a projects=("$PROJECT_NAME" spreedtest cassini-exapp-test)
  local project seen current_project
  local -a unique_projects=()
  for project in "${projects[@]}"; do
    seen=false
    for current_project in "${unique_projects[@]}"; do
      if [[ "$current_project" == "$project" ]]; then
        seen=true
        break
      fi
    done
    [[ "$seen" == "true" ]] || unique_projects+=("$project")
  done

  for project in "${unique_projects[@]}"; do
    log "Stopping/removing harness Compose resources for project '$project'"
    docker compose -p "$project" -f "$COMPOSE_FILE" --profile full --profile remote down --volumes --remove-orphans || true
  done
  harness_remove_installed_exapp_resources
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
