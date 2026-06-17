#!/usr/bin/env bash
# Real-entrypoint test for the Cassini ExApp image.
#
# Every other container CI script overrides --entrypoint and hand-injects
# CASSINI_OPERATOR_BASE_PATH, so the entrypoint (exapp-start.sh), the image's
# runtime ENV contract, and the HEALTHCHECK were executed by zero tests — which
# is exactly where two shipped production-breaking bugs (missing base-path ENV,
# healthcheck failing on the expected 401) hid.
#
# This script boots the image the way a real AppAPI daemon does:
#   - the image's own ENTRYPOINT (tini + exapp-start.sh), no override
#   - only AppAPI-shaped env (see app_api DockerActions::buildDeployEnvs);
#     in particular NO CASSINI_OPERATOR_BASE_PATH and NO HP_* vars, i.e. a
#     non-HaRP daemon, so the entrypoint must skip frpc and exec the operator
#   - the image's own HEALTHCHECK (exapp-healthcheck.sh)
#
# Asserts:
#   - GET /heartbeat        -> 200  (AppAPI registration probe, unauthed)
#   - GET /operator/jobs    -> 401  (middleware active AND base path baked in)
#   - GET /api/v1/welcome   -> 200  (Talk recording backend, outside middleware)
#   - docker health reaches "healthy"
set -euo pipefail

: "${IMAGE_REF:?IMAGE_REF must be set}"
PORT="${PORT:-18090}"
CONTAINER_NAME="cassini-exapp-entrypoint-e2e-$$"
APP_SECRET="${APP_SECRET:-ci-secret-$(head -c 8 /dev/urandom | base64 | tr -d '/+=' | head -c 16)}"
APP_ID="${APP_ID:-gocassini}"
APP_VERSION="${APP_VERSION:-0.1.0}"
AA_VERSION="${AA_VERSION:-5.0.0}"
LOG_DIR="${LOG_DIR:-/tmp/cassini-entrypoint-e2e-$$}"
mkdir -p "${LOG_DIR}"

log() {
  printf '[entrypoint-e2e] %s\n' "$*"
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

log "starting ${IMAGE_REF} on port ${PORT} via its real entrypoint (no HP_*, no CASSINI_* injection)"
# NEXTCLOUD_URL is part of the AppAPI env shape but nothing dials it in this
# test (the /init progress callback is the only consumer and /init is not
# called here), so a clearly-bogus value keeps the test hermetic.
docker run -d \
  --name "${CONTAINER_NAME}" \
  -e APP_HOST=0.0.0.0 \
  -e APP_PORT=8080 \
  -e APP_ID="${APP_ID}" \
  -e APP_VERSION="${APP_VERSION}" \
  -e APP_SECRET="${APP_SECRET}" \
  -e AA_VERSION="${AA_VERSION}" \
  -e NEXTCLOUD_URL="http://nextcloud.invalid" \
  -p "${PORT}:8080" \
  "${IMAGE_REF}" >/dev/null

log "waiting for the operator HTTP plane (entrypoint must exec the operator without HaRP env)"
for attempt in $(seq 1 30); do
  status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PORT}/heartbeat" 2>/dev/null || echo 000)
  if [[ "${status}" == "200" ]]; then
    log "operator HTTP up after ${attempt}s"
    break
  fi
  if [[ "$(docker inspect -f '{{.State.Running}}' "${CONTAINER_NAME}" 2>/dev/null || echo false)" != "true" ]]; then
    log "container exited during startup — entrypoint crashed without HaRP env?"
    exit 1
  fi
  if [[ ${attempt} -eq 30 ]]; then
    log "operator never became ready (last status=${status})"
    exit 1
  fi
  sleep 1
done

assert_status() {
  local label="$1" expected="$2" path="$3"
  local got
  got=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PORT}${path}" || echo 000)
  if [[ "${got}" != "${expected}" ]]; then
    log "FAIL ${label}: got ${got}, want ${expected}"
    return 1
  fi
  log "OK   ${label}: ${got}"
}

# Heartbeat answers without auth — AppAPI's registration probe.
assert_status "GET /heartbeat (AppAPI registration probe)" 200 /heartbeat

# 401 here proves two things at once: the AppAPI middleware is active (the
# image defaults CASSINI_APPAPI_REQUIRED=true) AND the JSON API is mounted
# under /operator from the image's own ENV — a missing base path would 404.
assert_status "GET /operator/jobs without auth (middleware + baked base path)" 401 /operator/jobs

# Talk recording-backend handshake endpoint lives outside the middleware
# (Talk authenticates with its own HMAC scheme, not AppAPI headers).
assert_status "GET /api/v1/welcome (Talk backend, outside middleware)" 200 /api/v1/welcome

# --- Docker health: the image HEALTHCHECK must converge on healthy ----------
#
# HEALTHCHECK is --interval=30s --start-period=20s, so the first probe lands
# ~30s after start; give it a few intervals before declaring failure.

log "waiting for docker health=healthy (image HEALTHCHECK, interval 30s)"
for attempt in $(seq 1 24); do
  if [[ "$(docker inspect -f '{{.State.Running}}' "${CONTAINER_NAME}" 2>/dev/null || echo false)" != "true" ]]; then
    log "container stopped while waiting for health"
    exit 1
  fi
  health=$(docker inspect -f '{{.State.Health.Status}}' "${CONTAINER_NAME}" 2>/dev/null || echo none)
  if [[ "${health}" == "healthy" ]]; then
    log "OK   docker health=healthy after ~$((attempt * 5))s"
    break
  fi
  if [[ ${attempt} -eq 24 ]]; then
    log "FAIL docker health never reached healthy (last=${health})"
    docker inspect -f '{{json .State.Health}}' "${CONTAINER_NAME}" || true
    exit 1
  fi
  sleep 5
done

log "entrypoint e2e passed"
