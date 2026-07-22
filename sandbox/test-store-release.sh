#!/usr/bin/env bash
# test-store-release.sh — fast, local tests for the App Store release selector.
#
# Sources sandbox/lib-store-release.sh and exercises store_latest_release_url
# against fixture catalogs. No network, no Docker. Locks the semver ordering the
# sandbox relies on so a future edit to the pipeline can't silently install the
# wrong release.
#
#   ./sandbox/test-store-release.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=sandbox/lib-store-release.sh
source "$SCRIPT_DIR/lib-store-release.sh"

PASS=0
FAIL=0

ok() { PASS=$((PASS + 1)); printf '  ok   %s\n' "$1"; }
bad() { FAIL=$((FAIL + 1)); printf '  FAIL %s\n' "$1" >&2; }

# eq <description> <expected> <actual>
eq() {
  if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1 (expected '$2', got '$3')"; fi
}

# u <version> — the gocassini release URL shape the catalog carries.
u() { printf 'https://github.com/codemyriad/gocassini/releases/download/v%s/gocassini.tar.gz' "$1"; }

# picks <description> <expected-version> <catalog-lines...> — assert the selector
# returns u(<expected-version>) when fed the given catalog lines (as JSON-ish
# text, one URL per line is enough since we match on the URL shape).
picks() {
  local desc="$1" want; want="$(u "$2")"; shift 2
  local catalog got
  catalog="$(printf '%s\n' "$@")"
  got="$(store_latest_release_url gocassini <<<"$catalog" || true)"
  eq "$desc" "$want" "$got"
}

echo "store_latest_release_url — semver precedence across channels"

# Newest stable wins even when its own pre-releases are present.
picks "stable beats its own rc/alpha" 0.2.0 \
  "$(u 0.2.0-alpha.2)" "$(u 0.2.0-rc.1)" "$(u 0.2.0)"

# A higher minor pre-release beats a lower stable.
picks "higher-minor prerelease beats lower stable" 0.3.0-alpha.1 \
  "$(u 0.2.0)" "$(u 0.3.0-alpha.1)"

# Pre-release numbers compare numerically, not lexically (10 > 2).
picks "alpha.10 beats alpha.2" 0.2.0-alpha.10 \
  "$(u 0.2.0-alpha.2)" "$(u 0.2.0-alpha.10)"

# Channel ranking within one version: rc > beta > alpha.
picks "rc beats beta beats alpha" 0.2.0-rc.1 \
  "$(u 0.2.0-alpha.5)" "$(u 0.2.0-beta.1)" "$(u 0.2.0-rc.1)"

# Only the sole release available is returned.
picks "single pre-release" 0.2.0-alpha.3 "$(u 0.2.0-alpha.3)"

echo "store_latest_release_url — selectivity"

# A different app's artifact in the catalog is ignored.
eq "ignores other apps" "$(u 0.2.0)" \
  "$(store_latest_release_url gocassini <<<"$(printf '%s\n' \
      'https://github.com/other/whisper/releases/download/v9.9.9/whisper.tar.gz' \
      "$(u 0.2.0)")" || true)"

echo "store_latest_release_url — no match"

# Empty catalog -> nonzero, no output.
if out="$(store_latest_release_url gocassini <<<"" )"; then
  bad "empty catalog should fail (got '$out')"
else
  eq "empty catalog fails with no output" "" "${out:-}"
fi

# A catalog with only a non-versioned URL -> nonzero.
if store_latest_release_url gocassini \
     <<<'https://github.com/codemyriad/gocassini/releases/download/latest/gocassini.tar.gz' >/dev/null; then
  bad "unversioned-only catalog should fail"
else
  ok "unversioned-only catalog fails"
fi

echo
printf 'store-release: %d passed, %d failed\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
