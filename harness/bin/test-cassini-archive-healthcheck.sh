#!/usr/bin/env bash
#
# Regression test for ops/cassini-archive-healthcheck.sh.
#
# The healthcheck is the thing that was missing on 2026-07-31, when the old
# recording pipeline stopped with every unit green, and for the three weeks
# cassini-ingest-batch.service sat `failed` unnoticed. A detector nobody has
# ever seen fire is not a detector, so this test fires every assertion
# individually against a fixture that violates exactly that one thing, and
# pins the healthy baseline in between.
#
# Entirely offline and synthetic: a fake archive under mktemp, no george, no
# systemd required, no docker, and no sleeps — every "age" in the fixture is
# set explicitly with `touch -d` or written into the JSON.
#
# Run directly:
#   ./harness/bin/test-cassini-archive-healthcheck.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
HC="$PROJECT_ROOT/ops/cassini-archive-healthcheck.sh"

fail() {
  echo "FAIL: $*" >&2
  [[ -n "${LAST_OUTPUT:-}" ]] && { echo "--- last healthcheck output ---" >&2; echo "$LAST_OUTPUT" >&2; }
  exit 1
}

[[ -x "$HC" ]] || fail "ops/cassini-archive-healthcheck.sh is missing or not executable"
command -v jq >/dev/null 2>&1 || fail "this test needs jq"

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

MEETING_DIR_NAME="2026-08-28T103012Z--mczuc3mb--exapp--daily-standup-meeting"
JOB_ID="01M11CF8G9FAVXB5GW0Y94VCNF"

# ---------------------------------------------------------------------------
# fixture: a healthy archive, one meeting, one snapshot, one archived capture
# ---------------------------------------------------------------------------
FIX=""
mkfixture() {
  FIX="$(mktemp -d "$TMP_ROOT/fixture.XXXXXX")"
  local arch="$FIX/archive" snaps="$FIX/snapshots" var="$FIX/var" exa="$FIX/exapp/operator/jobs"
  mkdir -p "$arch/meetings/$MEETING_DIR_NAME/ARCHIVE" "$arch/state" "$arch/reports" \
           "$arch/by-job-id" "$arch/unattributed/derived" "$arch/meetings/.staging" \
           "$snaps" "$var" "$exa/current"

  echo '{"schema":"cassini.archive.meeting.v1"}' >"$arch/meetings/$MEETING_DIR_NAME/ARCHIVE/meeting.json"
  echo 'd41d8cd98f00b204e9800998ecf8427e  cassini.json' >"$arch/meetings/$MEETING_DIR_NAME/ARCHIVE/MANIFEST.sha256"

  local today; today="$(date -u +%F)"
  jq -nc --arg d "meetings/$MEETING_DIR_NAME" --arg a "${today}T10:30:12Z" --arg j "$JOB_ID" \
    '{schema:"cassini.archive.index.v1", dir:$d, era:"exapp", anchor_utc:$a, job_id:$j}' \
    >"$arch/index.jsonl"

  ln -s "../meetings/$MEETING_DIR_NAME" "$arch/by-job-id/$JOB_ID"

  # A snapshot taken a few minutes ago.
  mkdir -p "$snaps/$(date -u -d '-10 minutes' +%Y-%m-%dT%H%M%SZ)"

  # The capture that produced the meeting: promoted two days ago, archived.
  mkdir -p "$exa/current/$JOB_ID.run"
  echo '{"kind":"run","state":"ready"}' >"$exa/current/$JOB_ID.run/cassini.json"
  touch -d '2 days ago' "$exa/current/$JOB_ID.run"

  write_health
}

