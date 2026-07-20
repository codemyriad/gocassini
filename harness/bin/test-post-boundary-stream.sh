#!/usr/bin/env bash
# Hermetic regressions for leave/rejoin recorder-side phase evidence.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VERIFY="$SCRIPT_DIR/verify-post-boundary-stream.sh"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

expect_pass() {
  local label="$1"
  shift
  "$VERIFY" "$@" >/dev/null || fail "$label should pass"
}

expect_fail() {
  local label="$1"
  shift
  if "$VERIFY" "$@" >/dev/null 2>&1; then
    fail "$label should fail"
  fi
}

cat >"$TMP_DIR/pre-only.ndjson" <<'JSON'
{"type":"session_started","mono_ns":100}
{"type":"stream_opened","mono_ns":199,"kind":"audio","participant_id":"bot","participant_name":"RejoinBot1","remote_session_id":"same-session","stream_id":"s1"}
JSON
expect_fail "pre-boundary-only evidence" \
  --events "$TMP_DIR/pre-only.ndjson" --boundary-ns 200 --participant RejoinBot1

cat >"$TMP_DIR/post.ndjson" <<'JSON'
{"type":"stream_opened","mono_ns":199,"kind":"video","participant_id":"bot","participant_name":"RejoinBot1","remote_session_id":"same-session","stream_id":"s1"}
{"type":"stream_opened","mono_ns":201,"kind":"audio","participant_id":"bot","participant_name":"RejoinBot1","remote_session_id":"same-session","stream_id":"s2"}
JSON
expect_pass "post-boundary evidence with reused remote session id" \
  --events "$TMP_DIR/post.ndjson" --boundary-ns 200 \
  --participant UnknownDisplay --remote-session-id same-session
expect_pass "participant-id matching" \
  --events "$TMP_DIR/post.ndjson" --boundary-ns 200 --participant bot
expect_fail "wrong participant" \
  --events "$TMP_DIR/post.ndjson" --boundary-ns 200 --participant SomebodyElse
expect_fail "wrong participant and remote session" \
  --events "$TMP_DIR/post.ndjson" --boundary-ns 200 \
  --participant SomebodyElse --remote-session-id another-session

cat >"$TMP_DIR/wrong-kind.ndjson" <<'JSON'
{"type":"stream_opened","mono_ns":201,"kind":"data","participant_id":"bot","participant_name":"RejoinBot1","remote_session_id":"same-session"}
JSON
expect_fail "non-media stream" \
  --events "$TMP_DIR/wrong-kind.ndjson" --boundary-ns 200 --participant RejoinBot1

cat >"$TMP_DIR/malformed.ndjson" <<'JSON'
{"type":"stream_opened","mono_ns":201,"kind":"audio"}
not-json
JSON
expect_fail "malformed events log" \
  --events "$TMP_DIR/malformed.ndjson" --boundary-ns 200 --participant RejoinBot1
expect_fail "missing events log" \
  --events "$TMP_DIR/missing.ndjson" --boundary-ns 200 --participant RejoinBot1
expect_fail "invalid boundary" \
  --events "$TMP_DIR/post.ndjson" --boundary-ns nope --participant RejoinBot1

echo "PASS: post-boundary recorder evidence rejects stale, wrong, and malformed events"
