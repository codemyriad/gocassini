#!/usr/bin/env bash
# Refuse a change that adds a large blob or a compiled executable to the tree.
#
# Why this exists: PR #261 landed a 14 MB Linux `cassini` binary, a 7 MB webm,
# a 7 MB opus and ~22k lines of transcript JSON — none of them intended, all of
# them swept in by a `git add -A` during a conflict-resolution merge. They were
# untracked scratch output that had been sitting in a working tree for months.
# `harness/go-talk-rotator/three-song-rotator` on main is the same accident,
# caught later. .gitignore cannot prevent this class: it can only name paths we
# already know about, and the next scratch directory will have a name nobody
# predicted. A gate on what a diff ADDS does not have to predict anything.
#
# Two rules, both about the post-image of every added or modified file:
#
#   - size: no blob over MAX_BLOB_BYTES;
#   - shape: no ELF, Mach-O or PE executable at any size, because a 40 KB
#     binary is no more reviewable than a 40 MB one.
#
# Deliberately NOT a media-signature rule. A small .wav or .mkv fixture is a
# legitimate thing to commit — the repo has one — and the two real media files
# in #261 were both caught by size anyway. A shape rule that fires on content
# people legitimately add teaches everyone to reach for the allow-list, which
# is how a gate stops meaning anything.
#
# Escape hatch: .github/large-blobs-allow.txt, one repo-relative path per line.
# A path allow-list rather than a commit-message trailer, because it lands in
# the diff and gets reviewed with everything else — the point is that adding a
# large file becomes a decision somebody made on the record, not that it becomes
# impossible.
#
# Files tracked by git-lfs are skipped: their in-tree blob is a ~130 byte
# pointer, so the size rule passes them anyway, but the shape rule would not,
# and LFS is already the sanctioned answer to "this really is a big binary".
#
# Usage: check-added-blobs.sh [<base-ref> [<head-ref>]]
# Defaults compare the merge base with origin/main against the working tree's
# HEAD, which is what a developer wants before pushing.
set -euo pipefail

# 512 KiB. Chosen against the repo as it stands: the largest text file anyone
# has legitimately added is well under it, and every accident so far has been
# more than ten times over it. Raising this is a bigger decision than adding one
# allow-list line, which is the intended order of escalation.
MAX_BLOB_BYTES="${MAX_BLOB_BYTES:-524288}"

ALLOW_FILE=".github/large-blobs-allow.txt"

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

base_ref="${1:-}"
head_ref="${2:-HEAD}"

if [ -z "$base_ref" ]; then
  # No base given: compare against where this branch left main. `git merge-base`
  # rather than origin/main directly, so a branch that has not rebased is judged
  # on what IT added and not on what main has moved on to.
  if ! base_ref="$(git merge-base origin/main HEAD 2>/dev/null)"; then
    echo "check-added-blobs: cannot find a merge base with origin/main; pass one explicitly" >&2
    exit 2
  fi
fi

# The allow-list is read into an associative array rather than grepped per file:
# the loop below runs once per changed path, and a grep per path over a file
# that is almost always empty is a lot of process for nothing.
declare -A allowed=()
if [ -f "$ALLOW_FILE" ]; then
  while IFS= read -r line; do
    line="${line%%#*}"
    # Trim both ends. Trailing whitespace in an allow-list entry is the kind of
    # thing that produces a "but I allow-listed it" bug report an hour long.
    line="${line#"${line%%[![:space:]]*}"}"
    line="${line%"${line##*[![:space:]]}"}"
    [ -n "$line" ] && allowed["$line"]=1
  done < "$ALLOW_FILE"
fi

# Magic numbers for the three executable formats that could plausibly reach this
# repo. Mach-O gets all four byte orders plus the universal-binary wrapper,
# because a macOS developer building locally is the likeliest source of one.
is_executable_blob() {
  local blob="$1" magic
  # Hex before the bytes cross a command substitution, which eats NULs. `od`
  # rather than `xxd`: od is coreutils and is on every runner and every
  # developer machine, xxd ships with vim and is one apt package away from not
  # being there.
  magic="$(git cat-file blob "$blob" 2>/dev/null | head -c 4 | od -An -tx1 | tr -d ' \n' || true)"
  case "$magic" in
    7f454c46) return 0 ;;                                     # ELF
    feedface|cefaedfe|feedfacf|cffaedfe|cafebabe) return 0 ;; # Mach-O, incl. fat
    4d5a*) return 0 ;;                                        # PE / MZ
    *) return 1 ;;
  esac
}

# Whether git-lfs claims this path. `git check-attr` is the authority, not a
# glob against .gitattributes, so a pattern change here needs no change there.
is_lfs_tracked() {
  [ "$(git check-attr filter -- "$1" | sed 's/.*: //')" = "lfs" ]
}

failures=0

report() {
  printf '::error file=%s::%s\n' "$1" "$2"
  printf '  %-58s %s\n' "$1" "$2" >&2
}

# --diff-filter=AM: added or modified. A deletion has no post-image to judge,
# and a rename with no content change carries the blob that was already here.
# -z, and a NUL-delimited read, because a path may contain anything but NUL.
while IFS= read -r -d '' path; do
  [ -n "$path" ] || continue
  blob="$(git rev-parse "$head_ref:$path" 2>/dev/null || true)"
  # Gone from the head tree: the diff named it, so this means the ref moved under
  # us rather than that the file is fine. Skipping is right — there is nothing to
  # measure — but it must not read as a pass for a path that does exist.
  [ -n "$blob" ] || continue

  if [ -n "${allowed[$path]+set}" ]; then
    continue
  fi
  if is_lfs_tracked "$path"; then
    continue
  fi

  size="$(git cat-file -s "$blob")"
  if [ "$size" -gt "$MAX_BLOB_BYTES" ]; then
    report "$path" "$size bytes exceeds the $MAX_BLOB_BYTES byte limit"
    failures=$((failures + 1))
    continue
  fi
  if is_executable_blob "$blob"; then
    report "$path" "is a compiled executable ($size bytes)"
    failures=$((failures + 1))
  fi
done < <(git diff --name-only --diff-filter=AM -z "$base_ref" "$head_ref")

if [ "$failures" -gt 0 ]; then
  cat >&2 <<EOF

$failures file(s) above cannot be added to the repository.

Almost always this is build output or scratch data that a broad \`git add\`
swept up. Check what the working tree actually held:

    git status --porcelain
    git rm --cached <path>          # then add it to .gitignore

If the file genuinely belongs in the repository:

  - large media or fixtures go to git-lfs (see .gitattributes);
  - anything else needs a line in $ALLOW_FILE, which a reviewer will see.

Never a compiled binary. Build it in CI, or ship it from a release artifact.
EOF
  exit 1
fi

echo "check-added-blobs: no oversized or executable blobs added between ${base_ref:0:12} and $head_ref"
