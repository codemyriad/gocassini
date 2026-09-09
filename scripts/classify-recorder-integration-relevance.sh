#!/usr/bin/env bash
# Skip standalone recorder scenarios only for known unrelated changes. These
# use the legacy Talk stack with CASSINI_MODE=none, not the operator or web UI.
# Unknown paths (including shared schemas, scripts, deployment and CI) run them.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Reuse the existing docs/assets policy without changing the ExApp image gate.
# shellcheck source=./classify-image-relevance.sh
source "$SCRIPT_DIR/classify-image-relevance.sh"

relevant=false
seen=false
while IFS= read -r path || [[ -n "$path" ]]; do
  [[ -n "$path" ]] || continue
  seen=true
  if classify_path_is_docs "$path"; then
    continue
  fi
  case "$path" in
    cassini-app/*|cassini-viewer/*|cassini-operator/*|cassini-microsite/*) ;;
    .github/workflows/pr-conflict-impact.yml|.github/workflows/microsite.yml) ;;
    *) relevant=true ;;
  esac
done
# An empty/unavailable diff is not evidence that tests are irrelevant.
[[ "$seen" == true ]] || relevant=true
printf '%s\n' "$relevant"
