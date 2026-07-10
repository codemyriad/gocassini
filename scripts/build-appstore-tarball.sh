#!/usr/bin/env bash
# build-appstore-tarball.sh — assemble the Nextcloud App Store package.
#
# Cassini is an ExApp: the runtime is the pinned Docker image in info.xml, so
# the App Store archive is just the manifest and a little metadata. Files are
# copied by an explicit allowlist — source, Dockerfiles, harness, and any
# signing material can never leak in because they are simply never listed.
#
# The archive root is `gocassini/` to match <id>gocassini</id>.
#
#   # Local, unsigned: build the staging tree and the tarball in one go.
#   ./scripts/build-appstore-tarball.sh --version 0.3.0-alpha.1
#
#   # CI signed flow, in three steps (occ signs the tree in between):
#   ./scripts/build-appstore-tarball.sh --version V --stage-only
#   occ integrity:sign-app --path build/appstore/gocassini ...   # adds signature.json
#   ./scripts/build-appstore-tarball.sh --version V --archive-only
#
# Options:
#   --version X.Y.Z[-pre]   required; must equal the packaged info.xml <version>
#   --staging DIR           staging root (default: build/appstore)
#   --output PATH           tarball path (default: build/artifacts/appstore/gocassini.tar.gz)
#   --stage-only            build the gocassini/ tree, do not create the tarball
#   --archive-only          tar an existing (e.g. already-signed) tree; do not rebuild it

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-release-version.sh
source "$SCRIPT_DIR/lib-release-version.sh"

# Allowlist of files copied into gocassini/ (paths relative to the repo root).
# README.md is intentionally excluded: the App Store reads metadata from
# info.xml, and <website> already links the repo README.
APPSTORE_FILES=(
  appinfo/app.php
  appinfo/info.xml
  img/app.svg
  CHANGELOG.md
  LICENSE
)

usage() {
  cat >&2 <<'EOF'
Usage:
  ./scripts/build-appstore-tarball.sh --version X.Y.Z[-pre]
      [--staging DIR] [--output PATH] [--stage-only | --archive-only] [--sign-app]

--sign-app runs `occ integrity:sign-app` on the staged tree. It needs, in the
environment:
  OCC              command that runs occ against a working Nextcloud, e.g.
                   "docker exec -u www-data nc php occ" — occ needs a bootable
                   Nextcloud, so how you provide one is left to the caller.
  APP_PRIVATE_KEY  PEM content of the app signing key
  APP_PUBLIC_CRT   PEM content of the signed certificate
EOF
}

die() { echo "error: $*" >&2; exit 1; }

main() {
  local version="" staging="" output="" mode="both" sign_app=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --version)      version="${2:?--version needs a value}"; shift 2 ;;
      --staging)      staging="${2:?--staging needs a value}"; shift 2 ;;
      --output)       output="${2:?--output needs a value}"; shift 2 ;;
      --stage-only)   mode="stage"; shift ;;
      --archive-only) mode="archive"; shift ;;
      --sign-app)     sign_app=1; shift ;;
      -h|--help)      usage; return 0 ;;
      *) echo "error: unknown argument '$1'" >&2; usage; return 1 ;;
    esac
  done
  [[ -n "$version" ]] || { echo "error: --version is required" >&2; usage; return 1; }
  rv_validate "$version" || return 1
  # --sign-app signs the tree as it is staged; there is nothing to sign when
  # only re-archiving an existing tree.
  [[ "$sign_app" -eq 1 && "$mode" == "archive" ]] \
    && die "--sign-app cannot be combined with --archive-only"

  local root
  root="$(git rev-parse --show-toplevel)"
  staging="${staging:-$root/build/appstore}"
  output="${output:-$root/build/artifacts/appstore/gocassini.tar.gz}"
  local tree="$staging/gocassini"

  if [[ "$mode" != "archive" ]]; then
    # The packaged manifest must be the one for this exact version.
    local packaged
    packaged="$(rv_read_version "$root/appinfo/info.xml")" || return 1
    [[ "$packaged" == "$version" ]] \
      || die "appinfo/info.xml is at $packaged, not $version; run scripts/release-version.sh first"

    rm -rf "$tree"
    local f
    for f in "${APPSTORE_FILES[@]}"; do
      [[ -f "$root/$f" ]] || die "missing packaged file: $f"
      mkdir -p "$tree/$(dirname "$f")"
      cp "$root/$f" "$tree/$f"
    done
    echo "Staged ${#APPSTORE_FILES[@]} files into ${tree#"$root"/}/"

    if [[ "$sign_app" -eq 1 ]]; then
      : "${OCC:?--sign-app requires OCC (a command that runs occ against a working Nextcloud) in the environment}"
      : "${APP_PRIVATE_KEY:?--sign-app requires APP_PRIVATE_KEY (PEM content) in the environment}"
      : "${APP_PUBLIC_CRT:?--sign-app requires APP_PUBLIC_CRT (PEM content) in the environment}"
      local keydir key crt
      keydir="$(mktemp -d)"
      # shellcheck disable=SC2064  # expand keydir now; it is fixed for this run.
      trap "rm -rf '$keydir'" EXIT
      key="$keydir/gocassini.key"
      crt="$keydir/gocassini.crt"
      ( umask 077; printf '%s\n' "$APP_PRIVATE_KEY" >"$key"; printf '%s\n' "$APP_PUBLIC_CRT" >"$crt" )
      echo "Signing $tree via: \$OCC integrity:sign-app"
      # $OCC is a command with arguments (e.g. docker exec … php occ); it must
      # word-split, so it is intentionally unquoted.
      # shellcheck disable=SC2086
      $OCC integrity:sign-app --privateKey="$key" --certificate="$crt" --path="$tree"
      [[ -f "$tree/appinfo/signature.json" ]] \
        || die "signing reported success but $tree/appinfo/signature.json is missing"
      echo "Signed: appinfo/signature.json present"
    fi
  fi

  if [[ "$mode" != "stage" ]]; then
    [[ -d "$tree" ]] || die "no staging tree at $tree (run without --archive-only first)"
    mkdir -p "$(dirname "$output")"
    # Archive root is gocassini/ because we tar the directory by name from its
    # parent. --owner/--group keep it independent of the building user.
    tar -czf "$output" --owner=0 --group=0 -C "$staging" gocassini
    echo "Wrote ${output#"$root"/} ($(wc -c <"$output") bytes)"
  fi
}

main "$@"
