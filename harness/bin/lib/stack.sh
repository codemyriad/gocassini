# Docker Compose/AppAPI stack helpers. Sourcing this file does not validate
# ambient harness topology; callers that start or bootstrap a stack must call
# harness_stack_init first.

if [[ "${CASSINI_HARNESS_LIB_STACK_SOURCED:-0}" == "1" ]]; then
  return 0
fi
CASSINI_HARNESS_LIB_STACK_SOURCED=1

# shellcheck source=./base.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/base.sh"
# shellcheck source=./stack-env.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/stack-env.sh"
# shellcheck source=./artifacts.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/artifacts.sh"

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

compose() {
  local profile_args=()
  if [[ "$SPREED_PROFILE" == "full" ]]; then
    profile_args+=(--profile full)
  fi
  if harness_remote_config_requested; then
    profile_args+=(--profile remote)
  fi
  if ((${#profile_args[@]} > 0)); then
    docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE" "${profile_args[@]}" "$@"
  else
    # Avoid expanding an empty array under macOS Bash 3.2 + `set -u`.
    docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE" "$@"
  fi
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
  if ! docker info >/dev/null 2>&1; then
    echo "Docker daemon is not available for the Cassini dev stack" >&2
    return 1
  fi
}

harness_verify_lan_signaling_reachability() {
  [[ "${CASSINI_HARNESS_PUBLIC_MODE:-local-http}" == "lan-http" ]] || return 0
  harness_media_selected || return 0

  local signaling_url="${SIGNALING_URL:-}"
  if [[ -z "$signaling_url" \
    || "$signaling_url" == "http://127.0.0.1:18082" \
    || "$signaling_url" == "http://127.0.0.1:28082" \
    || "$signaling_url" == "http://signaling.localhost:28082" ]]; then
    signaling_url="$(default_signaling_url)"
  fi
  signaling_url="${signaling_url%/}"

  local response=""
  local attempt
  for attempt in $(seq 1 30); do
    if response="$(curl -fsS --max-time 3 "$signaling_url/api/v1/welcome" 2>/dev/null)" \
      && [[ "$response" == *'nextcloud-spreed-signaling'* ]]; then
      log "Host can reach Talk signaling at $signaling_url"
      return 0
    fi
    sleep 2
  done

  echo "Talk signaling is not reachable from the host at $signaling_url." >&2
  if [[ "$(uname -s)" == "Darwin" ]]; then
    cat >&2 <<'EOF'
On macOS, enable Docker Desktop > Settings > Resources > Network >
"Enable host networking", apply/restart, and configure both the browser and
Nextcloud container to use the Mac LAN IP for signaling. See the macOS local
installed-ExApp guide in harness/README.md.
EOF
  else
    echo "Inspect the signaling container and host-network port 28082." >&2
  fi
  return 1
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

harness_remove_installed_exapp_containers() {
  # ExApp containers are AppAPI-spawned, not compose services, so `compose
  # down` never touches them; they also attach to the compose network and
  # block its removal. Ephemeral compute: removed on any teardown.
  docker rm -f cassini-exapp nc_app_gocassini >/dev/null 2>&1 || true
}

harness_remove_installed_exapp_resources() {
  harness_remove_installed_exapp_containers
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
    echo "Use 'cassini dev stack status' or 'cassini dev stack down --suspend' first." >&2
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
        echo "  cassini dev stack down --full   # remove all harness-owned resources" >&2
        return 1
      fi
      ;;
    resume)
      harness_validate_resume_resources
      ;;
    reset)
      log "Resetting Docker Compose resources for project '$PROJECT_NAME'"
      if [[ "${CASSINI_HARNESS_CASSINI_MODE:-none}" == "installed-exapp" ]]; then
        # AppAPI-spawned ExApp containers attach to the compose network, so
        # remove them before compose down or Docker can leave the network
        # behind as still in use. This mirrors stack down ordering.
        harness_remove_installed_exapp_resources
      fi
      compose down --volumes --remove-orphans
      ;;
    *)
      echo "Unknown CASSINI_HARNESS_EXISTING mode: $mode" >&2
      return 1
      ;;
  esac
}

harness_render_stack_configs() {
  [[ "$SPREED_PROFILE" == "full" ]] || return 0
  if harness_remote_config_requested; then
    harness_render_full_profile_configs false
  elif [[ "${CASSINI_HARNESS_CASSINI_MODE:-none}" == "installed-exapp" ]]; then
    # The local installed ExApp calls Nextcloud through Docker DNS
    # (reverse-proxy). Nextcloud then authenticates signaling backend updates
    # with that internal origin, which is intentionally not a fixed backend URL
    # in the host-network signaling config. Local harness only: accept the
    # shared backend secret for Docker-internal callback origins.
    harness_render_full_profile_configs true
  fi
}

harness_grant_nextcloud_docker_socket() {
  log "Granting Nextcloud access to host docker socket"
  if ! compose exec -T nextcloud test -S /var/run/docker.sock; then
    echo "Nextcloud container cannot see /var/run/docker.sock; AppAPI docker-install cannot be configured" >&2
    return 1
  fi

  local socket_gid
  socket_gid="$(compose exec -T nextcloud stat -c '%g' /var/run/docker.sock)"
  compose exec -T -u root nextcloud sh -c "
    EXISTING_GROUP=\$(getent group $socket_gid | cut -d: -f1)
    if [ -z \"\$EXISTING_GROUP\" ]; then
      groupadd -g $socket_gid docker-host
      GROUP_NAME=docker-host
    else
      GROUP_NAME=\$EXISTING_GROUP
    fi
    usermod -aG \"\$GROUP_NAME\" www-data
  "
  compose restart nextcloud
  wait_for_nextcloud 180
}

harness_configure_appapi_phase() {
  if [[ "${CASSINI_HARNESS_CASSINI_MODE:-none}" != "installed-exapp" ]]; then
    log "AppAPI phase: skipping because Cassini mode is '${CASSINI_HARNESS_CASSINI_MODE:-none}'"
    return 0
  fi

  local required_service
  for required_service in nextcloud appapi-harp reverse-proxy; do
    if ! compose ps --services --filter status=running | grep -Fxq "$required_service"; then
      echo "AppAPI phase requires running service '$required_service'" >&2
      return 1
    fi
  done

  harness_grant_nextcloud_docker_socket

  log "AppAPI phase: installing/enabling AppAPI"
  occ app:install app_api || true
  occ app:enable app_api

  log "AppAPI phase: registering HaRP deploy daemon"
  occ app_api:daemon:unregister docker_local >/dev/null 2>&1 || true
  occ app_api:daemon:unregister harp_local >/dev/null 2>&1 || true
  occ app_api:daemon:register \
    harp_local \
    "HaRP (local)" \
    docker-install \
    http \
    "appapi-harp:8780" \
    "http://reverse-proxy" \
    --net="${PROJECT_NAME}_default" \
    --harp \
    --harp_frp_address "appapi-harp:8782" \
    --harp_shared_key "dogfood-shared-key-not-secret" \
    --set-default \
    --compute_device=cpu

  case "${CASSINI_HARNESS_EXAPP_IMAGE_MODE:-reuse-local}" in
    build|reuse-local)
      log "AppAPI phase: mapping ghcr.io to local images"
      # Idempotent: re-runs (e.g. bootstrap after `up --resume`) hit
      # "registry map already exists", which is the desired end state.
      occ_ignore_failure app_api:daemon:registry:add harp_local \
        --registry-from=ghcr.io --registry-to=local
      ;;
    pull)
      log "AppAPI phase: leaving ghcr.io unmapped so AppAPI can pull the manifest image"
      ;;
    *)
      echo "Invalid CASSINI_HARNESS_EXAPP_IMAGE_MODE: ${CASSINI_HARNESS_EXAPP_IMAGE_MODE:-}" >&2
      return 2
      ;;
  esac
}

