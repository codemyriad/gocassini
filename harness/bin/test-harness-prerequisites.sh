#!/usr/bin/env bash
# Offline, no-Docker contract test for the two native Nextcloud prerequisites.
#
# Recordings live in a Team folder whose broad read mount is the virtual
# `everyone` group, so the substrate needs two PHP apps an ExApp cannot install
# for itself: `groupfolders` and `group_everyone`. Since D-554 neither is
# optional — there is no mode that serves recordings without them — and since
# PR #171 a missing `everyone` group makes the provisioner return BEFORE the
# Team folder is created, which is a silent no-op rather than a visible error.
#
# The bootstrap assertion here is the one real check rescued from
# test-exapp-access-control.sh, which was deleted with the
# CASSINI_NC_ACCESS_CONTROL flag it existed to pin.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
BOOTSTRAP="$SCRIPT_DIR/bootstrap.sh"
SANDBOX_WIRE="$ROOT/sandbox/wire-cassini.sh"
MANIFEST="$ROOT/appinfo/info.xml"

fail() { echo "FAIL: $*" >&2; exit 1; }

# 1. The harness must provide both apps before the enabled edge fires.
for app in groupfolders group_everyone; do
  grep -qF "app:install $app" "$BOOTSTRAP" \
    || fail "bootstrap does not install required Nextcloud app $app"
  grep -qF "app:enable $app" "$BOOTSTRAP" \
    || fail "bootstrap does not enable required Nextcloud app $app"
done

# 2. The sandbox is a dogfood instance people keep real recordings on, so its
#    enable is HARD — no `|| true`. bootstrap.sh only tries; a throwaway test
#    stack can survive a miss, a dogfood box cannot. Since the AIO rewrite
#    (D-515) the sandbox is wired by wire-cassini.sh, not deploy.sh.
for app in groupfolders group_everyone; do
  # ${app} braced: bare "$app[" reads as an array subscript to shellcheck (SC1087),
  # and the bracket here opens a POSIX character class, not an index.
  grep -qE "^[[:space:]]*occ app:enable ${app}[[:space:]]*\$" "$SANDBOX_WIRE" \
    || fail "sandbox/wire-cassini.sh does not hard-enable required Nextcloud app $app"
done

# 3. The flag is gone; nothing may declare it again.
grep -qF 'CASSINI_NC_ACCESS_CONTROL' "$MANIFEST" \
  && fail "appinfo/info.xml still declares the retired CASSINI_NC_ACCESS_CONTROL variable"

echo "PASS: Team folders + Everyone Group are provisioned by the harness and hard-enabled on the sandbox"
