#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

EXTRA_ARGS=()
if [[ "${1:-}" == "--volumes" ]]; then
  EXTRA_ARGS+=(--volumes --remove-orphans)
fi

log "Stopping Docker Compose stack"
compose down "${EXTRA_ARGS[@]}"
