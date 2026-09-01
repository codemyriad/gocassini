#!/usr/bin/env bash
#
# Regression test: the compatibility manual-test-setup.sh must delegate image
# preparation to D-493's canonical harness_prepare_exapp_image helper. That
# helper derives the "as if pulled from ghcr.io" image tag from info.xml and
# applies the build/reuse-local/pull policy. A direct helper call or hardcoded
# tag in the wrapper would let its setup drift from `cassini dev stack`.
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
# shellcheck disable=SC1091 # SCRIPT_DIR is resolved dynamically above.
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

# 4. manual-test-setup.sh delegates to the canonical D-493 image phase. The
#    wrapper selects build or reuse-local; it must not independently parse the
#    manifest or reproduce tagging policy.
setup_sh="$SCRIPT_DIR/manual-test-setup.sh"
stack_sh="$SCRIPT_DIR/lib/stack.sh"
grep -q '^harness_prepare_exapp_image$' "$setup_sh" \
  || fail "manual-test-setup.sh does not delegate to harness_prepare_exapp_image"
grep -q 'CASSINI_HARNESS_EXAPP_IMAGE_MODE="build"' "$setup_sh" \
  || fail "manual-test-setup.sh does not select canonical build mode"
grep -q 'CASSINI_HARNESS_EXAPP_IMAGE_MODE:-reuse-local' "$setup_sh" \
  || fail "manual-test-setup.sh does not default to canonical reuse-local mode"
if grep -q 'exapp_image_tag' "$setup_sh"; then
  fail "manual-test-setup.sh directly parses the manifest instead of delegating"
fi
if grep -nE 'gocassini:(latest|[0-9]+\.[0-9]+\.[0-9]+)' "$setup_sh"; then
  fail "manual-test-setup.sh hardcodes a gocassini image tag (see line above)"
fi

# 5. The canonical helper owns manifest extraction and composes, rather than
#    hardcoding, the production-facing tag. Guard the old floating :latest bug.
# shellcheck disable=SC2016 # The pattern intentionally matches source text.
grep -qF 'info_xml="$(harness_exapp_info_xml_path)"' "$stack_sh" \
  || fail "canonical image helper does not resolve the selected info.xml"
# shellcheck disable=SC2016 # The pattern intentionally matches source text.
grep -qF 'image_tag="$(exapp_image_tag "$info_xml")"' "$stack_sh" \
  || fail "canonical image helper does not derive image-tag from selected info.xml"
# shellcheck disable=SC2016 # The pattern intentionally matches source text.
grep -qF 'image_as_production="ghcr.io/codemyriad/gocassini:${image_tag}"' "$stack_sh" \
  || fail "canonical image helper does not compose the manifest-derived tag"
if grep -nE 'image_as_production=.*gocassini:(latest|[0-9]+\.[0-9]+\.[0-9]+)' "$stack_sh"; then
  fail "canonical image helper hardcodes a floating or release tag (see line above)"
fi

# 6. Touched scripts still parse.
bash -n "$setup_sh" || fail "bash -n failed for manual-test-setup.sh"
bash -n "$SCRIPT_DIR/lib-exapp-image.sh" || fail "bash -n failed for lib-exapp-image.sh"
bash -n "$stack_sh" || fail "bash -n failed for lib/stack.sh"

# 7. Every operator image declares whether its native runtime actually bundles
# CUDA. Hardware visibility is not enough: AppAPI may place the portable image
# on a GPU daemon after a silent -cuda tag fallback, and admission must still
# fail closed before the ASR binary starts.
for dockerfile in deployment/Dockerfile.exapp deployment/Dockerfile.operator; do
  grep -q 'CASSINI_STT_CUDA_CAPABLE=0' "$PROJECT_ROOT/$dockerfile" \
    || fail "$dockerfile does not declare the portable/CUDA-ineligible runtime"
done
grep -q 'CASSINI_STT_CUDA_CAPABLE=1' "$PROJECT_ROOT/deployment/Dockerfile.exapp.cuda" \
  || fail "Dockerfile.exapp.cuda does not declare its CUDA-capable runtime"

# 8. Local image builds resolve a concrete newest-stable FFmpeg version before
# invoking Docker. Passing only the literal value "latest" would allow the
# Docker cache to retain an older upstream release indefinitely.
grep -qF 'deployment/ffmpeg/resolve-latest.sh' "$stack_sh" \
  || fail "harness image build does not resolve newest stable FFmpeg"
# shellcheck disable=SC2016 # Match the literal shell source, not this test's variables.
grep -qF -- '--build-arg "FFMPEG_VERSION=$ffmpeg_version"' "$stack_sh" \
  || fail "harness image build does not pass the resolved FFmpeg version"
# shellcheck disable=SC2016 # Match the literal Compose interpolation syntax.
grep -qF 'FFMPEG_VERSION: ${FFMPEG_VERSION:-latest}' "$PROJECT_ROOT/deployment/compose.yml" \
  || fail "Compose operator build does not forward its resolved FFmpeg version"

echo "PASS: ExApp image setup delegates to the manifest-derived canonical helper (<image-tag>=$real_tag)"
