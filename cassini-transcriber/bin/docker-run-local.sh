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
READABLE_PROVIDER=0
OLLAMA_BIN="${CASSINI_OLLAMA_BINARY:-ollama}"

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

add_env_if_set() {
  local name="$1"
  if [[ -n "${!name:-}" ]]; then
    DOCKER_ARGS+=(--env "${name}=${!name}")
  fi
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
if ! contains_arg --keep-work-dir "${EXTRA_ARGS[@]}" && ! contains_arg --work-dir "${EXTRA_ARGS[@]}"; then
  EXTRA_ARGS+=(--keep-work-dir)
fi

for passthrough_env in \
  CASSINI_READABLE_BACKEND \
  CASSINI_READABLE_TRANSCRIPT_NAME \
  CASSINI_API_BASE_URL \
  CASSINI_API_KEY \
  CASSINI_API_MODEL \
  CASSINI_API_TIMEOUT_SECONDS \
  CASSINI_API_APP_NAME \
  CASSINI_API_SITE_URL \
  OPENAI_BASE_URL \
  OPENAI_API_KEY \
  OPENAI_MODEL \
  OPENROUTER_API_KEY \
  OPENROUTER_MODEL \
  CASSINI_OLLAMA_MODEL \
  CASSINI_OLLAMA_BINARY \
  OPENWEBUI_BASE_URL \
  OPENWEBUI_EMAIL \
  OPENWEBUI_PASSWORD \
  OPENWEBUI_MODEL
do
  add_env_if_set "${passthrough_env}"
done

if ! command -v "${OLLAMA_BIN}" >/dev/null 2>&1 && [[ -x /usr/local/bin/ollama ]]; then
  OLLAMA_BIN=/usr/local/bin/ollama
fi

if contains_arg --readable-backend "${EXTRA_ARGS[@]}" || contains_arg --readable-transcript-name "${EXTRA_ARGS[@]}"; then
  READABLE_PROVIDER=1
elif [[ -n "${CASSINI_API_KEY:-}" || -n "${OPENAI_API_KEY:-}" || -n "${OPENROUTER_API_KEY:-}" ]]; then
  READABLE_PROVIDER=1
elif [[ -n "${OPENWEBUI_BASE_URL:-}" && -n "${OPENWEBUI_EMAIL:-}" && -n "${OPENWEBUI_PASSWORD:-}" && -n "${OPENWEBUI_MODEL:-}" ]]; then
  READABLE_PROVIDER=1
elif [[ -x "${OLLAMA_BIN}" ]] && curl -fsS http://127.0.0.1:11434/v1/models >/dev/null 2>&1; then
  READABLE_PROVIDER=1
  DOCKER_ARGS+=(--add-host host.docker.internal:host-gateway)
  if [[ -z "${CASSINI_API_BASE_URL:-}" && -z "${OPENAI_BASE_URL:-}" ]]; then
    DOCKER_ARGS+=(--env "CASSINI_API_BASE_URL=http://host.docker.internal:11434/v1")
  fi
  if [[ -z "${CASSINI_API_KEY:-}" && -z "${OPENAI_API_KEY:-}" && -z "${OPENROUTER_API_KEY:-}" ]]; then
    DOCKER_ARGS+=(--env "CASSINI_API_KEY=ollama")
  fi
  if [[ -z "${CASSINI_API_MODEL:-}" && -z "${OPENAI_MODEL:-}" && -z "${OPENROUTER_MODEL:-}" ]]; then
    DOCKER_ARGS+=(--env "CASSINI_API_MODEL=${CASSINI_OLLAMA_MODEL:-qwen35-9b-q4:latest}")
  fi
fi

if [[ "${READABLE_PROVIDER}" == "1" ]] && ! contains_arg --readable-transcript-name "${EXTRA_ARGS[@]}"; then
  EXTRA_ARGS+=(--readable-transcript-name transcript.readable.v1.json)
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
