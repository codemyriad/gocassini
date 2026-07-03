#!/usr/bin/env bash
# build-appstore-tarball.sh — build the Nextcloud App Store release tarball.
#
# Stages the store-facing subset of the repo under a single top-level
# gocassini/ folder (the layout apps.nextcloud.com requires) and packs it as
# gocassini-<version>.tar.gz. By default the package is UNSIGNED — that is
# the correct current mode while app id registration is pending (see
# docs/release-policy.md).
#
# Usage:
#   ./scripts/build-appstore-tarball.sh [--version <version>] \
#       [--output-dir dist/appstore] [--sign-app]
#
# --sign-app generates appinfo/signature.json inside the staged folder before
# packing. It requires:
#   APP_PRIVATE_KEY   PEM content of the app signing key (env)
#   APP_PUBLIC_CRT    PEM content of the signed certificate (env)
#   OCC               command that runs Nextcloud's occ against a working
#                     instance (env, e.g. "docker exec -u www-data nc php occ");
#                     integrity:sign-app needs a bootable Nextcloud, so the
#                     caller provides one.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
APP_ID="gocassini"

VERSION=""
OUTPUT_DIR="${REPO_ROOT}/dist/appstore"
SIGN_APP=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)    VERSION="${2:?--version needs a value}"; shift 2 ;;
    --output-dir) OUTPUT_DIR="${2:?--output-dir needs a value}"; shift 2 ;;
    --sign-app)   SIGN_APP=true; shift ;;
    *) echo "error: unknown argument '$1'" >&2; exit 1 ;;
  esac
done

if [[ -z "$VERSION" ]]; then
  VERSION="$(xmllint --xpath 'string(/info/version)' "${REPO_ROOT}/appinfo/info.xml")"
fi
if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]]; then
  echo "error: version must be X.Y.Z or X.Y.Z-<prerelease>, got: '$VERSION'" >&2
  exit 1
fi

STAGE_DIR="${OUTPUT_DIR}/${APP_ID}"
TARBALL="${OUTPUT_DIR}/${APP_ID}-${VERSION}.tar.gz"

# The store-facing file set — matches the shape of the published
# v0.2.0-alpha.1 asset. Everything here ships publicly; add new files
# deliberately.
STAGE_FILES=(
  CHANGELOG.md
  LICENSE
  README.md
  appinfo/app.php
  appinfo/info.xml
  img/app.svg
)

rm -rf "$STAGE_DIR"
mkdir -p "$STAGE_DIR"
for f in "${STAGE_FILES[@]}"; do
  if [[ ! -f "${REPO_ROOT}/${f}" ]]; then
    echo "error: required file missing from repo: ${f}" >&2
    exit 1
  fi
  mkdir -p "${STAGE_DIR}/$(dirname "$f")"
  cp "${REPO_ROOT}/${f}" "${STAGE_DIR}/${f}"
done

if [[ "$SIGN_APP" == true ]]; then
  : "${APP_PRIVATE_KEY:?--sign-app requires APP_PRIVATE_KEY (PEM content) in the environment}"
  : "${APP_PUBLIC_CRT:?--sign-app requires APP_PUBLIC_CRT (PEM content) in the environment}"
  : "${OCC:?--sign-app requires OCC (a command running occ against a working Nextcloud) in the environment}"

  KEY_FILE="$(mktemp)"
  CRT_FILE="$(mktemp)"
  # shellcheck disable=SC2064 — expand the paths now; they don't change.
  trap "rm -f '$KEY_FILE' '$CRT_FILE'" EXIT
  chmod 600 "$KEY_FILE" "$CRT_FILE"
  printf '%s\n' "$APP_PRIVATE_KEY" > "$KEY_FILE"
  printf '%s\n' "$APP_PUBLIC_CRT" > "$CRT_FILE"

  echo "Signing app directory (occ integrity:sign-app) ..."
  $OCC integrity:sign-app \
    --privateKey="$KEY_FILE" \
    --certificate="$CRT_FILE" \
    --path="$STAGE_DIR"

  if [[ ! -f "${STAGE_DIR}/appinfo/signature.json" ]]; then
    echo "error: signing reported success but appinfo/signature.json is missing" >&2
    exit 1
  fi
  echo "SIGNED package: appinfo/signature.json present"
else
  echo "UNSIGNED package: appinfo/signature.json not generated (pass --sign-app after registration)"
fi

rm -f "$TARBALL"
tar -czf "$TARBALL" -C "$OUTPUT_DIR" "$APP_ID"

echo "Built ${TARBALL}"
tar -tzf "$TARBALL" | sort
