#!/usr/bin/env bash
# Deny-list classifier for the REQUIRED "Faithful installed ExApp" gate.
#
# Reads newline-delimited changed paths on stdin and prints `true` or `false`:
#   false -> skip the expensive gate; emitted ONLY when EVERY changed path is
#            provably docs/notes/assets, or in NOT_IMAGE_GLOBS (workflows
#            that don't build or test the image, the website, agent config).
#   true  -> run the gate; emitted the moment ANY changed path is not.
#
# With `--ci-suite` it answers the same question for ci.yml's test suites,
# using NOT_SUITE_GLOBS instead of NOT_IMAGE_GLOBS (D-906).
#
# With `--base SHA --head SHA` it also skips appinfo/info.xml when the change
# only sets <version> and <image-tag>, as a release sync to main does (D-915).
# The caller still validates the manifest. Without both SHAs, or when either
# side can't be read, info.xml counts as relevant.
#
# This is a DENY-list, deliberately mirroring .github/workflows/ci.yml's
# `paths-ignore`. The gate it feeds is a REQUIRED status check, so the cost
# asymmetry is the whole point (D-505): an unmatched path here costs ~30 wasted
# runner minutes -- loud and self-correcting. An allow-list's unmatched path
# instead reports the required check GREEN on an unbuilt, unrecorded product --
# silent and unbounded (the PR #36 incident). NEVER enumerate product paths
# here; only add a pattern for something provably NOT product.
#
# DENY_GLOBS mirrors ci.yml's paths-ignore, GitHub-glob -> bash-glob. In bash
# pattern matching `*` spans `/`, so a GitHub `**/` prefix and `/**` suffix both
# collapse to `*`: `docs/*` matches docs/a/b/c and `*.md` matches markdown at any
# depth. scripts/test-classify-image-relevance.sh asserts this list has not
# drifted from ci.yml's paths-ignore, so it can never silently become a fourth
# hand-copied divergent ignore-list.
DENY_GLOBS=(
  '*.md'            # ci.yml: **/*.md
  'docs/*'          # ci.yml: docs/**
  'docs-wip/*'      # ci.yml: docs-wip/**
  'dev-docs-wip/*'  # ci.yml: dev-docs-wip/**
  'planning/*'      # ci.yml: planning/**
  'img/*'           # ci.yml: img/**
  'media-kit.*'     # ci.yml: media-kit.*
)

# Workflow files that neither build the image nor run ci.yml's test suites, so
# a change to one tests nothing in either workflow (D-906). Named one by one so
# a NEW workflow is relevant until someone lists it here. Never list a workflow
# that another workflow calls (`uses: ./.github/workflows/...`), nor anything
# under .github/actions/: those run inside the suites.
OTHER_WORKFLOWS=(
  '.github/workflows/backport.yml'
  '.github/workflows/changelog-check.yml'
  '.github/workflows/deploy-preview.yml'
  '.github/workflows/deploy-sandbox.yml'
  '.github/workflows/lint.yml'
  '.github/workflows/microsite.yml'
  '.github/workflows/nextcloud-canary.yml'
  '.github/workflows/pr-conflict-impact.yml'
  '.github/workflows/registry-housekeeping.yml'
  '.github/workflows/release.yml'
)

# Paths that are not docs but that neither ci.yml's suites nor the image use
# (D-906). They stay OUT of DENY_GLOBS: ci.yml's push paths-ignore must not
# skip them. On a pull request ci.yml always runs and its `changes` job skips
# the suites with this list, so `CI passed` still reports.
NOT_SUITE_GLOBS=(
  "${OTHER_WORKFLOWS[@]}"
  '.github/FUNDING.yml'
  '.github/actionlint.yaml'
  '.claude/*'              # agent skills and settings
  'cassini-microsite/*'    # the website: not an npm workspace, never COPYed
)

# The same, for the image gate. ci.yml is here but not above: it runs its own
# suites on its own change, and nothing in the image workflow reads it.
NOT_IMAGE_GLOBS=(
  "${NOT_SUITE_GLOBS[@]}"
  '.github/workflows/ci.yml'
)

# classify_path_matches PATH GLOB... -> 0 (true) when PATH matches any GLOB.
classify_path_matches() {
  local path="$1" glob
  shift
  for glob in "$@"; do
    # RHS is an intentional glob, not a literal -- do not quote it.
    # shellcheck disable=SC2053
    [[ "$path" == $glob ]] && return 0
  done
  return 1
}

