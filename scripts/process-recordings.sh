#!/usr/bin/env bash
# Process all MKV recordings in /mnt/data/cassini/recordings/ into portable
# .opus files in /mnt/data/cassini/processed/ using the native cassini binary
# with sherpa-onnx for STT.
#
# Set OPENROUTER_API_KEY for LLM-based readable transcript cleanup.
# Set DEVICE=cuda to use GPU acceleration (default: cpu).
#
# Can be run on any host that has ffmpeg, or from inside container 100:
#   sudo pct exec 100 -- bash /workspace/gocassini/scripts/process-recordings.sh
set -euo pipefail

RECORDINGS_DIR="${RECORDINGS_DIR:-/mnt/data/cassini/recordings}"
PROCESSED_DIR="${PROCESSED_DIR:-/mnt/data/cassini/processed}"
CASSINI_BIN="${CASSINI_BIN:-/workspace/gocassini/bin/cassini-bin}"
DEVICE="${DEVICE:-cpu}"
CASSINI_LIB_DIR="${CASSINI_LIB_DIR:-$(dirname "$CASSINI_BIN")}"

export CASSINI_CACHE_ROOT="${CASSINI_CACHE_ROOT:-/mnt/data/cassini/.cache}"
export LD_LIBRARY_PATH="${CASSINI_LIB_DIR}${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"

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
  # if/else, not `cmd && { ok } || { fail }`: in the && || form the failure
  # branch also runs when the success branch itself fails, which quietly
  # double-counts a run as both processed and failed.
  if (
    cd "$PROCESSED_DIR"
    "$CASSINI_BIN" build "$mkv" \
      --out "$out" \
      --device "$DEVICE"
  ); then
    echo "ok   $base -> $out"
    (( ok++ )) || true
  else
    echo "FAIL $base"
    (( fail++ )) || true
  fi
  echo ""
done

echo "=== done: $ok processed, $skip skipped, $fail failed ==="
