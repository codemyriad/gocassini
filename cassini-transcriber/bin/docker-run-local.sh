#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
DOCKER_BIN="${DOCKER_BIN:-docker}"
CACHE_ROOT="${CASSINI_CACHE_ROOT:-${HOME}/.cache/cassini-transcriber}"

INPUT_PATH=""
OUTPUT_DIR=""
DEVICE="auto"
REBUILD=0
EXTRA_ARGS=()

contains_arg() {
  local needle="$1"
  shift
  local value
  for value in "$@"; do
    if [[ "${value}" == "${needle}" ]]; then
      return 0
    fi
  done
  return 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --input)
      INPUT_PATH="$2"
      shift 2
      ;;
    --output-dir)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    --device)
      DEVICE="$2"
      shift 2
      ;;
    --rebuild)
      REBUILD=1
      shift
      ;;
    --)
      shift
      EXTRA_ARGS+=("$@")
      break
      ;;
    *)
      EXTRA_ARGS+=("$1")
      shift
      ;;
  esac
done

if [[ -z "${INPUT_PATH}" || -z "${OUTPUT_DIR}" ]]; then
  echo "usage: $0 --input /path/to/meeting.mkv --output-dir /path/to/out [--device auto|cuda|cpu] [--rebuild] [extra cli args]" >&2
  exit 2
fi

INPUT_ABS="$(readlink -f "${INPUT_PATH}")"
OUTPUT_ABS="$(readlink -m "${OUTPUT_DIR}")"
INPUT_DIR="$(dirname "${INPUT_ABS}")"
INPUT_NAME="$(basename "${INPUT_ABS}")"

mkdir -p "${OUTPUT_ABS}" "${CACHE_ROOT}/whisper"

GPU_ENABLED=0
if [[ "${DEVICE}" == "cuda" ]]; then
  GPU_ENABLED=1
elif [[ "${DEVICE}" == "auto" ]] && command -v nvidia-smi >/dev/null 2>&1; then
  GPU_ENABLED=1
fi

WHISPER_DEVICE="cpu"
IMAGE_TAG="${CASSINI_TRANSCRIBER_IMAGE:-cassini-transcriber:cpu}"
BUILD_DEVICE="cpu"
DOCKER_ARGS=(run --rm)
if [[ "${GPU_ENABLED}" == "1" ]]; then
  DOCKER_ARGS+=(--gpus all)
  WHISPER_DEVICE="cuda"
  BUILD_DEVICE="cuda"
  if [[ -z "${CASSINI_TRANSCRIBER_IMAGE:-}" ]]; then
    IMAGE_TAG="cassini-transcriber:nvidia"
  fi
fi

HOST_UID="$(id -u)"
HOST_GID="$(id -g)"
DOCKER_ARGS+=(
  --user "${HOST_UID}:${HOST_GID}"
  --env "HOME=/tmp/cassini-transcriber-home"
)

if [[ "${REBUILD}" == "1" ]] || ! "${DOCKER_BIN}" image inspect "${IMAGE_TAG}" >/dev/null 2>&1; then
  CASSINI_TRANSCRIBER_DEVICE="${BUILD_DEVICE}" \
    CASSINI_TRANSCRIBER_IMAGE="${IMAGE_TAG}" \
    "${PROJECT_DIR}/bin/docker-build.sh"
fi

if ! contains_arg --transcriber-backend "${EXTRA_ARGS[@]}"; then
  EXTRA_ARGS+=(--transcriber-backend local-whisper)
fi
if ! contains_arg --whisper-device "${EXTRA_ARGS[@]}"; then
  EXTRA_ARGS+=(--whisper-device "${WHISPER_DEVICE}")
fi
if ! contains_arg --whisper-download-root "${EXTRA_ARGS[@]}"; then
  EXTRA_ARGS+=(--whisper-download-root /models/whisper)
fi
if ! contains_arg --readable-backend "${EXTRA_ARGS[@]}" && ! contains_arg --readable-transcript-name "${EXTRA_ARGS[@]}"; then
  EXTRA_ARGS+=(--readable-backend none)
fi
if ! contains_arg --keep-work-dir "${EXTRA_ARGS[@]}" && ! contains_arg --work-dir "${EXTRA_ARGS[@]}"; then
  EXTRA_ARGS+=(--keep-work-dir)
fi

DOCKER_ARGS+=(
  --mount "type=bind,src=${INPUT_DIR},dst=/input,readonly"
  --mount "type=bind,src=${OUTPUT_ABS},dst=/output"
  --mount "type=bind,src=${CACHE_ROOT}/whisper,dst=/models/whisper"
)

exec "${DOCKER_BIN}" "${DOCKER_ARGS[@]}" "${IMAGE_TAG}" \
  --input "/input/${INPUT_NAME}" \
  --output-dir /output \
  "${EXTRA_ARGS[@]}"
