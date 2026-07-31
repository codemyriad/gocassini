#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PUBLISHER_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO_ROOT="$(cd "${PUBLISHER_DIR}/.." && pwd)"
VIEWER_DIR="${REPO_ROOT}/cassini-viewer"

echo "warning: cassini-publisher/bin/export-static-meetings.sh is deprecated; prefer ./bin/cassini publish ... for .meeting bundles" >&2

if [[ ! -f "${VIEWER_DIR}/package.json" ]]; then
  echo "viewer package not found: ${VIEWER_DIR}" >&2
  exit 1
fi

# D-531: the export is lightweight by default (catalog.json + meetings/ only) and
# needs no viewer dist. Only --rebuild-viewer embeds the shell, and only then do
# we require the built dist (building it on demand if absent).
wants_rebuild_viewer=false
for arg in "$@"; do
  if [[ "$arg" == "--rebuild-viewer" ]]; then
    wants_rebuild_viewer=true
    break
  fi
done

if [[ "${wants_rebuild_viewer}" == true && ! -f "${VIEWER_DIR}/dist/index.html" ]]; then
  if ! command -v npm >/dev/null 2>&1; then
    echo "npm is required to build the viewer for --rebuild-viewer" >&2
    exit 1
  fi
  (cd "${VIEWER_DIR}" && npm install && npm run build)
fi

(cd "${VIEWER_DIR}" && node ./scripts/export-static-meetings.mjs "$@")