# classify_path_is_docs PATH -> 0 (true) when PATH matches a deny glob, i.e. the
# path is provably docs/assets; 1 otherwise.
classify_path_is_docs() {
  classify_path_matches "$1" "${DENY_GLOBS[@]}"
}

# classify_path_skips_image PATH -> 0 (true) when a change to PATH alone cannot
# change the image or what its tests exercise.
classify_path_skips_image() {
  classify_path_is_docs "$1" || classify_path_matches "$1" "${NOT_IMAGE_GLOBS[@]}"
}

# classify_path_skips_suite PATH -> 0 (true) when a change to PATH alone cannot
# change what ci.yml's suites test.
classify_path_skips_suite() {
  classify_path_is_docs "$1" || classify_path_matches "$1" "${NOT_SUITE_GLOBS[@]}"
}

# classify_info_xml_bump_only BASE HEAD -> 0 (true) when appinfo/info.xml
# differs between BASE and HEAD only in its <version> and <image-tag> lines.
# Fails closed: an unreadable side returns 1, i.e. relevant.
classify_info_xml_bump_only() {
  local base="$1" head="$2" before after
  [[ -n "$base" && -n "$head" ]] || return 1
  before="$(git show "$base:appinfo/info.xml" 2>/dev/null)" || return 1
  after="$(git show "$head:appinfo/info.xml" 2>/dev/null)" || return 1
  [[ "$(classify_blank_version "$before")" == "$(classify_blank_version "$after")" ]]
}

# classify_blank_version TEXT prints TEXT with the value of every <version>
# and <image-tag> element that sits alone on its line blanked out.
classify_blank_version() {
  printf '%s\n' "$1" | sed -E \
    -e 's#^([[:space:]]*<version>)[^<]*(</version>[[:space:]]*)$#\1\2#' \
    -e 's#^([[:space:]]*<image-tag>)[^<]*(</image-tag>[[:space:]]*)$#\1\2#'
}

# classify_stream [SKIPS] reads changed paths on stdin and prints the verdict:
# false when SKIPS (default classify_path_skips_image) holds for every path;
# true at the first path it doesn't.
# Set CLASSIFY_BASE and CLASSIFY_HEAD to skip a version-only info.xml bump.
classify_stream() {
  local skips="${1:-classify_path_skips_image}" relevant=false path
  # `|| [[ -n "$path" ]]` processes a final line with no trailing newline too:
  # a dropped last path would emit a false `false`, i.e. a green REQUIRED gate on
  # an unbuilt product -- exactly the failure D-505 exists to end. So we tolerate
  # any input framing, not just the caller's `printf '%s\n'`.
  while IFS= read -r path || [[ -n "$path" ]]; do
    # A caller feeding a trailing newline (or an empty diff) yields an empty
    # record; skipping it keeps a genuinely docs-only (or empty) diff classified
    # `false`. Under this DENY-list an unskipped empty path would fall through to
    # `relevant=true` and silently defeat the not-applicable-success path for
    # EVERY docs-only PR -- this guard is load-bearing, not decorative (see
    # test-classify-image-relevance.sh).
    [[ -n "$path" ]] || continue
    if [[ "$path" == appinfo/info.xml ]] \
      && classify_info_xml_bump_only "${CLASSIFY_BASE:-}" "${CLASSIFY_HEAD:-}"; then
      continue
    fi
    if ! "$skips" "$path"; then
      relevant=true
      break
    fi
  done
  printf '%s\n' "$relevant"
}

# Run only when executed directly; when sourced (by the test, which reads
# DENY_GLOBS and calls the functions) do nothing.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -euo pipefail
  skips=classify_path_skips_image
  CLASSIFY_BASE='' CLASSIFY_HEAD=''
  while (( $# )); do
    case "$1" in
      --ci-suite) skips=classify_path_skips_suite; shift ;;
      --base) CLASSIFY_BASE="${2:-}"; shift 2 || exit 2 ;;
      --head) CLASSIFY_HEAD="${2:-}"; shift 2 || exit 2 ;;
      *)
        echo "usage: $0 [--ci-suite] [--base SHA --head SHA] < changed-paths" >&2
        exit 2
        ;;
    esac
  done
  classify_stream "$skips"
fi
