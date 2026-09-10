#!/usr/bin/env bash
# Fast contract test for the current App Store manifest schema.
# Nextcloud's App Store first applies pre-info.xslt, then validates info.xsd.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
INFO="$ROOT/appinfo/info.xml"
COMPANION_INFO="$ROOT/cassini_capture/appinfo/info.xml"
XSLT="$ROOT/spec/appstore/pre-info.xslt"
XSD="$ROOT/spec/appstore/info.xsd"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

for tool in xsltproc xmllint; do
  command -v "$tool" >/dev/null 2>&1 || fail "required tool not found: $tool"
done
for file in "$INFO" "$XSLT" "$XSD"; do
  [[ -f "$file" ]] || fail "required file not found: $file"
done

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

validate() {
  local source="$1"
  local transformed="$2"
  xsltproc "$XSLT" "$source" >"$transformed"
  xmllint --noout --schema "$XSD" "$transformed"
}

validate "$INFO" "$TMP_DIR/info.pre.xml" \
  || fail "current appinfo/info.xml failed App Store schema validation"

# cassini_capture is an ordinary native app, not an AppAPI ExApp, so it must
# validate directly. Running the ExApp compatibility transform here could hide
# a packaging error that Nextcloud would reject when the companion is enabled.
if [[ -f "$COMPANION_INFO" ]]; then
  xmllint --noout --schema "$XSD" "$COMPANION_INFO" \
    || fail "cassini_capture/appinfo/info.xml failed App Store schema validation"
fi

# The companion ships as a separate app and an administrator installs the pair,
# so a version mismatch is a packaging bug. It is asserted HERE, in the contract
# job that every pull request must pass, rather than only inside the optional
# capture package build: main's release path bumps appinfo/info.xml on its own,
# and the drift that creates would otherwise go unnoticed until someone
# happened to build the companion.
if [[ -f "$COMPANION_INFO" ]]; then
  info_version="$(xmllint --xpath 'string(/info/version)' "$INFO" 2>/dev/null || true)"
  companion_version="$(xmllint --xpath 'string(/info/version)' "$COMPANION_INFO" 2>/dev/null || true)"
  [[ -n "$info_version" ]] || fail "could not read <version> from appinfo/info.xml"
  [[ -n "$companion_version" ]] || fail "could not read <version> from cassini_capture/appinfo/info.xml"
  if [[ "$info_version" != "$companion_version" ]]; then
    fail "version drift: gocassini is $info_version, cassini_capture is $companion_version. An administrator installs the pair, so they must be released together. Reconcile cassini_capture/appinfo/info.xml before merging."
  fi
fi

cp "$INFO" "$TMP_DIR/invalid.xml"
if ! grep -q '<category>[^<]*</category>' "$TMP_DIR/invalid.xml"; then
  fail "fixture setup could not find a category in info.xml"
fi
sed -i '0,/<category>[^<]*<\/category>/s//<category>not-a-real-category<\/category>/' \
  "$TMP_DIR/invalid.xml"
if validate "$TMP_DIR/invalid.xml" "$TMP_DIR/invalid.pre.xml" >/dev/null 2>&1; then
  fail "schema accepted an invalid category fixture"
fi

echo "PASS: ExApp and native companion manifests validate, versions agree, and an invalid fixture fails"
