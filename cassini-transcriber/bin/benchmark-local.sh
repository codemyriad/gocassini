#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNNER="${SCRIPT_DIR}/docker-run-local.sh"

INPUT_PATH=""
OUTPUT_ROOT=""
WHISPER_MODEL="${WHISPER_MODEL:-small.en}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --input)
      INPUT_PATH="$2"
      shift 2
      ;;
    --output-root)
      OUTPUT_ROOT="$2"
      shift 2
      ;;
    --whisper-model)
      WHISPER_MODEL="$2"
      shift 2
      ;;
    *)
      echo "unknown arg: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "${INPUT_PATH}" || -z "${OUTPUT_ROOT}" ]]; then
  echo "usage: $0 --input /path/to/meeting.mkv --output-root /path/to/benchmarks [--whisper-model small.en]" >&2
  exit 2
fi

mkdir -p "${OUTPUT_ROOT}"

GPU_OUT="${OUTPUT_ROOT}/gpu"
CPU_OUT="${OUTPUT_ROOT}/cpu"
GPU_TIME_FILE="${OUTPUT_ROOT}/gpu-seconds.txt"
CPU_TIME_FILE="${OUTPUT_ROOT}/cpu-seconds.txt"

/usr/bin/time -f '%e' -o "${GPU_TIME_FILE}" \
  "${RUNNER}" \
  --input "${INPUT_PATH}" \
  --output-dir "${GPU_OUT}" \
  --device cuda \
  --whisper-model "${WHISPER_MODEL}" \
  --readable-backend none

/usr/bin/time -f '%e' -o "${CPU_TIME_FILE}" \
  "${RUNNER}" \
  --input "${INPUT_PATH}" \
  --output-dir "${CPU_OUT}" \
  --device cpu \
  --whisper-model "${WHISPER_MODEL}" \
  --readable-backend none

echo "gpu_seconds=$(cat "${GPU_TIME_FILE}")"
echo "cpu_seconds=$(cat "${CPU_TIME_FILE}")"
