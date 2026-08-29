#!/usr/bin/env bash
# Entrypoint for the Nextcloud ExApp build of Cassini.
# When HaRP tunnel parameters are present in env, writes /tmp/frpc.toml and
# launches frpc before exec'ing cassini-operator with the AppAPI middleware
# enabled. AppAPI injects HP_* only for HaRP daemons without direct connect
# (app_api DockerActions::buildDeployParams) — on Docker Socket Proxy daemons,
# HaRP direct-connect, and manual installs the operator is exec'd directly.
set -euo pipefail

log() {
  printf '[exapp-start] %s\n' "$*" >&2
}

# Native media/CUDA failures can otherwise write multi-gigabyte core files into
# the container's writable overlay. Production images do not ship debugger
# symbols, and repeated decoder retries must not exhaust the host filesystem.
if ! ulimit -c 0; then
  log "WARNING: unable to disable core dumps"
fi

require_env() {
  local name="$1"
  if [[ -z "${!name:-}" ]]; then
    log "missing required env: ${name}"
    exit 2
  fi
}

# AppAPI injects these on container start for every daemon flavor. Without
# them the operator cannot verify proxied requests.
require_env APP_SECRET
require_env APP_ID
require_env APP_VERSION

# APP_HOST/APP_PORT default to 0.0.0.0:8080 via Dockerfile ENV but allow override.
: "${APP_HOST:=0.0.0.0}"
: "${APP_PORT:=8080}"

log "APP_ID=${APP_ID} APP_VERSION=${APP_VERSION} AA_VERSION=${AA_VERSION:-unset}"
log "operator bind ${APP_HOST}:${APP_PORT}"

start_frpc() {
  log "HaRP frps=${HP_FRP_ADDRESS}:${HP_FRP_PORT}"
  if command -v frpc >/dev/null 2>&1; then
    local frpc_version
    frpc_version=$(frpc --version 2>/dev/null || echo unknown)
    log "frpc ${frpc_version}"
  fi

  # Optional mutual TLS when /certs/frp is mounted by the deploy daemon.
  local frp_tls_block=""
  if [[ -d /certs/frp ]]; then
    frp_tls_block=$(cat <<EOF
transport.tls.certFile = "/certs/frp/client.crt"
transport.tls.keyFile  = "/certs/frp/client.key"
transport.tls.trustedCaFile = "/certs/frp/ca.crt"
EOF
)
    log "mutual TLS enabled (/certs/frp present)"
  fi

  cat >/tmp/frpc.toml <<EOF
serverAddr = "${HP_FRP_ADDRESS}"
serverPort = ${HP_FRP_PORT}
loginFailExit = false
transport.tls.enable = true
# Fixed magic value, never deployment-specific. HaRP's start.sh generates
# a self-signed cert with CN=harp.nc and SAN=DNS:harp.nc on every fresh
# container start (hard-coded, no env override). frpc uses this string
# for TLS hostname verification against that cert. Changing it produces
# x509 verification failures.
transport.tls.serverName = "harp.nc"
${frp_tls_block}
metadatas.token = "${HP_SHARED_KEY}"

# Register this ExApp as a TCP backend with HaRP. The remotePort, name, and
# token are how HaRP's HAProxy correlates incoming /exapps/${APP_ID}/...
# requests with this backend — without this registration HaRP responds 503
# on every proxy hit, including AppAPI's heartbeat probe during install.
#
# No [proxies.plugin] block: frpc's plain TCP proxy already forwards to
# localIP:localPort. The HaRP reference start.sh uses unix_domain_socket
# because its example ExApp binds to a unix socket — cassini-operator
# already binds to APP_HOST:APP_PORT (typically 127.0.0.1:APP_PORT under
# HaRP), so plain TCP forwarding is the right thing here.
[[proxies]]
name = "${APP_ID}"
type = "tcp"
remotePort = ${APP_PORT}
localIP = "127.0.0.1"
localPort = ${APP_PORT}
EOF

  # Launch frpc in the background. tini (PID 1) reaps it on shutdown.
  log "launching frpc"
  frpc -c /tmp/frpc.toml &
  local frpc_pid=$!
  echo "${frpc_pid}" >/tmp/frpc.pid
  log "frpc pid=${frpc_pid}"
}

# HaRP tunnel parameters. Partial HP_* env is a deploy-daemon misconfiguration,
# so require the full set as soon as any one of them is present.
if [[ -n "${HP_FRP_ADDRESS:-}" || -n "${HP_FRP_PORT:-}" || -n "${HP_SHARED_KEY:-}" ]]; then
  require_env HP_FRP_ADDRESS
  require_env HP_FRP_PORT
  require_env HP_SHARED_KEY
  start_frpc
else
  log "no HaRP env (HP_FRP_ADDRESS/HP_FRP_PORT/HP_SHARED_KEY unset) — assuming direct-reachable daemon, skipping frpc"
  rm -f /tmp/frpc.pid
fi

# Storage sanity warnings — non-fatal, helps admins notice missing persistence.
# Mirrors the operator's default resolution (exAppDataPathDefault in
# cassini-operator/internal/operator/exapp.go): when AppAPI's persistent
# volume is mounted (APP_PERSISTENT_STORAGE), data paths left unset or still
# at their baked image defaults are redirected under it; explicit overrides
# are checked as-is.
effective_data_path() {
  local value="$1" image_default="$2" persist_rel="$3"
  if [[ -n "${APP_PERSISTENT_STORAGE:-}" && ( -z "${value}" || "${value}" == "${image_default}" ) ]]; then
    printf '%s/%s' "${APP_PERSISTENT_STORAGE%/}" "${persist_rel}"
  else
    printf '%s' "${value:-${image_default}}"
  fi
}

db_path=$(effective_data_path "${CASSINI_OPERATOR_DB_PATH:-}" /var/lib/cassini-operator/jobs.sqlite3 operator/jobs.sqlite3)
work_root=$(effective_data_path "${CASSINI_OPERATOR_WORK_ROOT:-${WORK_ROOT:-}}" /var/lib/cassini-operator/jobs operator/jobs)
site_root=$(effective_data_path "${CASSINI_OPERATOR_SITE_ROOT:-${SITE_ROOT:-}}" /srv/cassini-site/published site/published)

for path in "$(dirname "${db_path}")" "${work_root}" "${site_root}"; do
  mkdir -p "${path}" 2>/dev/null || true
  fs=$(stat -f -c '%T' "${path}" 2>/dev/null || echo unknown)
  if [[ "${fs}" == "tmpfs" || "${fs}" == "overlayfs" || "${fs}" == "overlay" ]]; then
    log "WARNING: ${path} is on ${fs} — mount a persistent volume or recordings will be lost on restart"
  fi
done

# Exec operator. Operator binds to APP_HOST:APP_PORT when APP_PORT env is set.
# cassini-operator reads APP_HOST/APP_PORT internally; no flags needed here.
log "execing cassini-operator"
exec /usr/local/bin/cassini-operator
