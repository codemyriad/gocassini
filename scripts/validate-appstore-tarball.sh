#!/usr/bin/env bash
# validate-appstore-tarball.sh — validate a Cassini App Store release tarball.
#
# Replicates what apps.nextcloud.com actually checks (plus Cassini's own
# version-lockstep policy) against a built gocassini-<version>.tar.gz:
#
#   - exactly one top-level folder named gocassini
#   - required store-facing files present, no private key material
#   - appinfo/info.xml well-formed; <version> == <image-tag> == the version
#     in the archive filename
#   - CHANGELOG.md has the matching '## [<version>]' section
#   - archive < 20 MiB, info.xml < 512 KiB (store limits)
#   - store metadata validation: pre-info.xslt -> info.xsd. The store strips
#     unknown elements (including <external-app><routes>) BEFORE validating,
#     so raw XSD validation false-fails Cassini and is deliberately not used.
#
# Usage:
#   ./scripts/validate-appstore-tarball.sh <tarball> [--require-signature]
#
# Offline override: set APPSTORE_XSLT / APPSTORE_XSD to local copies to skip
# the download.

set -euo pipefail

TARBALL="${1:?usage: $0 <tarball> [--require-signature]}"
shift
REQUIRE_SIGNATURE=false
if [[ "${1:-}" == "--require-signature" ]]; then
  REQUIRE_SIGNATURE=true
fi

APP_ID="gocassini"
XSLT_URL="https://raw.githubusercontent.com/nextcloud/appstore/master/nextcloudappstore/api/v1/release/pre-info.xslt"
XSD_URL="https://apps.nextcloud.com/schema/apps/info.xsd"
MAX_ARCHIVE_BYTES=$((20 * 1024 * 1024))
MAX_INFO_BYTES=$((512 * 1024))

fail=0
err() { echo "INVALID: $*" >&2; fail=1; }
ok()  { echo "ok: $*"; }

[[ -f "$TARBALL" ]] || { echo "error: no such file: $TARBALL" >&2; exit 1; }

