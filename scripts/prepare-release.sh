#!/usr/bin/env bash
# prepare-release.sh — turn a version decision into a local release commit+tag.
#
# One command bumps appinfo/info.xml, folds changelog.d/ into CHANGELOG.md,
# extracts the release notes, commits as `release: <version>`, and tags
# `v<version>` — all LOCALLY. It never pushes: the maintainer inspects the diff
# and pushes the branch + tag intentionally (pushing the tag is what triggers
# publish-exapp-image.yml to build the release images).
#
#   ./scripts/prepare-release.sh --bump minor
#   ./scripts/prepare-release.sh --promote rc.1
#   ./scripts/prepare-release.sh --version 0.3.0-beta.1
#
# Options:
#   --bump patch|minor|major       compute the next version from info.xml
#   --promote beta|rc.1|rc.2|stable advance along the prerelease ladder
#   --version X.Y.Z[-pre]          set an explicit target version
#   --date YYYY-MM-DD              changelog date (default: today)
#   --allow-empty-changelog        release even with no changelog.d fragments
#   --no-tag                       make the commit but skip creating the tag

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-release-version.sh
source "$SCRIPT_DIR/lib-release-version.sh"

usage() {
  cat >&2 <<'EOF'
Usage:
  ./scripts/prepare-release.sh (--bump L | --promote T | --version V) [--date D]
                               [--allow-empty-changelog] [--no-tag]
EOF
}

die() { echo "error: $*" >&2; exit 1; }

main() {
  local mode="" arg="" date="" allow_empty=0 make_tag=1
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --bump)                  mode="bump";    arg="${2:?--bump needs a level}"; shift 2 ;;
      --promote)               mode="promote"; arg="${2:?--promote needs a target}"; shift 2 ;;
      --version)               mode="version"; arg="${2:?--version needs a value}"; shift 2 ;;
      --date)                  date="${2:?--date needs a value}"; shift 2 ;;
      --allow-empty-changelog) allow_empty=1; shift ;;
      --no-tag)                make_tag=0; shift ;;
      -h|--help)               usage; return 0 ;;
      *) echo "error: unknown argument '$1'" >&2; usage; return 1 ;;
    esac
  done
  [[ -n "$mode" ]] || { echo "error: one of --bump/--promote/--version is required" >&2; usage; return 1; }
  date="${date:-$(date +%F)}"

  local root info_xml current target
  root="$(git rev-parse --show-toplevel)"
  info_xml="$root/appinfo/info.xml"

  # --- Resolve the target version -----------------------------------------
  current="$(rv_read_version "$info_xml")" || return 1
  case "$mode" in
    bump)    target="$(rv_bump "$arg" "$current")" || return 1 ;;
    promote) target="$(rv_promote "$arg" "$current")" || return 1 ;;
    version) rv_validate "$arg" || return 1; target="$arg" ;;
  esac
  echo "Preparing release: $current -> $target"

  # --- Pre-flight checks (before mutating anything) -----------------------
  # Clean tracked worktree; untracked files are fine (they are not committed).
  git -C "$root" diff --quiet || die "worktree has unstaged changes; commit or stash first"
  git -C "$root" diff --cached --quiet || die "index has staged changes; commit or reset first"

  # info.xml well-formed (best effort — skip silently if no parser available).
  if command -v xmllint >/dev/null 2>&1; then
    xmllint --noout "$info_xml" || die "appinfo/info.xml is not well-formed"
  elif command -v python3 >/dev/null 2>&1; then
    python3 -c "import sys,xml.dom.minidom as m; m.parse(sys.argv[1])" "$info_xml" \
      || die "appinfo/info.xml is not well-formed"
  fi

  # Tag must not already exist.
  git -C "$root" rev-parse -q --verify "refs/tags/v$target" >/dev/null 2>&1 \
    && die "tag v$target already exists"

  # Target must be greater than the latest conforming release tag.
  local latest="" t v
  while IFS= read -r t; do
    [[ -n "$t" ]] || continue
    v="${t#v}"
    rv_validate "$v" >/dev/null 2>&1 || continue
    if [[ -z "$latest" ]] || rv_gt "$v" "$latest"; then latest="$v"; fi
  done < <(git -C "$root" tag --list 'v*')
  if [[ -n "$latest" ]] && ! rv_gt "$target" "$latest"; then
    die "target $target is not greater than latest release tag v$latest"
  fi

  # Fragment presence (unless explicitly allowed to be empty).
  local frag frag_count=0
  for frag in "$root"/changelog.d/*.md; do
    [[ -e "$frag" ]] || continue
    [[ "$(basename "$frag")" == "README.md" ]] && continue
    frag_count=$((frag_count + 1))
  done
  if [[ "$frag_count" -eq 0 && "$allow_empty" -eq 0 ]]; then
    die "no changelog.d fragments; add one or pass --allow-empty-changelog"
  fi

  # --- Mutate: version, changelog, release notes --------------------------
  rv_write_version "$info_xml" "$target" || die "failed to update info.xml"

  local notes_dir="$root/build/release" notes_file
  notes_file="$notes_dir/gocassini-$target-notes.md"
  mkdir -p "$notes_dir"

  local staged=("$info_xml")
  if [[ "$frag_count" -gt 0 ]]; then
    "$SCRIPT_DIR/fold-changelog.sh" --version "$target" --date "$date" --write \
      || die "changelog fold failed"
    "$SCRIPT_DIR/extract-release-notes.sh" --version "$target" >"$notes_file" \
      || die "could not extract release notes for $target"
    staged+=("$root/CHANGELOG.md" "$root/changelog.d")
  else
    printf 'Release %s.\n' "$target" >"$notes_file"
    echo "note: --allow-empty-changelog set; CHANGELOG.md not modified"
  fi

  # --- Commit and tag (local only) ----------------------------------------
  git -C "$root" add -- "${staged[@]}"
  git -C "$root" commit -q -m "release: $target"
  echo "Created commit: release: $target"
  if [[ "$make_tag" -eq 1 ]]; then
    git -C "$root" tag "v$target"
    echo "Created tag:    v$target"
  fi

  local branch
  branch="$(git -C "$root" rev-parse --abbrev-ref HEAD)"
  cat <<EOF

Release notes: ${notes_file#"$root"/}

Nothing has been pushed. Review the commit, then push intentionally:
  git show
  git push origin ${branch}$([[ "$make_tag" -eq 1 ]] && echo " v$target")
EOF
}

main "$@"
