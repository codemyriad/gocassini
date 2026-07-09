#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SANDBOX_HARNESS_DIR="$SCRIPT_DIR/harness_temp"
ENV_FILE="$SCRIPT_DIR/.env"

if [[ -f "$ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
fi

PROJECT_NAME="${PROJECT_NAME:-cassini-sandbox}"
SPREED_PROFILE="${SPREED_PROFILE:-full}"
REMOVE_VOLUMES=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --volumes) REMOVE_VOLUMES=true; shift ;;
    -h|--help)
      echo "Usage: sandbox/destroy.sh [--volumes]"
      exit 0
      ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done

COMPOSE=(
  docker compose
  -p "$PROJECT_NAME"
  --project-directory "$SANDBOX_HARNESS_DIR"
  -f "$SANDBOX_HARNESS_DIR/compose.yml"
  -f "$SCRIPT_DIR/compose.sandbox.yml"
)
if [[ "$SPREED_PROFILE" == "full" ]]; then
  COMPOSE+=(--profile full)
fi

args=(down --remove-orphans)
if [[ "$REMOVE_VOLUMES" == "true" ]]; then
  args+=(--volumes)
fi

"${COMPOSE[@]}" "${args[@]}"

