#!/usr/bin/env bash
# check-release-ready.sh — deterministic gate for cutting a Cassini release.
#
# Verifies the working tree is a coherent release state for <version> before
# a tag is created (see docs/release-policy.md). Run locally during release
# prep and by release.yml before it tags. This gate is for NEW release cuts
# only — publish workflows building from an existing tag use
# validate-appstore-tarball.sh instead, so tightening a check here never
# retro-breaks re-publishing an already-cut release.
#
# Usage:
#   ./scripts/check-release-ready.sh <version>   # e.g. 0.2.0-alpha.2

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
INFO_XML="${REPO_ROOT}/appinfo/info.xml"
CHANGELOG="${REPO_ROOT}/CHANGELOG.md"
FRAGMENT_DIR="${REPO_ROOT}/changelog.d"

VERSION="${1:?usage: $0 <version> (e.g. 0.2.0-alpha.2)}"

fail=0
err() { echo "NOT READY: $*" >&2; fail=1; }
ok()  { echo "ok: $*"; }

# Same shape bump-exapp-version.sh and CI accept: X.Y.Z with an optional
# semver pre-release suffix.
if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]]; then
  echo "error: version must be X.Y.Z or X.Y.Z-<prerelease>, got: '$VERSION'" >&2
  exit 1
fi

XML_VERSION="$(xmllint --xpath 'string(/info/version)' "$INFO_XML")"
XML_IMAGE_TAG="$(xmllint --xpath 'string(/info/external-app/docker-install/image-tag)' "$INFO_XML")"
XML_LICENCE="$(xmllint --xpath 'string(/info/licence)' "$INFO_XML")"

if [[ "$XML_VERSION" == "$VERSION" ]]; then
  ok "appinfo/info.xml <version> is ${VERSION}"
else
  err "appinfo/info.xml <version> is '${XML_VERSION}', expected '${VERSION}' (run scripts/bump-exapp-version.sh ${VERSION})"
fi

if [[ "$XML_IMAGE_TAG" == "$VERSION" ]]; then
  ok "appinfo/info.xml <image-tag> is ${VERSION}"
else
  err "appinfo/info.xml <image-tag> is '${XML_IMAGE_TAG}', expected '${VERSION}' (run scripts/bump-exapp-version.sh ${VERSION})"
fi

# The store reads release notes for <version> from CHANGELOG.md inside the
# tarball; release.yml extracts the same section for the GitHub Release.
if grep -qF "## [${VERSION}]" "$CHANGELOG"; then
  ok "CHANGELOG.md has a '## [${VERSION}]' section"
else
  err "CHANGELOG.md has no '## [${VERSION}]' section (fold changelog.d/ fragments first)"
fi

# All fragments must be folded into CHANGELOG.md before a release is cut —
# a leftover fragment means a change would ship without a release note.
pending=()
while IFS= read -r f; do
  pending+=("$f")
done < <(find "$FRAGMENT_DIR" -maxdepth 1 -name '*.md' ! -name 'README.md' | sort)
if [[ ${#pending[@]} -eq 0 ]]; then
  ok "no pending changelog.d/ fragments"
else
  err "pending changelog.d/ fragments must be folded into CHANGELOG.md: ${pending[*]#"$REPO_ROOT"/}"
fi

# App Store metadata policy (D-397): SPDX identifier, not the deprecated
# 'agpl' shorthand.
if [[ "$XML_LICENCE" == "AGPL-3.0-or-later" ]]; then
  ok "licence is AGPL-3.0-or-later"
else
  err "appinfo/info.xml <licence> is '${XML_LICENCE}', expected 'AGPL-3.0-or-later'"
fi

# When running on a tag ref in CI, the tag must be v<version> exactly.
if [[ "${GITHUB_REF:-}" == refs/tags/* ]]; then
  TAG="${GITHUB_REF##refs/tags/}"
  if [[ "$TAG" == "v${VERSION}" ]]; then
    ok "git tag ${TAG} matches v${VERSION}"
  else
    err "git tag '${TAG}' does not match expected 'v${VERSION}'"
  fi
fi

if [[ "$fail" -ne 0 ]]; then
  echo >&2
  echo "Release ${VERSION} is NOT ready." >&2
  exit 1
fi
echo
echo "Release ${VERSION} is ready to cut."
