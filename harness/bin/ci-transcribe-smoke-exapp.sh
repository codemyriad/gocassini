#!/usr/bin/env bash
# Transcribe smoke for the bundled-model ExApp image.
#
# Asserts, per D6 of planning/bundled-parakeet-images.md:
#   1. The bundled STT model files exist inside the image at the path
#      ${CASSINI_CACHE_ROOT}/models/${CASSINI_STT_MODEL}/ BEFORE any cassini
#      operation runs (positive proof the model came from the image, not a
#      runtime download).
#   2. `cassini build` against harness/media/parakeet-smoke.mkv succeeds and
#      produces a non-empty meeting bundle.
#   3. The build logs contain NO "downloading model" line (negative proof).
#   4. The bundled file mtimes are <= the image's Created timestamp (positive
#      proof the file was baked, not written at container start).
#
# Run after build-image in CI. Locally:
#   IMAGE_REF=ghcr.io/codemyriad/gocassini:branch-foo ./harness/bin/ci-transcribe-smoke-exapp.sh

set -euo pipefail

: "${IMAGE_REF:?IMAGE_REF must be set (e.g. ghcr.io/codemyriad/gocassini:sha-abc)}"

REPO_ROOT=$(git rev-parse --show-toplevel)
FIXTURE_HOST="${REPO_ROOT}/harness/media/parakeet-smoke.mkv"
if [[ ! -s "${FIXTURE_HOST}" ]]; then
  echo "[transcribe-smoke] FAIL fixture missing or empty: ${FIXTURE_HOST}" >&2
  echo "[transcribe-smoke] run scripts/fetch-smoke-fixture.sh to regenerate" >&2
  exit 1
fi

CONTAINER_NAME="cassini-transcribe-smoke-$$"
LOG_DIR="${LOG_DIR:-/tmp/cassini-transcribe-smoke-${$}}"
mkdir -p "${LOG_DIR}"

log() { printf '[transcribe-smoke] %s\n' "$*"; }