# Version according to the archive filename.
base="$(basename "$TARBALL")"
if [[ "$base" =~ ^${APP_ID}-([0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?)\.tar\.gz$ ]]; then
  FILE_VERSION="${BASH_REMATCH[1]}"
  ok "archive name parses: version ${FILE_VERSION}"
else
  echo "error: archive must be named ${APP_ID}-<version>.tar.gz, got: ${base}" >&2
  exit 1
fi

WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

# --- archive size (store limit) ---
archive_bytes=$(($(wc -c < "$TARBALL")))
if (( archive_bytes < MAX_ARCHIVE_BYTES )); then
  ok "archive size ${archive_bytes} bytes (< 20 MiB)"
else
  err "archive is ${archive_bytes} bytes, store limit is 20 MiB"
fi

# --- layout: exactly one top-level folder == app id ---
tar -xzf "$TARBALL" -C "$WORK_DIR"
top_entries=("$WORK_DIR"/*)
if [[ ${#top_entries[@]} -eq 1 && -d "${WORK_DIR}/${APP_ID}" ]]; then
  ok "single top-level folder '${APP_ID}/'"
else
  err "archive must contain exactly one top-level folder named '${APP_ID}', found: $(cd "$WORK_DIR" && ls)"
fi
APP_DIR="${WORK_DIR}/${APP_ID}"

# --- required files ---
for f in CHANGELOG.md LICENSE README.md appinfo/app.php appinfo/info.xml img/app.svg; do
  if [[ -f "${APP_DIR}/${f}" ]]; then
    ok "required file present: ${f}"
  else
    err "required file missing: ${f}"
  fi
done

# --- no private key / CSR material may ship ---
leaked="$(find "$WORK_DIR" -type f \( -name '*.key' -o -name '*.csr' -o -name '*.pem' -o -name 'id_*' \) | sed "s|^${WORK_DIR}/||" || true)"
if [[ -z "$leaked" ]]; then
  ok "no key/CSR/PEM material in the archive"
else
  err "private key material must not ship in the archive: ${leaked}"
fi

# --- info.xml checks ---
INFO_XML="${APP_DIR}/appinfo/info.xml"
if [[ -f "$INFO_XML" ]]; then
  info_bytes=$(($(wc -c < "$INFO_XML")))
  if (( info_bytes < MAX_INFO_BYTES )); then
    ok "info.xml size ${info_bytes} bytes (< 512 KiB)"
  else
    err "info.xml is ${info_bytes} bytes, store limit is 512 KiB"
  fi

  if xmllint --noout "$INFO_XML" 2>/dev/null; then
    ok "info.xml is well-formed XML"
    XML_VERSION="$(xmllint --xpath 'string(/info/version)' "$INFO_XML")"
    XML_IMAGE_TAG="$(xmllint --xpath 'string(/info/external-app/docker-install/image-tag)' "$INFO_XML")"
    if [[ "$XML_VERSION" == "$FILE_VERSION" ]]; then
      ok "info.xml <version> matches archive name (${FILE_VERSION})"
    else
      err "info.xml <version> '${XML_VERSION}' != archive version '${FILE_VERSION}'"
    fi
    if [[ "$XML_IMAGE_TAG" == "$XML_VERSION" ]]; then
      ok "info.xml <image-tag> matches <version>"
    else
      err "info.xml <image-tag> '${XML_IMAGE_TAG}' != <version> '${XML_VERSION}'"
    fi
  else
    err "info.xml is not well-formed XML"
  fi
fi

# --- changelog section ---
if [[ -f "${APP_DIR}/CHANGELOG.md" ]] && grep -qF "## [${FILE_VERSION}]" "${APP_DIR}/CHANGELOG.md"; then
  ok "CHANGELOG.md has '## [${FILE_VERSION}]' section"
else
  err "CHANGELOG.md inside the archive has no '## [${FILE_VERSION}]' section"
fi

# --- store metadata validation: XSLT -> XSD ---
XSLT_FILE="${APPSTORE_XSLT:-${WORK_DIR}/pre-info.xslt}"
XSD_FILE="${APPSTORE_XSD:-${WORK_DIR}/info.xsd}"
if [[ ! -f "$XSLT_FILE" ]]; then
  curl -fsSL "$XSLT_URL" -o "$XSLT_FILE" \
    || { echo "error: could not download pre-info.xslt (set APPSTORE_XSLT to a local copy to run offline)" >&2; exit 1; }
fi
if [[ ! -f "$XSD_FILE" ]]; then
  curl -fsSL "$XSD_URL" -o "$XSD_FILE" \
    || { echo "error: could not download info.xsd (set APPSTORE_XSD to a local copy to run offline)" >&2; exit 1; }
fi
if [[ -f "$INFO_XML" ]]; then
  if xsltproc "$XSLT_FILE" "$INFO_XML" > "${WORK_DIR}/info.pre.xml" \
     && xmllint --noout --schema "$XSD_FILE" "${WORK_DIR}/info.pre.xml" 2> "${WORK_DIR}/xsd.err"; then
    ok "store metadata validates (pre-info.xslt -> info.xsd)"
  else
    err "store metadata validation failed (pre-info.xslt -> info.xsd):"
    sed 's/^/  /' "${WORK_DIR}/xsd.err" >&2 || true
  fi
fi

# --- app-code signature ---
if [[ -f "${APP_DIR}/appinfo/signature.json" ]]; then
  ok "appinfo/signature.json present (signed package)"
elif [[ "$REQUIRE_SIGNATURE" == true ]]; then
  err "appinfo/signature.json missing but --require-signature was given"
else
  echo "warn: appinfo/signature.json missing — acceptable only until app registration completes"
fi

if [[ "$fail" -ne 0 ]]; then
  echo >&2
  echo "${base} is INVALID." >&2
  exit 1
fi
echo
echo "${base} is a valid App Store package."