harness_validate_recording_secrets() {
  if [[ -z "${CASSINI_TALK_RECORDING_SECRET:-}" ]]; then
    echo "CASSINI_TALK_RECORDING_SECRET must be set for Talk recording backend configuration" >&2
    return 1
  fi
  if [[ "${CASSINI_TALK_SIGNALING_INTERNAL_SECRET:-}" != "${SIGNALING_INTERNAL_SECRET:-}" ]]; then
    echo "CASSINI_TALK_SIGNALING_INTERNAL_SECRET must match SIGNALING_INTERNAL_SECRET" >&2
    return 1
  fi
}

harness_exapp_info_xml_path() {
  local info_xml="${CASSINI_HARNESS_INFO_XML:-$REPO_ROOT/appinfo/info.xml}"
  [[ -f "$info_xml" ]] || { echo "ExApp info.xml not found: $info_xml" >&2; return 1; }
  printf '%s\n' "$info_xml"
}

harness_prepare_exapp_image() {
  if [[ "${CASSINI_HARNESS_CASSINI_MODE:-none}" != "installed-exapp" ]]; then
    log "ExApp image phase: skipping because Cassini mode is '${CASSINI_HARNESS_CASSINI_MODE:-none}'"
    return 0
  fi

  local image_mode="${CASSINI_HARNESS_EXAPP_IMAGE_MODE:-reuse-local}"
  local image_local="cassini-exapp:e2e-v3-cpu-gpu"
  local info_xml image_tag image_as_production
  # shellcheck source=./lib-exapp-image.sh
  source "$TEST_DIR/bin/lib-exapp-image.sh"
  info_xml="$(harness_exapp_info_xml_path)"
  image_tag="$(exapp_image_tag "$info_xml")"
  image_as_production="ghcr.io/codemyriad/gocassini:${image_tag}"

  case "$image_mode" in
    build)
      log "ExApp image phase: building $image_local"
      docker build -f "$REPO_ROOT/deployment/Dockerfile.exapp" -t "$image_local" "$REPO_ROOT"
      docker tag "$image_local" "$image_as_production"
      log "ExApp image phase: tagged $image_local as $image_as_production"
      ;;
    reuse-local)
      if ! docker image inspect "$image_local" >/dev/null 2>&1; then
        echo "Missing local image $image_local; rerun with --build or set CASSINI_HARNESS_EXAPP_IMAGE_MODE=pull" >&2
        return 1
      fi
      docker tag "$image_local" "$image_as_production"
      log "ExApp image phase: reused and tagged $image_local as $image_as_production"
      ;;
    pull)
      log "ExApp image phase: using pull mode for $image_as_production"
      ;;
    *)
      echo "Invalid CASSINI_HARNESS_EXAPP_IMAGE_MODE: $image_mode" >&2
      return 2
      ;;
  esac
}

