#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
SCENARIO="$REPO_ROOT/harness/scenarios/showcase-lantern-festival.v1.json"
OUTPUT_DIR="$REPO_ROOT/harness/media/processed/showcase-lantern-festival-v1"

exec "$REPO_ROOT/harness/bin/roundtrip-synthetic-meeting.sh" \
  --scenario "$SCENARIO" \
  --media-output-dir "$OUTPUT_DIR" \
  "$@"
