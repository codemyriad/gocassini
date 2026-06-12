#!/usr/bin/env bash
#
# Regression test: manual-test-setup.sh must derive its "as if pulled from
# ghcr.io" image tag from appinfo/info.xml's <image-tag> instead of
# hardcoding one. The dogfood deploy maps ghcr.io -> local and lets AppAPI
# resolve <registry>/<image>:<image-tag> verbatim from info.xml, so a
# hardcoded tag (the old `:latest`) breaks the moment the manifest pins a
# version — AppAPI looks for :X.Y.Z locally and finds nothing.
#
# Fast and offline: no docker, no compose. Run directly:
#   ./harness/bin/test-exapp-image-ref.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[[ -f "$SCRIPT_DIR/lib-exapp-image.sh" ]] || fail "lib-exapp-image.sh is missing"
source "$SCRIPT_DIR/lib-exapp-image.sh"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

# 1. Extracts the pinned tag from a manifest.
cat > "$TMP_DIR/info.xml" <<'XML'
<?xml version="1.0"?>
<info>
  <external-app>
    <docker-install>
      <registry>ghcr.io</registry>
      <image>codemyriad/gocassini</image>
      <image-tag>9.9.9-test</image-tag>
    </docker-install>
  </external-app>
</info>
XML
got="$(exapp_image_tag "$TMP_DIR/info.xml")"
[[ "$got" == "9.9.9-test" ]] || fail "expected tag 9.9.9-test, got '$got'"

# 2. Fails loudly when the manifest has no <image-tag> (defensive guard:
#    a silently-empty tag would docker-tag `gocassini:` and confuse AppAPI).
cat > "$TMP_DIR/notag.xml" <<'XML'
<?xml version="1.0"?>
<info>
  <external-app>
    <docker-install>
      <registry>ghcr.io</registry>
      <image>codemyriad/gocassini</image>
    </docker-install>
  </external-app>
</info>
XML
if exapp_image_tag "$TMP_DIR/notag.xml" >/dev/null 2>&1; then
  fail "expected extraction to fail for a manifest without <image-tag>"
fi

# 3. The real manifest yields a non-empty tag that matches an independent
#    extraction (catches sed-idiom drift vs. the actual info.xml layout).
real_tag="$(exapp_image_tag "$PROJECT_ROOT/appinfo/info.xml")"
[[ -n "$real_tag" ]] || fail "empty <image-tag> from appinfo/info.xml"
independent="$(grep -o '<image-tag>[^<]*</image-tag>' "$PROJECT_ROOT/appinfo/info.xml" \
  | head -n1 | sed -e 's|<image-tag>||' -e 's|</image-tag>||')"
[[ "$real_tag" == "$independent" ]] \
  || fail "lib extracted '$real_tag' but manifest pins '$independent'"

# 4. manual-test-setup.sh consumes the helper and no longer hardcodes a
#    gocassini image tag (the original bug: IMAGE_AS_PRODUCTION pinned
#    :latest while info.xml pinned a version).
setup_sh="$SCRIPT_DIR/manual-test-setup.sh"
grep -q 'exapp_image_tag' "$setup_sh" \
  || fail "manual-test-setup.sh does not derive its image tag via exapp_image_tag"
if grep -nE 'gocassini:[a-zA-Z0-9._-]' "$setup_sh"; then
  fail "manual-test-setup.sh hardcodes a gocassini image tag (see line above)"
fi

# 5. Touched scripts still parse.
bash -n "$setup_sh" || fail "bash -n failed for manual-test-setup.sh"
bash -n "$SCRIPT_DIR/lib-exapp-image.sh" || fail "bash -n failed for lib-exapp-image.sh"

echo "PASS: manual-test-setup.sh image tag is derived from appinfo/info.xml (<image-tag>=$real_tag)"
