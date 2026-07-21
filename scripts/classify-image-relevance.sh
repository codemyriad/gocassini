#!/usr/bin/env bash
# Deny-list classifier for the REQUIRED "Faithful installed ExApp" gate.
#
# Reads newline-delimited changed paths on stdin and prints `true` or `false`:
#   false -> skip the expensive gate; emitted ONLY when EVERY changed path is
#            provably docs/notes/assets ("no non-docs file changed").
#   true  -> run the gate; emitted the moment ANY changed path is not.
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

# classify_path_is_docs PATH -> 0 (true) when PATH matches a deny glob, i.e. the
# path is provably docs/assets; 1 otherwise.
classify_path_is_docs() {
  local path="$1" glob
  for glob in "${DENY_GLOBS[@]}"; do
    # RHS is an intentional glob, not a literal -- do not quote it.
    # shellcheck disable=SC2053
    [[ "$path" == $glob ]] && return 0
  done
  return 1
}

# classify_stream reads changed paths on stdin and prints the ALL-docs verdict:
# "no non-docs path changed" -> false; first non-docs path -> true.
classify_stream() {
  local relevant=false path
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
    if ! classify_path_is_docs "$path"; then
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
  classify_stream
fi
