#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PUBLISHER_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO_ROOT="$(cd "${PUBLISHER_DIR}/.." && pwd)"
TRANSCRIBER_DIR="${REPO_ROOT}/cassini-transcriber"
RUNNER="${TRANSCRIBER_DIR}/bin/docker-run-local.sh"
EXPORTER="${PUBLISHER_DIR}/bin/export-static-meetings.sh"

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

if [[ ! -x "${RUNNER}" ]]; then
  echo "transcriber runner not found: ${RUNNER}" >&2
  exit 1
fi

INPUT_ABS="$(readlink -f "${INPUT_PATH}")"
OUTPUT_ROOT_ABS="$(readlink -m "${OUTPUT_ROOT}")"
MEETING_ID="$(basename "${INPUT_ABS}" .mkv)"
ARTIFACT_DIR="${OUTPUT_ROOT_ABS}/${MEETING_ID}.artifact"
RENDER_SOURCE_DIR="${OUTPUT_ROOT_ABS}/_viewer-source"
RENDER_OUTPUT_DIR="${OUTPUT_ROOT_ABS}/${MEETING_ID}.rendered"
RENDERED_INDEX="${RENDER_OUTPUT_DIR}/index.html"
RENDERED_CATALOG="${RENDER_OUTPUT_DIR}/catalog.json"
RENDERED_MEETING_DIR="${RENDER_OUTPUT_DIR}/meetings/${MEETING_ID}"

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

mkdir -p "${RENDER_SOURCE_DIR}"
ARTIFACT_MANIFEST="${ARTIFACT_DIR}/manifest.json"
VIEWER_DIST_INDEX="${REPO_ROOT}/cassini-viewer/dist/index.html"
if [[ -f "${RENDERED_INDEX}" \
  && -f "${RENDERED_CATALOG}" \
  && -f "${RENDERED_MEETING_DIR}/manifest.json" \
  && -f "${ARTIFACT_MANIFEST}" \
  && -f "${VIEWER_DIST_INDEX}" \
  && "${RENDERED_INDEX}" -nt "${ARTIFACT_MANIFEST}" \
  && "${RENDERED_INDEX}" -nt "${VIEWER_DIST_INDEX}" ]]; then
  echo "artifact_dir=${ARTIFACT_DIR}"
  echo "rendered_dir=${RENDER_OUTPUT_DIR}"
  echo "viewer_index=${RENDERED_INDEX}"
  echo "viewer_catalog=${RENDERED_CATALOG}"
  echo "viewer_meeting_dir=${RENDERED_MEETING_DIR}"
  exit 0
fi

rm -rf "${RENDER_SOURCE_DIR:?}/${MEETING_ID}"
cp -a "${ARTIFACT_DIR}" "${RENDER_SOURCE_DIR}/${MEETING_ID}"

"${EXPORTER}" --source-dir "${RENDER_SOURCE_DIR}" --output-dir "${RENDER_OUTPUT_DIR}"

echo "artifact_dir=${ARTIFACT_DIR}"
echo "rendered_dir=${RENDER_OUTPUT_DIR}"
echo "viewer_index=${RENDERED_INDEX}"
echo "viewer_catalog=${RENDERED_CATALOG}"
echo "viewer_meeting_dir=${RENDERED_MEETING_DIR}"
