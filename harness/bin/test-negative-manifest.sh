#!/usr/bin/env bash
# Offline, no-Docker contract test for make-negative-manifest.py.
#
# Proves the generator emits a manifest that differs from appinfo/info.xml by
# EXACTLY the removed CASSINI_TALK_SIGNALING_INTERNAL_SECRET declaration — the
# D-403 regression the faithful installed-ExApp sensitivity control models — and
# nothing else: sibling <variable> declarations, <version>, <image-tag>, and the
# markers the required "Validate appinfo/info.xml" job greps for all survive, so
# AppAPI can still register the app and the failure surfaces at route
# verification rather than at manifest validation.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
# shellcheck disable=SC1091 # SCRIPT_DIR is resolved dynamically above.
source "$SCRIPT_DIR/lib-exapp-manifest.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }

command -v python3 >/dev/null 2>&1 || fail "python3 is required"

GEN="$SCRIPT_DIR/make-negative-manifest.py"
SOURCE="$ROOT/appinfo/info.xml"
TARGET_VAR="CASSINI_TALK_SIGNALING_INTERNAL_SECRET"

count_env_vars() {
  python3 - "$1" <<'PY'
import sys
from xml.etree import ElementTree as ET
env = ET.parse(sys.argv[1]).getroot().find("./external-app/environment-variables")
print(0 if env is None else len(env.findall("variable")))
PY
}

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

negative="$TMP_DIR/info-no-signaling.xml"
python3 "$GEN" "$SOURCE" "$negative" || fail "generator failed on the real manifest"

# Sanity: the real manifest must actually declare the secret, else this test is
# vacuous.
grep -qF "<name>$TARGET_VAR</name>" "$SOURCE" \
  || fail "test bug: source manifest does not declare $TARGET_VAR"

# The one declaration is gone...
if grep -qF "<name>$TARGET_VAR</name>" "$negative"; then
  fail "negative manifest still declares $TARGET_VAR"
fi

# ...and it is the ONLY <variable> removed: exactly one fewer than the source.
src_vars="$(count_env_vars "$SOURCE")"
neg_vars="$(count_env_vars "$negative")"
[[ "$src_vars" -ge 2 ]] || fail "test bug: source manifest has too few <variable> nodes ($src_vars)"
[[ "$neg_vars" -eq $((src_vars - 1)) ]] \
  || fail "expected $((src_vars - 1)) variables in negative manifest, got $neg_vars"

# Every sibling declaration survives untouched — the recording secret in
# particular, because the control differentiates on it (secret_configured stays
# true while signaling_internal_secret_configured flips to false).
for sibling in CASSINI_TALK_RECORDING_SECRET CASSINI_TALK_BACKEND_URL \
  CASSINI_NC_ACCESS_CONTROL OPENROUTER_API_KEY LLM_BASE_URL LLM_MODEL \
  CASSINI_OPERATOR_API_TOKEN; do
  grep -qF "<name>$sibling</name>" "$negative" \
    || fail "negative manifest dropped sibling declaration $sibling"
done

# <version>, <image-tag>, and the derived image ref are copied verbatim, so the
# generated fixture cannot drift from appinfo/info.xml as versions bump.
[[ "$(exapp_app_version "$negative")" == "$(exapp_app_version "$SOURCE")" ]] \
  || fail "negative manifest <version> drifted from source"
[[ "$(exapp_image_tag "$negative")" == "$(exapp_image_tag "$SOURCE")" ]] \
  || fail "negative manifest <image-tag> drifted from source"
[[ "$(exapp_image_ref "$negative")" == "$(exapp_image_ref "$SOURCE")" ]] \
  || fail "negative manifest image ref drifted from source"

# It still passes the SAME required-fields check publish-exapp-image.yml's
# "Validate appinfo/info.xml" job runs, so the manifest remains registrable.
for field in '<id>gocassini</id>' '<external-app>' '<docker-install>' '<routes>'; do
  grep -qF "$field" "$negative" \
    || fail "negative manifest missing required marker: $field"
done
if grep -qE '<url>\^?/enabled' "$negative" || grep -qE '<url>\^?/init' "$negative"; then
  fail "negative manifest must not declare /enabled or /init under <routes>"
fi

# The generator fails loudly on a manifest that does not declare the secret: a
# second pass over its own output must exit non-zero rather than silently emit an
# unchanged file (which would let a broken generator masquerade as a control).
if python3 "$GEN" "$negative" "$TMP_DIR/second.xml" >/dev/null 2>&1; then
  fail "generator did not fail on a manifest lacking $TARGET_VAR"
fi

echo "PASS: make-negative-manifest.py strips only $TARGET_VAR and preserves the rest"
