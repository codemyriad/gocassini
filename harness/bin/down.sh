#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

EXTRA_ARGS=()
FULL=false
SUSPEND=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --full)
      FULL=true
      shift
      ;;
    --suspend)
      SUSPEND=true
      shift
      ;;
    --volumes)
      EXTRA_ARGS+=(--volumes --remove-orphans)
      shift
      ;;
    -h|--help)
      cat <<EOF
Usage: cassini dev stack down [--suspend|--volumes|--full]

  down (no flags) removes containers but keeps volumes (persistence).
  --suspend  Stop containers but keep them for 'up --resume'.
  --volumes  Remove containers and volumes for the current Compose project.
  --full     Remove all harness-owned Compose/ExApp resources, including volumes.
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

if [[ "$SUSPEND" == "true" ]]; then
  log "Suspending Docker Compose stack (containers kept for 'up --resume')"
  compose stop
  exit 0
fi

# Remove ExApp containers first: they attach to the compose network, so
# leaving them would block its removal. --volumes also drops their data volume.
if [[ ${#EXTRA_ARGS[@]} -gt 0 ]]; then
  log "Removing Docker Compose stack and volumes"
  harness_remove_installed_exapp_resources
  compose down "${EXTRA_ARGS[@]}"
else
  log "Removing Docker Compose stack (volumes kept)"
  harness_remove_installed_exapp_containers
  compose down
fi
