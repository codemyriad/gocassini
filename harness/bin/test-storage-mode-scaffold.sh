#!/usr/bin/env bash
# Offline regression for the harness's storage-mode predicates.
#
# These two decide what a stack is built with — whether the `cassini` service
# account and the Team folder appear at all, and whether the folder is mapped.
# They are one-line boolean tests, which is exactly the shape that inverts
# silently: nothing downstream fails loudly when a stack quietly comes up in the
# other model, it just produces a missing Team folder an hour later in an e2e log.
#
# The defaults are the load-bearing part. The harness has always built the
# access-controlled substrate and every e2e suite asserts it, so an absent or
# empty variable must keep meaning that.
#
# Since D-708 there is a third shape, `undecided`: build that same substrate and
# tell the ExApp NOTHING, so it starts with no storage mode chosen. Nothing falls
# back any more — an app that has not been told does not publish — and every
# other harness shape declares a mode precisely to skip that state, which left
# the setup wizard unreachable from the harness at all.

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
  fail "default was treated as access-controlled — the stack would be built for a mode the app is not in"
fi

# `undecided` is about what the ExApp is TOLD, not about what exists. It builds
# the access-controlled substrate, because a wizard with only one usable mode is
# not offering a choice.
export CASSINI_HARNESS_STORAGE_MODE=undecided
harness_storage_mode_is_acl || fail "undecided did not build the access-controlled substrate; the wizard would have only one usable mode"
harness_storage_mode_is_undecided || fail "undecided was not recognised"

for value in "" "acl-enabled" "default"; do
  export CASSINI_HARNESS_STORAGE_MODE="$value"
  if harness_storage_mode_is_undecided; then
    fail "CASSINI_HARNESS_STORAGE_MODE=$value was treated as undecided; only the literal word may be"
  fi
done
unset CASSINI_HARNESS_STORAGE_MODE

# --- what the ExApp is told ---------------------------------------------------
#
# The mode the app starts in has to follow the mode the stack is built for. A
# stack built default while the app boots access-controlled is an instance whose
# Team folder is missing: publishing refused, with the missing prerequisite named
# but nothing saying the two flags disagreed.

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

# `undecided` declares NOTHING. The empty answer is what makes the caller omit
# the deploy option entirely, which is the only way to reach the state the setup
# wizard exists for.
export CASSINI_HARNESS_STORAGE_MODE=undecided
expect_exapp_mode ""

# An explicit override still wins — that is how a deliberate mismatch is tested,
# and it overrides `undecided` too.
export CASSINI_STORAGE_MODE=access_controlled
expect_exapp_mode access_controlled
export CASSINI_HARNESS_STORAGE_MODE=acl-enabled
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
# e2e reports a missing prerequisite an hour later.

ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
MANIFEST="$ROOT/appinfo/info.xml"
STACK_LIB="$SCRIPT_DIR/lib/stack.sh"
SANDBOX_WIRE="$ROOT/sandbox/wire-cassini.sh"
INSTALL_E2E="$SCRIPT_DIR/ci-e2e-install-exapp.sh"
PUBLISH_WORKFLOW="$ROOT/.github/workflows/publish-exapp-image.yml"

# Declared exactly once. AppAPI injects deploy options at container creation and
# silently drops undeclared keys, so a duplicate or a typo here is a variable
# that never arrives.
declared="$(grep -c '<name>CASSINI_STORAGE_MODE</name>' "$MANIFEST" || true)"
[[ "$declared" == "1" ]] \
  || fail "appinfo/info.xml declares CASSINI_STORAGE_MODE $declared times, want exactly 1"

# The harness passes it through registration, derives it from the same predicate
# that decides what is built rather than hard-coding a second default, and OMITS
# it when that predicate answers empty. The omission is the `undecided` shape;
# passing `CASSINI_STORAGE_MODE=` instead would declare an unrecognised value and
# the app would log an error rather than simply not having been told.
# shellcheck disable=SC2016 # the literal `$(...)` IS the pattern being matched.
grep -qF -- 'exapp_storage_mode="$(harness_exapp_storage_mode)"' "$STACK_LIB" \
  || fail "lib/stack.sh does not derive the ExApp's mode from harness_exapp_storage_mode"
# shellcheck disable=SC2016 # the literal `$...` IS the pattern being matched.
grep -qF -- 'register_args+=(--env "CASSINI_STORAGE_MODE=$exapp_storage_mode")' "$STACK_LIB" \
  || fail "lib/stack.sh does not pass CASSINI_STORAGE_MODE through AppAPI registration"
# shellcheck disable=SC2016 # the literal `$...` IS the pattern being matched.
grep -qF -- 'if [[ -n "$exapp_storage_mode" ]]; then' "$STACK_LIB" \
  || fail "lib/stack.sh declares CASSINI_STORAGE_MODE unconditionally; the undecided shape needs it omitted"

# The dogfood box states its mode rather than letting it fall back. Switching it
# to the default model would move a real archive and make every recording
# readable by every account on that instance, so it must never happen by
# omission.
# shellcheck disable=SC2016 # the literal `${...}` IS the pattern being matched.
grep -qF 'CASSINI_STORAGE_MODE=${CASSINI_STORAGE_MODE:-access_controlled}' "$SANDBOX_WIRE" \
  || fail "sandbox/wire-cassini.sh does not declare access_controlled; the dogfood archive must not depend on a fallback"

# The manual-install acceptance test must pin the same model in both places:
# stack bootstrap constructs the topology and the manually started container
# records it. It runs the default leg independently so the access-controlled
# probes cannot pass while the private-root model quietly regresses.
grep -qF 'STORAGE_MODE="${CASSINI_E2E_STORAGE_MODE:-access_controlled}"' "$INSTALL_E2E" \
  || fail "ci-e2e-install-exapp.sh does not make its storage-model leg explicit"
grep -qF -- '--storage-mode "$HARNESS_STORAGE_MODE"' "$INSTALL_E2E" \
  || fail "ci-e2e-install-exapp.sh does not pass its model to stack bootstrap"
grep -qF -- '-e "CASSINI_STORAGE_MODE=$STORAGE_MODE"' "$INSTALL_E2E" \
  || fail "ci-e2e-install-exapp.sh does not pass its model to the manually started ExApp"
grep -qF 'CASSINI_E2E_STORAGE_MODE: default' "$PUBLISH_WORKFLOW" \
  || fail "publish-exapp-image.yml does not run the manual-install default-mode leg"

# The deploy option is documented as development and CI only (D-708). It cannot
# be removed from the manifest — AppAPI silently drops undeclared keys, so the
# harness's own --env would stop arriving — so the demotion is copy, and copy
# that CI does not check is copy that rots.
grep -qi 'development' "$MANIFEST" \
  || fail "appinfo/info.xml no longer says CASSINI_STORAGE_MODE is a development/CI option"

echo "PASS: storage-mode predicates default to the substrate the e2e suites assert, and reach the ExApp intact"
