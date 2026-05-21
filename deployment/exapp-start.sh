#!/usr/bin/env bash
# Entrypoint for the Nextcloud ExApp build of Cassini.
# Reads HaRP tunnel parameters from env, writes /tmp/frpc.toml, launches frpc,
# then execs cassini-operator with the AppAPI middleware enabled.
set -euo pipefail

log() {
  printf '[exapp-start] %s\n' "$*" >&2
}

require_env() {
  local name="$1"
  if [[ -z "${!name:-}" ]]; then
    log "missing required env: ${name}"
    exit 2
  fi
}

# AppAPI injects these on container start. Without them frpc cannot connect to HaRP
# and the operator cannot verify proxied requests.
require_env APP_SECRET
require_env APP_ID
require_env APP_VERSION
require_env AA_VERSION
require_env HP_FRP_ADDRESS
require_env HP_FRP_PORT
require_env HP_SHARED_KEY

# APP_HOST/APP_PORT default to 0.0.0.0:8080 via Dockerfile ENV but allow override.
: "${APP_HOST:=0.0.0.0}"
: "${APP_PORT:=8080}"

log "APP_ID=${APP_ID} APP_VERSION=${APP_VERSION} AA_VERSION=${AA_VERSION}"
log "HaRP frps=${HP_FRP_ADDRESS}:${HP_FRP_PORT}"
log "operator bind ${APP_HOST}:${APP_PORT}"
if command -v frpc >/dev/null 2>&1; then
  frpc_version=$(frpc --version 2>/dev/null || echo unknown)
  log "frpc ${frpc_version}"
fi

# Optional mutual TLS when /certs/frp is mounted by the deploy daemon.
frp_tls_block=""
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
FRPC_PID=$!
echo "${FRPC_PID}" >/tmp/frpc.pid
log "frpc pid=${FRPC_PID}"

# Storage sanity warnings — non-fatal, helps admins notice missing volume mounts.
for path in /var/lib/cassini-operator /srv/cassini-site; do
  fs=$(stat -f -c '%T' "${path}" 2>/dev/null || echo unknown)
  if [[ "${fs}" == "tmpfs" || "${fs}" == "overlayfs" || "${fs}" == "overlay" ]]; then
    log "WARNING: ${path} is on ${fs} — mount a persistent volume or recordings will be lost on restart"
  fi
done

# Exec operator. Operator binds to APP_HOST:APP_PORT when APP_PORT env is set.
# cassini-operator reads APP_HOST/APP_PORT internally; no flags needed here.
log "execing cassini-operator"
exec /usr/local/bin/cassini-operator
