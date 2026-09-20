#!/usr/bin/env bash
#
# cassini-archive-healthcheck — assert that the Cassini meeting archive is
# fresh, internally consistent, and still being fed.
#
# WHY THIS EXISTS
#   Two real incidents on this host, neither of which anything noticed:
#     * 2026-07-31 13:01 — the old recording pipeline simply stopped. Every
#       unit was green. Three business days of meetings are unrecoverable.
#     * cassini-ingest-batch.service in CT 112 sat `failed` for three weeks.
#   OnFailure= cannot fire for a unit that was disabled, masked or never
#   scheduled, and a healthy exit code says nothing about whether new meetings
#   are actually arriving. So this is a SEPARATE unit on a SEPARATE schedule
#   that checks outcomes, not exit codes: is the archive fresh, does it match
#   its own index, is there a finished capture sitting in the ExApp volume that
#   never made it in, and are the units that feed it still scheduled and green.
#
# HOW TO RUN
#   sudo /usr/local/sbin/cassini-archive-healthcheck            # human output
#   sudo /usr/local/sbin/cassini-archive-healthcheck --json     # machine output
#   sudo /usr/local/sbin/cassini-archive-healthcheck --quiet    # failures only
#   Run twice a day by cassini-archive-healthcheck.timer (07:30, 19:30 local).
#   Read-only: it writes nothing, anywhere. Root is not required, but the
#   ExApp-volume check needs read access to the operator volume.
#
# EXIT CODES
#   0  every assertion passed
#   2  usage, or the environment cannot support the checks (e.g. no jq)
#   3  one or more assertions failed — the message names which and why
#
# CONFIG  /etc/default/cassini-archive (shared with cassini-archive-sync). Every
#         threshold below is overridable from there or from the environment.

set -euo pipefail

CONFIG_FILE=${CONFIG_FILE:-/etc/default/cassini-archive}
# shellcheck disable=SC1090
[ -r "$CONFIG_FILE" ] && . "$CONFIG_FILE"

ARCHIVE_ROOT=${ARCHIVE_ROOT:-/mnt/data/cassini-archive}
SNAPSHOT_ROOT=${SNAPSHOT_ROOT:-/mnt/data/cassini-archive-snapshots}
VAR_DIR=${VAR_DIR:-/var/lib/cassini-archive}
SRC_EXA_DATA=${SRC_EXA_DATA:-/mnt/data/cassini-exapp/docker/volumes/nc_app_gocassini_data/_data}
SRC_EXA=${SRC_EXA:-$SRC_EXA_DATA/operator/jobs}

MEETINGS_DIR="$ARCHIVE_ROOT/meetings"
STAGING_DIR="$MEETINGS_DIR/.staging"
STATE_DIR="$ARCHIVE_ROOT/state"
REPORTS_DIR="$ARCHIVE_ROOT/reports"
INDEX_JSONL="$ARCHIVE_ROOT/index.jsonl"
HEALTH_JSON="$STATE_DIR/health.json"
BYJOB_DIR="$ARCHIVE_ROOT/by-job-id"
ALARM_FILE="$VAR_DIR/ALARM"

# Thresholds. The ingest runs 3x/day and the snapshot nightly, so 30 h is ~3
# missed passes: late enough not to false-alarm on one skipped run, early enough
# that a stopped pipeline is caught the next morning.
MAX_RUN_AGE_S=${MAX_RUN_AGE_S:-108000}          # 30 h
MAX_SNAPSHOT_AGE_S=${MAX_SNAPSHOT_AGE_S:-108000}
MAX_PENDING_AGE_S=${MAX_PENDING_AGE_S:-172800}  # 48 h — "build has been stuck for two days"
MAX_CAPTURE_LAG_S=${MAX_CAPTURE_LAG_S:-28800}   # 8 h — the operator DB is ahead of the archive
INGEST_GRACE_S=${INGEST_GRACE_S:-21600}         # 6 h — how long a finished .run may sit unarchived
MIN_FREE_BYTES=${MIN_FREE_BYTES:-214748364800}  # 200 GiB
MAX_QUIET_WEEKDAYS=${MAX_QUIET_WEEKDAYS:-3}
EXPECTED_QUIET_UNTIL=${EXPECTED_QUIET_UNTIL:-}

