#!/usr/bin/env bash
# Keep the faithful installed-ExApp validator's fresh-host dependencies explicit.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
WORKFLOW="$REPO_ROOT/.github/workflows/publish-exapp-image.yml"
ORCHESTRATOR="$SCRIPT_DIR/ci-e2e-installed-exapp-talk.sh"
VALIDATOR="$SCRIPT_DIR/validate-installed-exapp-private-talk.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }

# shellcheck disable=SC2016 # Assert the literal guard in the script source.
grep -F 'if [[ "$EXPECT_GPU_UNAVAILABLE" != "1" ]]' "$ORCHESTRATOR" >/dev/null \
  || fail "orchestrator does not exempt portable blocked mode from host media tools"
grep -F 'if (( ! EXPECT_BUILD_BLOCKED )); then' "$VALIDATOR" >/dev/null \
  || fail "validator does not exempt portable blocked mode from host media tools"
for script in "$ORCHESTRATOR" "$VALIDATOR"; do
  grep -F 'for tool in ffprobe ffmpeg' "$script" >/dev/null \
    || fail "$(basename "$script") does not preflight media tools in CUDA-positive mode"
done

faithful_job="$(sed -n '/^  faithful-installed-exapp-talk-cpu:/,/^  d403-manifest-sensitivity-control:/p' "$WORKFLOW")"
grep -F 'apt-get install -y --no-install-recommends jq libxml2-utils' <<<"$faithful_job" >/dev/null \
  || fail "faithful portable-image job does not install its minimal host tools"
if grep -Eq 'apt-get install .*\bffmpeg\b' <<<"$faithful_job"; then
  fail "faithful portable-image job unnecessarily installs host ffmpeg"
fi

echo "PASS: portable blocked mode avoids host decode tools; CUDA-positive mode preflights them"
