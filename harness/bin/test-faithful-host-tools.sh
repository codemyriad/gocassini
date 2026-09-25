#!/usr/bin/env bash
# Keep the faithful installed-ExApp validator's fresh-host dependencies explicit.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
WORKFLOW="$REPO_ROOT/.github/workflows/publish-exapp-image.yml"
ORCHESTRATOR="$SCRIPT_DIR/ci-e2e-installed-exapp-talk.sh"
VALIDATOR="$SCRIPT_DIR/validate-installed-exapp-private-talk.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }

# Every mode now decodes a published transcript — a GPU-less host transcribes on
# the CPU instead of blocking the build (D-702) — so both scripts must preflight
# the media tools unconditionally, and the CPU job must install them.
# shellcheck disable=SC2016 # Assert the literal guard in the script source.
if grep -F 'if [[ "$EXPECT_GPU_UNAVAILABLE" != "1" ]]' "$ORCHESTRATOR" >/dev/null; then
  fail "orchestrator still gates host media tools on the device mode"
fi
grep -F 'if (( ! EXPECT_BUILD_BLOCKED )); then' "$VALIDATOR" >/dev/null \
  || fail "validator does not exempt the blocked-build mode from host media tools"
for script in "$ORCHESTRATOR" "$VALIDATOR"; do
  grep -F 'for tool in ffprobe ffmpeg' "$script" >/dev/null \
    || fail "$(basename "$script") does not preflight media tools"
done

# One direct-share model applies to every installed scenario.
if grep -F -- '--storage-mode' "$ORCHESTRATOR" >/dev/null; then
  fail "faithful private-Talk orchestrator still chooses a storage mode"
fi

compatibility_job="$(sed -n '/^  compatibility:/,/^  faithful-installed-exapp-talk-cpu:/p' "$WORKFLOW")"
grep -F 'uses: ./.github/actions/compatibility-tools' <<<"$compatibility_job" >/dev/null \
  || fail "compatibility matrix does not prepare host tools"
tools_action="$REPO_ROOT/.github/actions/compatibility-tools/action.yml"
grep -Eq 'apt-get install .*\bffmpeg\b' "$tools_action" \
  || fail "installed compatibility tools must install host ffmpeg"
grep -F 'playwright install --with-deps chromium' "$tools_action" >/dev/null \
  || fail "installed compatibility tools must install a real browser"

echo "PASS: both device modes preflight host decode tools and the CPU job installs them"
