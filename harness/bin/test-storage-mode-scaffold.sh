#!/usr/bin/env bash
# Offline regression for the harness's storage-mode predicates.
#
# These two decide what a stack is built with — whether the `cassini` service
# account and the Team folder appear at all, and whether the folder is mapped.
# They are one-line boolean tests, which is exactly the shape that inverts
# silently: nothing downstream fails loudly when a stack quietly comes up in the
# other model, it just produces a `mode_mismatch` an hour later in an e2e log.
#
# The defaults are the load-bearing part. The harness has always built the
# access-controlled substrate and every e2e suite asserts it, so an absent or
# empty variable must keep meaning that.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091 # SCRIPT_DIR is resolved dynamically above.
source "$SCRIPT_DIR/lib/stack.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }

# --- the scaffold switch: off unless explicitly "1" ---------------------------
#
# Anything looser would let an ambient "0", "false" or a stray empty value strip
# a stack of its storage, which looks identical to a broken bootstrap.

unset CASSINI_HARNESS_SKIP_STORAGE_SCAFFOLD
harness_skip_storage_scaffold && fail "the scaffold was skipped with the variable unset"

for value in "" "0" "false" "yes" "true"; do
  export CASSINI_HARNESS_SKIP_STORAGE_SCAFFOLD="$value"
  if harness_skip_storage_scaffold; then
    fail "CASSINI_HARNESS_SKIP_STORAGE_SCAFFOLD=$value skipped the scaffold; only 1 may"
  fi
done

export CASSINI_HARNESS_SKIP_STORAGE_SCAFFOLD=1
harness_skip_storage_scaffold || fail "CASSINI_HARNESS_SKIP_STORAGE_SCAFFOLD=1 did not skip the scaffold"
unset CASSINI_HARNESS_SKIP_STORAGE_SCAFFOLD

# --- the storage mode: access-controlled unless it says default ---------------
#
# Defaulting the other way would silently stop building the substrate every e2e
# suite asserts, and the first symptom would be a 503 much later.

unset CASSINI_HARNESS_STORAGE_MODE
harness_storage_mode_is_acl || fail "an unset storage mode did not mean access-controlled"

export CASSINI_HARNESS_STORAGE_MODE=""
harness_storage_mode_is_acl || fail "an empty storage mode did not mean access-controlled"

export CASSINI_HARNESS_STORAGE_MODE=acl-enabled
harness_storage_mode_is_acl || fail "acl-enabled did not mean access-controlled"

export CASSINI_HARNESS_STORAGE_MODE=default
if harness_storage_mode_is_acl; then
  fail "default was treated as access-controlled — a mapped Team folder would win the Cassini path"
fi
unset CASSINI_HARNESS_STORAGE_MODE

# --- what the ExApp is told ---------------------------------------------------
#
# The mode the app starts in has to follow the mode the stack is built for. A
# stack built default while the app boots access-controlled is the
# `mode_mismatch` state: publishing refused, nothing naming the cause.

expect_exapp_mode() {
  local want="$1" got
  got="$(harness_exapp_storage_mode)"
  [[ "$got" == "$want" ]] || fail "ExApp mode was $got, wanted $want (harness mode=${CASSINI_HARNESS_STORAGE_MODE-unset}, override=${CASSINI_STORAGE_MODE-unset})"
}

unset CASSINI_HARNESS_STORAGE_MODE CASSINI_STORAGE_MODE
expect_exapp_mode access_controlled

export CASSINI_HARNESS_STORAGE_MODE=acl-enabled
expect_exapp_mode access_controlled

export CASSINI_HARNESS_STORAGE_MODE=default
expect_exapp_mode default

# An explicit override still wins — that is how a deliberate mismatch is tested.
export CASSINI_STORAGE_MODE=access_controlled
expect_exapp_mode access_controlled
unset CASSINI_HARNESS_STORAGE_MODE CASSINI_STORAGE_MODE

# --- the two compose ----------------------------------------------------------
#
# Skipping the scaffold does not change which mode the ExApp is told to start
# in. That combination is the point of the debug flag: access control selected
# with nothing built is the state the app's own setup flow exists to fix.

export CASSINI_HARNESS_SKIP_STORAGE_SCAFFOLD=1
export CASSINI_HARNESS_STORAGE_MODE=acl-enabled
harness_skip_storage_scaffold || fail "skip flag lost when combined with a mode"
harness_storage_mode_is_acl || fail "mode lost when combined with the skip flag"
unset CASSINI_HARNESS_SKIP_STORAGE_SCAFFOLD CASSINI_HARNESS_STORAGE_MODE

# --- the registration contract ------------------------------------------------
#
# The predicates above decide what gets BUILT. This section pins how the choice
# reaches the ExApp, because the two failing to line up is invisible until an
# e2e reports `mode_mismatch` an hour later.

ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
MANIFEST="$ROOT/appinfo/info.xml"
STACK_LIB="$SCRIPT_DIR/lib/stack.sh"
SANDBOX_WIRE="$ROOT/sandbox/wire-cassini.sh"

# Declared exactly once. AppAPI injects deploy options at container creation and
# silently drops undeclared keys, so a duplicate or a typo here is a variable
# that never arrives.
declared="$(grep -c '<name>CASSINI_STORAGE_MODE</name>' "$MANIFEST" || true)"
[[ "$declared" == "1" ]] \
  || fail "appinfo/info.xml declares CASSINI_STORAGE_MODE $declared times, want exactly 1"

# The harness passes it through registration, and derives it from the same
# predicate that decides what is built rather than hard-coding a second default.
# shellcheck disable=SC2016 # the literal `$(...)` IS the pattern being matched.
grep -qF -- '--env "CASSINI_STORAGE_MODE=$(harness_exapp_storage_mode)"' "$STACK_LIB" \
  || fail "lib/stack.sh does not pass CASSINI_STORAGE_MODE through AppAPI registration from harness_exapp_storage_mode"

# The dogfood box states its mode rather than letting it fall back. Switching it
# to the default model would move a real archive and make every recording
# readable by every account on that instance, so it must never happen by
# omission.
# shellcheck disable=SC2016 # the literal `${...}` IS the pattern being matched.
grep -qF 'CASSINI_STORAGE_MODE=${CASSINI_STORAGE_MODE:-access_controlled}' "$SANDBOX_WIRE" \
  || fail "sandbox/wire-cassini.sh does not declare access_controlled; the dogfood archive must not depend on a fallback"

echo "PASS: storage-mode predicates default to the substrate the e2e suites assert, and reach the ExApp intact"