harness_default_installed_exapp_backend_url() {
  # In every local-http installed topology Talk advertises a host-side URL
  # (normally 127.0.0.1:28080). That address is the ExApp container's own
  # loopback, even when the harness itself runs inside a VM whose detected
  # CASSINI_HARNESS_HOST is routable. Use Compose DNS for callbacks. Remote
  # HTTPS installs keep Talk's externally routable URL unless explicitly
  # overridden by the caller.
  if [[ "${CASSINI_HARNESS_CASSINI_MODE:-none}" == "installed-exapp" \
    && -z "${CASSINI_TALK_BACKEND_URL:-}" \
    && "${CASSINI_HARNESS_PUBLIC_MODE:-local-http}" == "local-http" ]]; then
    export CASSINI_TALK_BACKEND_URL="http://reverse-proxy"
  fi
}

harness_copy_exapp_info_xml() {
  local info_xml
  info_xml="$(harness_exapp_info_xml_path)"
  log "ExApp install phase: copying info.xml into Nextcloud ($info_xml)"
  compose cp "$info_xml" nextcloud:/tmp/gocassini-info.xml
  compose exec -T -u root nextcloud chown www-data:www-data /tmp/gocassini-info.xml
}

harness_create_standard_viewer_user() {
  log "ExApp install phase: ensuring standard viewer user 'alice'"
  export OC_PASS="Tn8mY3qVrJ2x!E2e"
  compose exec -T -e OC_PASS -u www-data nextcloud php occ user:add --password-from-env --display-name=Alice alice >/dev/null 2>&1 || true
  unset OC_PASS
}

harness_http_body_with_retry() {
  local desc="$1"
  shift
  local out=""
  local _attempt
  for _attempt in $(seq 1 60); do
    if out=$(curl -fsS "$@" 2>&1); then
      printf '%s' "$out"
      return 0
    fi
    sleep 2
  done
  echo "$desc failed: $out" >&2
  return 1
}

harness_http_ok_with_retry() {
  local desc="$1"
  shift
  local last=""
  local _attempt
  for _attempt in $(seq 1 60); do
    if last=$(curl -fsS "$@" 2>&1); then
      log "✓ $desc"
      return 0
    fi
    sleep 2
  done
  echo "$desc failed: $last" >&2
  return 1
}

