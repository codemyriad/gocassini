#!/usr/bin/env bash
# End-to-end test for the Cassini ExApp image.
#
# Runs the production image with the same env vars Nextcloud's AppAPI would
# inject on a real install, then verifies the contract from the outside:
#
#   - AUTHORIZATION-APP-API header with the matching shared secret is
#     accepted; missing/wrong header is rejected with 401
#   - PUT /enabled and POST /init lifecycle callbacks return 200
#   - Operator JSON API works under the configured BasePath
#   - Static SPAs (/control-panel, /viewer) serve content
#   - Direct probe without the AppAPI header returns 401 (the
#     proper middleware test, per the planning doc)
#
# The full "Nextcloud admin installs gocassini via AppAPI" flow is a
# follow-up (Slice D continued). This script is what gets us to "image
# tested in CI" today.
set -euo pipefail

: "${IMAGE_REF:?IMAGE_REF must be set}"
PORT="${PORT:-18080}"
CONTAINER_NAME="cassini-exapp-e2e-$$"
APP_SECRET="${APP_SECRET:-ci-secret-$(head -c 8 /dev/urandom | base64 | tr -d '/+=' | head -c 16)}"
APP_ID="${APP_ID:-gocassini}"
APP_VERSION="${APP_VERSION:-0.1.0}"
AA_VERSION="${AA_VERSION:-5.0.0}"
LOG_DIR="${LOG_DIR:-/tmp/cassini-e2e-$$}"
mkdir -p "${LOG_DIR}"

log() {
  printf '[e2e] %s\n' "$*"
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

log "starting ${IMAGE_REF} on port ${PORT} with APP_SECRET set"
# Bypass the entrypoint (which expects HaRP env vars and starts frpc).
# We're testing the operator's HTTP plane and AppAPI middleware directly;
# the frpc tunnel is a separate concern verified by the real-Nextcloud
# follow-up.
docker run -d \
  --name "${CONTAINER_NAME}" \
  -e CASSINI_APPAPI_REQUIRED=true \
  -e CASSINI_OPERATOR_BASE_PATH=/operator \
  -e APP_HOST=0.0.0.0 \
  -e APP_PORT=8080 \
  -e APP_ID="${APP_ID}" \
  -e APP_VERSION="${APP_VERSION}" \
  -e APP_SECRET="${APP_SECRET}" \
  -e AA_VERSION="${AA_VERSION}" \
  -p "${PORT}:8080" \
  --entrypoint /usr/local/bin/cassini-operator \
  "${IMAGE_REF}" >/dev/null

log "waiting for HTTP listener (middleware should refuse unauth requests)"
for attempt in $(seq 1 30); do
  status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PORT}/operator/jobs" || echo 000)
  if [[ "${status}" == "401" || "${status}" == "200" ]]; then
    log "operator HTTP up after ${attempt}s (status=${status})"
    break
  fi
  if [[ ${attempt} -eq 30 ]]; then
    log "operator never became ready (last status=${status})"
    docker logs "${CONTAINER_NAME}"
    exit 1
  fi
  sleep 1
done

auth_header() {
  local user="${1:-}"
  printf 'AUTHORIZATION-APP-API: %s' "$(printf '%s' "${user}:${APP_SECRET}" | base64 -w0)"
}

curl_with_headers() {
  local method="$1" path="$2"
  shift 2
  curl -sS -o /dev/null -w '%{http_code}' -X "${method}" "$@" "http://127.0.0.1:${PORT}${path}" || true
}

assert_status() {
  local label="$1" expected="$2" got="$3"
  if [[ "${got}" != "${expected}" ]]; then
    log "FAIL ${label}: got ${got}, want ${expected}"
    return 1
  fi
  log "OK   ${label}: ${got}"
}

# --- Negative tests: middleware refuses without proper auth ---

got=$(curl_with_headers GET /operator/jobs)
assert_status "GET /operator/jobs WITHOUT header" 401 "${got}"

got=$(curl_with_headers GET /operator/jobs -H 'AUTHORIZATION-APP-API: not-base64')
assert_status "GET /operator/jobs WITH malformed header" 401 "${got}"

bad_secret_b64=$(printf '%s' "alice:wrong-secret" | base64 -w0)
got=$(curl_with_headers GET /operator/jobs \
  -H "AUTHORIZATION-APP-API: ${bad_secret_b64}" \
  -H "EX-APP-ID: ${APP_ID}" \
  -H "EX-APP-VERSION: ${APP_VERSION}")
assert_status "GET /operator/jobs WITH wrong secret" 401 "${got}"