write_health() { # write_health [finished] [status] [assertions_json] [lag] [newest]
  local arch="$FIX/archive"
  local finished=${1:-$(date -u +%FT%TZ)}
  local status=${2:-ok}
  local assertions=${3:-[]}
  local lag=${4:-60}
  local newest=${5:-$(date -u +%F)T10:30:12Z}
  jq -n --arg f "$finished" --arg s "$status" --argjson a "$assertions" \
        --argjson lag "$lag" --arg n "$newest" \
    '{schema:"cassini.archive.health.v1", ingest_run_id:"20260828T000000Z-1",
      last_run_started_utc:$f, last_run_finished_utc:$f, last_run_status:$s,
      newest_capture_utc:$n, capture_lag_seconds:$lag, assertions_failed:$a,
      copy_mode:"reflink"}' >"$arch/state/health.json"
}

run_hc() { # run_hc [VAR=value ...] [--flag ...]  -> sets LAST_OUTPUT / LAST_RC
  local out rc=0 envs=() flags=() a
  for a in "$@"; do
    if [[ $a =~ ^[A-Za-z_][A-Za-z0-9_]*= ]]; then envs+=("$a"); else flags+=("$a"); fi
  done
  # Later env assignments win, so a caller can override any default below.
  out="$(env \
    CONFIG_FILE=/dev/null \
    ARCHIVE_ROOT="$FIX/archive" \
    SNAPSHOT_ROOT="$FIX/snapshots" \
    VAR_DIR="$FIX/var" \
    SRC_EXA="$FIX/exapp/operator/jobs" \
    MIN_FREE_BYTES=1 \
    WATCH_UNITS= \
    WATCH_TIMERS= \
    ${envs[@]+"${envs[@]}"} \
    "$HC" ${flags[@]+"${flags[@]}"} 2>&1)" || rc=$?
  LAST_OUTPUT="$out"
  LAST_RC="$rc"
}

expect_healthy() { # expect_healthy <what>
  run_hc "$@"
  [[ "$LAST_RC" -eq 0 ]] || fail "expected a healthy exit 0, got $LAST_RC"
  grep -q '^FAIL \[' <<<"$LAST_OUTPUT" && fail "healthy fixture still reported a failure"
  grep -q 'cassini-archive healthy' <<<"$LAST_OUTPUT" || fail "no healthy summary line"
  return 0
}

expect_fail() { # expect_fail <assertion-id> <description> [env...]
  local id=$1 what=$2
  shift 2
  run_hc "$@"
  [[ "$LAST_RC" -eq 3 ]] || fail "$what: expected exit 3, got $LAST_RC"
  grep -q "^FAIL \[$id\]" <<<"$LAST_OUTPUT" || fail "$what: expected FAIL [$id], got:"$'\n'"$LAST_OUTPUT"
  echo "  ok  [$id] fires on: $what"
}

# ---------------------------------------------------------------------------
# 0. the baseline must be green, or every assertion below proves nothing
# ---------------------------------------------------------------------------
mkfixture
expect_healthy
echo "  ok  baseline archive reports healthy"

# 1. A1 — no health file at all: the sync has never completed a run here.
mkfixture
rm -f "$FIX/archive/state/health.json"
expect_fail A1 "health.json missing"

# 2. A2 — the ingest silently stopped. This is the 2026-07-31 shape.
mkfixture
write_health "$(date -u -d '40 hours ago' +%FT%TZ)"
expect_fail A2 "no completed run for 40 h"
# ...and 20 h is still inside the 30 h budget, so a single skipped pass is quiet.
mkfixture
write_health "$(date -u -d '20 hours ago' +%FT%TZ)"
expect_healthy
echo "  ok  [A2] one missed pass (20 h) does NOT false-alarm"

# 3. A3 / A4 — the run itself reported trouble.
mkfixture
write_health "$(date -u +%FT%TZ)" degraded
expect_fail A3 "last run status degraded"
mkfixture
write_health "$(date -u +%FT%TZ)" ok '["byte conservation mismatch on daily-meeting-2026-04-15"]'
expect_fail A4 "the run raised its own assertion"

