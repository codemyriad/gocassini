#!/usr/bin/env bash
# validate-appstore-tarball.sh — enforce the App Store archive invariants.
#
# Runs against a built tarball with no secrets, so CI can gate an upload
# without App Store credentials. Checks:
#   - every entry is under a single `gocassini/` root
#   - gocassini/appinfo/info.xml exists and parses
#   - <id> is gocassini
#   - <version> equals the requested release version
#   - the Docker <image-tag> equals the requested version
#   - the archive is below 20 MiB
#   - no private key / certificate / CSR material is present
#   - gocassini/appinfo/signature.json exists when --signed is given
#
#   ./scripts/validate-appstore-tarball.sh --version 0.3.0-alpha.1 --tarball path.tar.gz
#   ./scripts/validate-appstore-tarball.sh --version 0.3.0-alpha.1 --tarball path.tar.gz --signed

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-release-version.sh
source "$SCRIPT_DIR/lib-release-version.sh"

MAX_BYTES=$((20 * 1024 * 1024))  # App Store archive ceiling: 20 MiB
# Extensions that must never ship in the package (signing material). Note
# signature.json is JSON, not one of these, so it is allowed.
FORBIDDEN_EXT_RE='\.(key|pem|crt|csr|p12|pfx)$'

usage() {
  cat >&2 <<'EOF'
Usage:
  ./scripts/validate-appstore-tarball.sh --version X.Y.Z[-pre] --tarball FILE [--signed]
EOF
}

main() {
  local version="" tarball="" signed=0 fail=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --version) version="${2:?--version needs a value}"; shift 2 ;;
      --tarball) tarball="${2:?--tarball needs a value}"; shift 2 ;;
      --signed)  signed=1; shift ;;
      -h|--help) usage; return 0 ;;
      *) echo "error: unknown argument '$1'" >&2; usage; return 1 ;;
    esac
  done
  [[ -n "$version" ]] || { echo "error: --version is required" >&2; usage; return 1; }
  [[ -n "$tarball" ]] || { echo "error: --tarball is required" >&2; usage; return 1; }
  rv_validate "$version" || return 1
  [[ -f "$tarball" ]] || { echo "error: $tarball not found" >&2; return 1; }

  # bad <message> — record a failed check but keep going so one run reports
  # every problem at once.
  bad() { echo "FAIL: $1" >&2; fail=1; }

  local entries
  entries="$(tar -tzf "$tarball")" || { echo "error: cannot read $tarball" >&2; return 1; }

  # Single gocassini/ root.
  local line
  while IFS= read -r line; do
    [[ -z "$line" ]] && continue
    if [[ "$line" != gocassini/* && "$line" != "gocassini" ]]; then
      bad "entry outside gocassini/ root: '$line'"
    fi
  done <<<"$entries"

  # No signing material.
  while IFS= read -r line; do
    if [[ "$line" =~ $FORBIDDEN_EXT_RE ]]; then
      bad "forbidden signing/secret file in archive: '$line'"
    fi
  done <<<"$entries"

  # Signature presence in signed mode.
  if [[ "$signed" -eq 1 ]] && ! grep -qxF "gocassini/appinfo/signature.json" <<<"$entries"; then
    bad "signed mode requested but gocassini/appinfo/signature.json is absent"
  fi

  # Size ceiling.
  local size
  size="$(wc -c <"$tarball")"
  if (( size > MAX_BYTES )); then
    bad "archive is ${size} bytes, over the ${MAX_BYTES}-byte (20 MiB) limit"
  fi

  # Manifest checks — extract just info.xml to a temp dir.
  local tmp info
  tmp="$(mktemp -d)"
  trap 'rm -rf "${tmp:-}"' EXIT
  info="$tmp/gocassini/appinfo/info.xml"
  if tar -xzf "$tarball" -C "$tmp" gocassini/appinfo/info.xml 2>/dev/null && [[ -f "$info" ]]; then
    if command -v xmllint >/dev/null 2>&1; then
      xmllint --noout "$info" 2>/dev/null || bad "gocassini/appinfo/info.xml is not well-formed"
    elif command -v python3 >/dev/null 2>&1; then
      python3 -c "import sys,xml.dom.minidom as m; m.parse(sys.argv[1])" "$info" 2>/dev/null \
        || bad "gocassini/appinfo/info.xml is not well-formed"
    fi
    local id ver tag
    id="$(sed -n 's|.*<id>\(.*\)</id>.*|\1|p' "$info" | head -n1)"
    ver="$(rv_read_version "$info" 2>/dev/null || true)"
    tag="$(rv_read_image_tag "$info" 2>/dev/null || true)"
    [[ "$id" == "gocassini" ]] || bad "<id> is '$id', expected 'gocassini'"
    [[ "$ver" == "$version" ]] || bad "<version> is '$ver', expected '$version'"
    [[ "$tag" == "$version" ]] || bad "<image-tag> is '$tag', expected '$version'"
  else
    bad "gocassini/appinfo/info.xml is missing from the archive"
  fi

  if [[ "$fail" -eq 0 ]]; then
    echo "OK: $tarball is a valid gocassini $version App Store archive$([[ "$signed" -eq 1 ]] && echo ' (signed)')."
    return 0
  fi
  echo "error: $tarball failed App Store validation" >&2
  return 1
}

main "$@"
