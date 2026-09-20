#!/usr/bin/env bash
#
# Regression test for ops/cassini-archive-alert.sh — the OnFailure= handler.
#
# george has no mail, no metrics exporter and no messaging credentials, so this
# handler IS the notification system. Two properties matter more than anything
# it sends:
#
#   1. the DURABLE half always happens — the ALARM marker, the detail report,
#      the journal record and the `failed` stamp on the off-host heartbeat —
#      because that is what survives nobody reading a notification;
#   2. an unconfigured or broken notifier NEVER fails the unit. A handler that
#      goes red because a webhook is missing teaches everyone to ignore red.
#
# Hermetic and offline: logger/wall/systemctl/journalctl are stubbed onto PATH
# so the test cannot spam the real journal or wall(1) the developer's terminal,
# and the only network call is to 127.0.0.1:1, which refuses instantly.
#
# Run directly:
#   ./harness/bin/test-cassini-archive-alert.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
ALERT="$PROJECT_ROOT/ops/cassini-archive-alert.sh"

fail() {
  echo "FAIL: $*" >&2
  [[ -n "${LAST_OUTPUT:-}" ]] && { echo "--- last alert output ---" >&2; echo "$LAST_OUTPUT" >&2; }
  exit 1
}

[[ -x "$ALERT" ]] || fail "ops/cassini-archive-alert.sh is missing or not executable"
command -v jq >/dev/null 2>&1 || fail "this test needs jq"

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

# --- stubs: nothing here may touch the real journal, wall or systemd ---------
STUB="$TMP_ROOT/stubbin"
mkdir -p "$STUB"
# Mimics real logger: it reads stdin ONLY when no message operand is given.
# A stub that always `cat`s would block forever on an inherited stdin.
cat >"$STUB/logger" <<EOF
#!/bin/sh
msg=""
while [ \$# -gt 0 ]; do
  case "\$1" in
    -p|-t) shift 2 ;;
    -*) shift ;;
    *) msg="\$msg \$1"; shift ;;
  esac
done
if [ -n "\$msg" ]; then echo "\$msg" >> "$TMP_ROOT/logger.log"
else cat >> "$TMP_ROOT/logger.log"; fi
EOF
cat >"$STUB/wall" <<EOF
#!/bin/sh
cat >> "$TMP_ROOT/wall.log"
EOF
cat >"$STUB/systemctl" <<'EOF'
#!/bin/sh
# `systemctl show <unit> -p <Prop> --value` -> a plausible value for a failure
case "$*" in
  *Result*)      echo exit-code ;;
  *ExecMainStatus*) echo 5 ;;
  *ExecMainCode*)   echo 1 ;;
  *InvocationID*)   echo "" ;;
  *Description*)    echo "Ingest new Cassini captures" ;;
  *) echo "" ;;
