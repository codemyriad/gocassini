#!/usr/bin/env bash
# Offline regression for seed-nc-files.sh pack validation.
#
# Everything here runs before the script touches Docker: a pack is checked in
# full first, so a malformed one is rejected without half-loading it into a
# stack. That ordering is the thing under test, as much as the individual
# messages are.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SEEDER="$SCRIPT_DIR/seed-nc-files.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# run_seeder <pack-dir> [extra args...] sets STATUS and OUT.
#
# Not a command substitution: that would run the seeder in a subshell and lose
# the exit status this whole file is asserting on.
STATUS=0
OUT=""
run_seeder() {
  set +e
  "$SEEDER" --pack "$@" >"$WORK/seeder.out" 2>&1
  STATUS=$?
  set -e
  OUT="$(cat "$WORK/seeder.out")"
}

expect_rejected() {
  local label="$1" needle="$2"
  (( STATUS != 0 )) || fail "$label: exited 0, expected a rejection. Output: $OUT"
  grep -q "$needle" <<<"$OUT" || fail "$label: expected '$needle' in output, got: $OUT"
}

make_pack() {
  local dir="$1" catalog="$2"
  mkdir -p "$dir/meetings"
  printf '%s' "$catalog" >"$dir/catalog.json"
}

# --- a directory that is not a pack ------------------------------------------

run_seeder "$WORK/absent"
expect_rejected "missing directory" "is not a directory"

mkdir -p "$WORK/empty"
run_seeder "$WORK/empty"
expect_rejected "no catalog" "holds no catalog.json"

# --- a catalog this seeder does not read -------------------------------------

make_pack "$WORK/wrongversion" '{"version":"cassini.viewer.catalog.v2","meetings":[]}'
run_seeder "$WORK/wrongversion"
expect_rejected "wrong version" "cassini.viewer.catalog.v1"

make_pack "$WORK/notjson" 'this is not json'
run_seeder "$WORK/notjson"
expect_rejected "not JSON" "not valid JSON"

make_pack "$WORK/nomeetings" '{"version":"cassini.viewer.catalog.v1","meetings":[]}'
run_seeder "$WORK/nomeetings"
expect_rejected "empty catalog" "lists no meetings"

# --- an index that does not match the files ----------------------------------
#
# The failure this prevents is the expensive one: a stack whose catalog
# advertises meetings that 404, which reads as a product bug rather than as an
# incomplete download.

make_pack "$WORK/missingasset" \
  '{"version":"cassini.viewer.catalog.v1","meetings":[{"id":"A","audioPath":"./meetings/A.opus"}]}'
run_seeder "$WORK/missingasset"
expect_rejected "asset not on disk" "which is not in the pack"

# --- a pack that tries to place files outside itself -------------------------
#
# A pack can arrive from anywhere, and this script runs cp with the paths its
# catalog names.

for hostile in "/etc/passwd" "../../escape.opus" "./meetings/../../escape.opus" "https://example.com/x.opus"; do
  dir="$WORK/hostile-$(printf '%s' "$hostile" | tr -c 'a-zA-Z0-9' '_')"
  make_pack "$dir" \
    "$(printf '{"version":"cassini.viewer.catalog.v1","meetings":[{"id":"A","audioPath":"%s"}]}' "$hostile")"
  run_seeder "$dir"
  expect_rejected "hostile audioPath $hostile" "not a path inside the pack"
done

# --- a well-formed pack gets past validation ---------------------------------
#
# It cannot be seeded here — that needs a running stack — so the assertion is
# that it fails for a stack reason and not a pack reason.

make_pack "$WORK/good" \
  '{"version":"cassini.viewer.catalog.v1","meetings":[{"id":"A","audioPath":"./meetings/A.opus"}]}'
printf 'opus' >"$WORK/good/meetings/A.opus"
run_seeder "$WORK/good" --dry-run
if (( STATUS == 0 )); then
  # A stack happens to be up and the pack validated and planned. Then the plan
  # must be the one this script promises.
  grep -q "Team folder id=" <<<"$OUT" || fail "valid pack: dry run printed no folder id: $OUT"
  grep -q "Nothing was changed" <<<"$OUT" || fail "valid pack: dry run did not say it changed nothing: $OUT"
else
  grep -qE "could not list Team folders|no '?Cassini'? Team folder|nextcloud container is not running|docker" <<<"$OUT" \
    || fail "valid pack was rejected for a pack reason: $OUT"
fi

# --- usage -------------------------------------------------------------------

set +e
"$SEEDER" >/dev/null 2>&1
status=$?
set -e
(( status != 0 )) || fail "running with no --pack exited 0"

set +e
"$SEEDER" --help >/dev/null 2>&1
status=$?
set -e
(( status == 0 )) || fail "--help exited $status"

echo "[test] seed-nc-files.sh pack validation OK"