# Units whose failure means the archive is not being maintained. Seeded with
# `${VAR-default}` rather than `${VAR:-default}`, so setting either list to the
# empty string genuinely disables that check instead of silently restoring the
# default. Seeded with cassini-exapp-backup.service on purpose: an unnoticed three-week failure of
# exactly that class of unit is why this file exists.
WATCH_UNITS=${WATCH_UNITS-cassini-archive-sync.service cassini-archive-snapshot.service cassini-archive-healthcheck.service cassini-archive-backfill.service cassini-exapp-backup.service}
# Timers that must be ACTIVE. A failed unit is loud; a quietly disabled timer is
# not, and is the more dangerous of the two.
WATCH_TIMERS=${WATCH_TIMERS-cassini-archive-sync.timer cassini-archive-snapshot.timer cassini-archive-healthcheck.timer cassini-exapp-backup.timer}

JSON=0
QUIET=0
while [ $# -gt 0 ]; do
  case "$1" in
    --json) JSON=1 ;;
    --quiet|-q) QUIET=1 ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "$0" | sed -e 's/^# \{0,1\}//' -e '/^set -euo/d'; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

command -v jq >/dev/null 2>&1 || { echo "FATAL: jq is required" >&2; exit 2; }

NOW_S=$(date -u +%s)
NOW_ISO=$(date -u +%FT%TZ)
FAILURES=0
RESULTS=()   # id<TAB>status<TAB>message

record() { # record <id> <status> <message>
  RESULTS+=("$1"$'\t'"$2"$'\t'"$3")
  [ "$2" = fail ] && FAILURES=$((FAILURES + 1))
  return 0
}
pass() { record "$1" ok "$2"; }
fail() { record "$1" fail "$2"; }
skip() { record "$1" skip "$2"; }

human_age() { # seconds -> "3h12m"
  local s=$1
  printf '%dh%02dm' "$((s / 3600))" "$(((s % 3600) / 60))"
}

epoch_of() { # epoch_of <RFC3339>  -> epoch, or "" when unparseable
  [ -n "${1:-}" ] || { printf ''; return 0; }
  date -u -d "$1" +%s 2>/dev/null || printf ''
}

jget() { # jget <file> <filter> -> value, "" for null/missing/unreadable
  local v
  v=$(jq -r "$2 // empty" "$1" 2>/dev/null) || v=""
  printf '%s' "$v"
}

# ---------------------------------------------------------------------------
# A1 the archive exists at all and carries a health file
# ---------------------------------------------------------------------------
if [ ! -d "$ARCHIVE_ROOT" ]; then
  fail A1 "archive root $ARCHIVE_ROOT does not exist — nothing has ever been archived"
elif [ ! -d "$MEETINGS_DIR" ]; then
  fail A1 "$MEETINGS_DIR does not exist — the archive layout is not initialised"
elif [ ! -f "$HEALTH_JSON" ]; then
  fail A1 "$HEALTH_JSON is missing — cassini-archive-sync has never completed a run here"
else
  pass A1 "archive present at $ARCHIVE_ROOT with a health file"
fi

HEALTH_OK=0
[ -f "$HEALTH_JSON" ] && HEALTH_OK=1

