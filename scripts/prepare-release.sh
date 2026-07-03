#!/usr/bin/env bash
# prepare-release.sh — orchestrate local Cassini release prep.
#
# Runs the release-prep steps from docs/release-policy.md on the current
# branch (use a release-prep/<version> branch): bumps the manifest version,
# stops with instructions while changelog.d/ fragments still need
# summarizing, then validates release readiness and builds + validates the
# App Store tarball. Never tags and never pushes — cutting the release is
# release.yml's job after the prep PR merges.
#
# Usage:
#   ./scripts/prepare-release.sh <version>   # e.g. 0.2.0-alpha.2
#
# Re-entrant: run it once to bump, summarize the changelog (agent/human),
# then run it again to validate and package.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
SCRIPTS="${REPO_ROOT}/scripts"
VERSION="${1:?usage: $0 <version> (e.g. 0.2.0-alpha.2)}"

"${SCRIPTS}/bump-exapp-version.sh" "$VERSION"

pending=()
while IFS= read -r f; do
  pending+=("$f")
done < <(find "${REPO_ROOT}/changelog.d" -maxdepth 1 -name '*.md' ! -name 'README.md' | sort)

if [[ ${#pending[@]} -gt 0 ]]; then
  cat <<EOF

Pending changelog fragments — summarize them before packaging:
$(printf '  %s\n' "${pending[@]#"$REPO_ROOT"/}")

Next step (agent or human):
  Summarize the fragments above into a new '## [${VERSION}] - $(date +%Y-%m-%d)'
  section in CHANGELOG.md (directly under the '## [Unreleased]' section) using
  Keep-a-Changelog headings (Added/Changed/Deprecated/Removed/Fixed/Security).
  Merge duplicates and write operator/user-facing language. Delete the
  consumed fragment files, then re-run:

    ./scripts/prepare-release.sh ${VERSION}
EOF
  exit 1
fi

"${SCRIPTS}/check-release-ready.sh" "$VERSION"
"${SCRIPTS}/build-appstore-tarball.sh" --version "$VERSION"
"${SCRIPTS}/validate-appstore-tarball.sh" "${REPO_ROOT}/dist/appstore/gocassini-${VERSION}.tar.gz"

cat <<EOF

Release prep for ${VERSION} is complete. Next steps:
  git add -A && git commit -m "release: ${VERSION}"
  gh pr create --base main --title "release: ${VERSION}"
  # after review + merge:
  git switch main && git pull --ff-only
  ./scripts/tag-release.sh ${VERSION}
EOF
