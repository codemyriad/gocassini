#!/usr/bin/env bash
# release-version.sh — inspect and advance the Cassini release version.
#
# The version lives in appinfo/info.xml <version>, pinned in lockstep to the
# AppAPI Docker <image-tag>. This CLI is the single entry point for moving
# along the Nextcloud release ladder:
#
#   X.Y.Z-alpha.1 -> X.Y.Z-beta.1 -> X.Y.Z-rc.1 -> X.Y.Z-rc.2 -> X.Y.Z
#
# It only edits appinfo/info.xml. Committing, tagging and pushing are left to
# the operator (or scripts/prepare-release.sh), so you can inspect the diff
# before a release becomes real.
#
# Usage:
#   ./scripts/release-version.sh current
#   ./scripts/release-version.sh bump patch|minor|major
#   ./scripts/release-version.sh promote beta|rc.1|rc.2|stable
#   ./scripts/release-version.sh set X.Y.Z[-(alpha|beta|rc).N]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-release-version.sh
source "$SCRIPT_DIR/lib-release-version.sh"

usage() {
  cat >&2 <<'EOF'
Usage:
  ./scripts/release-version.sh current
  ./scripts/release-version.sh bump patch|minor|major
  ./scripts/release-version.sh promote beta|rc.1|rc.2|stable
  ./scripts/release-version.sh set X.Y.Z[-(alpha|beta|rc).N]
EOF
}

# report <from> <to>: summarize the edit and print the release follow-up.
report() {
  local from="$1" to="$2" branch
  branch="$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo '<branch>')"
  echo "appinfo/info.xml: version ${from} -> ${to} (image-tag kept in lockstep)"
  echo
  echo "Next steps (or run scripts/prepare-release.sh to do this for you):"
  echo "  git commit -am \"release: ${to}\""
  echo "  git tag v${to}"
  echo "  git push origin ${branch} v${to}"
}

main() {
  local info_xml current next cmd
  info_xml="$(rv_info_xml)"
  cmd="${1:-}"
  case "$cmd" in
    current)
      rv_read_version "$info_xml"
      ;;
    bump)
      local level="${2:?usage: $0 bump patch|minor|major}"
      current="$(rv_read_version "$info_xml")"
      next="$(rv_bump "$level" "$current")"
      rv_write_version "$info_xml" "$next"
      report "$current" "$next"
      ;;
    promote)
      local target="${2:?usage: $0 promote beta|rc.1|rc.2|stable}"
      current="$(rv_read_version "$info_xml")"
      next="$(rv_promote "$target" "$current")"
      rv_write_version "$info_xml" "$next"
      report "$current" "$next"
      ;;
    set)
      local explicit="${2:?usage: $0 set X.Y.Z[-(alpha|beta|rc).N]}"
      rv_validate "$explicit"
      current="$(rv_read_version "$info_xml")"
      if [[ "$current" == "$explicit" ]]; then
        echo "appinfo/info.xml is already at $explicit — nothing to do."
        return 0
      fi
      rv_write_version "$info_xml" "$explicit"
      report "$current" "$explicit"
      ;;
    -h|--help|help|"")
      usage
      ;;
    *)
      echo "error: unknown command '$cmd'" >&2
      usage >&2
      exit 1
      ;;
  esac
}

main "$@"
