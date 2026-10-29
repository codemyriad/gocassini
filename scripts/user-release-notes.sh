#!/usr/bin/env bash
# user-release-notes.sh — the release notes people who run Cassini read.
#
# CHANGELOG.md is the developer record: every change, one line each, folded
# from changelog.d/. The user-facing notes are curated once per release, by
# following docs/release-notes.md, as entries of the public changelog
# (cassini-microsite/src/content/changelog/*.md) whose frontmatter names the
# release in `version:`. This script reads them.
#
#   ./scripts/user-release-notes.sh --sources              # what the next notes must cover
#   ./scripts/user-release-notes.sh --version 0.3.0        # one version's notes
#   ./scripts/user-release-notes.sh --release-body 0.3.0   # GitHub release body
#   ./scripts/user-release-notes.sh --changelog            # App Store CHANGELOG.md
#   ./scripts/user-release-notes.sh --publish 0.3.0        # take its notes out of draft
#
# --sources prints every CHANGELOG.md section newer than the last version that
# has notes, then the pending changelog.d/ fragments: the material to draft
# from. --version exits 1 when the version has no notes. --release-body puts the
# notes first and the developer section in a collapsed block, or prints the
# developer section alone when there are no notes. --changelog renders a Keep a
# Changelog file with each version's notes, or its developer section where it
# has none; the App Store shows that file's section for each release.
#
# Notes are merged ahead of their release with `draft: true`, which keeps them
# off the site (it deploys from main). They count as notes all the same, and
# --publish, run by prepare-release.sh, drops the draft line in the release
# commit and prints the entries it changed.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-release-version.sh
source "$SCRIPT_DIR/lib-release-version.sh"

NOTES_REL="cassini-microsite/src/content/changelog"
DEV_CHANGELOG_URL="https://github.com/codemyriad/gocassini/blob/main/CHANGELOG.md"

usage() {
  cat >&2 <<'EOF'
Usage:
  ./scripts/user-release-notes.sh --sources
  ./scripts/user-release-notes.sh --version X.Y.Z[-pre]
  ./scripts/user-release-notes.sh --release-body X.Y.Z[-pre]
  ./scripts/user-release-notes.sh --changelog
  ./scripts/user-release-notes.sh --publish X.Y.Z[-pre]
EOF
}

# field <entry> <key> — print a frontmatter value, without surrounding quotes.
field() {
  awk -v key="$2" '
    /^---[[:space:]]*$/ { if (++n == 2) exit; next }
    n == 1 && index($0, key ":") == 1 {
      v = substr($0, length(key) + 2)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", v)
      if (v ~ /^".*"$/ || v ~ /^\047.*\047$/) v = substr(v, 2, length(v) - 2)
      print v; exit
    }
  ' "$1"
}

# body <entry> — print the markdown after the frontmatter, outer blank lines trimmed.
body() {
  awk '
    n >= 2 { buf[c++] = $0; next }
    /^---[[:space:]]*$/ { n++ }
    END {
      s = 0; while (s < c && buf[s] ~ /^[[:space:]]*$/) s++
      e = c - 1; while (e >= s && buf[e] ~ /^[[:space:]]*$/) e--
      for (i = s; i <= e; i++) print buf[i]
    }
  ' "$1"
}

