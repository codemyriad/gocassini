#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TRANSCRIBER_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO_ROOT="$(cd "${TRANSCRIBER_DIR}/.." && pwd)"
VIEWER_DIR="${REPO_ROOT}/cassini-viewer"
RUNNER="${TRANSCRIBER_DIR}/bin/docker-run-local.sh"

INPUT_PATH=""
OUTPUT_ROOT=""
BUNDLE_VIEWER=0
DEVICE="auto"
EXTRA_ARGS=()

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
    --bundle-viewer)
      BUNDLE_VIEWER=1
      shift
      ;;
    --device)
      DEVICE="$2"
      shift 2
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

if [[ -z "${INPUT_PATH}" || -z "${OUTPUT_ROOT}" ]]; then
  echo "usage: $0 --input /path/to/meeting.mkv --output-root /path/to/results [--bundle-viewer] [--device auto|cuda|cpu] [extra transcriber args]" >&2
  exit 2
fi

INPUT_ABS="$(readlink -f "${INPUT_PATH}")"
OUTPUT_ROOT_ABS="$(readlink -m "${OUTPUT_ROOT}")"
MEETING_ID="$(basename "${INPUT_ABS}" .mkv)"
ARTIFACT_DIR="${OUTPUT_ROOT_ABS}/${MEETING_ID}.artifact"
RENDER_SOURCE_DIR="${OUTPUT_ROOT_ABS}/_viewer-source"
RENDER_OUTPUT_DIR="${OUTPUT_ROOT_ABS}/${MEETING_ID}.rendered"

mkdir -p "${OUTPUT_ROOT_ABS}"

"${RUNNER}" \
  --input "${INPUT_ABS}" \
  --output-dir "${ARTIFACT_DIR}" \
  --device "${DEVICE}" \
  "${EXTRA_ARGS[@]}"

if [[ "${BUNDLE_VIEWER}" != "1" ]]; then
  echo "artifact_dir=${ARTIFACT_DIR}"
  exit 0
fi

if ! command -v npm >/dev/null 2>&1; then
  echo "npm is required for --bundle-viewer" >&2
  exit 1
fi

mkdir -p "${RENDER_SOURCE_DIR}"
rm -rf "${RENDER_SOURCE_DIR:?}/${MEETING_ID}"
cp -a "${ARTIFACT_DIR}" "${RENDER_SOURCE_DIR}/${MEETING_ID}"

if [[ ! -f "${VIEWER_DIR}/dist/index.html" ]]; then
  (cd "${VIEWER_DIR}" && npm install && npm run build)
fi

(cd "${VIEWER_DIR}" && node ./scripts/export-static-meetings.mjs --source-dir "${RENDER_SOURCE_DIR}" --output-dir "${RENDER_OUTPUT_DIR}")

echo "artifact_dir=${ARTIFACT_DIR}"
echo "rendered_dir=${RENDER_OUTPUT_DIR}"
