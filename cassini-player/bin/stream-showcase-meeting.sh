#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
SCENARIO="$REPO_ROOT/test/scenarios/showcase-lantern-festival.v1.json"
OUTPUT_DIR="$REPO_ROOT/test/media/processed/showcase-lantern-festival-v1"

exec "$REPO_ROOT/test/bin/stream-synthetic-meeting.sh" \
  --scenario "$SCENARIO" \
  --output-dir "$OUTPUT_DIR" \
  "$@"
