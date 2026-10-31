#!/usr/bin/env bash
# release-preview.sh — decide your next release move, read-only.
#
# Shows the current version, an on-the-fly fold of the pending changelog.d/
# fragments (so you can see what would ship BEFORE choosing a bump), and the
# concrete next moves:
#   - on a stable version: the patch / minor / major candidates, with semver
#     guidance and the branch each is cut on, so the changelog tells you which
#     to pick.
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
  patch="$(rv_bump patch "$current")"
  echo "This is a stable version — a patch goes straight to stable (--version ${patch}-rc.1"
  echo "for a risky fix); a minor or major starts a prerelease train at -alpha.1."
  echo "Pick the bump from the changes above:"
  printf '  %-6s → %-16s %-36s on %s\n' patch "$patch" "backward-compatible fixes only" "$(rv_release_branch "$patch")"
  printf '  %-6s → %-16s %-36s on %s\n' minor "$(rv_bump minor "$current")" "new features, no breaking changes" main
  printf '  %-6s → %-16s %-36s on %s\n' major "$(rv_bump major "$current")" "breaking changes" main
  if [[ -z "${RELEASE_PREVIEW_NO_HINT:-}" ]]; then
    echo
    echo "Then:  ./scripts/prepare-release.sh --bump <patch|minor|major> --push"
  fi
else
  base="${_maj}.${_min}.${_pat}"
  echo "This is a prerelease — advance it along the ladder (beta onwards is cut on release/${_maj}.${_min}):"
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
  if "$SCRIPT_DIR/user-release-notes.sh" --version "$base" >/dev/null 2>&1; then
    echo "  (user-facing notes for $base are written)"
  else
    echo "  (going stable needs user-facing notes for $base first: docs/release-notes.md)"
  fi
  echo "  or jump to any explicit target with --version (skip stages), e.g."
  echo "     --version ${base}        # straight to stable, no more prereleases"
  echo "     --version ${base}-rc.1   # skip ahead to a candidate"
  if [[ -z "${RELEASE_PREVIEW_NO_HINT:-}" ]]; then
    echo
    echo "Then:  ./scripts/prepare-release.sh --promote <target> --push"
  fi
fi
