#!/usr/bin/env bash
# tag-release.sh — cut a Cassini release by tagging main.
#
# The annotated tag v<version> IS the release (docs/release-policy.md): the
# tag push triggers publish-exapp-image.yml (Docker images) and release.yml
# (GitHub Release + App Store tarball). The tag is deliberately pushed from a
# human machine — GitHub suppresses workflow triggers for refs pushed with
# the Actions GITHUB_TOKEN, so a CI-pushed tag would publish nothing.
#
# Usage:
#   git switch main && git pull --ff-only
#   ./scripts/tag-release.sh <version>   # e.g. 0.2.0-alpha.2

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
VERSION="${1:?usage: $0 <version> (e.g. 0.2.0-alpha.2)}"
TAG="v${VERSION}"

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "error: working tree is not clean — commit or stash first" >&2
  exit 1
fi

BRANCH="$(git -C "$REPO_ROOT" rev-parse --abbrev-ref HEAD)"
if [[ "$BRANCH" != "main" ]]; then
  echo "error: releases are tagged on main, current branch is '${BRANCH}'" >&2
  exit 1
fi

git -C "$REPO_ROOT" fetch origin main --tags --quiet
if [[ "$(git -C "$REPO_ROOT" rev-parse HEAD)" != "$(git -C "$REPO_ROOT" rev-parse origin/main)" ]]; then
  echo "error: HEAD is not origin/main — run 'git pull --ff-only' (or push your merge) first" >&2
  exit 1
fi

if git -C "$REPO_ROOT" rev-parse -q --verify "refs/tags/${TAG}" >/dev/null; then
  echo "error: tag ${TAG} already exists — releases are immutable, cut a new version instead" >&2
  exit 1
fi

"${REPO_ROOT}/scripts/check-release-ready.sh" "$VERSION"

git -C "$REPO_ROOT" tag -a "$TAG" -m "release: ${VERSION}"
git -C "$REPO_ROOT" push origin "$TAG"

echo
echo "Pushed ${TAG}. CI now publishes images (publish-exapp-image.yml) and the"
echo "GitHub Release + App Store package (release.yml)."
echo "Watch: gh run list --limit 5"
