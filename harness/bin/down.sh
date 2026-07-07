#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

EXTRA_ARGS=()
FULL=false
STOP_ONLY=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --full)
      FULL=true
      shift
      ;;
    --stop-only)
      STOP_ONLY=true
      shift
      ;;
    --volumes)
      EXTRA_ARGS+=(--volumes --remove-orphans)
      shift
      ;;
    -h|--help)
      cat <<EOF
Usage: cassini dev stack stop [--full]
       cassini dev stack down [--volumes|--full]

  stop (no flags) stops containers but keeps them for 'up --resume'.
  --full     Remove all harness-owned Compose/ExApp resources, including volumes.
  --volumes  Compatibility option: remove volumes for the current Compose project.
EOF
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 2
      ;;
  esac
done

if [[ "$FULL" == "true" ]]; then
  log "Stopping/removing all harness-owned resources"
  harness_full_down
  exit 0
fi

if [[ "$STOP_ONLY" == "true" ]]; then
  log "Stopping Docker Compose stack (containers kept for 'up --resume')"
  compose stop
  exit 0
fi

log "Stopping Docker Compose stack"
compose down "${EXTRA_ARGS[@]}"
