#!/usr/bin/env bash
# Offline contract test for scripts/classify-image-relevance.sh.
#
# Two independent checks:
#   (1) Behavioral fixtures -- the ALL-docs deny-list predicate, including the
#       empty-path guard (the single most likely way to break D-505) and
#       order-independence, plus a "it BITES" pair proving a set the OLD
#       allow-list called not-relevant is now relevant.
#   (2) A DRIFT assertion -- the classifier's DENY_GLOBS must equal ci.yml's
#       paths-ignore (GitHub-glob -> bash-glob), so this list can never silently
#       become a fourth hand-copied, divergent ignore-list (D-505 R7).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CLASSIFIER="$SCRIPT_DIR/classify-image-relevance.sh"
CI_YML="$ROOT/.github/workflows/ci.yml"

fail() { echo "FAIL: $*" >&2; exit 1; }

[[ -x "$CLASSIFIER" ]] || fail "classifier not executable: $CLASSIFIER"
[[ -f "$CI_YML" ]]     || fail "ci.yml not found: $CI_YML"

# ---- (1) behavioral fixtures -------------------------------------------------
# expect WANT INPUT : feed INPUT to the classifier on stdin, assert verdict WANT.
expect() {
  local want="$1" input="$2" got
  got="$(printf '%s' "$input" | "$CLASSIFIER")"
  [[ "$got" == "$want" ]] \
    || fail "classifier returned '$got', wanted '$want' for input <<${input}>>"
}

# docs-only sets -> false (skip the expensive required gate)
expect false ""                                   # empty diff
expect false $'\n'                                # bare trailing newline (the R6 guard)
expect false "README.md"
expect false "docs/a/b/c.md"                      # * spans /
expect false "docs/img/x.svg"                     # docs/* arm, a non-.md asset
expect false "img/app.svg"                        # the one live img/ file
expect false "changelog.d/foo.md"                 # subsumed by *.md (brief's extra is inert)
expect false $'README.md\ndocs/guide.md\nimg/app.svg'   # several docs-only files

# any non-docs path -> true (run the required gate)
expect true  "cassini-go-recorder/main.go"
expect true  "Dockerfile"                         # a bare root build input
expect true  ".dockerignore"                      # #131's point-fix, now covered by default
expect true  ".gitattributes"
expect true  "scripts/process-recordings.sh"          # un-enumerated build input
expect true  "sandbox/whatever.txt"               # enumerated nowhere
expect true  "harness/bin/x.sh"                   # PR #36's original incident

# ALL-docs, not ANY-docs -- order-independent (R4)
expect true  $'README.md\nspec/x.schema.json'
expect true  $'spec/x.schema.json\nREADME.md'

# It BITES: sets the PRE-D-505 allow-list at publish-exapp-image.yml:71 did NOT
# enumerate, so a PR touching only these auto-passed the REQUIRED gate green.
# The deny-list must now classify them relevant. spec/ is even called out by
# name in ci.yml as deliberately not-ignorable.
expect true  "spec/cassini-portable-meeting-manifest-v1.schema.json"
expect true  "scripts/process-recordings.sh"

echo "PASS: classifier behavioral fixtures"

# ---- (2) drift assertion vs ci.yml paths-ignore ------------------------------
# Load the classifier's authored deny-list (sourcing does not run it).
# shellcheck source=scripts/classify-image-relevance.sh
source "$CLASSIFIER"
(( ${#DENY_GLOBS[@]} > 0 )) || fail "classifier exposes no DENY_GLOBS array"

# Translate one GitHub filter glob to its bash-glob equivalent -- the same rule
# ci.yml's own comment documents (`*` spans `/` in bash, so `**/` and `/**`
# collapse to `*`). Kept in lockstep with the DENY_GLOBS comments.
gh_to_bash_glob() {
  case "$1" in
    '**/'*) printf '%s\n' "${1#'**/'}" ;;    # **/*.md -> *.md
    *'/**') printf '%s/*\n' "${1%'/**'}" ;;  # docs/** -> docs/*
    *)      printf '%s\n' "$1" ;;            # media-kit.* -> media-kit.*
  esac
}

# Extract ci.yml's paths-ignore items. push + pull_request carry identical
# lists, so we collect both and dedupe. No yq on the contracts runner, so parse
# the small, stable YAML with awk: take `- '...'` items directly under a
# `paths-ignore:` key, then strip the dash prefix and surrounding quotes.
mapfile -t ci_globs_raw < <(
  awk '
    /paths-ignore:/ { collect=1; next }
    collect && /^[[:space:]]*-[[:space:]]/ { print; next }
    collect { collect=0 }
  ' "$CI_YML" | sed -E 's/^[[:space:]]*-[[:space:]]*//' | tr -d "\"'"
)
(( ${#ci_globs_raw[@]} > 0 )) || fail "no paths-ignore items parsed from ci.yml"

mapfile -t expected < <(
  for g in "${ci_globs_raw[@]}"; do gh_to_bash_glob "$g"; done | sort -u
)
mapfile -t actual < <(printf '%s\n' "${DENY_GLOBS[@]}" | sort -u)

if [[ "${expected[*]}" != "${actual[*]}" ]]; then
  {
    echo "classifier DENY_GLOBS has DRIFTED from ci.yml paths-ignore."
    echo "  ci.yml (translated): ${expected[*]}"
    echo "  classifier         : ${actual[*]}"
    echo "reconcile scripts/classify-image-relevance.sh with ci.yml's paths-ignore."
  } >&2
  exit 1
fi

echo "PASS: classifier DENY_GLOBS matches ci.yml paths-ignore (${#actual[@]} globs)"
echo "PASS: all classifier contract checks"