# 4. A5 — snapshots stopped, or never happened.
mkfixture
rm -rf "$FIX/snapshots"/*
mkdir -p "$FIX/snapshots/$(date -u -d '4 days ago' +%Y-%m-%dT%H%M%SZ)"
expect_fail A5 "newest snapshot 4 days old"
mkfixture
rm -rf "$FIX/snapshots"/*
expect_fail A5 "no snapshot at all"
# A pinned baseline snapshot taken minutes ago is recognised by name.
mkfixture
rm -rf "$FIX/snapshots"/*
mkdir -p "$FIX/snapshots/000-baseline--$(date -u +%Y-%m-%dT%H%M%SZ)"
expect_healthy
echo "  ok  [A5] 000-baseline--<stamp> counts as a snapshot"

# 5. A6 — the index drifted away from the tree, or a meeting lost its proof.
mkfixture
mkdir -p "$FIX/archive/meetings/2026-08-27T090000Z--mczuc3mb--cron--daily-meeting/ARCHIVE"
expect_fail A6 "a meeting directory with no index row"
mkfixture
rm -f "$FIX/archive/meetings/$MEETING_DIR_NAME/ARCHIVE/MANIFEST.sha256"
expect_fail A6 "a meeting with no MANIFEST.sha256"

# 6. A7 — something is sitting in a bucket that needs a human.
mkfixture
mkdir -p "$FIX/archive/meetings/.staging/half-copied.partial"
expect_fail A7 "a leftover .staging partial"
mkfixture
printf 'key\ta\tb\tduplicate-identity-key\t2026-08-28T00:00:00Z\n' >"$FIX/archive/state/conflicts.tsv"
expect_fail A7 "a conflicts.tsv row"
mkfixture
mkdir -p "$FIX/archive/unattributed/derived/orphan.meeting"
expect_fail A7 "an unattributable derived bundle"

# 7. A8 — a capture has been deferred for days and is STILL being deferred.
mkfixture
{
  printf '%s\t/exapp/current/X.run\tG3-stage\tfp1\n' "$(date -u -d '5 days ago' +%FT%TZ)"
  printf '%s\t/exapp/current/X.run\tG3-stage\tfp1\n' "$(date -u -d '1 hour ago' +%FT%TZ)"
} >"$FIX/archive/state/pending.tsv"
expect_fail A8 "a capture deferred for 5 days and still deferring"
# The control that makes A8 meaningful: an old deferral that stopped recurring
# means the bundle was ingested later. It must NOT alarm forever.
mkfixture
{
  printf '%s\t/exapp/current/X.run\tG3-stage\tfp1\n' "$(date -u -d '5 days ago' +%FT%TZ)"
  printf '%s\t/exapp/current/X.run\tG3-stage\tfp1\n' "$(date -u -d '4 days ago' +%FT%TZ)"
} >"$FIX/archive/state/pending.tsv"
expect_healthy
echo "  ok  [A8] a resolved old deferral does not alarm forever"

# 8. A9 — free space.
mkfixture
expect_fail A9 "free space under the floor" MIN_FREE_BYTES=999999999999999

# 9. A11 — a timer that is not scheduled. This is the failure mode OnFailure=
#    cannot catch, and the reason the watchdog is a separate unit.
if [[ -d /run/systemd/system ]] && command -v systemctl >/dev/null 2>&1; then
  mkfixture
  expect_fail A11 "a watched timer that does not exist" \
    WATCH_TIMERS=cassini-archive-definitely-not-installed.timer
else
  echo "  note: systemd not running here, skipping the A10/A11 unit-state checks"
fi

# 10. A12 — THE one that matters: a finished capture is sitting in the ExApp
#     volume and never reached the archive. Filesystem evidence only.
mkfixture
mkdir -p "$FIX/exapp/operator/jobs/current/01KYVVJHT3T2XKVH8BFQHGMYGT.run"
echo '{"kind":"run","state":"ready"}' >"$FIX/exapp/operator/jobs/current/01KYVVJHT3T2XKVH8BFQHGMYGT.run/cassini.json"
touch -d '2 days ago' "$FIX/exapp/operator/jobs/current/01KYVVJHT3T2XKVH8BFQHGMYGT.run"
expect_fail A12 "a ready capture two days old that is not in the archive"

# A capture promoted minutes ago is inside the grace window: not an alarm.
mkfixture
mkdir -p "$FIX/exapp/operator/jobs/current/01KYVVJHT3T2XKVH8BFQHGMYGT.run"
echo '{"kind":"run","state":"ready"}' >"$FIX/exapp/operator/jobs/current/01KYVVJHT3T2XKVH8BFQHGMYGT.run/cassini.json"
expect_healthy
echo "  ok  [A12] a just-promoted capture is inside the grace window"

# A bundle cassini has NOT marked ready is still being written: never ours.
mkfixture
mkdir -p "$FIX/exapp/operator/jobs/current/01KYVVJHT3T2XKVH8BFQHGMYGT.run"
echo '{"kind":"run","state":"preparing"}' >"$FIX/exapp/operator/jobs/current/01KYVVJHT3T2XKVH8BFQHGMYGT.run/cassini.json"
touch -d '2 days ago' "$FIX/exapp/operator/jobs/current/01KYVVJHT3T2XKVH8BFQHGMYGT.run"
expect_healthy
echo "  ok  [A12] a bundle still in state=preparing is not counted against the archive"

# The source volume being unreadable is itself a failure: the check went blind.
mkfixture
expect_fail A12 "the ExApp volume is not visible" SRC_EXA="$FIX/does-not-exist"

# 11. A13 — the ingest runs and exits 0 while the operator DB races ahead.
mkfixture
write_health "$(date -u +%FT%TZ)" ok '[]' 36000
expect_fail A13 "operator DB 10 h ahead of the archive"

# 12. A14 — upstream liveness. The 2026-08-03/04/05 loss was NO NEW RECORDINGS
#     with everything green, so this must fire on data, not on a unit state.
mkfixture
write_health "$(date -u +%FT%TZ)" ok '[]' 60 "$(date -u -d '12 days ago' +%F)T10:30:12Z"
expect_fail A14 "no capture for 12 days"
# ...and a declared holiday suppresses it, on the record, in config.
mkfixture
write_health "$(date -u +%FT%TZ)" ok '[]' 60 "$(date -u -d '12 days ago' +%F)T10:30:12Z"
expect_healthy EXPECTED_QUIET_UNTIL="$(date -u -d '+7 days' +%F)"
echo "  ok  [A14] EXPECTED_QUIET_UNTIL suppresses the quiet alarm deliberately"

# 13. A15 — an unacknowledged alarm outranks a green run. A later success must
#     NOT erase it; only an explicit --ack clears it.
mkfixture
printf 'unit cassini-archive-sync.service\n' >"$FIX/var/ALARM"
expect_fail A15 "an unacknowledged ALARM marker"

# 14. --json stays machine-readable in both states.
mkfixture
run_hc --json
jq -e '.schema == "cassini.archive.healthcheck.v1" and .status == "ok" and (.checks | length) >= 14' \
  <<<"$LAST_OUTPUT" >/dev/null || fail "--json output is not the expected healthy document"
mkfixture
printf 'unit x\n' >"$FIX/var/ALARM"
run_hc --json
[[ "$LAST_RC" -eq 3 ]] || fail "--json must still exit 3 when an assertion fails"
jq -e '.status == "failed" and .failed >= 1 and ([.checks[] | select(.status=="fail") | .id] | index("A15") != null)' \
  <<<"$LAST_OUTPUT" >/dev/null || fail "--json does not name the failed assertion"
echo "  ok  --json is machine-readable in both states"

echo "PASS: cassini archive healthcheck — every assertion fires on its own fixture and the healthy baseline stays green"
