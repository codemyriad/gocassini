#!/usr/bin/env bash
# release-preview.sh — decide your next release move, read-only.
#
# Shows the current version, an on-the-fly fold of the pending changelog.d/
# fragments (so you can see what would ship BEFORE choosing a bump), and the
# concrete next moves:
#   - on a stable version: the patch / minor / major candidates, with semver
#     guidance, so the changelog tells you which to pick.
#   - mid-prerelease: how to advance the ladder (and that you can skip stages
#     or jump straight to stable).
#
# Touches nothing. Follow up with scripts/prepare-release.sh.
#
#   ./scripts/release-preview.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-release-version.sh
source "$SCRIPT_DIR/lib-release-version.sh"

info_xml="$(rv_info_xml)"
current="$(rv_read_version "$info_xml")"

echo "Current version: $current"
echo
echo "Pending changes (changelog.d/):"
echo
"$SCRIPT_DIR/fold-changelog.sh" --preview | sed 's/^/  /'
echo

read -r _maj _min _pat pretype _prenum <<<"$(rv_parse "$current")"

if [[ -z "$pretype" ]]; then
  echo "This is a stable version — a release starts a new prerelease train at -alpha.1."
  echo "Pick the bump from the changes above:"
  printf '  %-6s → %-16s %s\n' patch "$(rv_bump patch "$current")" "backward-compatible fixes only"
  printf '  %-6s → %-16s %s\n' minor "$(rv_bump minor "$current")" "new features, no breaking changes"
  printf '  %-6s → %-16s %s\n' major "$(rv_bump major "$current")" "breaking changes"
  echo
  echo "Then:  ./scripts/prepare-release.sh --bump <patch|minor|major> --push"
else
  base="${_maj}.${_min}.${_pat}"
  echo "This is a prerelease — advance it along the ladder:"
  case "$pretype" in
    alpha) printf '  %-14s → %s\n' "promote beta"   "$(rv_promote beta "$current")" ;;
    beta)  printf '  %-14s → %s\n' "promote rc.1"   "$(rv_promote rc.1 "$current")" ;;
    rc)
      if [[ "$_prenum" == 1 ]]; then
        printf '  %-14s → %s\n' "promote rc.2"   "$(rv_promote rc.2 "$current")"
      fi
      printf '  %-14s → %-16s %s\n' "promote stable" "$(rv_promote stable "$current")" "(finish the release)"
      ;;
  esac
  echo "  or jump to any explicit target with --version (skip stages), e.g."
  echo "     --version ${base}        # straight to stable, no more prereleases"
  echo "     --version ${base}-rc.1   # skip ahead to a candidate"
  echo
  echo "Then:  ./scripts/prepare-release.sh --promote <target> --push"
fi
