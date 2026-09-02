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
#   5. (CUDA images only, D-363) the GPU was ACTUALLY used:
#      - negative proof: no sherpa-onnx "... Fallback to cpu!" line in the
#        build log (sherpa-onnx logs that — and exits 0 — when the bundled
#        onnxruntime has no CUDA provider; it logs nothing on success), and
#      - positive proof: a process from this smoke's container appeared in
#        `nvidia-smi --query-compute-apps` (i.e. held a CUDA context) while
#        the build was running.
#      Silent CPU fallback is the documented CUDA failure mode: a CUDA
#      release that transcribes on CPU must fail this smoke, not ship.
#      Set CASSINI_SMOKE_GPU_ASSERT=0 to skip assertion 5 (e.g. exotic local
#      setups where the GPU is reachable but host nvidia-smi is not).
#
# Run after build-image in CI. Locally:
#   IMAGE_REF=ghcr.io/codemyriad/gocassini:branch-foo ./harness/bin/ci-transcribe-smoke-exapp.sh

set -euo pipefail

: "${IMAGE_REF:?IMAGE_REF must be set (e.g. ghcr.io/codemyriad/gocassini:sha-abc)}"

# CASSINI_SMOKE_MODELS_ONLY=1 checks only the image's model contract — every
# declared model bundled and accepted by the recorder's pre-build checks. That
# needs no GPU, no transcription, no ffmpeg and no LFS fixture, so the portable
# image can be held to it on a plain runner (D-702).
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
DEVICE=$(read_env CASSINI_STT_DEVICE)
BUNDLED_ROOT=$(read_env CASSINI_BUNDLED_MODEL_ROOT)
: "${DEVICE:=cpu}"

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
if [[ -z "${MODEL_ID}" ]]; then
  log "FAIL image does not set CASSINI_STT_MODEL in ENV"
  exit 1
fi
if [[ -z "${BUNDLED_ROOT}" ]]; then
  log "FAIL image does not set CASSINI_BUNDLED_MODEL_ROOT"
  log "     the recorder reads baked models from that directory before it downloads anything"
  exit 1
fi

MODEL_DIR="${BUNDLED_ROOT}/models/${MODEL_ID}"
# The VAD is baked into the image, so assert it under the bundled root. The two
# roots hold the same path in the images today, and an assertion against the
# writable cache would keep passing if that stopped being true.
VAD_PATH="${BUNDLED_ROOT}/vad/silero_vad.onnx"
log "image ref:       ${IMAGE_REF}"
log "cache root:      ${CACHE_ROOT}"
log "model id:        ${MODEL_ID}"
log "model dir:       ${MODEL_DIR}"
log "device:          ${DEVICE}"
log "vad path:        ${VAD_PATH}"

# Image creation time as Unix seconds.
IMG_CREATED_ISO=$(docker inspect --format='{{.Created}}' "${IMAGE_REF}")
IMG_CREATED_TS=$(date -u -d "${IMG_CREATED_ISO}" +%s)
log "image created:   ${IMG_CREATED_ISO}  (ts=${IMG_CREATED_TS})"

# Start the container with the entrypoint overridden so we have a quiet host
# to docker-exec into (no operator startup, no frpc dial-out, no listener).
log "starting container ${CONTAINER_NAME} ${GPU_FLAGS[*]:-(cpu)}"
docker run -d --rm \
  --name "${CONTAINER_NAME}" \
  "${GPU_FLAGS[@]}" \
  --entrypoint /bin/sh \
  "${IMAGE_REF}" \
  -c 'tail -f /dev/null' >/dev/null

# ---- Assertion 1: model files exist BEFORE any cassini operation runs ----
# Every model the image declares, not just its default: the runtime forbids
# downloads, so this set is exactly the set of quality tiers the image can
# execute, and a missing one is a tier an admin can select and never run
# (D-702).
tier_model_files() {
  case "$1" in
    parakeet-tdt-ctc-110m-en-int8) echo "model.int8.onnx tokens.txt NOTICE" ;;
    *-int8)                        echo "encoder.int8.onnx decoder.int8.onnx joiner.int8.onnx tokens.txt NOTICE" ;;
    # fp32 variants: unsuffixed onnx + external weights sidecar
    *)                             echo "encoder.onnx encoder.weights decoder.onnx joiner.onnx tokens.txt NOTICE" ;;
  esac
}

# The image declares what it bundles; each variant carries the models for the
# device it serves (the portable image the CPU tiers, the CUDA image fp32).
BUNDLED_MODELS=$(read_env CASSINI_BUNDLED_MODELS)
if [[ -z "${BUNDLED_MODELS}" ]]; then
  log "FAIL image does not declare CASSINI_BUNDLED_MODELS"
  exit 1
fi
read -r -a TIER_MODELS <<< "${BUNDLED_MODELS}"
log "declared bundled models: ${BUNDLED_MODELS}"

