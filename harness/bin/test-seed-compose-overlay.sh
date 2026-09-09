#!/usr/bin/env bash
# Offline regression: the seed overlay is added when a pack is asked for, and
# is absent otherwise.
#
# The second half is the one that matters. Every existing e2e leg and every CI
# run brings a stack up with no seed, and this feature is only safe to land
# because those runs produce the compose invocation they always did. A regression
# here would change the topology of every test in the repo at once.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091 # SCRIPT_DIR is resolved dynamically above.
source "$SCRIPT_DIR/lib/base.sh"
# shellcheck disable=SC1091 # SCRIPT_DIR is resolved dynamically above.
source "$SCRIPT_DIR/lib/stack-env.sh"
# shellcheck disable=SC1091 # SCRIPT_DIR is resolved dynamically above.
source "$SCRIPT_DIR/lib/stack.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }

harness_stack_env_resolve
COMPOSE_FILE="$SCRIPT_DIR/../compose.yml"

# --- no seed asked for: no overlay -------------------------------------------

unset CASSINI_HARNESS_SEED_DIR
args="$(harness_seed_compose_args)"
[[ -z "$args" ]] || fail "an unseeded stack added compose arguments: $args"

export CASSINI_HARNESS_SEED_DIR=""
args="$(harness_seed_compose_args)"
[[ -z "$args" ]] || fail "an empty CASSINI_HARNESS_SEED_DIR added compose arguments: $args"

# --- a seed asked for: the overlay, once --------------------------------------

export CASSINI_HARNESS_SEED_DIR="/tmp/some-pack"
mapfile -t args < <(harness_seed_compose_args)
(( ${#args[@]} == 2 )) || fail "expected exactly '-f <overlay>', got: ${args[*]}"
[[ "${args[0]}" == "-f" ]] || fail "first argument is ${args[0]}, want -f"
[[ -f "${args[1]}" ]] || fail "overlay ${args[1]} does not exist"
[[ "${args[1]}" == */compose.seed.yml ]] || fail "overlay is ${args[1]}, want compose.seed.yml"

# --- the overlay says what it is supposed to ---------------------------------
#
# Read rather than rendered: `docker compose config` needs a daemon, and this
# file must stay runnable with nothing installed.

overlay="${args[1]}"
grep -q 'CASSINI_HARNESS_SEED_DIR' "$overlay" \
  || fail "the overlay does not reference CASSINI_HARNESS_SEED_DIR"
grep -q '/cassini-seed:ro' "$overlay" \
  || fail "the overlay does not mount the pack read-only at /cassini-seed"
if grep -q '__groupfolders' "$overlay"; then
  fail "the overlay binds Nextcloud's data directory; the seeder copies into it instead"
fi

echo "[test] seed compose overlay OK"
