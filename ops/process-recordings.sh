#!/usr/bin/env bash
# Process all MKV recordings in /mnt/data/cassini/recordings/ into portable
# .opus files in /mnt/data/cassini/processed/ using the cassini-transcriber
# Docker container (requires NVIDIA GPU via container 100).
#
# Run this from INSIDE the ai-worker container (CT 100) where Docker is
# available, or via: sudo pct exec 100 -- bash /workspace/gocassini/ops/process-recordings.sh
set -euo pipefail

RECORDINGS_DIR="${RECORDINGS_DIR:-/mnt/data/cassini/recordings}"
PROCESSED_DIR="${PROCESSED_DIR:-/mnt/data/cassini/processed}"
CASSINI_BIN="${CASSINI_BIN:-/workspace/gocassini/bin/cassini-bin}"
DEVICE="${DEVICE:-cuda}"

export CASSINI_TRANSCRIBER_RUNNER="/workspace/gocassini/cassini-transcriber/bin/docker-run-local.sh"
export CASSINI_CACHE_ROOT="/mnt/data/cassini/.cache"

if [[ -z "${OPENROUTER_API_KEY:-}" ]]; then
  echo "warn: OPENROUTER_API_KEY not set — readable transcript (LLM cleanup) will be skipped" >&2
fi

mkdir -p "$PROCESSED_DIR"

shopt -s nullglob
mkv_files=("$RECORDINGS_DIR"/*.mkv)
if [[ ${#mkv_files[@]} -eq 0 ]]; then
  echo "no .mkv files found in $RECORDINGS_DIR" >&2
  exit 1
fi

echo "Found ${#mkv_files[@]} recording(s) to consider"
echo ""

ok=0
skip=0
fail=0

for mkv in "${mkv_files[@]}"; do
  base="$(basename "$mkv" .mkv)"
  out="$PROCESSED_DIR/${base}.opus"

  if [[ -f "$out" ]]; then
    echo "skip $base (already processed)"
    (( skip++ )) || true
    continue
  fi

  echo "--- processing: $base"
  (
    cd "$PROCESSED_DIR"
    "$CASSINI_BIN" build "$mkv" \
      --out "$out" \
      --device "$DEVICE"
  ) && {
    echo "ok   $base -> $out"
    (( ok++ )) || true
  } || {
    echo "FAIL $base"
    (( fail++ )) || true
  }
  echo ""
done

echo "=== done: $ok processed, $skip skipped, $fail failed ==="
