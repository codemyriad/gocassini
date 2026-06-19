#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

GENERATED_DIR="$RUNTIME_DIR/generated"
GENERATED_JANUS_DIR="$GENERATED_DIR/janus"
mkdir -p "$GENERATED_DIR" "$GENERATED_JANUS_DIR"

host="$CASSINI_HARNESS_HOST"
if [[ -z "$host" || "$host" == *[[:space:]]* || "$host" == *"://"* || "$host" == */* || "$host" == *:* ]]; then
  echo "CASSINI_HARNESS_HOST must be a bare host or IPv4 address without scheme, path, or port: $host" >&2
  exit 2
fi

backend_list="backend-1, backend-2, backend-3"
extra_backend=""

is_builtin_backend_host() {
  case "$1" in
    127.0.0.1|localhost|host.docker.internal)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

if ! is_builtin_backend_host "$host"; then
  backend_list+=", backend-4"
  extra_backend="

[backend-4]
url = http://$host:28080
secret = $SIGNALING_SHARED_SECRET"
fi

cat > "$GENERATED_DIR/signaling.conf" <<EOF
[http]
listen = 0.0.0.0:28082

[app]
debug = false

[sessions]
hashkey = 39a5433df8f8334f6eff8a67f6afcfc594a2599f5389b4fef316ec30277fb910

[clients]
internalsecret = $SIGNALING_INTERNAL_SECRET

[backend]
backends = $backend_list
allowall = false
timeout = 10
connectionsperhost = 16

[backend-1]
url = http://127.0.0.1:28080
secret = $SIGNALING_SHARED_SECRET

[backend-2]
url = http://localhost:28080
secret = $SIGNALING_SHARED_SECRET

[backend-3]
url = http://host.docker.internal:28080
secret = $SIGNALING_SHARED_SECRET$extra_backend

[nats]
url = nats://127.0.0.1:14222

[mcu]
type = janus
url = ws://127.0.0.1:28188
adminkey = 01e2fcd0d226d7f4cf34a8a61397f110693f05042e57ab68e94f8476a4b8f22a

[turn]
apikey = 127.0.0.1
secret = $TURN_SHARED_SECRET
servers = turn:$host:13479?transport=udp,turn:$host:13479?transport=tcp
EOF

cat > "$GENERATED_JANUS_DIR/janus.jcfg" <<EOF
general: {
  debug_level = 4
  admin_secret = "01e2fcd0d226d7f4cf34a8a61397f110693f05042e57ab68e94f8476a4b8f22a"
  plugins_folder = "/opt/janus/lib/janus/plugins"
  transports_folder = "/opt/janus/lib/janus/transports"
  events_folder = "/opt/janus/lib/janus/events"
}

nat: {
  # Rendered from CASSINI_HARNESS_HOST so Janus advertises browser-reachable candidates.
  nat_1_1_mapping = "$host"
}

media: {
  rtp_port_range = "20000-20100"
}

admin: {
  admin_port = 17088
  admin_secret = "01e2fcd0d226d7f4cf34a8a61397f110693f05042e57ab68e94f8476a4b8f22a"
}
EOF

cat > "$GENERATED_DIR/turnserver.conf" <<EOF
# Local coturn config for the test harness.
listening-port=13479
tls-listening-port=0

external-ip=$host
relay-ip=$host

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
EOF
