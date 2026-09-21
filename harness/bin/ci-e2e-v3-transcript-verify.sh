#!/usr/bin/env bash
# Dual-variant end-to-end transcript-quality gate on the merged integration
# stack (PR #22 + PR #24 + PR #26 + PR #30 + PR #32).
#
# Why this exists:
#   The existing ci-transcribe-smoke-exapp.sh proves the bundled model loads
#   and `cassini build` produces output files, then shares this script's
#   quality assertion. This standalone entry point retains the same
#   Levenshtein-distance assertion so a regression in the v3 pipeline — bad
#   model load, wrong feature dim, broken VAD, decoder corruption — fails
#   loudly instead of passing on "non-empty output".
#
# Flow:
#   1. Start the selected image with an overridden quiet entrypoint.
#   2. Copy the LibriSpeech fixture in and run `cassini build` directly.
#   3. Copy transcript.words.v1.json out of the container.
#   4. Concatenate all words → lowercase ASCII text.
#   5. Compare against the fixture reference and assert character-level
#      Levenshtein ratio ≥ MIN_LEVENSHTEIN (default 0.50). This is a broad
#      corruption/empty-output floor, not a transcript-quality SLA.
#
# Invocation:
#   IMAGE_REF=cassini-exapp:e2e-v3-cpu-gpu ./harness/bin/ci-e2e-v3-transcript-verify.sh
#
# CUDA invocation:
#   IMAGE_REF=ghcr.io/codemyriad/gocassini:sha-...-cuda \
#     CASSINI_STT_DEVICE_OVERRIDE=cuda \
#     ./harness/bin/ci-e2e-v3-transcript-verify.sh
#
# The script auto-detects CUDA from image ENV (CASSINI_STT_DEVICE=cuda) and
# adds --device nvidia.com/gpu=all when needed.

set -euo pipefail

: "${IMAGE_REF:?IMAGE_REF must be set}"
# Default threshold is intentionally a low smoke floor — it catches "model
# loaded but produced gibberish / empty output" without trying to gate on
# absolute WER. Empirically on the 14.225s LibriSpeech 6930-75918-0001
# fixture: v3 fp32 (CUDA) lands ~0.95, v3 int8 (CPU) lands ~0.65 because
# sherpa-onnx's streaming VAD drops the soft opening words on this clip.
# Override with MIN_LEVENSHTEIN if your invocation knows the device variant.
MIN_LEVENSHTEIN="${MIN_LEVENSHTEIN:-0.50}"

REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
FIXTURE_HOST="${REPO_ROOT}/harness/media/parakeet-smoke.mkv"

if [[ ! -s "${FIXTURE_HOST}" ]]; then
  echo "[v3-verify] FAIL fixture missing or empty: ${FIXTURE_HOST}" >&2
  exit 1
fi

CONTAINER_NAME="cassini-v3-verify-$$"
LOG_DIR="${LOG_DIR:-/tmp/cassini-v3-verify-${$}}"
mkdir -p "${LOG_DIR}"

log() { printf '[v3-verify] %s\n' "$*"; }

cleanup() {
  local rc=$?
  log "cleanup (rc=${rc})"
  docker logs "${CONTAINER_NAME}" > "${LOG_DIR}/container.log" 2>&1 || true
  if [[ ${rc} -ne 0 && -s "${LOG_DIR}/build.log" ]]; then
    log "last build log lines:"
    tail -n 40 "${LOG_DIR}/build.log" | sed 's/^/    /' || true
  fi
  docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

read_env() {
  local var="$1"
  docker inspect --format "{{range .Config.Env}}{{println .}}{{end}}" "${IMAGE_REF}" \
    | awk -F= -v v="${var}" '$1==v {sub(/^[^=]+=/,""); print; exit}'
}

CACHE_ROOT=$(read_env CASSINI_CACHE_ROOT)
MODEL_ID=$(read_env CASSINI_STT_MODEL)
DEVICE=$(read_env CASSINI_STT_DEVICE)
: "${DEVICE:=cpu}"

GPU_FLAGS=()
if [[ "${DEVICE}" == "cuda" || "${DOCKER_RUN_GPU:-0}" == "1" ]]; then
  GPU_FLAGS=(--device nvidia.com/gpu=all)
fi

log "image:       ${IMAGE_REF}"
log "device:      ${DEVICE}"
log "model id:    ${MODEL_ID}"
log "cache root:  ${CACHE_ROOT}"

log "starting container ${CONTAINER_NAME}"
docker run -d --rm \
  --name "${CONTAINER_NAME}" \
  "${GPU_FLAGS[@]}" \
  --entrypoint /bin/sh \
  "${IMAGE_REF}" \
  -c 'tail -f /dev/null' >/dev/null

docker exec "${CONTAINER_NAME}" mkdir -p /tmp/v3-in /tmp/v3-out
docker cp "${FIXTURE_HOST}" "${CONTAINER_NAME}:/tmp/v3-in/parakeet-smoke.mkv"

log "running cassini build (device=${DEVICE})"
set +e
docker exec "${CONTAINER_NAME}" /usr/local/bin/cassini build \
  /tmp/v3-in/parakeet-smoke.mkv \
  --out /tmp/v3-out \
  --device "${DEVICE}" \
  > "${LOG_DIR}/build.log" 2>&1
BUILD_RC=$?
set -e
if [[ ${BUILD_RC} -ne 0 ]]; then
  log "FAIL cassini build exited ${BUILD_RC}"
  exit 1
fi
log "OK   cassini build exited 0"

if grep -qiE 'downloading (model|silero)' "${LOG_DIR}/build.log"; then
  log "FAIL build log contains 'downloading ...' — bundled assets were NOT used"
  exit 1
fi
log "OK   no runtime download (bundled assets used)"

# Pull the transcript out of the container.
TRANSCRIPT_HOST="${LOG_DIR}/transcript.words.v1.json"
docker cp "${CONTAINER_NAME}:/tmp/v3-out/transcript.words.v1.json" "${TRANSCRIPT_HOST}"
if [[ ! -s "${TRANSCRIPT_HOST}" ]]; then
  log "FAIL transcript file empty or missing"
  exit 1
fi
log "OK   transcript artifact pulled (${TRANSCRIPT_HOST})"

# Shared assertion; this standalone entry point still produces its own transcript.
python3 "$REPO_ROOT/harness/bin/verify-smoke-transcript.py" \
  "$TRANSCRIPT_HOST" --minimum "$MIN_LEVENSHTEIN"

log "transcript verification passed (device=${DEVICE}, model=${MODEL_ID})"
