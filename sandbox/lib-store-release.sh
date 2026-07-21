#!/usr/bin/env bash
# lib-store-release.sh — pure helper for picking a Cassini release from the
# Nextcloud AppAPI ExApp store catalog. No network, no Docker; sourced by
# sandbox/deploy.sh and exercised by sandbox/test-store-release.sh.

# store_latest_release_url <app-id> [catalog-file]
#
# Prints the download URL of the newest published release for <app-id> in the
# AppAPI catalog (appapi_apps.json), across ALL channels — a pre-release
# (alpha/beta/rc) or a stable version, whichever is highest. Reads the catalog
# from <catalog-file>, or from stdin when it is omitted or "-". Returns nonzero
# (printing nothing) when no matching release is found.
#
# Release download URLs embed the version tag as …/download/v<version>/<id>.tar.gz.
# We keep every gocassini artifact whose path carries a vX.Y.Z tag and take the
# highest via `sort -V`. `sort -V` follows semver precedence for these URLs: a
# pre-release sorts before its own stable (v0.2.0-rc.1 < v0.2.0), so the newest
# stable wins when both exist, and a pre-release is only picked when it is
# genuinely the newest (e.g. v0.3.0-alpha.1 over v0.2.0). test-store-release.sh
# locks that ordering so a future "simplification" of this pipeline can't
# silently install the wrong release.
store_latest_release_url() {
  local app_id="$1" catalog="${2:--}"
  local url
  url="$(grep -oE "https://[^\"']*/${app_id}[^\"']*\.tar\.gz" -- "$catalog" \
    | grep -E "/v[0-9]+\.[0-9]+\.[0-9]+" \
    | sort -V | tail -n1)"
  [[ -n "$url" ]] || return 1
  printf '%s\n' "$url"
}