cleanup() {
  local rc=$?
  log "cleanup (rc=${rc})"
  docker logs "${CONTAINER_NAME}" > "${LOG_DIR}/container.log" 2>&1 || true
  if [[ ${rc} -ne 0 ]]; then
    log "container log:"
    sed 's/^/    /' "${LOG_DIR}/container.log" || true
    if [[ -s "${LOG_DIR}/build.log" ]]; then
      log "build log:"
      sed 's/^/    /' "${LOG_DIR}/build.log" || true
    fi
  fi
  docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# Resolve the model id + cache root the image was built with. Both come from
# the runtime ENV — we inspect the image rather than hard-code so a future
# variant (e.g. CUDA image with model id parakeet-tdt-0.6b-v3) works without
# editing this script.
read_env() {
  local var="$1"
  docker inspect --format "{{range .Config.Env}}{{println .}}{{end}}" "${IMAGE_REF}" \
    | awk -F= -v v="${var}" '$1==v {sub(/^[^=]+=/,""); print; exit}'
}

CACHE_ROOT=$(read_env CASSINI_CACHE_ROOT)
MODEL_ID=$(read_env CASSINI_STT_MODEL)
DISALLOW=$(read_env CASSINI_DISALLOW_MODEL_DOWNLOAD)

if [[ -z "${CACHE_ROOT}" ]]; then
  log "FAIL image does not set CASSINI_CACHE_ROOT in ENV"
  exit 1
fi
if [[ -z "${MODEL_ID}" ]]; then
  log "FAIL image does not set CASSINI_STT_MODEL in ENV"
  exit 1
fi
if [[ "${DISALLOW}" != "1" && "${DISALLOW}" != "true" ]]; then
  log "FAIL image does not set CASSINI_DISALLOW_MODEL_DOWNLOAD=1 (production should disallow runtime downloads)"
  exit 1
fi

MODEL_DIR="${CACHE_ROOT}/models/${MODEL_ID}"
log "image ref:       ${IMAGE_REF}"
log "cache root:      ${CACHE_ROOT}"
log "model id:        ${MODEL_ID}"
log "model dir:       ${MODEL_DIR}"

# Image creation time as Unix seconds.
IMG_CREATED_ISO=$(docker inspect --format='{{.Created}}' "${IMAGE_REF}")
IMG_CREATED_TS=$(date -u -d "${IMG_CREATED_ISO}" +%s)
log "image created:   ${IMG_CREATED_ISO}  (ts=${IMG_CREATED_TS})"

# Start the container with the entrypoint overridden so we have a quiet host
# to docker-exec into (no operator startup, no frpc dial-out, no listener).
log "starting container ${CONTAINER_NAME}"
docker run -d --rm \
  --name "${CONTAINER_NAME}" \
  --entrypoint /bin/sh \
  "${IMAGE_REF}" \
  -c 'tail -f /dev/null' >/dev/null

# ---- Assertion 1: model files exist BEFORE any cassini operation runs ----
log "asserting bundled model files exist at ${MODEL_DIR}"
expected_files=(encoder.int8.onnx decoder.int8.onnx joiner.int8.onnx tokens.txt NOTICE)
for f in "${expected_files[@]}"; do
  if ! docker exec "${CONTAINER_NAME}" test -s "${MODEL_DIR}/${f}"; then
    log "FAIL bundled file missing or empty: ${MODEL_DIR}/${f}"
    docker exec "${CONTAINER_NAME}" ls -la "${MODEL_DIR}" 2>&1 || true
    exit 1
  fi
  log "OK   present ${MODEL_DIR}/${f}"
done

# ---- Assertion 4: bundled file mtimes are <= image creation time ----
# (positive proof the file came from the image build, not container start)
ENC_MTIME=$(docker exec "${CONTAINER_NAME}" stat -c %Y "${MODEL_DIR}/encoder.int8.onnx")
if (( ENC_MTIME > IMG_CREATED_TS + 5 )); then
  log "FAIL encoder.int8.onnx mtime (${ENC_MTIME}) is newer than image creation (${IMG_CREATED_TS})"
  log "     this indicates the file was written AFTER the image was built (runtime download?)"
  exit 1
fi
log "OK   encoder.int8.onnx mtime (${ENC_MTIME}) <= image created (${IMG_CREATED_TS})"

# ---- Assertion 2 + 3: `cassini build` succeeds + no download log line ----
docker exec "${CONTAINER_NAME}" mkdir -p /tmp/smoke-in /tmp/smoke-out
docker cp "${FIXTURE_HOST}" "${CONTAINER_NAME}:/tmp/smoke-in/parakeet-smoke.mkv"

log "running cassini build on the fixture (device=cpu)"
# Capture stdout+stderr separately so we can grep for "downloading model"
# without false positives from progress-bar control chars on stderr.
set +e
docker exec "${CONTAINER_NAME}" /usr/local/bin/cassini build \
  /tmp/smoke-in/parakeet-smoke.mkv \
  --out /tmp/smoke-out \
  --device cpu \
  > "${LOG_DIR}/build.log" 2>&1
BUILD_RC=$?
set -e
if [[ ${BUILD_RC} -ne 0 ]]; then
  log "FAIL cassini build exited ${BUILD_RC}"
  exit 1
fi
log "OK   cassini build exited 0"

if grep -qi 'downloading model' "${LOG_DIR}/build.log"; then
  log "FAIL build log contains 'downloading model' — bundled model was NOT used"
  grep -i 'downloading model' "${LOG_DIR}/build.log" | sed 's/^/    /'
  exit 1
fi
log "OK   build log contains no 'downloading model' line"

# ---- Assertion 2 (cont): meeting bundle has content ----
OUT_FILES=$(docker exec "${CONTAINER_NAME}" find /tmp/smoke-out -type f -size +0)
if [[ -z "${OUT_FILES}" ]]; then
  log "FAIL build output directory is empty"
  exit 1
fi
log "OK   build produced output files:"
printf '%s\n' "${OUT_FILES}" | sed 's/^/    /'

log "transcribe smoke passed"