# entries_for <version> — print the entries for <version>, drafts included, in
# file (date) order.
entries_for() {
  local e
  for e in "$NOTES_DIR"/*.md; do
    [[ -e "$e" ]] || continue
    [[ "$(field "$e" version)" == "$1" ]] || continue
    printf '%s\n' "$e"
  done
}

# publish <version> — drop the `draft:` line from each of <version>'s entries
# and print the ones that had it.
publish() {
  local e
  while IFS= read -r e; do
    [[ -n "$(field "$e" draft)" ]] || continue
    awk '
      /^---[[:space:]]*$/ { n++ }
      n == 1 && /^draft:/ { next }
      { print }
    ' "$e" >"$e.tmp" && mv "$e.tmp" "$e"
    printf '%s\n' "$e"
  done < <(entries_for "$1")
}

# notes <version> — print the notes for <version>. One entry prints its body;
# several (an older release announced feature by feature) each go under their
# title. Fails when the version has none.
notes() {
  local -a found
  mapfile -t found < <(entries_for "$1")
  [[ "${#found[@]}" -gt 0 ]] || return 1
  if [[ "${#found[@]}" -eq 1 ]]; then
    body "${found[0]}"
    return 0
  fi
  local e first=1
  for e in "${found[@]}"; do
    [[ "$first" -eq 1 ]] || printf '\n'
    first=0
    printf '### %s\n\n' "$(field "$e" title)"
    body "$e"
  done
}

# dev_versions — print each released version in CHANGELOG.md, newest first,
# as "<version> <date>".
dev_versions() {
  sed -nE 's/^## \[([^]]+)\] - ([0-9]{4}-[0-9]{2}-[0-9]{2}).*/\1 \2/p' "$CHANGELOG"
}

dev_section() {
  "$SCRIPT_DIR/extract-release-notes.sh" --version "$1" --changelog "$CHANGELOG"
}

sources() {
  local v d last="" pending=()
  while read -r v d; do
    if [[ -n "$(entries_for "$v")" ]]; then last="$v"; break; fi
    pending+=("$v $d")
  done < <(dev_versions)

  echo "# Material for the next user-facing release notes"
  echo
  if [[ -n "$last" ]]; then
    echo "The last release with user-facing notes is $last. Everything below shipped"
    echo "after it, or is still pending, and the next notes cover all of it."
  else
    echo "No release has user-facing notes yet; everything below is uncovered."
  fi
  local entry
  for entry in "${pending[@]}"; do
    read -r v d <<<"$entry"
    printf '\n## [%s] - %s\n\n' "$v" "$d"
    dev_section "$v"
  done
  printf '\n## Pending (changelog.d/)\n\n'
  "$SCRIPT_DIR/fold-changelog.sh" --preview
}

release_body() {
  local dev
  dev="$(dev_section "$1")"
  if notes "$1" >/dev/null 2>&1; then
    notes "$1"
    printf '\n<details>\n<summary>Every change in %s</summary>\n\n%s\n\n</details>\n' "$1" "$dev"
  else
    printf '%s\n' "$dev"
  fi
}

render_changelog() {
  cat <<EOF
# Changelog

What changed in each Cassini release. The developer changelog, with every
change, is at $DEV_CHANGELOG_URL.
EOF
  local v d
  while read -r v d; do
    printf '\n## [%s] - %s\n\n' "$v" "$d"
    notes "$v" 2>/dev/null || dev_section "$v"
  done < <(dev_versions)
}

main() {
  local mode="" version=""
  case "${1:-}" in
    --sources|--changelog) mode="${1#--}" ;;
    --version|--release-body|--publish)
      mode="${1#--}"; version="${2:-}"
      [[ -n "$version" ]] || { echo "error: $1 needs a version" >&2; usage; return 1; }
      rv_validate "$version" || return 1
      ;;
    -h|--help) usage; return 0 ;;
    *) usage; return 1 ;;
  esac

  local root
  root="$(git rev-parse --show-toplevel)"
  NOTES_DIR="$root/$NOTES_REL"
  CHANGELOG="$root/CHANGELOG.md"
  [[ -f "$CHANGELOG" ]] || { echo "error: $CHANGELOG not found" >&2; return 1; }

  case "$mode" in
    sources)      sources ;;
    changelog)    render_changelog ;;
    release-body) release_body "$version" ;;
    publish)      publish "$version" ;;
    version)
      notes "$version" || {
        echo "error: no user-facing release notes for $version in $NOTES_REL/ (see docs/release-notes.md)" >&2
        return 1
      }
      ;;
  esac
}

main "$@"
