#!/usr/bin/env bash
# Offline lifecycle contract for resuming stopped containers or retained data
# volumes. Docker calls are stubbed so this runs in the fast test suite.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091 # SCRIPT_DIR is resolved dynamically above.
source "$SCRIPT_DIR/lib/stack.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT
COMPOSE_CALLS="$TMP_DIR/compose-calls"

TEST_DESIRED=""
TEST_EXISTING=""
TEST_RUNNING=""
TEST_PROJECT_VOLUMES=""
TEST_EXAPP_VOLUMES=""

emit_test_lines() {
  local lines="$1"
  if [[ -n "$lines" ]]; then
    printf '%s\n' "$lines"
  fi
}

# Replace Docker-backed discovery and Compose execution with deterministic
# fixtures. The production functions under test resolve these names at runtime.
harness_desired_compose_services() { emit_test_lines "$TEST_DESIRED"; }
harness_existing_compose_services() { emit_test_lines "$TEST_EXISTING"; }
harness_running_compose_services() { emit_test_lines "$TEST_RUNNING"; }
harness_project_volumes() { emit_test_lines "$TEST_PROJECT_VOLUMES"; }
harness_installed_exapp_volumes() { emit_test_lines "$TEST_EXAPP_VOLUMES"; }
compose() { printf '%s\n' "$*" >>"$COMPOSE_CALLS"; }

export PROJECT_NAME=resume-contract
export SPREED_PROFILE=default
export CASSINI_HARNESS_SERVICE_MODE=core
export CASSINI_HARNESS_EXISTING=resume
export CASSINI_HARNESS_CASSINI_MODE=none
TEST_DESIRED=$'db\nnextcloud'

assert_compose_call() {
  local expected="$1"
  local actual=""
  if [[ -f "$COMPOSE_CALLS" ]]; then
    actual="$(<"$COMPOSE_CALLS")"
  fi
  [[ "$actual" == "$expected" ]] \
    || fail "Compose call was '$actual', expected '$expected'"
}

# `down --suspend` leaves a complete stopped service set. Resume must preserve
# the old behavior and start those exact containers without recreating them.
TEST_EXISTING=$'db\nnextcloud'
TEST_PROJECT_VOLUMES=""
harness_validate_resume_resources \
  || fail "matching stopped containers were rejected"
: >"$COMPOSE_CALLS"
harness_start_compose_stack
assert_compose_call "start db nextcloud"

# Bare `down` removes containers and the network while retaining named volumes.
# Resume must recreate the resolved containers with `compose up`, which mounts
# those existing project volumes by their stable Compose names.
TEST_EXISTING=""
TEST_PROJECT_VOLUMES="resume-contract_nextcloud_data"
harness_validate_resume_resources \
  || fail "retained Compose volume was rejected"
: >"$COMPOSE_CALLS"
harness_start_compose_stack
assert_compose_call "up -d db nextcloud"

# Installed-ExApp data is harness state too. Accept its retained volume even if
# no Compose project volume survives (for example after manual Docker cleanup).
TEST_PROJECT_VOLUMES=""
TEST_EXAPP_VOLUMES="nc_app_gocassini_data"
export CASSINI_HARNESS_CASSINI_MODE=installed-exapp
harness_validate_resume_resources \
  || fail "retained installed-ExApp volume was rejected"

# `--resume` still requires something to resume. With neither stopped
# containers nor retained volumes, plain `stack up` is the correct operation.
TEST_EXAPP_VOLUMES=""
export CASSINI_HARNESS_CASSINI_MODE=none
if output="$(harness_validate_resume_resources 2>&1)"; then
  fail "empty project was accepted for resume"
fi
[[ "$output" == *"No stopped compose containers or retained data volumes"* ]] \
  || fail "empty-project error did not explain the resume requirement: $output"

# A partial stopped stack remains unsafe: unlike the volume-only case, its
# surviving containers encode a topology that must exactly match the plan.
TEST_EXISTING="db"
if output="$(harness_validate_resume_resources 2>&1)"; then
  fail "partial stopped service set was accepted"
fi
[[ "$output" == *"Missing services:"* && "$output" == *"nextcloud"* ]] \
  || fail "service mismatch was not reported: $output"

echo "PASS: stack resume accepts retained volumes and preserves stopped-container checks"
