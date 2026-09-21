#!/usr/bin/env bash
# Model-free image and explicit-install transcription smoke (D-797).
# Checks an empty store, then installs a model outside the image and proves
# transcription with installed-only resolution. CUDA still requires real GPU use.

set -euo pipefail

: "${IMAGE_REF:?IMAGE_REF must be set (e.g. ghcr.io/codemyriad/gocassini:sha-abc)}"

# Models-only checks need no GPU, model download, or speech fixture.
MODELS_ONLY="${CASSINI_SMOKE_MODELS_ONLY:-0}"

REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
FIXTURE_HOST="${REPO_ROOT}/harness/media/parakeet-smoke.mkv"
if [[ "${MODELS_ONLY}" != "1" ]]; then
  "$(dirname "${BASH_SOURCE[0]}")/ci-ffmpeg-bundle.sh"
  if [[ ! -s "${FIXTURE_HOST}" ]]; then
    echo "[transcribe-smoke] FAIL fixture missing or empty: ${FIXTURE_HOST}" >&2
    echo "[transcribe-smoke] run scripts/fetch-smoke-fixture.sh to regenerate" >&2
    exit 1
  fi
fi

CONTAINER_NAME="cassini-transcribe-smoke-$$"
LOG_DIR="${LOG_DIR:-/tmp/cassini-transcribe-smoke-${$}}"
mkdir -p "${LOG_DIR}"

log() { printf '[transcribe-smoke] %s\n' "$*"; }

# Background nvidia-smi/docker-top sampler (assertion 5); started right
# before the build when GPU_ASSERT=1.
GPU_SAMPLER_PID=""