# The image must bake the model of the tier it runs by default, so an install
# transcribes without reaching the network. Other tiers download on demand
# (D-704), so their absence here is expected.
case " ${BUNDLED_MODELS} " in
  *" ${MODEL_ID} "*) ;;
  *)
    log "FAIL image declares models [${BUNDLED_MODELS}] but its default CASSINI_STT_MODEL is ${MODEL_ID}"
    log "     the default tier must not need a download on a fresh install"
    exit 1
    ;;
esac
for model in "${TIER_MODELS[@]}"; do
  model_dir="${BUNDLED_ROOT}/models/${model}"
  log "asserting bundled model files exist at ${model_dir}"
  for f in $(tier_model_files "${model}"); do
    if ! docker exec "${CONTAINER_NAME}" test -s "${model_dir}/${f}"; then
      log "FAIL bundled file missing or empty: ${model_dir}/${f}"
      docker exec "${CONTAINER_NAME}" ls -la "${model_dir}" 2>&1 || true
      log "     every quality tier must be executable in an image that disallows downloads"
      exit 1
    fi
    log "OK   present ${model_dir}/${f}"
  done
done

# VAD is bundled separately at ${CACHE_ROOT}/vad/silero_vad.onnx
if ! docker exec "${CONTAINER_NAME}" test -s "${VAD_PATH}"; then
  log "FAIL bundled VAD missing or empty: ${VAD_PATH}"
  exit 1
fi
log "OK   present ${VAD_PATH}"

# ---- Assertion 4: bundled file mtimes are <= image creation time ----
# (positive proof the file came from the image build, not container start)
if [[ "${MODEL_ID}" == *-int8 ]]; then
  enc_file="${MODEL_DIR}/encoder.int8.onnx"
else
  enc_file="${MODEL_DIR}/encoder.onnx"
fi
ENC_MTIME=$(docker exec "${CONTAINER_NAME}" stat -c %Y "${enc_file}")
if (( ENC_MTIME > IMG_CREATED_TS + 5 )); then
  log "FAIL ${enc_file} mtime (${ENC_MTIME}) is newer than image creation (${IMG_CREATED_TS})"
  log "     this indicates the file was written AFTER the image was built (runtime download?)"
  exit 1
fi
log "OK   ${enc_file} mtime (${ENC_MTIME}) <= image created (${IMG_CREATED_TS})"

# ---- Assertion 1b: every tier passes the recorder's own pre-build checks ----
# Presence is not enough: `cassini build` runs doctor first and aborts on a
# failed check, so a tier can be bundled and still be unrunnable if doctor
# expects a different file layout (D-702 — the CTC "fast" model was checked
# against transducer file names). doctor loads no model, so this is cheap.
for model in "${TIER_MODELS[@]}"; do
  log "running cassini doctor with CASSINI_STT_MODEL=${model}"
  if ! docker exec -e "CASSINI_STT_MODEL=${model}" "${CONTAINER_NAME}" \
       /usr/local/bin/cassini doctor > "${LOG_DIR}/doctor-${model}.log" 2>&1; then
    log "FAIL cassini doctor rejected bundled model ${model}"
    sed -n '1,40p' "${LOG_DIR}/doctor-${model}.log" | while IFS= read -r line; do log "     ${line}"; done
    exit 1
  fi
  log "OK   doctor accepts ${model}"
done

# The image contract — every declared model bundled, and every one of them
# accepted by the recorder's own pre-build checks — holds for both variants and
# needs no GPU and no transcription. CASSINI_SMOKE_MODELS_ONLY=1 runs just that
# part, so the portable image can be held to it on a plain runner instead of
# being covered only where a GPU happens to be (D-702).
if [[ "${MODELS_ONLY}" == "1" ]]; then
  log "PASS image bundles and validates every model it declares (${BUNDLED_MODELS})"
  exit 0
fi

# ---- Assertion 2 + 3: `cassini build` succeeds + no download log line ----
docker exec "${CONTAINER_NAME}" mkdir -p /tmp/smoke-in /tmp/smoke-out
docker cp "${FIXTURE_HOST}" "${CONTAINER_NAME}:/tmp/smoke-in/parakeet-smoke.mkv"

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
docker exec "${CONTAINER_NAME}" /usr/local/bin/cassini build \
  /tmp/smoke-in/parakeet-smoke.mkv \
  --out /tmp/smoke-out \
  --device "${DEVICE}" \
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

# Any "downloading ..." string in the log means the bundled assets were
# bypassed — either the STT model OR the VAD. Both must be in-image.
if grep -qiE 'downloading (model|silero)' "${LOG_DIR}/build.log"; then
  log "FAIL build log contains a 'downloading ...' line — bundled assets were NOT used"
  grep -iE 'downloading (model|silero)' "${LOG_DIR}/build.log" | sed 's/^/    /'
  exit 1
fi
log "OK   build log contains no 'downloading ...' line (STT model + VAD both bundled)"

# ---- Assertion 5 (CUDA only): the GPU was ACTUALLY used ----
if (( GPU_ASSERT )); then
  # 5a. Negative proof. sherpa-onnx v1.13.1 (csrc/session.cc) logs
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

log "transcribe smoke passed"
