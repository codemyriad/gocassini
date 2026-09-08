#!/usr/bin/env bash
# Offline regression cases for selective standalone recorder CI.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
expect() {
  local want="$1" input="$2" got
  got="$(printf '%s' "$input" | "$SCRIPT_DIR/classify-recorder-integration-relevance.sh")"
  [[ "$got" == "$want" ]] || {
    printf 'FAIL: wanted %s, got %s for %q\n' "$want" "$got" "$input" >&2
    exit 1
  }
}
# Real PR shapes: #253 (app), #246 (viewer), #255 (operator), #273 (automation).
expect false $'cassini-app/src/Operator.svelte\ncassini-app/src/Operator.test.ts\nchangelog.d/change.md\n'
expect false $'cassini-viewer/src/viewer/portable.ts\nchangelog.d/change.md'
expect false $'cassini-operator/internal/operator/record_runtime.go\nchangelog.d/change.md'
expect false $'.github/workflows/pr-conflict-impact.yml\nCONTRIBUTING.md'
expect false $'cassini-microsite/src/pages/index.astro\n.github/workflows/microsite.yml'
expect false $'docs/guide.md\n\n'
# Shared inputs and unknown/new paths remain relevant, even in a mixed PR.
for path in \
  cassini-go-recorder/internal/talk/recorder.go \
  harness/go-talk-rotator/main.go harness/bin/ci-e2e-mute.sh harness/compose.yml \
  scripts/lib-exapp-register.sh deployment/compose.yml appinfo/info.xml \
  spec/cassini-portable-meeting-manifest-v1.schema.json package-lock.json \
  .github/workflows/ci.yml .github/workflows/publish-exapp-image.yml \
  scripts/classify-recorder-integration-relevance.sh \
  .gitattributes new-component/input.json; do
  expect true "$path"
  expect true $'cassini-app/src/Operator.svelte\n'"$path"
  expect true "$path"$'\ncassini-operator/internal/operator/run.go\n'
done
expect true ''
expect true $'\n'
# No trailing newline, deletion/rename source paths (git diff --no-renames).
expect true $'cassini-go-recorder/removed.go\ncassini-viewer/moved.go'
expect true $'cassini-viewer/moved.go\ncassini-go-recorder/removed.go'
echo 'PASS: recorder integration relevance fixtures'
