#!/usr/bin/env bash
# Smoke test for the Cassini ExApp image: runs the container with dev-mode env
# (no AppAPI required), then verifies that:
#   - /enabled and /init lifecycle endpoints respond
#   - /operator/jobs serves the existing JSON API
#   - /viewer/ serves the bundled Cassini SPA (D-420: one unified entry)
#   - /viewer/ serves the bundled viewer SPA
#
# This does NOT exercise AppAPI auth or installation. See ci-e2e-exapp.sh for
# the bare AppAPI middleware contract, ci-e2e-install-exapp.sh for the real
# Nextcloud/manual-install proxy tier, and ci-e2e-installed-exapp-talk.sh for
# the faithful AppAPI/HaRP product path.
set -euo pipefail

: "${IMAGE_REF:?IMAGE_REF must be set (e.g. ghcr.io/codemyriad/gocassini:sha-abc)}"
PORT="${PORT:-18080}"
CONTAINER_NAME="cassini-exapp-smoke-$$"
LOG_DIR="${LOG_DIR:-/tmp/cassini-smoke-${$}}"
mkdir -p "${LOG_DIR}"

log() {
  printf '[smoke] %s\n' "$*"
}

cleanup() {
  local rc=$?
  log "cleanup (rc=${rc})"
  docker logs "${CONTAINER_NAME}" >"${LOG_DIR}/container.log" 2>&1 || true
  docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
  if [[ ${rc} -ne 0 ]]; then
    log "container log:"
    sed 's/^/    /' "${LOG_DIR}/container.log" || true
  fi
}
trap cleanup EXIT

log "starting ${IMAGE_REF} on port ${PORT} (dev-mode, no APP_SECRET)"
docker run -d \
  --name "${CONTAINER_NAME}" \
  -e CASSINI_APPAPI_REQUIRED=false \
  -e CASSINI_OPERATOR_BASE_PATH=/operator \
  -e APP_HOST=0.0.0.0 \
  -e APP_PORT=8080 \
  -p "${PORT}:8080" \
  --entrypoint /usr/local/bin/cassini-operator \
  "${IMAGE_REF}" >/dev/null

log "waiting for HTTP listener"
for attempt in $(seq 1 30); do
  status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PORT}/operator/jobs" || echo 000)
  if [[ "${status}" == "200" ]]; then
    log "operator API up after ${attempt}s"
    break
  fi
  if [[ ${attempt} -eq 30 ]]; then
    log "operator API never became ready (last status=${status})"
    exit 1
  fi
  sleep 1
done

assert_status() {
  local method="$1" path="$2" expected="$3"
  local got
  got=$(curl -sS -o /dev/null -w '%{http_code}' -X "${method}" "http://127.0.0.1:${PORT}${path}" || true)
  if [[ "${got}" != "${expected}" ]]; then
    log "FAIL ${method} ${path} -> ${got}, want ${expected}"
    return 1
  fi
  log "OK   ${method} ${path} -> ${got}"
}

assert_status GET  /operator/jobs              200
assert_status PUT  '/enabled?enabled=1'        200
assert_status PUT  '/enabled?enabled=0'        200
assert_status POST /init                        200
assert_status GET  /viewer/                    200
assert_status GET  /viewer/some/spa/path       200
# /published may be empty (no recordings in CI), but a 404 from FileServer
# on the directory listing is fine; the route must at least be wired.
got=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PORT}/published/catalog.json" || true)
case "${got}" in
  200|404) log "OK   GET /published/catalog.json -> ${got}" ;;
  *)       log "FAIL GET /published/catalog.json -> ${got}"; exit 1 ;;
esac

log "smoke test passed"
