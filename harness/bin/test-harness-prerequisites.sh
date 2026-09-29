#!/usr/bin/env bash
# Offline contract for the single direct-share recordings model.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
fail() { echo "FAIL: $*" >&2; exit 1; }

grep -q 'harness_install_app spreed' "$SCRIPT_DIR/bootstrap.sh" \
  || fail "Talk must be installed for recording tests"
if grep -Eq 'harness_install_app (groupfolders|group_everyone)|groupfolders:(create|group|permissions)' "$SCRIPT_DIR/bootstrap.sh"; then
  fail "recording bootstrap still creates a Team folder"
fi
if grep -Eq 'CASSINI_STORAGE_MODE|harness_storage_mode_is_acl' "$SCRIPT_DIR/lib/stack.sh"; then
  fail "stack still selects a storage mode"
fi
if grep -Eq 'app:(install|enable) (groupfolders|group_everyone)|CASSINI_STORAGE_MODE' "$ROOT/sandbox/wire-cassini.sh"; then
  fail "sandbox still requires a recording permission app or mode"
fi
if grep -q '<name>CASSINI_STORAGE_MODE</name>' "$ROOT/appinfo/info.xml"; then
  fail "manifest still declares a storage mode"
fi
grep -q 'ncRecordingsRoot = "CassiniRecordings"' "$ROOT/cassini-operator/internal/operator/nc_storage_paths.go" \
  || fail "private recordings root changed"
echo "PASS: recordings require core Files shares and one private owner account"
