#!/usr/bin/env bash
#
# Offline unit tests for scripts/lib-exapp-register.sh.
#
# No Docker, no network, no stack — the pure decision functions only. The rules
# under test are the ones that cost real production incidents: refusing moving
# tags, deriving the image variant from the compute device rather than forking
# the deploy, and never writing a device suffix into <image-tag>.
#
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-exapp-register.sh
source "$SCRIPT_DIR/lib-exapp-register.sh"

exapp_log() { :; }

FAILURES=0
ok()   { printf 'ok   %s\n' "$1"; }
fail() { printf 'FAIL %s\n' "$1"; FAILURES=$((FAILURES + 1)); }

expect_ok() {
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then ok "$desc"; else fail "$desc (expected success)"; fi
}
expect_fail() {
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then fail "$desc (expected failure)"; else ok "$desc"; fi
}
expect_eq() {
  local desc="$1" want="$2" got="$3"
  if [[ "$want" == "$got" ]]; then ok "$desc"; else fail "$desc (want '$want', got '$got')"; fi
}

# --- moving and non-release tags are refused --------------------------------
for invalid in latest latest-cuda latest-rocm cuda rocm main stable branch-foo dispatch-20260101 nightly develop prod-current v1.0.0; do
  expect_fail "refuses non-release tag '$invalid'" exapp_assert_immutable_tag "$invalid"
done
expect_fail "refuses an empty tag" exapp_assert_immutable_tag ""

# --- released tags are accepted --------------------------------------------
for released in 0.1.0 1.2.3 0.2.0-alpha.4 2.0.0-rc.1; do
  expect_ok "accepts released tag '$released'" exapp_assert_immutable_tag "$released"
done

# --- raw commit pins need an explicit opt-in --------------------------------
EXAPP_ALLOW_UNRELEASED_TAG=0
expect_fail "refuses a sha- pin by default" exapp_assert_immutable_tag sha-d64b6aa28d59
EXAPP_ALLOW_UNRELEASED_TAG=1
expect_ok "accepts a sha- pin when opted in" exapp_assert_immutable_tag sha-d64b6aa28d59
EXAPP_ALLOW_UNRELEASED_TAG=0

# --- compute device is a parameter, not a fork ------------------------------
expect_eq "cpu device leaves the tag alone" \
  "0.2.0" "$(exapp_image_tag_for_device 0.2.0 cpu)"
expect_eq "an absent device means cpu" \
  "0.2.0" "$(exapp_image_tag_for_device 0.2.0)"
expect_eq "cuda device appends -cuda" \
  "0.2.0-cuda" "$(exapp_image_tag_for_device 0.2.0 cuda)"
expect_eq "rocm device appends -rocm" \
  "0.2.0-rocm" "$(exapp_image_tag_for_device 0.2.0 rocm)"
expect_fail "rejects an unknown compute device" exapp_image_tag_for_device 0.2.0 tpu

# --- manifest rendering -----------------------------------------------------
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

cat > "$WORK/info.xml" <<'XML'
<info>
  <version>9.9.9</version>
  <external-app>
    <docker-install>
      <registry>ghcr.io</registry>
      <image>codemyriad/gocassini</image>
      <image-tag>9.9.9</image-tag>
    </docker-install>
    <environment-variables>
      <variable><name>CASSINI_TALK_RECORDING_SECRET</name></variable>
      <variable><name>CASSINI_TALK_SIGNALING_INTERNAL_SECRET</name></variable>
    </environment-variables>
  </external-app>
</info>
XML

expect_ok "renders a manifest with a pinned tag" \
  exapp_render_manifest "$WORK/info.xml" "$WORK/out.xml" 1.2.3
expect_eq "pins <image-tag>" "1.2.3" "$(exapp_manifest_get "$WORK/out.xml" image-tag)"
expect_eq "leaves <version> alone" "9.9.9" "$(exapp_manifest_get "$WORK/out.xml" version)"

# AppAPI appends the device suffix itself; a suffixed <image-tag> would make it
# ask for `1.2.3-cuda-cuda`.
expect_fail "refuses a device-suffixed <image-tag>" \
  exapp_render_manifest "$WORK/info.xml" "$WORK/out.xml" 1.2.3-cuda
expect_fail "refuses a missing source manifest" \
  exapp_render_manifest "$WORK/nope.xml" "$WORK/out.xml" 1.2.3

expect_ok "accepts a manifest declaring both Talk secrets" \
  exapp_assert_manifest_declares "$WORK/out.xml" \
    CASSINI_TALK_RECORDING_SECRET CASSINI_TALK_SIGNALING_INTERNAL_SECRET
# AppAPI silently drops --env for undeclared keys, so this must be loud.
expect_fail "rejects a manifest missing a required declaration" \
  exapp_assert_manifest_declares "$WORK/out.xml" CASSINI_TOTALLY_UNDECLARED

# --- the real manifest must stay deployable --------------------------------
REAL_INFO="$SCRIPT_DIR/../appinfo/info.xml"
expect_ok "the checked-in manifest declares both Talk secrets" \
  exapp_assert_manifest_declares "$REAL_INFO" \
    CASSINI_TALK_RECORDING_SECRET CASSINI_TALK_SIGNALING_INTERNAL_SECRET
expect_eq "the checked-in manifest pins <image-tag> to <version>" \
  "$(exapp_manifest_get "$REAL_INFO" version)" \
  "$(exapp_manifest_get "$REAL_INFO" image-tag)"
expect_ok "the checked-in <image-tag> is a deployable pin" \
  exapp_assert_immutable_tag "$(exapp_manifest_get "$REAL_INFO" image-tag)"

# --- the library must never be able to destroy the archive ------------------
# The volume is the recording archive. Comments may name --rm-data (they
# explain why it is absent); executable lines may not.
f="$SCRIPT_DIR/lib-exapp-register.sh"
if grep -n -- '--rm-data' "$f" | grep -qv ':[[:space:]]*#'; then
  fail "$(basename "$f") has a --rm-data code path"
else
  ok "$(basename "$f") has no --rm-data code path"
fi

echo
if (( FAILURES )); then
  echo "$FAILURES failure(s)"
  exit 1
fi
echo "all checks passed"