# ---------------------------------------------------------------------------
# A2 freshness of the last completed run
# ---------------------------------------------------------------------------
LAST_FINISHED=""
if [ "$HEALTH_OK" = 1 ]; then
  LAST_FINISHED=$(jget "$HEALTH_JSON" '.last_run_finished_utc')
  last_s=$(epoch_of "$LAST_FINISHED")
  if [ -z "$last_s" ]; then
    fail A2 "health.json has no parseable last_run_finished_utc (got '${LAST_FINISHED:-null}')"
  else
    age=$((NOW_S - last_s))
    if [ "$age" -gt "$MAX_RUN_AGE_S" ]; then
      fail A2 "no archive run has completed for $(human_age "$age") (last $LAST_FINISHED, limit $(human_age "$MAX_RUN_AGE_S")) — the ingest has stopped"
    else
      pass A2 "last run completed $(human_age "$age") ago ($LAST_FINISHED)"
    fi
  fi
else
  skip A2 "no health.json"
fi

# ---------------------------------------------------------------------------
# A3 the last run reported ok, and A4 it raised no assertions of its own
# ---------------------------------------------------------------------------
if [ "$HEALTH_OK" = 1 ]; then
  st=$(jget "$HEALTH_JSON" '.last_run_status')
  if [ "$st" = ok ]; then
    pass A3 "last run status ok"
  else
    fail A3 "last archive run status is '${st:-unknown}' (run $(jget "$HEALTH_JSON" '.ingest_run_id'))"
  fi

  n_assert=$(jq -r '(.assertions_failed // []) | length' "$HEALTH_JSON" 2>/dev/null || echo 0)
  if [ "${n_assert:-0}" -gt 0 ]; then
    fail A4 "$n_assert assertion(s) failed in the last run: $(jq -rc '.assertions_failed' "$HEALTH_JSON" 2>/dev/null)"
  else
    pass A4 "the last run raised no assertions"
  fi
else
  skip A3 "no health.json"; skip A4 "no health.json"
fi

# ---------------------------------------------------------------------------
# A5 snapshot freshness — local snapshots are the entire backup story
# ---------------------------------------------------------------------------
if [ ! -d "$SNAPSHOT_ROOT" ]; then
  fail A5 "$SNAPSHOT_ROOT does not exist — the archive has no snapshots at all"
