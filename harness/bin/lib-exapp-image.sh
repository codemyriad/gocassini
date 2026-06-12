# shellcheck shell=bash
#
# Shared helper: derive the ExApp image tag from appinfo/info.xml.
#
# AppAPI resolves the deploy image as <registry>/<image>:<image-tag> straight
# from info.xml, so anything that pre-tags a locally-built image (e.g.
# manual-test-setup.sh's --registry-from=ghcr.io --registry-to=local mapping)
# must use the exact <image-tag> the manifest pins. Hardcoding a tag silently
# breaks whenever the manifest moves — which it now does on every release,
# since scripts/bump-exapp-version.sh keeps <image-tag> pinned to <version>.
#
# Deliberately sed-based (same idiom as scripts/bump-exapp-version.sh):
# xmllint is not guaranteed to exist in dev shells.
#
# Meant to be sourced; defines functions only, no side effects.

# Print the <image-tag> pinned in the given info.xml.
# Fails (exit 1) with a message on stderr when the tag cannot be extracted.
exapp_image_tag() {
  local info_xml="$1"
  local tag
  tag="$(sed -n 's|.*<image-tag>\(.*\)</image-tag>.*|\1|p' "$info_xml" | head -n1)"
  if [[ -z "$tag" ]]; then
    echo "error: could not extract <image-tag> from $info_xml" >&2
    return 1
  fi
  printf '%s\n' "$tag"
}