harness_register_exapp() {
  harness_validate_recording_secrets
  harness_default_installed_exapp_backend_url

  local register_log="$RUNTIME_DIR/manual-test-register.log"
  local -a register_args=(
    app_api:app:register gocassini harp_local
    --info-xml /tmp/gocassini-info.xml
    --env "CASSINI_TALK_RECORDING_SECRET=$CASSINI_TALK_RECORDING_SECRET"
    --env "CASSINI_TALK_SIGNALING_INTERNAL_SECRET=$CASSINI_TALK_SIGNALING_INTERNAL_SECRET"
    --env "CASSINI_NC_ACCESS_CONTROL=$CASSINI_NC_ACCESS_CONTROL"
    --test-deploy-mode
    --wait-finish
  )
  if [[ -n "${CASSINI_TALK_BACKEND_URL:-}" ]]; then
    register_args+=(--env "CASSINI_TALK_BACKEND_URL=$CASSINI_TALK_BACKEND_URL")
  fi
  local optional_env
  for optional_env in OPENROUTER_API_KEY LLM_BASE_URL LLM_MODEL CASSINI_OPERATOR_API_TOKEN; do
    if [[ -n "${!optional_env:-}" ]]; then
      register_args+=(--env "$optional_env=${!optional_env}")
    fi
  done

  log "ExApp install phase: registering and enabling Cassini"
  if ! occ "${register_args[@]}" >"$register_log" 2>&1; then
    tail -200 "$register_log" >&2 || true
    echo "app_api:app:register failed; see $register_log" >&2
    return 1
  fi
  if grep -q 'heartbeat check failed' "$register_log"; then
    tail -200 "$register_log" >&2 || true
    echo "app_api:app:register reported heartbeat failure; see $register_log" >&2
    return 1
  fi

  log "ExApp install phase: cycling enable state"
  occ app_api:app:disable gocassini >/dev/null 2>&1 || true
  occ app_api:app:enable gocassini >/dev/null
}

harness_verify_exapp_routes() {
  log "ExApp install phase: verifying routes"
  occ app_api:app:list | grep -q 'gocassini' || { echo "gocassini missing from app_api:app:list" >&2; return 1; }

  local proxy_url="http://127.0.0.1:${NEXTCLOUD_HOST_PORT:-28080}/index.php/apps/app_api/proxy/gocassini"
  local welcome_json status_json
  welcome_json="$(harness_http_body_with_retry "welcome route" "$proxy_url/api/v1/welcome")"
  echo "$welcome_json" | grep -q '"version":1' || { echo "unexpected welcome response: $welcome_json" >&2; return 1; }

  status_json="$(harness_http_body_with_retry "operator status" -u admin:admin "$proxy_url/operator/status")"
  echo "$status_json" | grep -q '"secret_configured":true' || { echo "recording secret missing from status: $status_json" >&2; return 1; }
  echo "$status_json" | grep -q '"signaling_internal_secret_configured":true' || { echo "signaling internal secret missing from status: $status_json" >&2; return 1; }

  # D-420: one unified "Cassini" entry — the separate admin control-panel route
  # is gone; the operator surface lives inside the single app (admin-gated in V3).
  harness_http_ok_with_retry "viewer route" -u alice:Tn8mY3qVrJ2x!E2e -o /dev/null "$proxy_url/viewer/"
}

harness_install_exapp_phase() {
  if [[ "${CASSINI_HARNESS_CASSINI_MODE:-none}" != "installed-exapp" ]]; then
    log "ExApp install phase: skipping because Cassini mode is '${CASSINI_HARNESS_CASSINI_MODE:-none}'"
    return 0
  fi
  harness_copy_exapp_info_xml
  harness_create_standard_viewer_user
  harness_register_exapp
  harness_verify_exapp_routes
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

  # Remove AppAPI-spawned ExApp containers first: they attach to the project
  # network, and a compose down cannot delete a network that is still in use.
  harness_remove_installed_exapp_resources
  for project in "${unique_projects[@]}"; do
    log "Stopping/removing harness Compose resources for project '$project'"
    docker compose -p "$project" -f "$COMPOSE_FILE" --profile full --profile remote down --volumes --remove-orphans || true
  done
}

occ() {
  local env_args=()
  if [[ -n "${OC_PASS:-}" ]]; then
    env_args+=(-e "OC_PASS=$OC_PASS")
  fi
  if [[ -n "${NC_PASS:-}" ]]; then
    env_args+=(-e "NC_PASS=$NC_PASS")
  fi
  if ((${#env_args[@]} > 0)); then
    compose exec -T "${env_args[@]}" -u www-data nextcloud php occ "$@"
  else
    # Avoid expanding an empty array under macOS Bash 3.2 + `set -u`.
    compose exec -T -u www-data nextcloud php occ "$@"
  fi
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