cleanup() {
  local rc=$?
  log "cleanup (rc=${rc})"
  if [[ -n "${GPU_SAMPLER_PID}" ]]; then
    kill "${GPU_SAMPLER_PID}" 2>/dev/null || true
  fi
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

# Select an explicit model for the native device supported by this image.
read_env() {
  local var="$1"
  docker inspect --format "{{range .Config.Env}}{{println .}}{{end}}" "${IMAGE_REF}" \
    | awk -F= -v v="${var}" '$1==v {sub(/^[^=]+=/,""); print; exit}'
}

CACHE_ROOT=$(read_env CASSINI_CACHE_ROOT)
MODEL_ID=parakeet-tdt-0.6b-v3-int8
DEVICE=$(read_env CASSINI_STT_DEVICE)

: "${DEVICE:=cpu}"
if [[ "$DEVICE" == cuda ]]; then MODEL_ID=parakeet-tdt-0.6b-v3; fi

# CUDA images need GPU exposed via CDI. Set DOCKER_RUN_GPU=1 to opt-in (or
# set CASSINI_STT_DEVICE=cuda in the image ENV — we honor either).
GPU_FLAGS=()
if [[ "${MODELS_ONLY}" != "1" ]] && [[ "${DEVICE}" == "cuda" || "${DOCKER_RUN_GPU:-0}" == "1" ]]; then
  GPU_FLAGS=(--device nvidia.com/gpu=all)
fi

# Assertion 5 (see header): prove the GPU is actually used when the image
# says device=cuda. Needs the host's nvidia-smi to observe compute apps.
GPU_ASSERT=0
if [[ "${DEVICE}" == "cuda" && "${MODELS_ONLY}" != "1" && "${CASSINI_SMOKE_GPU_ASSERT:-1}" == "1" ]]; then
  GPU_ASSERT=1
  if ! command -v nvidia-smi >/dev/null 2>&1; then
    log "FAIL device=cuda but nvidia-smi is not on the host PATH"
    log "     the CUDA smoke must prove real GPU use (D-363); install the driver"
    log "     utilities or set CASSINI_SMOKE_GPU_ASSERT=0 to skip the assertion"
    exit 1
  fi
fi

if [[ -z "${CACHE_ROOT}" ]]; then
  log "FAIL image does not set CASSINI_CACHE_ROOT in ENV"
  exit 1
fi
# Start the container with the entrypoint overridden so we have a quiet host
# to docker-exec into (no operator startup, no frpc dial-out, no listener).
log "starting container ${CONTAINER_NAME} ${GPU_FLAGS[*]:-(cpu)}"
docker run -d --rm \
  --name "${CONTAINER_NAME}" \
  "${GPU_FLAGS[@]}" \
  --entrypoint /bin/sh \
  "${IMAGE_REF}" \
  -c 'tail -f /dev/null' >/dev/null

# The image must contain no model data; inspect all known weight roots.
docker exec "${CONTAINER_NAME}" sh -c 'test ! -d /opt/cassini/cache/models && test ! -f /opt/cassini/cache/vad/silero_vad.onnx'
docker exec "${CONTAINER_NAME}" /usr/local/bin/cassini models list --json --cache-root /tmp/empty-model-store > "${LOG_DIR}/models.json"
python3 - "${LOG_DIR}/models.json" <<'CHECK'
import json,sys
models=json.load(open(sys.argv[1]))
assert len(models)==3 and all(not m['installed'] and not m['ready'] for m in models)
CHECK
docker exec -e CASSINI_TRANSCRIPTION=off "${CONTAINER_NAME}" /usr/local/bin/cassini doctor --target build > "${LOG_DIR}/doctor.log" 2>&1
if [[ "${MODELS_ONLY}" == "1" ]]; then
  log "PASS model-free image starts and lists its shipped catalogue without downloading"
  exit 0
fi

# Audio-only processing must work with an empty model store and bad STT config.
docker exec "${CONTAINER_NAME}" mkdir -p /tmp/smoke-in /tmp/smoke-out
docker cp "${FIXTURE_HOST}" "${CONTAINER_NAME}:/tmp/smoke-in/parakeet-smoke.mkv"
docker exec -e CASSINI_DISALLOW_MODEL_DOWNLOAD=1 -e CASSINI_STT_BACKEND=invalid \
  "${CONTAINER_NAME}" /usr/local/bin/cassini build /tmp/smoke-in/parakeet-smoke.mkv \
  --out /tmp/audio-only.opus --transcription off > "${LOG_DIR}/audio-only.log" 2>&1
docker exec "${CONTAINER_NAME}" /usr/local/bin/cassini inspect /tmp/audio-only.opus > "${LOG_DIR}/audio-only-inspect.log"
grep -q 'cassini=ok' "${LOG_DIR}/audio-only-inspect.log"
grep -q 'words=0' "${LOG_DIR}/audio-only-inspect.log"

# Explicit acquisition happens before a meeting. Keep it outside image layers.
log "installing ${MODEL_ID} into the writable model store"
docker exec "${CONTAINER_NAME}" /usr/local/bin/cassini models install "${MODEL_ID}" --cache-root "${CACHE_ROOT}" --device "${DEVICE}" --progress-json > "${LOG_DIR}/install.log" 2>&1
# A warm install is locally satisfied even when network acquisition is forbidden.
docker exec -e CASSINI_DISALLOW_MODEL_DOWNLOAD=1 "${CONTAINER_NAME}" /usr/local/bin/cassini models install "${MODEL_ID}" --cache-root "${CACHE_ROOT}" --device "${DEVICE}" --no-probe > "${LOG_DIR}/reuse.log" 2>&1

# While the build runs, sample (a) the host PIDs of processes inside OUR
# container (docker top) and (b) the host-visible CUDA compute apps
# (nvidia-smi). Assertion 5b later intersects the two: matching on container
# PIDs rather than process names means a concurrently-running cassini
# elsewhere on a shared GPU runner (e.g. a stale deploy-preview container)
# cannot fake a pass. The recognizer holds its CUDA context for the whole
# transcription stage, so 0.5s sampling cannot miss it.
GPU_SAMPLE_LOG="${LOG_DIR}/gpu-compute-apps.csv"
CONTAINER_PIDS_LOG="${LOG_DIR}/container-pids.log"
if (( GPU_ASSERT )); then
  log "sampling nvidia-smi compute apps during the build (GPU-use proof)"
  : > "${GPU_SAMPLE_LOG}"
  : > "${CONTAINER_PIDS_LOG}"
  (
    while :; do
      docker top "${CONTAINER_NAME}" 2>/dev/null | awk 'NR > 1 { print $2 }' >> "${CONTAINER_PIDS_LOG}" || true
      nvidia-smi --query-compute-apps=pid,process_name --format=csv,noheader >> "${GPU_SAMPLE_LOG}" 2>/dev/null || true
      sleep 0.5
    done
  ) &
  GPU_SAMPLER_PID=$!
fi

log "running cassini build on the fixture (device=${DEVICE})"
set +e
docker exec -e CASSINI_DISALLOW_MODEL_DOWNLOAD=1 -e CASSINI_STT_MODEL="${MODEL_ID}" "${CONTAINER_NAME}" /usr/local/bin/cassini build \
  /tmp/smoke-in/parakeet-smoke.mkv \
  --out /tmp/smoke-out \
  --device "${DEVICE}" --transcription on \
  > "${LOG_DIR}/build.log" 2>&1
BUILD_RC=$?
set -e

if [[ -n "${GPU_SAMPLER_PID}" ]]; then
  kill "${GPU_SAMPLER_PID}" 2>/dev/null || true
  wait "${GPU_SAMPLER_PID}" 2>/dev/null || true
  GPU_SAMPLER_PID=""
fi
if [[ ${BUILD_RC} -ne 0 ]]; then
  log "FAIL cassini build exited ${BUILD_RC}"
  exit 1
fi
log "OK   cassini build exited 0"

# Meeting builds must use only the already installed model and VAD.
if grep -qiE 'downloading (model|silero)' "${LOG_DIR}/build.log"; then
  log "FAIL build log contains a 'downloading ...' line — installed models were NOT used"
  grep -iE 'downloading (model|silero)' "${LOG_DIR}/build.log" | sed 's/^/    /'
  exit 1
fi
log "OK   build log contains no 'downloading ...' line (STT model + VAD both installed)"

# ---- Assertion 5 (CUDA only): the GPU was ACTUALLY used ----
if (( GPU_ASSERT )); then
  # 5a. Negative proof. sherpa-onnx v1.13.7 (csrc/session.cc) logs
  #     "Please compile with -DSHERPA_ONNX_ENABLE_GPU=ON. Available
  #     providers: %s. Fallback to cpu!" to stderr — and carries on, exit 0 —
  #     when the bundled onnxruntime has no CUDA provider. It logs NOTHING on
  #     the success path, so the positive proof below is empirical instead.
  if grep -qiE 'fallback to cpu' "${LOG_DIR}/build.log"; then
    log "FAIL device=cuda but sherpa-onnx fell back to CPU:"
    grep -iE 'fallback to cpu' "${LOG_DIR}/build.log" | sed 's/^/    /'
    log "     the image's onnxruntime has no usable CUDA provider — check the"
    log "     sherpa-cuda-fetcher lib swap in deployment/Dockerfile.exapp.cuda"
    exit 1
  fi
  log "OK   build log has no sherpa-onnx CPU-fallback line"

  # 5b. Positive proof: at least one host PID observed inside our container
  #     (docker top) also showed up as an nvidia-smi compute app — i.e. the
  #     build held a CUDA context. Catches every silent-fallback variant,
  #     including ones that don't log (wrong --device wiring, EP registered
  #     but unused, future sherpa-onnx log changes).
  GPU_HITS=$(awk -F', *' '
    NR == FNR { if ($1 != "") pids[$1] = 1; next }
    ($1 != "" && $1 in pids) { print }
  ' "${CONTAINER_PIDS_LOG}" "${GPU_SAMPLE_LOG}" | sort -u)
  if [[ -z "${GPU_HITS}" ]]; then
    log "FAIL device=cuda but no process from this container appeared in"
    log "     'nvidia-smi --query-compute-apps' while the build ran —"
    log "     transcription silently ran on the CPU (D-363)"
    log "     compute apps observed on the host during the build:"
    sort -u "${GPU_SAMPLE_LOG}" | sed 's/^/    /'
    log "     (empty = nothing touched the GPU at all)"
    exit 1
  fi
  log "OK   GPU was used by this container during the build (pid, process):"
  printf '%s\n' "${GPU_HITS}" | sed 's/^/    /'
fi

# ---- Assertion 2 (cont): meeting bundle has content ----
OUT_FILES=$(docker exec "${CONTAINER_NAME}" find /tmp/smoke-out -type f -size +0)
if [[ -z "${OUT_FILES}" ]]; then
  log "FAIL build output directory is empty"
  exit 1
fi
log "OK   build produced output files:"
printf '%s\n' "${OUT_FILES}" | sed 's/^/    /'

# Assert quality before cleanup, directly from this invocation's container and
# exact IMAGE_REF. No cross-job transcript cache or second model execution.
TRANSCRIPT_HOST="${LOG_DIR}/transcript.words.v1.json"
docker cp "${CONTAINER_NAME}:/tmp/smoke-out/transcript.words.v1.json" "$TRANSCRIPT_HOST"
python3 "$REPO_ROOT/harness/bin/verify-smoke-transcript.py" \
  "$TRANSCRIPT_HOST" --minimum "${MIN_LEVENSHTEIN:-0.50}"
log "transcribe smoke and transcript quality passed"
