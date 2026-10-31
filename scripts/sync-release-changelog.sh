#!/usr/bin/env bash
# sync-release-changelog.sh — bring a release cut on release/X.Y back to main.
#
# prepare-release.sh on a release branch folds that branch's changelog.d/
# fragments into its CHANGELOG.md and deletes them there. The fixes were
# cherry-picked from main with their fragments, so main still holds them: left
# alone, they would be released a second time in the next minor's notes, and
# main's CHANGELOG.md would never list the patch. Run this on a branch from main
# after the release is tagged; it makes one local commit that:
#
#   - inserts the release's `## [<version>]` section, as tagged, into
#     CHANGELOG.md in version order;
#   - deletes the fragments the release commit consumed (a cherry-pick keeps
#     the fragment's filename, so the names match on main);
#   - replaces main's release notes for the version (docs/release-notes.md),
#     usually its draft, with the entries as tagged. The release commit took
#     them out of draft, and the site deploys from main, so this is what puts
#     them on gocassini.com.
#
#   git switch -c chore/sync-0.2.1-changelog origin/main
#   ./scripts/sync-release-changelog.sh --version 0.2.1
#
# It never pushes: push the branch and open a PR to main.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-release-version.sh
source "$SCRIPT_DIR/lib-release-version.sh"

usage() {
  cat >&2 <<'EOF'
Usage:
  ./scripts/sync-release-changelog.sh --version X.Y.Z[-pre]

Run on a branch from main, after v<version> was cut on its release branch.
EOF
}

die() { echo "error: $*" >&2; exit 1; }

# notes_version — print the `version:` of the release notes entry on stdin,
# without surrounding quotes (as user-release-notes.sh reads it).
notes_version() {
  awk '
    /^---[[:space:]]*$/ { if (++n == 2) exit; next }
    n == 1 && index($0, "version:") == 1 {
      v = substr($0, 9)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", v)
      if (v ~ /^".*"$/ || v ~ /^\047.*\047$/) v = substr(v, 2, length(v) - 2)
      print v; exit
    }
  '
}

main() {
  local version=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --version) version="${2:?--version needs a value}"; shift 2 ;;
      -h|--help) usage; return 0 ;;
      *) echo "error: unknown argument '$1'" >&2; usage; return 1 ;;
    esac
  done
  [[ -n "$version" ]] || { echo "error: --version is required" >&2; usage; return 1; }
  rv_validate "$version" || return 1

  local root changelog tag branch
  root="$(git rev-parse --show-toplevel)"
  changelog="$root/CHANGELOG.md"
  tag="v$version"

  branch="$(git -C "$root" symbolic-ref --quiet --short HEAD)" \
    || die "HEAD is detached; switch to a branch from main first"
  [[ "$branch" != release/* ]] \
    || die "this brings a release back to main; run it on a branch from main, not $branch"
  git -C "$root" diff --quiet || die "worktree has unstaged changes; commit or stash first"
  git -C "$root" diff --cached --quiet || die "index has staged changes; commit or reset first"

  if git -C "$root" remote get-url origin >/dev/null 2>&1; then
    git -C "$root" fetch --quiet --tags origin \
      || die "could not fetch tags from origin"
  else
    echo "warning: no origin remote; using local tags only" >&2
  fi
  git -C "$root" rev-parse -q --verify "refs/tags/$tag" >/dev/null \
    || die "tag $tag not found"
  [[ "$(git -C "$root" log -1 --format=%s "$tag")" == "release: $version" ]] \
    || die "$tag is not a prepare-release.sh commit ('release: $version'); sync it by hand"
  [[ "$(rv_release_branch "$version")" != main ]] \
    || die "$version is cut on main; there is nothing to bring back"
  if grep -qF "## [$version]" "$changelog"; then
    die "CHANGELOG.md already has a $version section"
  fi

  # The section as tagged, header included, trailing blank lines trimmed.
  local section
  section="$(git -C "$root" show "$tag:CHANGELOG.md" | awk -v hdr="## [$version]" '
    index($0, hdr) == 1 { insec = 1 }
    insec && index($0, "## [") == 1 && index($0, hdr) != 1 { insec = 0 }
    insec { buf[n++] = $0 }
    END {
      e = n - 1; while (e >= 0 && buf[e] ~ /^[[:space:]]*$/) e--
      for (i = 0; i <= e; i++) print buf[i]
    }
  ')"
  [[ -n "$section" ]] || die "$tag's CHANGELOG.md has no $version section"

  # Insert before the first versioned section that is older than this one; main
  # lists 0.3.0-alpha.1 above 0.2.2 above 0.2.1. Append if every one is newer.
  local insert_at="" n hdr v
  while IFS=: read -r n hdr; do
    v="${hdr#'## ['}"
    v="${v%%]*}"
    rv_validate "$v" >/dev/null 2>&1 || continue
    if rv_gt "$version" "$v"; then insert_at="$n"; break; fi
  done < <(grep -n '^## \[' "$changelog")

  local tmp
  tmp="$(mktemp)"
  if [[ -n "$insert_at" ]]; then
    {
      head -n "$((insert_at - 1))" "$changelog"
      printf '%s\n\n' "$section"
      tail -n "+$insert_at" "$changelog"
    } >"$tmp"
  else
    { cat "$changelog"; printf '\n%s\n' "$section"; } >"$tmp"
  fi
  mv "$tmp" "$changelog"
  git -C "$root" add -- "$changelog"

  # Fragments the release commit deleted. One written directly on the release
  # branch was never on main; say so rather than skip it silently.
  local frag removed=0
  while IFS= read -r frag; do
    [[ -n "$frag" ]] || continue
    if [[ -e "$root/$frag" ]]; then
      git -C "$root" rm -q -- "$frag"
      removed=$((removed + 1))
    else
      echo "note: $frag is not on this branch (written on the release branch?)"
    fi
  done < <(git -C "$root" diff --name-only --diff-filter=D "$tag^" "$tag" -- changelog.d/)

  # Release notes: the tagged entries for this version replace main's.
  local notes_rel="cassini-microsite/src/content/changelog" entry notes=0
  while IFS= read -r entry; do
    [[ "$entry" == *.md ]] || continue
    if [[ "$(notes_version <"$root/$entry")" == "$version" ]]; then
      git -C "$root" rm -q -- "$entry"
    fi
  done < <(git -C "$root" ls-files -- "$notes_rel/")
  while IFS= read -r entry; do
    [[ "$entry" == *.md ]] || continue
    if [[ "$(git -C "$root" show "$tag:$entry" | notes_version)" == "$version" ]]; then
      git -C "$root" checkout -q "$tag" -- "$entry"
      notes=$((notes + 1))
    fi
  done < <(git -C "$root" ls-tree -r --name-only "$tag" -- "$notes_rel/")
  if [[ "$notes" -eq 0 && "$version" != *-* ]]; then
    echo "note: $tag has no release notes for $version (docs/release-notes.md)"
  fi

  git -C "$root" commit -q -m "chore(release): bring the $version changelog to main"
  echo "Added the $version section to CHANGELOG.md, removed $removed released fragment(s) and brought $notes release notes entr$([[ $notes -eq 1 ]] && echo y || echo ies)."
  cat <<EOF

Nothing has been pushed. Review the commit, then open a PR to main:
  git show
  git push -u origin $branch
EOF
}

main "$@"
