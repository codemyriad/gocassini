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

# Not docs, but never in the image or its tests (D-906): #457 changed only
# lint.yml and pr-conflict-impact.yml and still built and tested the image.
expect false $'.github/workflows/lint.yml\n.github/workflows/pr-conflict-impact.yml'
expect false $'.github/workflows/ci.yml\nREADME.md'
expect false $'cassini-microsite/src/pages/index.astro\n.github/workflows/microsite.yml'
expect false $'.claude/skills/x/SKILL.md\n.claude/settings.json'
expect false $'.github/workflows/release.yml\nchangelog.d/x.md'
# ...but the image's own workflow, its actions and new workflows still build it.
expect true  ".github/workflows/publish-exapp-image.yml"
expect true  ".github/actions/load-exapp-image/action.yml"
expect true  ".github/workflows/new-workflow.yml"
expect true  $'.github/workflows/lint.yml\ncassini-operator/main.go'
expect true  $'cassini-microsite/x.astro\nscripts/lib-release-version.sh'

# ci.yml's suites (--ci-suite): the same, except that ci.yml tests itself.
expect_suite() {
  local want="$1" input="$2" got
  got="$(printf '%s' "$input" | "$CLASSIFIER" --ci-suite)"
  [[ "$got" == "$want" ]] \
    || fail "--ci-suite returned '$got', wanted '$want' for input <<${input}>>"
}
expect_suite false ""
expect_suite false $'\n'
expect_suite false $'README.md\ndocs/guide.md'
expect_suite false $'.github/workflows/lint.yml\n.github/workflows/pr-conflict-impact.yml'
expect_suite false $'cassini-microsite/src/pages/index.astro\n.claude/settings.json'
expect_suite true  ".github/workflows/ci.yml"
expect_suite true  $'.github/workflows/ci.yml\nREADME.md'
# contracts tests the image workflow's gate, so it runs the suites too.
expect_suite true  ".github/workflows/publish-exapp-image.yml"
expect_suite true  ".github/workflows/new-workflow.yml"
expect_suite true  "scripts/classify-image-relevance.sh"
expect_suite true  $'README.md\ncassini-operator/main.go'
"$CLASSIFIER" --bogus </dev/null >/dev/null 2>&1 && fail "an unknown option must fail"

# A release sync's version-only info.xml bump (D-915), against a scratch repo
# holding the real manifest.
repo="$(mktemp -d)"
trap 'rm -rf "$repo"' EXIT
git -C "$repo" init -q
git -C "$repo" config user.email ci@example.invalid
git -C "$repo" config user.name ci
mkdir "$repo/appinfo"
cp "$ROOT/appinfo/info.xml" "$repo/appinfo/info.xml"
commit() { git -C "$repo" commit -qam "$1" --allow-empty && git -C "$repo" rev-parse HEAD; }
git -C "$repo" add appinfo/info.xml
base="$(commit base)"
sed -i -E 's#<version>[^<]*</version>#<version>9.9.0-alpha.0</version>#; s#<image-tag>[^<]*</image-tag>#<image-tag>9.9.0-alpha.0</image-tag>#' "$repo/appinfo/info.xml"
bump="$(commit bump)"
grep -q '<image-tag>9.9.0-alpha.0</image-tag>' "$repo/appinfo/info.xml" || fail "fixture bump did not apply"
sed -i '0,/<\/routes>/s#</routes>#<route><url>x</url></route></routes>#' "$repo/appinfo/info.xml"
bump_and_route="$(commit route)"
git -C "$repo" diff --quiet "$bump" "$bump_and_route" && fail "fixture route change did not apply"

expect_sync() {
  local want="$1" input="$2" got
  shift 2
  got="$(cd "$repo" && printf '%s' "$input" | "$CLASSIFIER" "$@")"
  [[ "$got" == "$want" ]] \
    || fail "classifier $* returned '$got', wanted '$want' for input <<${input}>>"
}
sync=$'appinfo/info.xml\nCHANGELOG.md\nchangelog.d/x.md'
expect_sync false "$sync" --base "$base" --head "$bump"
expect_sync false "$sync" --ci-suite --base "$base" --head "$bump"
expect_sync true  "$sync" --base "$base" --head "$bump_and_route"
expect_sync true  "$sync" --ci-suite --base "$base" --head "$bump_and_route"
expect_sync true  "$sync"                                    # no SHAs: fail closed
expect_sync true  "$sync" --base "$base" --head 0000000000000000000000000000000000000000
expect_sync true  $'appinfo/info.xml\nDockerfile' --base "$base" --head "$bump"

echo "PASS: classifier behavioral fixtures"

# ---- (2) drift assertion vs ci.yml paths-ignore ------------------------------
# Load the classifier's authored deny-list (sourcing does not run it).
# shellcheck source=scripts/classify-image-relevance.sh
source "$CLASSIFIER"
(( ${#DENY_GLOBS[@]} > 0 )) || fail "classifier exposes no DENY_GLOBS array"

# Every listed workflow exists, so a rename can't leave a stale entry behind.
for wf in "${OTHER_WORKFLOWS[@]}"; do
  [[ -f "$ROOT/$wf" ]] || fail "OTHER_WORKFLOWS lists a missing file: $wf"
done

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

# Extract ci.yml's paths-ignore items (push only: pull requests always run
# and classify in the `changes` job). Collected and deduped all the same. No yq on the contracts runner, so parse
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