# --- Positive tests: middleware accepts proper auth ---

got=$(curl_with_headers GET /operator/jobs \
  -H "$(auth_header alice)" \
  -H "EX-APP-ID: ${APP_ID}" \
  -H "EX-APP-VERSION: ${APP_VERSION}" \
  -H "AA-VERSION: ${AA_VERSION}" \
  -H "AA-REQUEST-ID: trace-e2e-$$")
assert_status "GET /operator/jobs WITH valid auth" 200 "${got}"

# --- Lifecycle callbacks ---

got=$(curl_with_headers PUT '/enabled?enabled=1' \
  -H "$(auth_header '')" \
  -H "EX-APP-ID: ${APP_ID}" \
  -H "EX-APP-VERSION: ${APP_VERSION}")
assert_status "PUT /enabled?enabled=1" 200 "${got}"

got=$(curl_with_headers PUT '/enabled?enabled=0' \
  -H "$(auth_header '')" \
  -H "EX-APP-ID: ${APP_ID}" \
  -H "EX-APP-VERSION: ${APP_VERSION}")
assert_status "PUT /enabled?enabled=0" 200 "${got}"

got=$(curl_with_headers POST /init \
  -H "$(auth_header '')" \
  -H "EX-APP-ID: ${APP_ID}" \
  -H "EX-APP-VERSION: ${APP_VERSION}")
assert_status "POST /init" 200 "${got}"

# Re-running POST /init must be idempotent.
got=$(curl_with_headers POST /init \
  -H "$(auth_header '')" \
  -H "EX-APP-ID: ${APP_ID}" \
  -H "EX-APP-VERSION: ${APP_VERSION}")
assert_status "POST /init (replay, idempotent)" 200 "${got}"

# --- Static SPAs through AppAPI middleware ---

got=$(curl_with_headers GET '/control-panel/' \
  -H "$(auth_header admin)" \
  -H "EX-APP-ID: ${APP_ID}" \
  -H "EX-APP-VERSION: ${APP_VERSION}")
assert_status "GET /control-panel/ WITH admin auth" 200 "${got}"

# A 200 on the SPA root only proves the proxy didn't refuse — it doesn't
# prove the SPA actually shipped in the image. Pin the body shape against
# cassini-control-panel/index.html so a missing/broken bundle fails here
# instead of surfacing later as a blank page in a real Nextcloud install.
body=$(curl -sS -X GET \
  -H "$(auth_header admin)" \
  -H "EX-APP-ID: ${APP_ID}" \
  -H "EX-APP-VERSION: ${APP_VERSION}" \
  "http://127.0.0.1:${PORT}/control-panel/")
if grep -qF "<title>Cassini Control Panel</title>" <<<"${body}"; then
  log "OK   GET /control-panel/ returns the cassini SPA HTML (title marker present)"
else
  log "FAIL GET /control-panel/ body did not contain the SPA title marker"
  log "first 200 chars of response: ${body:0:200}"
  exit 1
fi

got=$(curl_with_headers GET '/control-panel/some/spa/route' \
  -H "$(auth_header admin)" \
  -H "EX-APP-ID: ${APP_ID}" \
  -H "EX-APP-VERSION: ${APP_VERSION}")
assert_status "GET /control-panel/<spa-route> (SPA fallback)" 200 "${got}"

got=$(curl_with_headers GET '/viewer/' \
  -H "$(auth_header alice)" \
  -H "EX-APP-ID: ${APP_ID}" \
  -H "EX-APP-VERSION: ${APP_VERSION}")
assert_status "GET /viewer/ WITH user auth" 200 "${got}"

# --- Lifecycle state persistence after restart ---

log "restarting container; lifecycle state should survive only if the data path is persistent"
# State lives in /var/lib/cassini-operator inside the container. With docker
# restart (not run -rm), the same writable layer is reused.
docker restart "${CONTAINER_NAME}" >/dev/null
for attempt in $(seq 1 30); do
  status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PORT}/operator/jobs" || echo 000)
  if [[ "${status}" == "401" || "${status}" == "200" ]]; then
    log "container back up after restart (status=${status})"
    break
  fi
  if [[ ${attempt} -eq 30 ]]; then
    log "container did not recover from restart (last status=${status})"
    exit 1
  fi
  sleep 1
done

got=$(curl_with_headers GET /operator/jobs \
  -H "$(auth_header alice)" \
  -H "EX-APP-ID: ${APP_ID}" \
  -H "EX-APP-VERSION: ${APP_VERSION}")
assert_status "GET /operator/jobs after restart" 200 "${got}"

log "e2e test passed"
