#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PUBLISHER_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO_ROOT="$(cd "${PUBLISHER_DIR}/.." && pwd)"
VIEWER_DIR="${REPO_ROOT}/cassini-viewer"

if ! command -v npm >/dev/null 2>&1; then
  echo "npm is required to export static meetings" >&2
  exit 1
fi

if [[ ! -f "${VIEWER_DIR}/package.json" ]]; then
  echo "viewer package not found: ${VIEWER_DIR}" >&2
  exit 1
fi

if [[ ! -f "${VIEWER_DIR}/dist/index.html" ]]; then
  (cd "${VIEWER_DIR}" && npm install && npm run build)
fi

(cd "${VIEWER_DIR}" && node ./scripts/export-static-meetings.mjs "$@")