else
  newest_iso="" newest_s=""
  shopt -s nullglob
  for d in "$SNAPSHOT_ROOT"/*/; do
    n=$(basename "${d%/}")
    stamp=$n
    case "$n" in 000-baseline--*) stamp=${n#000-baseline--} ;; esac
    if [[ $stamp =~ ^([0-9]{4}-[0-9]{2}-[0-9]{2})T([0-9]{2})([0-9]{2})([0-9]{2})Z$ ]]; then
      s=$(epoch_of "${BASH_REMATCH[1]}T${BASH_REMATCH[2]}:${BASH_REMATCH[3]}:${BASH_REMATCH[4]}Z")
    else
      s=$(stat -c %Y "${d%/}" 2>/dev/null || printf '')
    fi
    [ -n "$s" ] || continue
    if [ -z "$newest_s" ] || [ "$s" -gt "$newest_s" ]; then newest_s=$s; newest_iso=$n; fi
  done
  shopt -u nullglob
  if [ -z "$newest_s" ]; then
    fail A5 "$SNAPSHOT_ROOT holds no snapshot — a single bad extent would be unrecoverable"
  else
    age=$((NOW_S - newest_s))
    if [ "$age" -gt "$MAX_SNAPSHOT_AGE_S" ]; then
      fail A5 "newest snapshot is $(human_age "$age") old ($newest_iso, limit $(human_age "$MAX_SNAPSHOT_AGE_S")) — cassini-archive-snapshot.service is not running"
    else
      pass A5 "newest snapshot $(human_age "$age") old ($newest_iso)"
    fi
  fi
fi

# ---------------------------------------------------------------------------
# A6 the index and the tree agree, and every meeting carries its own proof
# ---------------------------------------------------------------------------
if [ -d "$MEETINGS_DIR" ]; then
  shopt -s nullglob
  mdirs=("$MEETINGS_DIR"/*/)
  shopt -u nullglob
  n_dirs=${#mdirs[@]}
  if [ ! -f "$INDEX_JSONL" ]; then
    if [ "$n_dirs" -gt 0 ]; then
      fail A6 "index.jsonl is missing while $n_dirs meeting directories exist"
    else
      skip A6 "empty archive"
    fi
  else
    n_rows=$(jq -rs 'map(select(.dir != null)) | length' "$INDEX_JSONL" 2>/dev/null || echo -1)
    if [ "$n_rows" != "$n_dirs" ]; then
      fail A6 "index.jsonl lists $n_rows meetings but meetings/ holds $n_dirs — either the index has drifted (run cassini-archive-sync --apply --rebuild-index) or a meeting's ARCHIVE/meeting.json is unreadable and its row was dropped (check the run log for index.unreadable)"
    else
      missing=() unreadable=()
      for d in "${mdirs[@]}"; do
        d=${d%/}
        if [ -f "$d/ARCHIVE/meeting.json" ]; then
          # A meeting.json that exists but will not parse is worse than a
          # missing one: the reindex drops its row, so n_rows == n_dirs never
          # even gets a chance to disagree once the index has been rebuilt from
          # the broken file, and --rebuild-index (what this check used to
          # advise) cannot fix it.
          jq -e . "$d/ARCHIVE/meeting.json" >/dev/null 2>&1 || unreadable+=("$(basename "$d")")
        else
          missing+=("$(basename "$d"):meeting.json")
        fi
        [ -f "$d/ARCHIVE/MANIFEST.sha256" ] || missing+=("$(basename "$d"):MANIFEST.sha256")
      done
      if [ "${#missing[@]}" -gt 0 ]; then
        fail A6 "${#missing[@]} meeting(s) lack their ARCHIVE proof files: ${missing[*]:0:5}"
      elif [ "${#unreadable[@]}" -gt 0 ]; then
        fail A6 "${#unreadable[@]} meeting(s) have an unparseable ARCHIVE/meeting.json and are therefore missing from index.jsonl: ${unreadable[*]:0:5} — restore the file from the newest snapshot under $SNAPSHOT_ROOT, then run cassini-archive-sync --apply --rebuild-index"
      else
        pass A6 "$n_dirs meetings, $n_rows index rows, every one with meeting.json + MANIFEST.sha256"
      fi
    fi
  fi
else
  skip A6 "no meetings directory"
fi

# ---------------------------------------------------------------------------
# A7 nothing is sitting in the "a human must look at this" buckets
# ---------------------------------------------------------------------------
dirty=()
if [ -d "$STAGING_DIR" ]; then
  shopt -s nullglob dotglob
  st=("$STAGING_DIR"/*)
  shopt -u nullglob dotglob
  [ "${#st[@]}" -gt 0 ] && dirty+=("meetings/.staging has ${#st[@]} leftover partial ingest(s)")
fi
for f in "$STATE_DIR/conflicts.tsv" "$REPORTS_DIR/anomalies.tsv"; do
  if [ -s "$f" ]; then
    dirty+=("$(basename "$f") has $(wc -l <"$f") row(s)")
  fi
done
if [ -d "$ARCHIVE_ROOT/unattributed/derived" ]; then
  shopt -s nullglob
  ud=("$ARCHIVE_ROOT/unattributed/derived"/*)
  shopt -u nullglob
  [ "${#ud[@]}" -gt 0 ] && dirty+=("unattributed/derived holds ${#ud[@]} bundle(s) whose source could not be resolved")
fi
if [ "${#dirty[@]}" -gt 0 ]; then
  fail A7 "$(IFS='; '; echo "${dirty[*]}")"
else
  pass A7 "no staging leftovers, no conflicts, no anomalies, nothing unattributed"
fi

# ---------------------------------------------------------------------------
# A8 nothing has been deferred for too long
#    pending.tsv is append-only: ts \t path \t gate \t fingerprint. A path is
#    STUCK when it was first deferred long ago AND is still being deferred by
#    recent runs; a path that was later ingested stops getting fresh rows.
# ---------------------------------------------------------------------------
PENDING_TSV="$STATE_DIR/pending.tsv"
if [ -s "$PENDING_TSV" ]; then
  stuck=$(awk -F'\t' -v now="$NOW_S" -v maxage="$MAX_PENDING_AGE_S" -v fresh="$MAX_RUN_AGE_S" '
    function ep(s,   cmd, r) { if (s in memo) return memo[s]
                               cmd = "date -u -d \"" s "\" +%s 2>/dev/null"; r = 0
                               cmd | getline r; close(cmd); memo[s] = r+0; return r+0 }
    NF >= 3 { t = ep($1); if (t == 0) next
              if (!(($2) in first) || t < first[$2]) first[$2] = t
              if (!(($2) in last)  || t > last[$2])  last[$2]  = t
              gate[$2] = $3 }
    END { for (p in first)
            if (now - first[p] > maxage && now - last[p] <= fresh)
              printf "%s (gate=%s, deferred %dh)\n", p, gate[p], (now - first[p]) / 3600 }
  ' "$PENDING_TSV")
  if [ -n "$stuck" ]; then
    n=$(printf '%s\n' "$stuck" | wc -l)
    fail A8 "$n capture(s) deferred for more than $(human_age "$MAX_PENDING_AGE_S"): $(printf '%s' "$stuck" | head -3 | tr '\n' ';')"
  else
    pass A8 "no capture has been deferred for more than $(human_age "$MAX_PENDING_AGE_S")"
  fi
else
  pass A8 "nothing deferred"
fi

# ---------------------------------------------------------------------------
# A9 free space
# ---------------------------------------------------------------------------
if [ -d "$ARCHIVE_ROOT" ]; then
  free=$(df -B1 --output=avail "$ARCHIVE_ROOT" 2>/dev/null | tail -1 | tr -d ' ' || printf '')
  if [ -z "$free" ] || [[ ! $free =~ ^[0-9]+$ ]]; then
    fail A9 "cannot read free space for $ARCHIVE_ROOT"
  elif [ "$free" -lt "$MIN_FREE_BYTES" ]; then
    fail A9 "only $((free / 1073741824)) GiB free on $ARCHIVE_ROOT (need $((MIN_FREE_BYTES / 1073741824)) GiB) — the next ingest will refuse to run"
  else
    pass A9 "$((free / 1073741824)) GiB free"
  fi
else
  skip A9 "no archive root"
fi

# ---------------------------------------------------------------------------
# A10 the units that feed the archive are not failed, and
# A11 their timers are still scheduled (a masked timer is silent)
# ---------------------------------------------------------------------------
if [ -z "${WATCH_UNITS// /}" ]; then
  skip A10 "WATCH_UNITS is empty"
elif ! command -v systemctl >/dev/null 2>&1 || [ ! -d /run/systemd/system ]; then
  skip A10 "systemd is not running here"
else
  bad=()
  for u in $WATCH_UNITS; do
    if systemctl is-failed --quiet "$u" 2>/dev/null; then
      bad+=("$u=failed")
    fi
  done
  if [ "${#bad[@]}" -gt 0 ]; then
    fail A10 "failed unit(s): ${bad[*]} — this is the exact state cassini-ingest-batch.service sat in for three weeks"
  else
    pass A10 "no watched unit is failed"
  fi
fi

if [ -z "${WATCH_TIMERS// /}" ]; then
  skip A11 "WATCH_TIMERS is empty"
elif ! command -v systemctl >/dev/null 2>&1 || [ ! -d /run/systemd/system ]; then
  skip A11 "systemd is not running here"
else
  bad=()
  for t in $WATCH_TIMERS; do
    state=$(systemctl show "$t" -p LoadState --value 2>/dev/null || printf '')
    [ "$state" = loaded ] || { bad+=("$t=$([ -n "$state" ] && echo "$state" || echo absent)"); continue; }
    systemctl is-active --quiet "$t" 2>/dev/null || bad+=("$t=$(systemctl is-active "$t" 2>/dev/null || echo inactive)")
  done
  if [ "${#bad[@]}" -gt 0 ]; then
    fail A11 "timer(s) not scheduled: ${bad[*]} — a disabled or masked timer fails silently forever"
  else
    pass A11 "every watched timer is loaded and active"
  fi
fi

# ---------------------------------------------------------------------------
# A12 THE ONE THAT MATTERS: a finished capture is sitting in the ExApp volume
#     and never reached the archive. Filesystem evidence only — it does not
#     trust the operator DB, the ingest's own bookkeeping, or an exit code.
# ---------------------------------------------------------------------------
CURRENT_DIR="$SRC_EXA/current"
if [ ! -d "$CURRENT_DIR" ]; then
  fail A12 "cannot see $CURRENT_DIR — the archive's only source of new captures is unreadable, so freshness cannot be asserted"
else
  unarchived=() checked=0
  shopt -s nullglob
  for r in "$CURRENT_DIR"/*.run; do
    base=$(basename "$r")
    case "$base" in .*) continue ;; esac
    [ -d "$r" ] || continue
    # Only bundles cassini itself marked ready are the archive's responsibility.
    state=$(jget "$r/cassini.json" '.state')
    [ "$state" = ready ] || continue
    mt=$(stat -c %Y "$r" 2>/dev/null || printf '')
    [ -n "$mt" ] || continue
    [ $((NOW_S - mt)) -lt "$INGEST_GRACE_S" ] && continue   # still inside the grace window
    checked=$((checked + 1))
    job=${base%.run}
    [ -e "$BYJOB_DIR/$job" ] && continue
    if [ -f "$INDEX_JSONL" ] && grep -qF "\"$job\"" "$INDEX_JSONL"; then continue; fi
    unarchived+=("$base ($(human_age "$((NOW_S - mt))") old)")
  done
  shopt -u nullglob
  if [ "${#unarchived[@]}" -gt 0 ]; then
    fail A12 "${#unarchived[@]} finished ExApp capture(s) older than $(human_age "$INGEST_GRACE_S") are NOT in the archive: ${unarchived[*]:0:5}"
  else
    pass A12 "every ready .run in the ExApp volume older than $(human_age "$INGEST_GRACE_S") is archived ($checked checked)"
  fi
fi

# ---------------------------------------------------------------------------
# A13 the operator DB is not ahead of the archive (the ingest runs, exits 0,
#     and is no longer seeing new captures)
# ---------------------------------------------------------------------------
if [ "$HEALTH_OK" = 1 ]; then
  lag=$(jget "$HEALTH_JSON" '.capture_lag_seconds')
  if [ -z "$lag" ] || [[ ! $lag =~ ^[0-9]+$ ]]; then
    skip A13 "the last run recorded no capture lag"
  elif [ "$lag" -gt "$MAX_CAPTURE_LAG_S" ]; then
    fail A13 "the operator DB's newest finished recording is $(human_age "$lag") ahead of the newest archived meeting (limit $(human_age "$MAX_CAPTURE_LAG_S"))"
  else
    pass A13 "capture lag $(human_age "$lag")"
  fi
else
  skip A13 "no health.json"
fi

# ---------------------------------------------------------------------------
# A14 upstream liveness — the 2026-08-03/04/05 loss was NO NEW RECORDINGS with
#     every unit green. Counted in weekdays, computed here rather than trusted
#     from health.json, so it holds even when the last pass was snapshot-only.
# ---------------------------------------------------------------------------
newest_capture=""
[ "$HEALTH_OK" = 1 ] && newest_capture=$(jget "$HEALTH_JSON" '.newest_capture_utc')
if [ -z "$newest_capture" ] && [ -f "$INDEX_JSONL" ]; then
  newest_capture=$(jq -rs 'map(select(.dir != null) | .anchor_utc) | sort | last // empty' "$INDEX_JSONL" 2>/dev/null || printf '')
fi
today=$(date -u +%F)
if [ -z "$newest_capture" ]; then
  skip A14 "no archived capture to measure quiet time from"
elif [ -n "$EXPECTED_QUIET_UNTIL" ] && [[ ! "$today" > "$EXPECTED_QUIET_UNTIL" ]]; then
  skip A14 "quiet period declared until $EXPECTED_QUIET_UNTIL"
else
  d=${newest_capture%%T*}
  weekdays=0 guard=0
  while [ "$d" != "$today" ] && [ "$guard" -lt 400 ]; do
    d=$(date -u -d "$d +1 day" +%F)
    dow=$(date -u -d "$d" +%u)
    [ "$dow" -le 5 ] && weekdays=$((weekdays + 1))
    guard=$((guard + 1))
  done
  if [ "$weekdays" -ge "$MAX_QUIET_WEEKDAYS" ]; then
    fail A14 "no new meeting has been captured in $weekdays weekdays (newest $newest_capture) — recording may have stopped upstream; set EXPECTED_QUIET_UNTIL=YYYY-MM-DD to declare a holiday"
  else
    pass A14 "newest capture $newest_capture ($weekdays quiet weekday(s), limit $MAX_QUIET_WEEKDAYS)"
  fi
fi

# ---------------------------------------------------------------------------
# A15 an unacknowledged alarm outranks everything above
# ---------------------------------------------------------------------------
if [ -f "$ALARM_FILE" ]; then
  first=$(grep -m1 '^unit ' "$ALARM_FILE" 2>/dev/null || head -1 "$ALARM_FILE" 2>/dev/null || printf '')
  fail A15 "unacknowledged alarm at $ALARM_FILE (${first:-see the file}) — clear it with: cassini-archive-sync --apply --ack"
else
  pass A15 "no unacknowledged alarm"
fi

# ---------------------------------------------------------------------------
# report
# ---------------------------------------------------------------------------
if [ "$JSON" = 1 ]; then
  printf '%s\n' "${RESULTS[@]}" | jq -R 'split("\t") | {id:.[0], status:.[1], message:.[2]}' \
    | jq -s --arg at "$NOW_ISO" --arg root "$ARCHIVE_ROOT" --argjson failed "$FAILURES" \
        '{schema:"cassini.archive.healthcheck.v1", checked_at:$at, archive_root:$root,
          status:(if $failed == 0 then "ok" else "failed" end), failed:$failed, checks:.}'
else
  for row in "${RESULTS[@]}"; do
    id=${row%%$'\t'*}; rest=${row#*$'\t'}
    status=${rest%%$'\t'*}; msg=${rest#*$'\t'}
    case "$status" in
      fail) printf 'FAIL [%s] %s\n' "$id" "$msg" ;;
      skip) [ "$QUIET" = 1 ] || printf 'skip [%s] %s\n' "$id" "$msg" ;;
      *)    [ "$QUIET" = 1 ] || printf 'ok   [%s] %s\n' "$id" "$msg" ;;
    esac
  done
  if [ "$FAILURES" -gt 0 ]; then
    printf 'cassini-archive UNHEALTHY: %d assertion(s) failed at %s\n' "$FAILURES" "$NOW_ISO" >&2
  else
    [ "$QUIET" = 1 ] || printf 'cassini-archive healthy: %d assertions passed at %s\n' "${#RESULTS[@]}" "$NOW_ISO"
  fi
fi

[ "$FAILURES" -eq 0 ] || exit 3
exit 0
