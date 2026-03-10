#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
READABLE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO_ROOT="$(cd "${READABLE_DIR}/.." && pwd)"
TRANSCRIBER_DIR="${REPO_ROOT}/cassini-transcriber"

cd "${TRANSCRIBER_DIR}"
python3 -m cassini_transcriber.readable_cli "$@"