esac
EOF
cat >"$STUB/journalctl" <<'EOF'
#!/bin/sh
echo "assert.failed detail=\"byte conservation mismatch\""
EOF
chmod +x "$STUB"/*

FIX=""
mkfixture() {
  FIX="$(mktemp -d "$TMP_ROOT/fixture.XXXXXX")"
  mkdir -p "$FIX/var" "$FIX/archive/state" "$FIX/heartbeat"
  jq -n '{schema:"cassini.archive.health.v1", last_run_status:"ok",
          last_run_finished_utc:"2026-08-28T09:00:00Z", meetings_total:196}' \
    >"$FIX/archive/state/health.json"
  : >"$TMP_ROOT/logger.log"
  : >"$TMP_ROOT/wall.log"
}

run_alert() { # run_alert [VAR=value ...] -> LAST_OUTPUT / LAST_RC
  local out rc=0
  out="$(env PATH="$STUB:$PATH" \
    CONFIG_FILE=/dev/null \
    ALERT_CONFIG_FILE=/dev/null \
    ARCHIVE_ROOT="$FIX/archive" \
    VAR_DIR="$FIX/var" \
    HEARTBEAT_DIR="$FIX/heartbeat" \
    HEARTBEAT_FILE="$FIX/heartbeat/cassini-archive-health.json" \
    ALERT_HOSTNAME=test-host \
    ALERT_WALL=0 \
    "$@" \
    "$ALERT" cassini-archive-sync.service </dev/null 2>&1)" || rc=$?
  LAST_OUTPUT="$out"
  LAST_RC="$rc"
}

assert_durable_half() { # assert_durable_half <what>
  local what=$1
  [[ -f "$FIX/var/ALARM" ]] || fail "$what: no durable ALARM marker was written"
  grep -q 'cassini-archive-sync.service' "$FIX/var/ALARM" \
    || fail "$what: the ALARM marker does not name the failing unit"
  grep -q 'cassini-archive-sync --apply --ack' "$FIX/var/ALARM" \
    || fail "$what: the ALARM marker does not say how to clear itself"
  local reports
  reports=$(find "$FIX/var/alarms" -name '*cassini-archive-sync.service.txt' | wc -l)
  [[ "$reports" -ge 1 ]] || fail "$what: no detail report under alarms/"
  grep -q 'cassini-archive FAILURE on test-host' "$TMP_ROOT/logger.log" \
    || fail "$what: nothing was written to the journal"
  # The failing unit's own journal must be quoted into the report: without it
  # the alarm says "something failed" and the operator still has to go digging.
  grep -q 'byte conservation mismatch' "$FIX/var/ALARM" \
    || fail "$what: the failing unit's journal was not captured into the alarm"
}

# ---------------------------------------------------------------------------
# 1. No notifier configured at all — the normal state on george today.
# ---------------------------------------------------------------------------
mkfixture
run_alert
[[ "$LAST_RC" -eq 0 ]] || fail "an unconfigured notifier must not fail the unit (exit $LAST_RC)"
assert_durable_half "unconfigured"
grep -q 'notifier=unconfigured' "$TMP_ROOT/logger.log" \
  || fail "the handler did not record that no notifier is configured"
echo "  ok  unconfigured notifier: durable marker written, unit still succeeds"

# 2. The heartbeat and health file carry the failure, so the off-host copy does
#    too after the next 03:30 restic run.
jq -e '.last_run_status == "failed" and .alarm_unit == "cassini-archive-sync.service"' \
  "$FIX/archive/state/health.json" >/dev/null \
  || fail "health.json was not stamped with the failure"
jq -e '.last_run_status == "failed"' "$FIX/heartbeat/cassini-archive-health.json" >/dev/null \
  || fail "the off-host heartbeat was not stamped with the failure — the only channel that survives george being dead"
echo "  ok  health.json and the off-host heartbeat both carry last_run_status=failed"

# 3. A webhook that cannot be reached must not fail the unit.
mkfixture
run_alert ALERT_WEBHOOK_URL=http://127.0.0.1:1/nope
[[ "$LAST_RC" -eq 0 ]] || fail "an unreachable webhook must not fail the unit (exit $LAST_RC)"
assert_durable_half "unreachable webhook"
grep -q 'webhook FAILED' "$TMP_ROOT/logger.log" \
  || fail "an unreachable webhook was not reported in the journal"
echo "  ok  unreachable webhook: logged, marker intact, unit still succeeds"

# 4. A notifier command that exits non-zero must not fail the unit either.
mkfixture
run_alert ALERT_COMMAND=false
[[ "$LAST_RC" -eq 0 ]] || fail "a failing ALERT_COMMAND must not fail the unit (exit $LAST_RC)"
assert_durable_half "failing ALERT_COMMAND"
grep -q 'command FAILED' "$TMP_ROOT/logger.log" || fail "a failing ALERT_COMMAND was not reported"
echo "  ok  failing ALERT_COMMAND: logged, marker intact, unit still succeeds"

# 5. A working notifier actually receives the report on stdin.
mkfixture
run_alert ALERT_COMMAND="cat > $FIX/notified.txt"
[[ "$LAST_RC" -eq 0 ]] || fail "a working ALERT_COMMAND should exit 0 (exit $LAST_RC)"
[[ -f "$FIX/notified.txt" ]] || fail "ALERT_COMMAND received nothing"
grep -q 'cassini-archive-sync.service' "$FIX/notified.txt" \
  || fail "the notifier payload does not name the failing unit"
grep -q 'command delivered' "$TMP_ROOT/logger.log" || fail "a successful delivery was not logged"
echo "  ok  configured ALERT_COMMAND receives the full report on stdin"

# 6. Alarms LATCH: a second failure appends, it does not overwrite the first.
mkfixture
run_alert
first_len=$(wc -l <"$FIX/var/ALARM")
run_alert
second_len=$(wc -l <"$FIX/var/ALARM")
[[ "$second_len" -gt "$first_len" ]] \
  || fail "a second failure overwrote the first alarm instead of appending to it"
[[ $(find "$FIX/var/alarms" -type f | wc -l) -ge 2 ]] || fail "the second failure left no detail report"
echo "  ok  a second failure appends to the latched marker"

# 7. The marker is what cassini-archive-sync --ack looks for, and what the
#    healthcheck fails on. Pin the exact path so those three cannot drift apart.
[[ -f "$FIX/var/ALARM" ]] || fail "the marker must be exactly \$VAR_DIR/ALARM"
# shellcheck disable=SC2016  # the literal source text is the point
grep -q 'ALARM_FILE="$VAR_DIR/ALARM"' "$PROJECT_ROOT/ops/cassini-archive-healthcheck.sh" \
  || fail "the healthcheck no longer looks for \$VAR_DIR/ALARM"
# shellcheck disable=SC2016
grep -q 'ALARM_FILE="$VAR_DIR/ALARM"' "$PROJECT_ROOT/ops/cassini-archive-sync.sh" \
  || fail "cassini-archive-sync no longer looks for \$VAR_DIR/ALARM"
echo "  ok  the alert, the healthcheck and the sync all agree on \$VAR_DIR/ALARM"

echo "PASS: cassini archive alert — the durable half always happens and no notifier failure can fail the unit"
