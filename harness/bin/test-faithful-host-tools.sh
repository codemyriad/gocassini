#!/usr/bin/env bash
# Keep the faithful installed-ExApp validator's fresh-host dependencies explicit.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
WORKFLOW="$REPO_ROOT/.github/workflows/publish-exapp-image.yml"
ORCHESTRATOR="$SCRIPT_DIR/ci-e2e-installed-exapp-talk.sh"
VALIDATOR="$SCRIPT_DIR/validate-installed-exapp-private-talk.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }

for script in "$ORCHESTRATOR" "$VALIDATOR"; do
  for tool in ffprobe ffmpeg; do
    grep -Eq "for tool in .*\\b${tool}\\b" "$script" \
      || fail "$(basename "$script") does not preflight $tool"
  done
done

grep -Eq 'apt-get install .*\bffmpeg\b' "$WORKFLOW" \
  || fail "faithful CI job does not install the ffmpeg package"

echo "PASS: faithful host ffmpeg/ffprobe dependencies are explicit"
