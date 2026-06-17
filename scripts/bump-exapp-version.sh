#!/usr/bin/env bash
# bump-exapp-version.sh — bump the Cassini ExApp release version.
#
# Rewrites <version> and <docker-install><image-tag> in appinfo/info.xml in
# lock-step. The two must stay equal: the image tag pins AppAPI installs to
# the exact build a release was cut from, and CI (validate-manifest in
# publish-exapp-image.yml) rejects a manifest where they disagree, or a
# release tag vX.Y.Z whose X.Y.Z doesn't match the manifest.
#
# Release flow:
#   ./scripts/bump-exapp-version.sh 0.2.0
#   git commit -am "release: 0.2.0"
#   git tag v0.2.0
#   git push origin main v0.2.0
#
# The tag push makes publish-exapp-image.yml publish
# ghcr.io/codemyriad/gocassini:{0.2.0,0.2.0-cuda,0.2.0-rocm} alongside the
# usual sha-<shortsha>[-cuda] and latest[-cuda|-rocm] tags.
#
# Usage:
#   ./scripts/bump-exapp-version.sh <new-version>   # e.g. 0.2.0

set -euo pipefail

INFO_XML="$(git rev-parse --show-toplevel)/appinfo/info.xml"
NEW_VERSION="${1:?usage: $0 <new-version> (e.g. 0.2.0)}"

if [[ ! "$NEW_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "error: version must be X.Y.Z, got: '$NEW_VERSION'" >&2
  exit 1
fi

CURRENT_VERSION="$(sed -n 's|.*<version>\(.*\)</version>.*|\1|p' "$INFO_XML" | head -n1)"
CURRENT_TAG="$(sed -n 's|.*<image-tag>\(.*\)</image-tag>.*|\1|p' "$INFO_XML" | head -n1)"

if [[ -z "$CURRENT_VERSION" || -z "$CURRENT_TAG" ]]; then
  echo "error: could not find <version> and <image-tag> in $INFO_XML" >&2
  exit 1
fi

if [[ "$CURRENT_VERSION" == "$NEW_VERSION" && "$CURRENT_TAG" == "$NEW_VERSION" ]]; then
  echo "appinfo/info.xml is already at $NEW_VERSION — nothing to do."
  exit 0
fi

sed -i \
  -e "s|<version>${CURRENT_VERSION}</version>|<version>${NEW_VERSION}</version>|" \
  -e "s|<image-tag>${CURRENT_TAG}</image-tag>|<image-tag>${NEW_VERSION}</image-tag>|" \
  "$INFO_XML"

# Verify the rewrite landed exactly once per element.
for marker in "<version>${NEW_VERSION}</version>" "<image-tag>${NEW_VERSION}</image-tag>"; do
  count="$(grep -cF "$marker" "$INFO_XML")"
  if [[ "$count" -ne 1 ]]; then
    echo "error: expected exactly one '$marker' in $INFO_XML after rewrite, found $count" >&2
    exit 1
  fi
done

echo "Bumped appinfo/info.xml: version ${CURRENT_VERSION} -> ${NEW_VERSION}, image-tag ${CURRENT_TAG} -> ${NEW_VERSION}"
echo
echo "Next steps:"
echo "  git commit -am \"release: ${NEW_VERSION}\""
echo "  git tag v${NEW_VERSION}"
echo "  git push origin main v${NEW_VERSION}"
