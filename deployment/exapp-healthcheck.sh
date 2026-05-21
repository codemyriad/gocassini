#!/usr/bin/env bash
# Healthcheck for the Cassini ExApp container.
# Reports healthy when frpc is alive AND the operator HTTP endpoint responds
# with 401 (AppAPI middleware active, refusing requests without the header).
# 200 is also acceptable in case middleware is disabled (local dev image).
set -euo pipefail

: "${APP_PORT:=8080}"
PROBE_URL="http://127.0.0.1:${APP_PORT}/operator/jobs"

if [[ -f /tmp/frpc.pid ]]; then
  FRPC_PID=$(cat /tmp/frpc.pid)
  if ! kill -0 "${FRPC_PID}" 2>/dev/null; then
    echo "frpc pid ${FRPC_PID} not alive" >&2
    exit 1
  fi
fi

status=$(curl -fsS -o /dev/null -w '%{http_code}' "${PROBE_URL}" || echo 000)
case "${status}" in
  200|401) exit 0 ;;
  *)
    echo "operator probe ${PROBE_URL} -> ${status}" >&2
    exit 1
    ;;
esac
