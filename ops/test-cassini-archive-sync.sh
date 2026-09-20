#!/usr/bin/env bash
#
# Hermetic regression suite for ops/cassini-archive-sync.sh.
#
# The archive sync is the only thing standing between us and losing the
# pre-mix corpus: it copies 123.6 GiB of irreplaceable recordings, it is the
# only detector for "recording stopped upstream", and it runs unattended as
# root. Every behaviour it depends on is pinned here against a SYNTHETIC
# corpus built under mktemp -d that reproduces every real shape:
#
#   - an old `ready` cron bundle          session/streams/*.rtplog + .idx
#   - an old `failed` cron bundle         sessions/<id>/streams/ layout, plus a
#                                         mode-0700 recording-segments-1/
#                                         artifact-remux-work/ scratch dir that
#                                         MUST be excluded and MUST survive
#   - an old bare *.mkv                   no raw; room token read out of the
#                                         mkv title tag by ffprobe
#   - a new ExApp *.run                   cassini.json + recording.mkv + session/
#   - two derived *.meeting bundles       one ULID (job-gated), one imported
#   - TWO CONCURRENT captures             same date + same room, different
#                                         recorder_identity.display
#   - an in-progress *.run                cassini.json state=preparing
#   - current/.staging/<ULID>.run         must never be traversed
#   - an hpb Talk/Janus recording         Recording-<room>-<date>_<time>_<us>.mkv
#                                         plus its sidecar .json and a .mjr whose
#                                         instant falls in exactly one window —
#                                         the ONE shape that reaches
#                                         refresh_manifest with no derived/
#   - an imported *.meeting whose         /recordings/<dir>/recording.mkv — 45 of
#     source_path points INTO a cron      the 77 real imports have this shape,
#     bundle                              and none of them has a resolvable
#                                         basename
#
# Everything is offline and fast: no george, no docker, no network, no root.
# Three tiny PATH shims stand in for tools this box does not have or must not
# really run — `id` (fake root, so the archive's own root check is exercised
# rather than bypassed), `btrfs` (subvolume show/snapshot/delete) and `sqlite3`
# (a python3 re-implementation of the three CLI invocations the script makes).
# The real root check is tested separately, WITHOUT the shim.
#
# NO FIXED SLEEPS. The one "wait" in the design — a bundle that is not ready
# yet — is tested by flipping the fixture's cassini.json to `ready` and running
# the pass again: ordered against a concrete state change, never a quiet window.
#
# Run directly:
#   ./ops/test-cassini-archive-sync.sh
#
# Exit 0 and a final "PASS:" line on success; non-zero with "FAIL:" otherwise.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# SYNC= lets a reviewer run this suite against another copy of the script
# (e.g. the pre-fix baseline) to prove a test actually fails without the fix.
SYNC="${SYNC:-$SCRIPT_DIR/cassini-archive-sync.sh}"

CHECKS=0

fail() {
  echo "FAIL: $*" >&2
  [ -n "${LAST_LOG:-}" ] && [ -f "${LAST_LOG:-}" ] && {
    echo "--- last run log (tail) ------------------------------------" >&2
    tail -40 "$LAST_LOG" >&2
    echo "------------------------------------------------------------" >&2
  }
  exit 1
}
ok() { CHECKS=$((CHECKS + 1)); echo "  ok  $*"; }
section() { echo; echo "== $* =="; }

[[ -f "$SYNC" ]] || fail "cassini-archive-sync.sh is missing at $SYNC"
[[ -x "$SYNC" ]] || fail "cassini-archive-sync.sh is not executable"
bash -n "$SYNC" || fail "bash -n failed for cassini-archive-sync.sh"

for t in jq python3 sha256sum flock find stat cp ffmpeg ffprobe; do
  command -v "$t" >/dev/null || fail "this test needs $t on PATH"
done

T="$(mktemp -d -t cassini-archive-test.XXXXXXXX)"
LOGS="$(mktemp -d -t cassini-archive-logs.XXXXXXXX)"
# KEEP=1 leaves the fixture and the per-run logs behind for post-mortem.
cleanup() {
  if [ "${KEEP:-0}" = 1 ]; then echo "kept: fixture=$T logs=$LOGS" >&2; return 0; fi
  chmod -R u+w "$T" 2>/dev/null || true; rm -rf "$T" "$LOGS"
}
trap cleanup EXIT

# =============================================================================
# PATH shims
# =============================================================================
BIN="$T/bin"
mkdir -p "$BIN"

# Fake root. The script's own `id -u` check therefore still runs; only its
# answer is supplied. Test 11 removes this shim and asserts the real refusal.
cat > "$BIN/id" <<'SH'
#!/usr/bin/env bash
if [ "$#" = 1 ] && [ "$1" = "-u" ]; then echo 0; exit 0; fi
exec /usr/bin/id "$@"
SH

# btrfs: subvolume show/create/snapshot/delete against plain directories.
cat > "$BIN/btrfs" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-} ${2:-}" in
  "subvolume show")     [ -e "$3/.btrfs-subvol" ] && { echo "$3"; exit 0; }; exit 1 ;;
  "subvolume create")   mkdir -p "$3"; : > "$3/.btrfs-subvol" ;;
  "subvolume snapshot") shift 2; [ "${1:-}" = "-r" ] && shift; cp -a "$1" "$2" ;;
  "subvolume delete")   shift 2; chmod -R u+w "$1" 2>/dev/null || true; rm -rf "$1" ;;
  *) echo "btrfs shim: unsupported: $*" >&2; exit 1 ;;
esac
SH

# sqlite3: exactly the three CLI shapes cassini-archive-sync.sh uses —
#   sqlite3 "file:DB?mode=ro" ".backup 'DST'"
#   sqlite3 DB 'pragma integrity_check;'
#   sqlite3 -separator $'\t' DB "select ..."
cat > "$BIN/sqlite3" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
sep='|'; args=()
while [ $# -gt 0 ]; do
  case "$1" in
    -separator) sep="$2"; shift 2 ;;
    -*) shift ;;
    *) args+=("$1"); shift ;;
  esac
done
exec python3 -c '
import sys, sqlite3, shutil, re, urllib.parse
sep, db = sys.argv[1], sys.argv[2]
sql = sys.argv[3] if len(sys.argv) > 3 else ""
def real(u):
    return urllib.parse.unquote(u[5:].split("?")[0]) if u.startswith("file:") else u
m = re.match(r"^\s*\.backup\s+.?([^\x27\"]*).?\s*$", sql)
if m and sql.strip().startswith(".backup"):
    shutil.copyfile(real(db), m.group(1)); sys.exit(0)
con = sqlite3.connect(real(db))
for row in con.execute(sql):
    print(sep.join("" if v is None else str(v) for v in row))
' "$sep" "${args[@]}"
SH

# cp: passes through to the real cp, except that FAKE_CP_NO_REFLINK=1 makes
# --reflink=always fail. That makes "reflink unavailable" deterministic on any
# filesystem instead of depending on what /tmp happens to be.
cat > "$BIN/cp" <<'SH'
#!/usr/bin/env bash
[ -n "${FAKE_CP_LOG:-}" ] && printf '%s\n' "$*" >> "$FAKE_CP_LOG"
if [ "${FAKE_CP_NO_REFLINK:-0}" = 1 ]; then
  for a in "$@"; do [ "$a" = "--reflink=always" ] && exit 1; done
fi
# FAKE_CP_FORCE_REFLINK=1 makes --reflink=always SUCCEED on a filesystem that
# does not support it (the suite runs on /tmp), by performing a plain copy.
# Used to isolate "does the probe wrongly refuse a working pair?" from "does
# this filesystem support reflink?".
if [ "${FAKE_CP_FORCE_REFLINK:-0}" = 1 ]; then
  args=(); for a in "$@"; do [ "$a" = "--reflink=always" ] || args+=("$a"); done
  exec /usr/bin/cp "${args[@]}"
fi
exec /usr/bin/cp "$@"
SH

# stat: faithful pass-through, except FAKE_STAT_SPLIT_DEV=1 makes `stat -c %d`
# report a DIFFERENT device id for paths under $ARCHIVE_ROOT than for anything
# else -- exactly what the real systemd units see, because ProtectSystem=strict
# + BindPaths puts the archive in a mount namespace where the same underlying
# btrfs reports a different st_dev. Reflink still works across that pair.
cat > "$BIN/stat" <<'SH'
#!/usr/bin/env bash
if [ "${FAKE_STAT_SPLIT_DEV:-0}" = 1 ] && [ "$1" = "-c" ] && [ "$2" = "%d" ] && [ $# -eq 3 ]; then
  case "$3" in
    "${ARCHIVE_ROOT:-/nonexistent}"|"${ARCHIVE_ROOT:-/nonexistent}"/*) echo 1048653 ;;
    *) echo 48 ;;
  esac
  exit 0
fi
exec /usr/bin/stat "$@"
SH

chmod +x "$BIN"/*

# =============================================================================
# fixture corpus
# =============================================================================
SRC="$T/src"
SRC_OLD="$SRC/recordings"
SRC_HPB="$SRC/hpb-talk-recordings"
EXA_DATA="$SRC/exapp/_data"
SRC_EXA="$EXA_DATA/operator/jobs"
CURRENT="$SRC_EXA/current"
DB="$EXA_DATA/operator/jobs.sqlite3"
ARCHIVE="$T/archive"
ARCHIVE_NEG="$T/archive-neg"

JOB_OK=01M11CF8G9FAVXB5GW0Y94VCNF        # the .run that is ready and done
JOB_WIP=01KYVP0WWQBW0W6D81BASCTNPG       # in progress: cassini.json state=preparing
JOB_STAGED=01KYVVJHT3T2XKVH8BFQHGMYGT    # sitting in current/.staging: invisible
JOB_NOMEDIA=01KVAF4E7KG05AB6EA2X382426   # failed, zero media: an index row, no dir
JOB_NULLCOL=01KVBF4E7KG05AB6EA2X382427   # NULL room_name + NULL record_finished_at

mkdir -p "$SRC_OLD" "$SRC_HPB" "$CURRENT" "$SRC_EXA/runs" "$EXA_DATA/operator/backups"
mkdir -p "$ARCHIVE" "$ARCHIVE_NEG" "$T/snapshots" "$T/var"
: > "$ARCHIVE/.btrfs-subvol"
: > "$ARCHIVE_NEG/.btrfs-subvol"

blob() { # blob <path> <kib>
  mkdir -p "$(dirname "$1")"
  head -c $(( $2 * 1024 )) /dev/zero | tr '\0' "${3:-x}" > "$1"
}

session_json() { # session_json <path> <room> <display> <started_wall_utc>
  mkdir -p "$(dirname "$1")"
  cat > "$1" <<JSON
{
  "schema": "cassini.session.v1",
  "started_wall_utc": "$4",
  "platform": {
    "room": "$2",
    "recorder_identity": { "display": "$3", "id": "bot" }
  }
}
JSON
}

# --- 1. old cron bundle, state=ready, session/ layout -------------------------
CRON_READY="$SRC_OLD/daily-meeting-2026-07-31"
mkdir -p "$CRON_READY/session/streams"
echo '{"kind":"recording","state":"ready"}' > "$CRON_READY/cassini.json"
blob "$CRON_READY/recording.mkv" 64 a
session_json "$CRON_READY/session/session.json" mczuc3mb CodemyriadRecorder 2026-07-31T10:30:12.614802877Z
printf '{"event":"stream_opened"}\n' > "$CRON_READY/session/events.ndjson"
for s in 1 2 3; do
  blob "$CRON_READY/session/streams/stream-$s.rtplog" 8 r
  blob "$CRON_READY/session/streams/stream-$s.rtplog.idx" 1 i
done
# Operator transients that must be excluded, counted, and left alone: a
# dot-prefixed scratch file and a *.backup. Both are exclusion RULES with no
# fixture behind them until now.
blob "$CRON_READY/.nfs-scratch" 1 h
blob "$CRON_READY/recording.mkv.backup" 3 k
READY_EXCL_BYTES=$(( 1 * 1024 + 3 * 1024 ))
CRON_READY_DIR=2026-07-31T103012Z--mczuc3mb--cron--daily-meeting

# --- 2. old cron bundle, state=failed, sessions/<id>/ layout ------------------
CRON_FAILED="$SRC_OLD/daily-meeting-2026-04-15"
SID=recording_20260415T093508.886865073Z
mkdir -p "$CRON_FAILED/sessions/$SID/streams" "$CRON_FAILED/recording-segments-1/artifact-remux-work"
echo '{"kind":"recording","state":"failed"}' > "$CRON_FAILED/cassini.json"
blob "$CRON_FAILED/recording.mkv" 4 t                    # truncated mixdown
session_json "$CRON_FAILED/sessions/$SID/session.json" mczuc3mb CodemyriadRecorder 2026-04-15T09:35:08.886865073Z
printf '{"event":"stream_opened"}\n' > "$CRON_FAILED/sessions/$SID/events.ndjson"
for s in 1 2; do blob "$CRON_FAILED/sessions/$SID/streams/stream-$s.rtplog" 16 r; done
blob "$CRON_FAILED/recording-segments-1/segment-000.mkv" 12 s
# The orphaned crashed-remux scratch: 40 GiB of it on george, mode 0700, and a
# non-root traversal silently skips it while still exiting 0.
blob "$CRON_FAILED/recording-segments-1/artifact-remux-work/remux-scratch.mkv" 48 w
blob "$CRON_FAILED/recording-segments-1/artifact-remux-work/pass.log" 2 l
chmod 0700 "$CRON_FAILED/recording-segments-1/artifact-remux-work"
CRON_FAILED_DIR=2026-04-15T093508Z--mczuc3mb--cron--daily-meeting
REMUX_BYTES=$(( 48 * 1024 + 2 * 1024 ))

# --- 3. old bare mkv, no raw, room token lives in the mkv title tag -----------
LEGACY_MKV="$SRC_OLD/daily-meeting-2026-03-10--12:30.mkv"
legacy_mkv_built=0
for codec in libopus libvorbis flac; do
  if ffmpeg -v error -y -f lavfi -i "anullsrc=r=8000:cl=mono" -t 0.2 -c:a "$codec" \
       -metadata title="Cassini Go Recording mczuc3mb" "file:$LEGACY_MKV" 2>/dev/null; then
    legacy_mkv_built=1; break
  fi
done
[ "$legacy_mkv_built" = 1 ] || fail "could not build the legacy fixture mkv with ffmpeg"
[ "$(ffprobe -v error -show_entries format_tags=title -of default=nw=1:nk=1 -i "file:$LEGACY_MKV")" \
  = "Cassini Go Recording mczuc3mb" ] || fail "legacy fixture mkv lost its title tag"
LEGACY_DIR=2026-03-10T123000--mczuc3mb--legacy--daily-meeting

# --- 4. new ExApp .run, ready and done ---------------------------------------
RUN_OK="$CURRENT/$JOB_OK.run"
mkdir -p "$RUN_OK/session/streams"
echo '{"kind":"run","state":"ready"}' > "$RUN_OK/cassini.json"
blob "$RUN_OK/recording.mkv" 72 b
session_json "$RUN_OK/session/session.json" mczuc3mb CassiniRecorder 2026-07-31T10:30:01.201477000Z
printf '{"event":"stream_opened"}\n' > "$RUN_OK/session/events.ndjson"
for s in 1 2 3; do
  blob "$RUN_OK/session/streams/stream-$s.rtplog" 10 r
  blob "$RUN_OK/session/streams/stream-$s.rtplog.idx" 1 i
done
EXAPP_DIR=2026-07-31T103001Z--mczuc3mb--exapp--daily-standup-meeting

# --- 5. derived .meeting bundles (no raw) ------------------------------------
MEET_OK="$CURRENT/$JOB_OK.meeting"
mkdir -p "$MEET_OK"
cat > "$MEET_OK/cassini.json" <<JSON
{"kind":"meeting","state":"ready","source_path":"/nc_app_gocassini_data/operator/jobs/current/$JOB_OK.run"}
JSON
echo '{"tracks":[]}' > "$MEET_OK/manifest.json"
blob "$MEET_OK/meeting.webm" 20 m
blob "$CURRENT/$JOB_OK.opus" 6 o

MEET_LEGACY="$CURRENT/daily-meeting-2026-03-10--12:30.meeting"
mkdir -p "$MEET_LEGACY"
cat > "$MEET_LEGACY/cassini.json" <<'JSON'
{"kind":"meeting","state":"ready","source_path":"/recordings/daily-meeting-2026-03-10--12:30.mkv"}
JSON
blob "$MEET_LEGACY/meeting.webm" 14 m

# --- 6. in-progress .run: cassini.json says preparing ------------------------
RUN_WIP="$CURRENT/$JOB_WIP.run"
mkdir -p "$RUN_WIP/session/streams"
echo '{"kind":"run","state":"preparing"}' > "$RUN_WIP/cassini.json"
blob "$RUN_WIP/recording.mkv" 9 p
session_json "$RUN_WIP/session/session.json" mczuc3mb CassiniRecorder 2026-08-27T10:32:55.123456789Z
blob "$RUN_WIP/session/streams/stream-1.rtplog" 5 r
WIP_DIR=2026-08-27T103255Z--mczuc3mb--exapp--ivan

# --- 6b. a .run whose job row has NULL columns in the MIDDLE of the select ----
# talk_binding carries no room_name (17 of the real rows do not) and
# record_finished_at is NULL while completed_at is set. Read with a tab
# separator those two NULLs collapse and completed_at lands in
# record_finished_at, which is exactly G3 phase 1's gate: a bundle that never
# finished recording would be ingested as if it had. It must stay deferred.
RUN_NULLCOL="$CURRENT/$JOB_NULLCOL.run"
mkdir -p "$RUN_NULLCOL/session/streams"
echo '{"kind":"run","state":"ready"}' > "$RUN_NULLCOL/cassini.json"
blob "$RUN_NULLCOL/recording.mkv" 7 n
session_json "$RUN_NULLCOL/session/session.json" mczuc3mb CassiniRecorder 2026-08-20T10:00:00.000000000Z
blob "$RUN_NULLCOL/session/streams/stream-1.rtplog" 4 r

# --- 7. current/.staging/ — promoteDirectory's landing zone, never a candidate
mkdir -p "$CURRENT/.staging/$JOB_STAGED.run/session/streams"
echo '{"kind":"run","state":"ready"}' > "$CURRENT/.staging/$JOB_STAGED.run/cassini.json"
blob "$CURRENT/.staging/$JOB_STAGED.run/recording.mkv" 30 z
session_json "$CURRENT/.staging/$JOB_STAGED.run/session/session.json" \
  mczuc3mb CassiniRecorder 2026-08-28T09:00:00.000000000Z
# ... and a dot-prefixed sibling directly under current/, likewise invisible.
mkdir -p "$CURRENT/.$JOB_STAGED.run"
echo '{"kind":"run","state":"ready"}' > "$CURRENT/.$JOB_STAGED.run/cassini.json"

# --- 5c. an imported .meeting whose source_path points INTO a cron bundle ----
# 45 of the 77 real imported bundles carry exactly this shape. `basename` on it
# is the literal string "recording.mkv", which is not a path and can never be a
# `# source` line, so resolution has to map the FIRST path component onto the
# ingested cron directory.
MEET_CRON="$CURRENT/daily-meeting-2026-07-31.meeting"
mkdir -p "$MEET_CRON"
cat > "$MEET_CRON/cassini.json" <<'JSON'
{"kind":"meeting","state":"ready","source_path":"/recordings/daily-meeting-2026-07-31/recording.mkv"}
JSON
blob "$MEET_CRON/meeting.webm" 11 m

# --- 5d. the /work/work/<date>-recovered.mkv shape ---------------------------
# /work exists in no container any more and no script in the repo performs that
# recovery, so the DATE is the only evidence. It attaches only when the whole
# corpus has exactly one meeting with a mixdown that day, and is marked
# low-confidence forever. A dry run has committed nothing, so this is also what
# proves the PLAN is as resolvable as the result.
MEET_WORK="$CURRENT/2026-04-15-recovered.meeting"
mkdir -p "$MEET_WORK"
cat > "$MEET_WORK/cassini.json" <<'JSON'
{"kind":"meeting","state":"ready","source_path":"/work/work/2026-04-15-recovered.mkv"}
JSON
blob "$MEET_WORK/meeting.webm" 9 m

# --- 8. an hpb Talk/Janus recording, its sidecar, and one .mjr ----------------
# The hpb era is the only one that reaches refresh_manifest with no derived/
# directory: no imported .meeting points at a June hpb mkv on george either, so
# all 7 real .mjr attach to bundles shaped exactly like this one.
HPB_MKV="$SRC_HPB/Recording-mrzd4477-2026-06-10_13-44-56_850364.mkv"
hpb_mkv_built=0
for codec in libopus libvorbis flac; do
  if ffmpeg -v error -y -f lavfi -i "anullsrc=r=8000:cl=mono" -t 0.2 -c:a "$codec" \
       "file:$HPB_MKV" 2>/dev/null; then
    hpb_mkv_built=1; break
  fi
done
[ "$hpb_mkv_built" = 1 ] || fail "could not build the hpb fixture mkv with ffmpeg"
[ -n "$(ffprobe -v error -show_entries format=duration -of default=nw=1:nk=1 -i "file:$HPB_MKV")" ] \
  || fail "the hpb fixture mkv has no readable duration"
printf '{"generated_at":"2026-06-10T13:45:14+00:00"}\n' > "${HPB_MKV%.mkv}.json"
# One .mjr, 2 m 56 s before the filename's finalize stamp: inside the single
# [started - 300 s, anchor] window, so it must attach rather than alarm.
MJR_US=$(( $(date -u -d 2026-06-10T13:42:00Z +%s) * 1000000 ))
MJR="$SRC_HPB/videoroom-$MJR_US-audio-0.mjr"
printf 'MJR00002fixture\n' > "$MJR"
HPB_DIR=2026-06-10T134456Z--mrzd4477--hpb--talk-recording

# --- the operator DB ---------------------------------------------------------
python3 - "$DB" "$JOB_OK" "$JOB_WIP" "$JOB_NOMEDIA" "$JOB_NULLCOL" <<'PY'
import sqlite3, sys
db, job_ok, job_wip, job_nomedia, job_nullcol = sys.argv[1:6]
con = sqlite3.connect(db)
con.execute("""create table jobs(
  id text primary key, stage text, state text, artifact_run_path text,
  talk_binding text, request_json text, record_finished_at text,
  completed_at text, created_at text)""")
rows = [
  (job_ok, "done", "succeeded",
   "/nc_app_gocassini_data/operator/jobs/current/%s.run" % job_ok,
   '{"room_token":"mczuc3mb","room_name":"Daily standup meeting","owner":"silviot"}',
   '{"roomToken":"mczuc3mb"}', "2026-07-31T11:02:18Z", "2026-07-31T11:20:04Z",
   "2026-07-31T10:29:00Z"),
  (job_wip, "record", "running",
   "/nc_app_gocassini_data/operator/jobs/current/%s.run" % job_wip,
   '{"room_token":"mczuc3mb","room_name":"Ivan","owner":"silviot"}',
   '{"roomToken":"mczuc3mb"}', None, None, "2026-08-27T10:32:00Z"),
  # A WHOLE-SECOND record_finished_at (Go's RFC3339Nano drops trailing zeros),
  # late enough in UTC that Europe/Rome is already the next day: it pins both
  # truncate_second's doubled Z and the job-only row's date_local.
  (job_nomedia, "done", "failed", None,
   '{"room_token":"btsq78i8","room_name":"Chris","owner":"silviot"}',
   '{"roomToken":"btsq78i8"}', "2026-06-17T22:38:24Z", "2026-06-17T22:39:00Z",
   "2026-06-17T22:30:00Z"),
  (job_nullcol, "build", "running",
   "/nc_app_gocassini_data/operator/jobs/current/%s.run" % job_nullcol,
   '{"room_token":"mczuc3mb","owner":"silviot"}',            # no room_name
   '{"roomToken":"mczuc3mb"}', None, "2026-08-20T10:44:00Z", # no record_finished_at
   "2026-08-20T09:59:00Z"),
]
con.executemany("insert into jobs values (?,?,?,?,?,?,?,?,?)", rows)
con.commit()
PY

EXPECTED_MEETINGS="$CRON_FAILED_DIR
$LEGACY_DIR
$EXAPP_DIR
$HPB_DIR
$CRON_READY_DIR"
EXPECTED_MEETINGS="$(printf '%s\n' "$EXPECTED_MEETINGS" | LC_ALL=C sort)"

# =============================================================================
# helpers
# =============================================================================
RUN_N=0
LAST_RC=0
LAST_LOG=""

run_sync() { # run_sync [VAR=VAL ...] -- <args...>
  local envs=()
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do envs+=("$1"); shift; done
  [ "${1:-}" = "--" ] && shift
  RUN_N=$((RUN_N + 1))
  LAST_LOG="$LOGS/run-$(printf '%02d' "$RUN_N").log"
  LAST_RC=0
  env PATH="$BIN:$PATH" \
      CONFIG_FILE=/dev/null \
      ARCHIVE_ROOT="$ARCHIVE" SNAPSHOT_ROOT="$T/snapshots" VAR_DIR="$T/var" \
      SRC_OLD="$SRC_OLD" SRC_HPB="$SRC_HPB" \
      SRC_EXA_DATA="$EXA_DATA" SRC_EXA="$SRC_EXA" OPERATOR_DB="$DB" \
      LOCKFILE="$LOGS/cassini-archive.lock" \
      MIN_FREE_BYTES=1 MIN_RUN_INTERVAL=0 ALLOW_FULL_COPY=1 \
      EXPECTED_QUIET_UNTIL=2099-12-31 \
      EXPECT_CRON=2 EXPECT_LEGACY=1 EXPECT_HPB=1 EXPECT_EXAPP=1 \
      "${envs[@]}" \
      "$SYNC" "$@" >"$LAST_LOG" 2>&1 || LAST_RC=$?
  return 0
}

log_has()  { grep -q -- "$1" "$LAST_LOG"; }
log_lacks() { ! grep -q -- "$1" "$LAST_LOG"; }

# names + modes + sizes + mtimes + content hashes: the strongest cheap statement
# of "this tree did not change".
tree_digest() { # tree_digest <dir>
  ( cd "$1" && find . -mindepth 1 -printf '%y %m %s %T@ %P\n' | LC_ALL=C sort
    cd "$1" && find . -type f -printf '%P\0' | LC_ALL=C sort -z | xargs -0 -r sha256sum )
}
# The committed corpus only: meetings/.staging is scratch whose mtime moves
# whenever a crashed .partial is swept, which is not a change to any meeting.
meetings_digest() {
  ( cd "$ARCHIVE/meetings" \
      && find . -mindepth 1 \( -path './.staging' -o -path './.staging/*' \) -prune \
             -o -printf '%y %m %s %T@ %P\n' | LC_ALL=C sort
    cd "$ARCHIVE/meetings" \
      && find . \( -path './.staging/*' \) -prune -o -type f -printf '%P\0' \
         | LC_ALL=C sort -z | xargs -0 -r sha256sum )
}
meetings_list() { ( cd "$ARCHIVE/meetings" 2>/dev/null && find . -mindepth 1 -maxdepth 1 -type d -not -name '.*' -printf '%P\n' | LC_ALL=C sort ) || true; }

echo "fixture: $T"

# =============================================================================
section "1. dry run writes nothing at all"
# =============================================================================
# The flock lockfile lives in $LOGS, outside the fixture: it is runtime state
# (on george it is /run/lock/cassini-archive.lock), not something the pass may
# write into the archive or the sources. Everything else in $T must be untouched.
BEFORE_ALL="$(tree_digest "$T")"
run_sync -- --dry-run --all
[ "$LAST_RC" = 0 ] || fail "dry run exited $LAST_RC (expected 0)"
log_has 'event=run.dry_run' || fail "dry run did not announce itself"
log_has 'event=ingest.plan' || fail "dry run planned no ingest at all — the fixture is not being seen"
log_has "dir=\"$CRON_READY_DIR\"" || fail "dry run did not plan $CRON_READY_DIR"
AFTER_DRY="$(tree_digest "$T")"
[ "$BEFORE_ALL" = "$AFTER_DRY" ] || {
  diff <(printf '%s\n' "$BEFORE_ALL") <(printf '%s\n' "$AFTER_DRY") >&2 || true
  fail "--dry-run modified the filesystem (diff above)"
}
[ -z "$(meetings_list)" ] || fail "--dry-run created meeting directories"
ok "--dry-run planned 5 bundles and left every byte of the tree untouched"

# A bare invocation is a dry run too: no --apply, nothing written.
run_sync -- --backfill-old
[ "$LAST_RC" = 0 ] || fail "bare --backfill-old exited $LAST_RC"
[ "$(tree_digest "$T")" = "$BEFORE_ALL" ] || fail "a bare (no --apply) run wrote to the filesystem"
ok "a run without --apply is a dry run by default"

# =============================================================================
section "12. reflink unavailable is a loud failure, never a silent full copy"
# =============================================================================
run_sync ARCHIVE_ROOT="$ARCHIVE_NEG" ALLOW_FULL_COPY=0 FAKE_CP_NO_REFLINK=1 -- --apply --backfill-old
[ "$LAST_RC" = 3 ] || fail "reflink-unavailable exited $LAST_RC, expected 3 (preflight failed)"
log_has 'event=reflink.unavailable_on_archive' || fail "no reflink.unavailable_on_archive event"
log_has 'ALLOW_FULL_COPY=1' || fail "the refusal does not tell the operator how to override it"
log_lacks 'event=ingest.done' || fail "it copied bundles anyway after failing the reflink probe"
[ -z "$(cd "$ARCHIVE_NEG" && find meetings -mindepth 1 -maxdepth 1 -type d -not -name '.*' 2>/dev/null || true)" ] \
  || fail "a failed reflink probe still produced meeting directories"
ok "reflink probe failure aborts at exit 3 before a single byte is copied"

# ... and the override is explicit, logged, and recorded in copy_mode.
run_sync ARCHIVE_ROOT="$ARCHIVE_NEG" ALLOW_FULL_COPY=1 FAKE_CP_NO_REFLINK=1 -- --apply --backfill-old
[ "$LAST_RC" = 0 ] || fail "ALLOW_FULL_COPY=1 run exited $LAST_RC"
log_has 'event=reflink.degraded' || fail "ALLOW_FULL_COPY=1 did not warn that it writes real bytes"
log_has 'copy_mode=copy' || fail "copy_mode was not recorded as 'copy'"
grep -q '"copy_mode":"copy"' "$ARCHIVE_NEG/index.jsonl" || fail "index.jsonl does not record copy_mode=copy"
ok "ALLOW_FULL_COPY=1 is the only way past it, and it is recorded in every row"

# =============================================================================
section "11. non-root refuses, with the documented reason"
# =============================================================================
if [ "$(/usr/bin/id -u)" = 0 ]; then
  echo "  --  skipped: this suite is running as real root"
else
  RUN_N=$((RUN_N + 1)); LAST_LOG="$LOGS/run-$(printf '%02d' "$RUN_N").log"; LAST_RC=0
  # No $BIN on PATH: the real `id` answers, so the real check runs.
  env CONFIG_FILE=/dev/null ARCHIVE_ROOT="$ARCHIVE_NEG" SNAPSHOT_ROOT="$T/snapshots" \
      VAR_DIR="$T/var" SRC_OLD="$SRC_OLD" SRC_HPB="$SRC_HPB" SRC_EXA_DATA="$EXA_DATA" \
      SRC_EXA="$SRC_EXA" OPERATOR_DB="$DB" LOCKFILE="$LOGS/cassini-archive.lock" \
      "$SYNC" --apply --all >"$LAST_LOG" 2>&1 || LAST_RC=$?
  [ "$LAST_RC" = 2 ] || fail "non-root run exited $LAST_RC, expected the documented 2"
  log_has 'must run as root' || fail "the non-root refusal does not say it must run as root"
  log_has '40,248,765,505' || fail "the non-root refusal no longer states what a non-root pass silently loses"
  ok "a non-root invocation exits 2 and explains the 40,248,765,505 B it would silently skip"
fi

# =============================================================================
section "2. backfill ingests every expected bundle and nothing else"
# =============================================================================
SRC_BEFORE="$(tree_digest "$SRC_OLD")$(tree_digest "$SRC_HPB")$(tree_digest "$SRC_EXA")$(sha256sum "$DB")"

run_sync -- --apply --all
[ "$LAST_RC" = 0 ] || fail "the backfill exited $LAST_RC (expected 0)"
GOT="$(meetings_list)"
[ "$GOT" = "$EXPECTED_MEETINGS" ] || {
  diff <(printf '%s\n' "$EXPECTED_MEETINGS") <(printf '%s\n' "$GOT") >&2 || true
  fail "meetings/ does not hold exactly the expected bundles (want < / got > above)"
}
ok "meetings/ holds exactly the 5 expected bundles, named per §2"
[ ! -s "$ARCHIVE/reports/anomalies.tsv" ] \
  || fail "the backfill quarantined something: $(cat "$ARCHIVE/reports/anomalies.tsv")"
[ ! -s "$ARCHIVE/state/conflicts.tsv" ] || fail "the backfill recorded a conflict"
ok "a clean backfill leaves reports/anomalies.tsv and state/conflicts.tsv empty"
log_has 'event=acceptance.ok era=cron count=2' || fail "the cron acceptance count was not asserted"
log_has 'event=acceptance.ok era=exapp count=1' || fail "the exapp acceptance count was not asserted"
ok "the backfill asserts its own per-era acceptance counts"

# A wrong acceptance number is a failed backfill, not a shrug.
run_sync EXPECT_CRON=99 -- --apply --all
[ "$LAST_RC" = 5 ] || fail "a wrong acceptance count exited $LAST_RC, expected 5"
log_has 'era cron: 2 meetings archived, expected 99' || fail "the acceptance mismatch was not reported"
ok "a mismatched acceptance count fails the run (exit 5)"

# --- 4. both old layouts are handled -----------------------------------------
section "4. both old layouts (session/ and sessions/<id>/) are handled"
M1="$ARCHIVE/meetings/$CRON_READY_DIR"
M2="$ARCHIVE/meetings/$CRON_FAILED_DIR"
M3="$ARCHIVE/meetings/$LEGACY_DIR"
M4="$ARCHIVE/meetings/$EXAPP_DIR"
M5="$ARCHIVE/meetings/$HPB_DIR"
[ -f "$M1/session/session.json" ] || fail "ready bundle: session/session.json is missing"
[ -f "$M1/session/streams/stream-1.rtplog" ] || fail "ready bundle: rtplog not ingested"
[ -f "$M1/session/streams/stream-1.rtplog.idx" ] || fail "ready bundle: .idx not ingested"
[ ! -L "$M1/session" ] || fail "ready bundle: session/ should be the real directory, not a symlink"
ok "session/ layout: rtplog + idx + session.json ingested verbatim"

[ -f "$M2/sessions/$SID/session.json" ] || fail "failed bundle: sessions/<id>/session.json is missing"
[ -f "$M2/sessions/$SID/streams/stream-1.rtplog" ] || fail "failed bundle: rtplog not ingested"
[ -L "$M2/session" ] || fail "failed bundle: the additive session -> sessions/<id> symlink is missing"
[ "$(readlink "$M2/session")" = "sessions/$SID" ] \
  || fail "failed bundle: session symlink points at '$(readlink "$M2/session")', expected relative sessions/$SID"
[ -f "$M2/session/session.json" ] || fail "failed bundle: the session symlink does not resolve"
ok "sessions/<id>/ layout: ingested verbatim plus a relative session -> sessions/<id> symlink"

[ -f "$M3/daily-meeting-2026-03-10--12:30.mkv" ] || fail "legacy: the original mkv name was not preserved"
[ -L "$M3/recording.mkv" ] || fail "legacy: the uniform recording.mkv read path is missing"
[ "$(readlink "$M3/recording.mkv")" = "daily-meeting-2026-03-10--12:30.mkv" ] \
  || fail "legacy: recording.mkv points at $(readlink "$M3/recording.mkv")"
[ "$(jq -r .room_source "$M3/ARCHIVE/meeting.json")" = mkv-title-tag ] \
  || fail "legacy: the room token did not come from the mkv title tag"
[ "$(jq -r .anchor_zone_proven "$M3/ARCHIVE/meeting.json")" = false ] \
  || fail "legacy: an unproven wall-clock zone was recorded as proven"
ok "bare legacy mkv: original name kept, room read from the title tag, no false Z"

# every meeting verifies against its own manifest
for m in "$M1" "$M2" "$M3" "$M4" "$M5"; do
  ( cd "$m" && sha256sum -c --quiet ARCHIVE/MANIFEST.sha256 ) \
    || fail "MANIFEST.sha256 does not verify in $(basename "$m")"
done
ok "sha256sum -c ARCHIVE/MANIFEST.sha256 passes in all 5 bundles"

# =============================================================================
section "3. artifact-remux-work is excluded, counted, and left in place"
# =============================================================================
[ ! -e "$M2/recording-segments-1/artifact-remux-work" ] \
  || fail "artifact-remux-work was COPIED into the archive"
[ -f "$M2/recording-segments-1/segment-000.mkv" ] \
  || fail "other content under recording-segments-* was excluded too — only the scratch dir should be"
grep -q 'artifact-remux-work/remux-scratch.mkv' "$M2/ARCHIVE/EXCLUDED.tsv" \
  || fail "the excluded scratch file is not named in ARCHIVE/EXCLUDED.tsv"
grep -q 'orphan-remux-scratch' "$M2/ARCHIVE/EXCLUDED.tsv" \
  || fail "ARCHIVE/EXCLUDED.tsv does not carry the exclusion reason"
EXC_SUM=$(awk -F'\t' '{n+=$2} END{print n+0}' "$M2/ARCHIVE/EXCLUDED.tsv")
[ "$EXC_SUM" = "$REMUX_BYTES" ] \
  || fail "EXCLUDED.tsv accounts for $EXC_SUM B, the scratch dir holds $REMUX_BYTES B"
[ "$(jq -r .excluded_bytes "$M2/ARCHIVE/meeting.json")" = "$REMUX_BYTES" ] \
  || fail "meeting.json excluded_bytes != $REMUX_BYTES"
grep -q 'artifact-remux-work' "$ARCHIVE/state/EXCLUDED.tsv" \
  || fail "the exclusion is not recorded in the archive-wide state/EXCLUDED.tsv"
[ -f "$CRON_FAILED/recording-segments-1/artifact-remux-work/remux-scratch.mkv" ] \
  || fail "the scratch dir was DELETED from the source — that is a separate human decision"
[ "$(stat -c %a "$CRON_FAILED/recording-segments-1/artifact-remux-work")" = 700 ] \
  || fail "the source scratch dir's mode was changed"
ok "$REMUX_BYTES B of remux scratch excluded, byte-counted twice, still in the source at 0700"

# byte conservation: copied + excluded == every regular-file byte in the source
SRC_FILE_BYTES=$(find "$CRON_FAILED" -type f -printf '%s\n' | awk '{n+=$1} END{print n+0}')
MJ="$M2/ARCHIVE/meeting.json"
[ $(( $(jq -r .bytes_total "$MJ") + $(jq -r .excluded_bytes "$MJ") )) = "$SRC_FILE_BYTES" ] \
  || fail "byte conservation broken: bytes_total + excluded_bytes != $SRC_FILE_BYTES"
ok "byte conservation holds: bytes_total + excluded_bytes == the source's file bytes"

# The other two exclusion rules: a dot-prefixed operator transient and a *.backup.
[ ! -e "$M1/.nfs-scratch" ] || fail "a dot-prefixed operator transient was copied into the archive"
[ ! -e "$M1/recording.mkv.backup" ] || fail "a *.backup was copied into the archive"
grep -q 'operator-transient-dotfile' "$M1/ARCHIVE/EXCLUDED.tsv" \
  || fail "the excluded dotfile is not named with its reason in ARCHIVE/EXCLUDED.tsv"
grep -q 'operator-transient-backup' "$M1/ARCHIVE/EXCLUDED.tsv" \
  || fail "the excluded *.backup is not named with its reason in ARCHIVE/EXCLUDED.tsv"
[ "$(jq -r .excluded_bytes "$M1/ARCHIVE/meeting.json")" = "$READY_EXCL_BYTES" ] \
  || fail "meeting.json accounts for $(jq -r .excluded_bytes "$M1/ARCHIVE/meeting.json") excluded B, expected $READY_EXCL_BYTES"
[ -f "$CRON_READY/.nfs-scratch" ] && [ -f "$CRON_READY/recording.mkv.backup" ] \
  || fail "an excluded operator transient was deleted from the source"
ok "dot-prefixed and *.backup transients are excluded, byte-counted, and left in the source"

# =============================================================================
section "5. the two concurrent same-date captures both land, distinctly"
# =============================================================================
{ [ -d "$M1" ] && [ -d "$M4" ]; } || fail "the two 2026-07-31 captures did not both land"
[ "$M1" != "$M4" ] || fail "the two concurrent captures collapsed into one directory"
[ "$(jq -r .recorder_display "$M1/ARCHIVE/meeting.json")" = CodemyriadRecorder ] \
  || fail "cron capture lost its recorder_identity.display"
[ "$(jq -r .recorder_display "$M4/ARCHIVE/meeting.json")" = CassiniRecorder ] \
  || fail "exapp capture lost its recorder_identity.display"
[ "$(jq -r .era "$M1/ARCHIVE/meeting.json")" = cron ] || fail "wrong era on the cron capture"
[ "$(jq -r .era "$M4/ARCHIVE/meeting.json")" = exapp ] || fail "wrong era on the exapp capture"
C1=$(jq -r 'select(.dir=="meetings/'"$CRON_READY_DIR"'") | .concurrent_with[]?' "$ARCHIVE/index.jsonl")
C4=$(jq -r 'select(.dir=="meetings/'"$EXAPP_DIR"'") | .concurrent_with[]?' "$ARCHIVE/index.jsonl")
[ "$C1" = "2026-07-31T103001Z--mczuc3mb--exapp" ] \
  || fail "the cron capture is not linked to its concurrent twin (got '$C1')"
[ "$C4" = "2026-07-31T103012Z--mczuc3mb--cron" ] \
  || fail "the exapp capture is not linked to its concurrent twin (got '$C4')"
[ "$(find "$ARCHIVE/by-date/2026-07-31" -mindepth 1 -maxdepth 1 | wc -l)" = 2 ] \
  || fail "by-date/2026-07-31 does not list both captures"
ok "both 2026-07-31 captures archived under distinct names and cross-linked via concurrent_with"

# --- derived attachment ------------------------------------------------------
section "5b. derived .meeting / .opus bundles attach to their capture"
[ -d "$M4/derived/$JOB_OK.meeting" ] \
  || fail "the ULID .meeting did not attach to its .run capture (check unattributed/derived/)"
[ -f "$M4/derived/$JOB_OK.opus" ] || fail "the .opus did not attach"
[ "$(jq -r .opus_attached "$M4/ARCHIVE/meeting.json")" = true ] || fail "opus_attached is not true"
[ "$(jq -r .state "$M4/ARCHIVE/meeting.json")" = complete ] \
  || fail "the exapp meeting did not flip to state=complete once its .meeting attached"
[ -d "$M3/derived/daily-meeting-2026-03-10--12:30.meeting" ] \
  || fail "the imported .meeting did not attach to the legacy mkv it was built from"
[ -d "$M1/derived/daily-meeting-2026-07-31.meeting" ] \
  || fail "the .meeting whose source_path is /recordings/<dir>/recording.mkv did not attach to its cron capture — this is the shape 45 of the 77 real imports have"
# The attach itself happened in an earlier run, so look across every log.
grep -qh 'component="daily-meeting-2026-07-31.meeting" method=source-parent-dir' "$LOGS"/*.log \
  || fail "the /recordings/<dir>/recording.mkv import did not resolve through its parent directory"
[ -d "$M2/derived/2026-04-15-recovered.meeting" ] \
  || fail "the /work/work/<date>-recovered.mkv import did not attach by date-uniqueness"
grep -q '2026-04-15-recovered.meeting' "$ARCHIVE/state/attach-low-confidence.tsv" \
  || fail "a date-unique attach was not recorded as low confidence"
[ -z "$(ls -A "$ARCHIVE/unattributed/derived")" ] \
  || fail "something landed in unattributed/derived/: $(ls "$ARCHIVE/unattributed/derived")"
[ -L "$ARCHIVE/viewer/daily-standup-meeting-exapp--20260731T103001.meeting" ] \
  || fail "the viewer symlink farm has no entry for the exapp meeting"
[ -d "$ARCHIVE/viewer/daily-standup-meeting-exapp--20260731T103001.meeting" ] \
  || fail "the viewer symlink does not resolve to the attached .meeting bundle"
compgen -G "$ARCHIVE/viewer/*:*" >/dev/null && fail "a viewer symlink name contains a colon"
ok "ULID + imported .meeting and the .opus attached; viewer/ symlinks resolve, colon-free"

# --- the hpb era, and the .mjr that has no derived/ to sit next to -----------
section "5c. the hpb era ingests and its .mjr attaches under janus-mjr/"
[ -f "$M5/Recording-mrzd4477-2026-06-10_13-44-56_850364.mkv" ] \
  || fail "the hpb recording was not ingested under its original name"
[ -f "$M5/Recording-mrzd4477-2026-06-10_13-44-56_850364.json" ] \
  || fail "the hpb sidecar .json was not ingested alongside the mkv"
[ "$(readlink "$M5/recording.mkv")" = "Recording-mrzd4477-2026-06-10_13-44-56_850364.mkv" ] \
  || fail "hpb: the uniform recording.mkv read path is missing"
[ "$(jq -r .room "$M5/ARCHIVE/meeting.json")" = mrzd4477 ] || fail "hpb: room token not read from the filename"
[ "$(jq -r .anchor_kind "$M5/ARCHIVE/meeting.json")" = recording-finalized ] \
  || fail "hpb: the anchor is not labelled as the finalize time"
# The one shape that reaches refresh_manifest with NO derived/ directory: `find
# derived` exits 1 there, and under pipefail + set -e that killed the whole run
# after MANIFEST.sha256 had been rewritten but before meeting.json, index.jsonl
# or health.json existed.
[ -f "$M5/janus-mjr/videoroom-$MJR_US-audio-0.mjr" ] \
  || fail "the .mjr did not attach under janus-mjr/ (all 7 real .mjr land on a bundle with no derived/)"
[ ! -d "$M5/derived" ] || fail "the hpb bundle grew a derived/ it should not have"
[ "$(jq -r '.derived | length' "$M5/ARCHIVE/meeting.json")" = 0 ] \
  || fail "the hpb meeting.json claims a derived component it does not have"
( cd "$M5" && sha256sum -c --quiet ARCHIVE/MANIFEST.sha256 ) \
  || fail "the hpb bundle does not verify after the .mjr attach"
grep -q "janus-mjr/videoroom-$MJR_US-audio-0.mjr" "$M5/ARCHIVE/MANIFEST.sha256" \
  || fail "the attached .mjr is not listed in MANIFEST.sha256 — sha256sum -c only checks what it lists"
[ -z "$(ls -A "$ARCHIVE/unattributed/hpb-mjr")" ] || fail "the .mjr landed in unattributed/hpb-mjr/"
ok "hpb mkv + sidecar ingested, the .mjr attached under janus-mjr/ and reached MANIFEST.sha256"

# =============================================================================
section "9. in-progress and current/.staging are never ingested"
# =============================================================================
meetings_list | grep -q -- "--exapp--ivan" && fail "the in-progress (state=preparing) .run was ingested"
grep -q "$JOB_WIP" "$ARCHIVE/state/pending.tsv" || fail "the in-progress .run was not recorded as deferred"
grep -q "G2:cassini.json kind=run state=preparing" "$ARCHIVE/state/pending.tsv" \
  || fail "the deferral does not name the gate that failed (G2)"
ok "a .run whose cassini.json says state=preparing is deferred by G2, naming the gate"

meetings_list | grep -q -- "2026-08-20T100000Z" \
  && fail "a .run whose job row has record_finished_at NULL was ingested — G3 phase 1 was bypassed"
grep -q "$JOB_NULLCOL.*G3:record_finished_at is null" "$ARCHIVE/state/pending.tsv" \
  || fail "the NULL-record_finished_at .run was not deferred by G3 (a NULL room_name shifted the columns)"
ok "G3 phase 1 still sees record_finished_at as NULL when an earlier column is NULL too"

if grep -rq "$JOB_STAGED" "$ARCHIVE/meetings" "$ARCHIVE/index.jsonl" 2>/dev/null; then
  fail "content from current/.staging (or a dot-prefixed .run) reached the archive"
fi
grep -q "$JOB_STAGED" "$ARCHIVE/state/pending.tsv" 2>/dev/null \
  && fail "current/.staging was traversed at all — it must be invisible, not merely deferred"
ok "current/.staging/ and dot-prefixed entries are invisible to the pass"

# =============================================================================
section "10. index.jsonl is valid NDJSON, one complete row per meeting"
# =============================================================================
IDX="$ARCHIVE/index.jsonl"
[ -f "$IDX" ] || fail "index.jsonl was not written"
while IFS= read -r line; do
  printf '%s' "$line" | jq -e . >/dev/null || fail "index.jsonl has a line that is not valid JSON"
done < "$IDX"
ROWS=$(wc -l < "$IDX")
[ "$ROWS" = 6 ] || fail "index.jsonl has $ROWS rows, expected 5 meetings + 1 job-only row"
[ "$(jq -rs 'map(select(.dir != null)) | length' "$IDX")" = 5 ] \
  || fail "index.jsonl does not carry exactly 5 meeting rows"
[ "$(jq -rs 'map(.key) | unique | length' "$IDX")" = 6 ] || fail "index.jsonl keys are not unique"
[ "$(jq -rs 'map(select(.schema != "cassini.archive.index.v1")) | length' "$IDX")" = 0 ] \
  || fail "an index row carries the wrong schema tag"
# every documented field present on every row, explicit null rather than absent
FIELDS='["schema","id","key","dir","era","recorder_display","room","room_source","room_name","owner","anchor_utc","anchor_kind","anchor_zone_proven","started_at_utc","started_at_source","start_precision","tz_hypothesis","ended_at_utc","duration_s","date_local","capture_state","job_id","job_state","job_stage","attempt","source_root","source_layout","source_paths","source_present","source_fingerprint","has_raw","raw_kind","rtplog_count","raw_bytes","has_mix","mix_bytes","media","derived","opus_attached","buildable","remuxable","concurrent_with","bytes_total","files_total","excluded_bytes","excluded_files","sha256_manifest","last_verified_utc","copy_mode","state","notes","ingested_at_utc","ingest_run_id","tool_sha256"]'
MISSING=$(jq -rs --argjson f "$FIELDS" 'map(. as $r | $f - ($r|keys)) | add | unique | join(",")' "$IDX")
[ -z "$MISSING" ] || fail "index.jsonl rows are missing documented fields: $MISSING"
# every meeting row points at a directory that exists and has a manifest
while IFS= read -r d; do
  [ -d "$ARCHIVE/$d" ] || fail "index row points at a missing directory: $d"
  [ -f "$ARCHIVE/$d/ARCHIVE/meeting.json" ] || fail "$d has no ARCHIVE/meeting.json"
done < <(jq -r 'select(.dir != null) | .dir' "$IDX")
[ "$(jq -rs 'map(select(.dir == null) | .job_id)[0]' "$IDX")" = "$JOB_NOMEDIA" ] \
  || fail "the zero-media job did not get its own index row — a recorded loss is data"
# The zero-media job's artifact_run_path is NULL, i.e. an empty column in the
# middle of the operator-DB select. If those collapse, every later column shifts
# left: the run path becomes the room token and this row disappears entirely.
[ "$(jq -rs 'map(select(.dir == null) | .room)[0]' "$IDX")" = btsq78i8 ] \
  || fail "the job-only row's room is wrong — a NULL column shifted the operator-DB row"
[ "$(jq -rs 'map(select(.dir == null) | .owner)[0]' "$IDX")" = silviot ] \
  || fail "the job-only row's owner is wrong — a NULL column shifted the operator-DB row"
# record_finished_at for the zero-media job is a WHOLE-SECOND RFC3339 stamp
# (Go's RFC3339Nano drops trailing zeros, so producers do emit these).
# truncate_second used to re-append the Z it had never stripped, putting
# "2026-06-17T22:38:24ZZ" into anchor_utc, ended_at_utc and the directory name.
JOBROW=$(jq -rs 'map(select(.dir == null))[0]' "$IDX")
[ "$(printf '%s' "$JOBROW" | jq -r .anchor_utc)" = "2026-06-17T22:38:24Z" ] \
  || fail "the job-only row's anchor_utc is $(printf '%s' "$JOBROW" | jq -r .anchor_utc), expected 2026-06-17T22:38:24Z"
[ "$(printf '%s' "$JOBROW" | jq -r .ended_at_utc)" = "2026-06-17T22:38:24Z" ] \
  || fail "the job-only row's ended_at_utc carries a doubled Z"
# ... and its date_local is the LOCAL date, exactly like every meeting row
[ "$(printf '%s' "$JOBROW" | jq -r .date_local)" \
  = 2026-06-18 ] \
  || fail "the job-only row's date_local is $(printf '%s' "$JOBROW" | jq -r .date_local): a UTC date, while every meeting row uses the local one"
[ -f "$IDX.sha256" ] || fail "index.jsonl.sha256 was not written"
( cd "$ARCHIVE" && sha256sum -c --quiet index.jsonl.sha256 ) || fail "index.jsonl.sha256 does not verify"
ok "6 rows, valid NDJSON, all 54 documented fields on every row, every dir resolves"

# --check must agree with what is on disk
run_sync -- --check
[ "$LAST_RC" = 0 ] || fail "--check on a consistent archive exited $LAST_RC"
log_has 'event=index.check ok=true' || fail "--check did not confirm the index"
printf '{"schema":"cassini.archive.index.v1"}\n' >> "$IDX"
run_sync -- --check
[ "$LAST_RC" = 5 ] || fail "--check did not fail on a drifted index (exit $LAST_RC)"
log_has 'event=index.check ok=false' || fail "--check did not report index drift"
run_sync -- --apply --rebuild-index
[ "$LAST_RC" = 0 ] || fail "--rebuild-index exited $LAST_RC"
[ "$(wc -l < "$IDX")" = 6 ] || fail "--rebuild-index did not restore the index wholesale"
ok "--check detects a drifted index (exit 5); --rebuild-index restores it wholesale"

# =============================================================================
section "6. re-running is a no-op"
# =============================================================================
MEET_BEFORE="$(meetings_digest)"
IDX_BEFORE="$(sha256sum < "$IDX")"
LEDGER_BEFORE="$(cat "$ARCHIVE/state/ingested.tsv")"
run_sync -- --apply --all
[ "$LAST_RC" = 0 ] || fail "the second run exited $LAST_RC"
log_has 'event=ingest.skip' || fail "the second run did not report an already-ingested bundle"
[ "$(grep -c 'event=ingest.skip .*reason=unchanged' "$LAST_LOG")" = 5 ] \
  || fail "expected 5 'already ingested / unchanged' skips, got $(grep -c 'event=ingest.skip' "$LAST_LOG")"
log_lacks 'event=ingest.done' || fail "the second run copied a bundle again"
[ "$(meetings_digest)" = "$MEET_BEFORE" ] || {
  diff <(printf '%s\n' "$MEET_BEFORE") <(meetings_digest) >&2 || true
  fail "the second run changed meetings/ (diff above)"
}
[ "$(sha256sum < "$IDX")" = "$IDX_BEFORE" ] || fail "the second run changed index.jsonl"
[ "$(cat "$ARCHIVE/state/ingested.tsv")" = "$LEDGER_BEFORE" ] || fail "the second run appended to the ledger"
ok "second --apply --all: 5 unchanged-skips, zero copies, meetings/ and index byte-identical"

# The fingerprint is over (relpath, size, mtime). Rendering the mtime in the
# CALLER's timezone made a run under a different TZ report EVERY already-ingested
# source as source-mutated — a corpus-wide false conflict that looks exactly like
# the alarm you most want to trust.
run_sync TZ=Australia/Sydney -- --apply --all
[ "$LAST_RC" = 0 ] || fail "a run under a different TZ exited $LAST_RC (expected 0)"
[ "$(grep -c 'event=ingest.skip .*reason=unchanged' "$LAST_LOG")" = 5 ] \
  || fail "a different TZ changed the source fingerprints: $(grep -c 'event=conflict' "$LAST_LOG") conflict(s)"
log_lacks 'source-mutated' || fail "a TZ change was reported as a mutated source"
[ "$(meetings_digest)" = "$MEET_BEFORE" ] || fail "the TZ run changed meetings/"
ok "the source fingerprint is timezone-independent: a run under another TZ is still a no-op"

# =============================================================================
section "7. a half-committed meeting (dir present, ledger line lost) is repaired"
# =============================================================================
INO_BEFORE=$(stat -c %i "$M1/recording.mkv")
grep -v "^2026-07-31T10:30:12Z|mczuc3mb|cron" "$ARCHIVE/state/ingested.tsv" > "$T/ledger.tmp"
mv "$T/ledger.tmp" "$ARCHIVE/state/ingested.tsv"
[ "$(wc -l < "$ARCHIVE/state/ingested.tsv")" = 4 ] || fail "could not remove the ledger line for the test"
run_sync -- --apply --all
[ "$LAST_RC" = 0 ] || fail "the ledger-repair run exited $LAST_RC"
log_has 'event=ingest.ledger_repaired' || fail "the missing ledger line was not repaired"
log_lacks 'event=ingest.done' || fail "the ledger repair RE-COPIED the bundle instead of re-deriving the line"
[ "$(stat -c %i "$M1/recording.mkv")" = "$INO_BEFORE" ] \
  || fail "recording.mkv changed inode: the bundle was copied again rather than re-ledgered"
[ "$(wc -l < "$ARCHIVE/state/ingested.tsv")" = 5 ] || fail "the ledger line was not appended back"
[ "$(meetings_digest)" = "$MEET_BEFORE" ] || fail "the ledger repair modified meetings/"
ok "dir present + ledger line missing: line re-derived, same inode, nothing re-copied"

# A staging leftover from a crash before the commit rename is swept, not resumed.
mkdir -p "$ARCHIVE/meetings/.staging/$CRON_READY_DIR.partial/session"
echo garbage > "$ARCHIVE/meetings/.staging/$CRON_READY_DIR.partial/recording.mkv"
run_sync -- --apply --all
[ "$LAST_RC" = 0 ] || fail "the staging-sweep run exited $LAST_RC"
log_has 'event=staging.sweep' || fail "a .partial staging leftover was not swept"
[ -z "$(ls -A "$ARCHIVE/meetings/.staging")" ] || fail ".staging is not empty after the sweep"
[ "$(meetings_digest)" = "$MEET_BEFORE" ] || fail "the staging sweep disturbed a committed meeting"
ok "a crashed .partial is discarded and re-derived from source, never resumed"

# A meeting directory with no ARCHIVE/meeting.json is quarantined, not trusted.
mkdir -p "$ARCHIVE/meetings/2026-01-01T000000Z--zzzzzzzz--cron--bogus"
echo x > "$ARCHIVE/meetings/2026-01-01T000000Z--zzzzzzzz--cron--bogus/recording.mkv"
run_sync -- --apply --all
[ "$LAST_RC" = 5 ] || fail "an orphan meeting dir did not fail the run (exit $LAST_RC)"
log_has 'event=orphan.quarantined' || fail "the orphan directory was not quarantined"
QUAR=( "$ARCHIVE"/quarantine/*--bogus.orphan-* )
[ -d "${QUAR[0]:-/nonexistent}" ] || fail "the orphan is not in quarantine/"
[ ! -d "$ARCHIVE/meetings/2026-01-01T000000Z--zzzzzzzz--cron--bogus" ] || fail "the orphan is still in meetings/"
[ -f "${QUAR[0]}/recording.mkv" ] || fail "the orphan's content was deleted instead of preserved"
ok "a meeting dir without ARCHIVE/meeting.json is quarantined (content kept) and fails the run"

# =============================================================================
section "8. the source trees are never mutated"
# =============================================================================
SRC_AFTER="$(tree_digest "$SRC_OLD")$(tree_digest "$SRC_HPB")$(tree_digest "$SRC_EXA")$(sha256sum "$DB")"
[ "$SRC_BEFORE" = "$SRC_AFTER" ] || {
  diff <(tree_digest "$SRC_OLD"; tree_digest "$SRC_EXA") /dev/null >&2 || true
  fail "a source tree changed across the whole suite (names, modes, sizes, mtimes or content)"
}
ok "recordings/, hpb-talk-recordings/, jobs/ and jobs.sqlite3 are byte-identical after 8 runs"

# A directory that EXISTS but is not a prepared subvolume: the previous version
# of this check pointed at a path that did not exist at all, so it tripped
# preflight.missing_archive_root and never exercised `btrfs subvolume show`.
NOTSUBVOL="$T/not-a-subvolume"
mkdir -p "$NOTSUBVOL"
run_sync ARCHIVE_ROOT="$NOTSUBVOL" -- --apply --backfill-old
[ "$LAST_RC" = 3 ] || fail "an archive root that is not a btrfs subvolume exited $LAST_RC, expected 3"
log_has 'event=preflight.not_a_subvolume' \
  || fail "the refusal did not name the missing subvolume (it may have failed for another reason)"
[ -z "$(find "$NOTSUBVOL" -mindepth 1 2>/dev/null)" ] || fail "it wrote into a non-subvolume archive root"
ok "an archive root that exists but is not a btrfs subvolume is refused (exit 3), nothing written"

# The write guard is structural: a room token carrying ../ resolves outside the
# writable roots and must abort the run, not escape it.
mkdir -p "$T/nul/hpb"
TRAV="$T/traversal"
mkdir -p "$TRAV/src/recordings/daily-meeting-2026-02-02/session/streams" "$TRAV/archive"
: > "$TRAV/archive/.btrfs-subvol"
echo '{"kind":"recording","state":"ready"}' > "$TRAV/src/recordings/daily-meeting-2026-02-02/cassini.json"
blob "$TRAV/src/recordings/daily-meeting-2026-02-02/recording.mkv" 2 a
session_json "$TRAV/src/recordings/daily-meeting-2026-02-02/session/session.json" \
  '../../../../../../escaped' CodemyriadRecorder 2026-02-02T08:00:00.000000000Z
run_sync ARCHIVE_ROOT="$TRAV/archive" SRC_OLD="$TRAV/src/recordings" SRC_HPB="$T/nul/hpb" \
  EXPECT_CRON=1 EXPECT_LEGACY=0 EXPECT_HPB=0 -- --apply --backfill-old
[ "$LAST_RC" = 5 ] || fail "a room token containing ../ exited $LAST_RC, expected the guard's 5"
log_has 'event=guard.violation' || fail "guard_write() did not refuse a path outside the writable roots"
log_has 'reason=outside-writable-roots' || fail "the guard violation does not say why"
if [ -e "$T/escaped" ] || [ -e "$TRAV/escaped" ]; then fail "a path traversal escaped the archive root"; fi
ok "a source whose room token contains ../ is stopped by guard_write (exit 5), nothing escapes"

# =============================================================================
section "13. deferral is re-evaluated on a state change, never on a sleep"
# =============================================================================
# Flip the fixture's own readiness predicate and give the job a finished
# recording. Nothing waits; the next pass simply sees a different fact.
echo '{"kind":"run","state":"ready"}' > "$RUN_WIP/cassini.json"
python3 - "$DB" "$JOB_WIP" <<'PY'
import sqlite3, sys
con = sqlite3.connect(sys.argv[1])
con.execute("update jobs set stage='done', state='succeeded', "
            "record_finished_at='2026-08-27T11:02:00Z', completed_at='2026-08-27T11:10:00Z' "
            "where id=?", (sys.argv[2],))
con.commit()
PY
run_sync EXPECT_EXAPP=2 -- --apply --all
[ "$LAST_RC" = 0 ] || fail "the post-flip run exited $LAST_RC"
[ -d "$ARCHIVE/meetings/$WIP_DIR" ] \
  || fail "the bundle was not ingested after its cassini.json flipped to ready (want $WIP_DIR)"
[ "$(jq -r .room_name "$ARCHIVE/meetings/$WIP_DIR/ARCHIVE/meeting.json")" = "Ivan" ] \
  || fail "the talk_binding room_name did not reach the row"
[ "$(meetings_list | wc -l)" = 6 ] || fail "the flip produced $(meetings_list | wc -l) meetings, expected 6"
ok "state=preparing -> ready flips a deferred bundle into the archive on the very next pass"

# =============================================================================
section "14. a mutated source is a conflict; the archived copy is never healed"
# =============================================================================
ARCH_BEFORE="$(tree_digest "$ARCHIVE/meetings/$CRON_READY_DIR")"
EV="$CRON_READY/session/events.ndjson"
EV_MTIME="$(stat -c %y "$EV")"
echo 'tampered' >> "$EV"
run_sync EXPECT_EXAPP=2 -- --apply --all
[ "$LAST_RC" = 5 ] || fail "a mutated ingested source did not fail the run (exit $LAST_RC)"
log_has 'event=conflict' || fail "no conflict was recorded for the mutated source"
grep -q 'source-mutated' "$ARCHIVE/state/conflicts.tsv" || fail "conflicts.tsv has no source-mutated row"
[ "$(tree_digest "$ARCHIVE/meetings/$CRON_READY_DIR")" = "$ARCH_BEFORE" ] \
  || fail "the archived copy was overwritten to match the mutated source"
# restore both the bytes and the mtime: the fingerprint is over (relpath, size,
# mtime), so a content-only restore would leave the source looking mutated.
printf '{"event":"stream_opened"}\n' > "$EV"
touch -m -d "$EV_MTIME" "$EV"
ok "a changed fingerprint on an ingested source is a conflict (exit 5), never a silent heal"

# =============================================================================
section "15. the detectors actually fail the run"
# =============================================================================
# Channel 3: no new capture for MAX_QUIET_WEEKDAYS weekdays fails the run even
# though the ingest itself worked perfectly. This is the 2026-08-03/04/05 loss.
# The threshold is derived from the real gap between the newest fixture capture
# and today's date, so the assertion holds whatever day the suite runs on.
GAP=$(python3 -c 'import sys,datetime
a=datetime.date.fromisoformat("2026-08-27"); b=datetime.date.today()
n=0; d=a
while d<b:
    d+=datetime.timedelta(days=1)
    if d.weekday()<5: n+=1
print(n)')
if [ "$GAP" -lt 1 ]; then
  echo "  --  skipped: this machine's clock is at or before the newest fixture capture"
else
  run_sync EXPECT_EXAPP=2 EXPECTED_QUIET_UNTIL= MAX_QUIET_WEEKDAYS="$GAP" -- --apply --sync-new
  [ "$LAST_RC" = 6 ] || fail "the upstream-quiet detector did not fail the run (exit $LAST_RC, expected 6)"
  log_has 'no new capture in' || fail "the quiet alarm text is missing"
  QW=$(jq -r .quiet_weekdays "$ARCHIVE/state/health.json")
  [ "$QW" = "$GAP" ] \
    || fail "health.json quiet_weekdays is '$QW', expected $GAP — a log line leaked into the value"
  ok "channel 3: a working pass that sees no new capture for $GAP weekdays exits 6 and records it"

  run_sync EXPECT_EXAPP=2 EXPECTED_QUIET_UNTIL= MAX_QUIET_WEEKDAYS=$((GAP + 1)) -- --apply --sync-new
  [ "$LAST_RC" = 0 ] || fail "a gap below the threshold still failed the run (exit $LAST_RC)"
  log_has "event=quiet.ok quiet_weekdays=$GAP" || fail "a below-threshold gap was not reported as ok"
  ok "one weekday below the threshold it stays green — the alarm is a threshold, not a tripwire"

  run_sync EXPECT_EXAPP=2 EXPECTED_QUIET_UNTIL=2099-12-31 MAX_QUIET_WEEKDAYS="$GAP" -- --apply --sync-new
  [ "$LAST_RC" = 0 ] || fail "EXPECTED_QUIET_UNTIL did not suppress the quiet alarm (exit $LAST_RC)"
  log_has 'event=quiet.suppressed' || fail "the suppression was not logged"
  ok "EXPECTED_QUIET_UNTIL suppresses it deliberately and on the record"
fi

# Reverse reconciliation: something started deleting from the source trees.
mv "$RUN_OK" "$T/hidden.run"
run_sync EXPECT_EXAPP=2 -- --apply --sync-new
[ "$LAST_RC" = 5 ] || fail "a vanished source did not fail the run (exit $LAST_RC, expected 5)"
log_has 'event=source.vanished' || fail "the vanished source was not reported"
[ "$(jq -r .vanished_sources "$ARCHIVE/state/health.json")" = 1 ] \
  || fail "health.json does not count the vanished source"
[ -d "$ARCHIVE/meetings/$EXAPP_DIR" ] || fail "the archive dropped a meeting whose source vanished"
mv "$T/hidden.run" "$RUN_OK"
ok "a source that disappears is detected, counted in health.json, and fails the run (exit 5)"

# health.json + the off-host heartbeat
run_sync EXPECT_EXAPP=2 -- --apply --all
[ "$LAST_RC" = 0 ] || fail "the final clean run exited $LAST_RC"
H="$ARCHIVE/state/health.json"
jq -e . "$H" >/dev/null || fail "health.json is not valid JSON"
[ "$(jq -r .last_run_status "$H")" = ok ] || fail "a clean run did not report last_run_status=ok"
[ "$(jq -r .meetings_total "$H")" = 6 ] || fail "health.json meetings_total is wrong"
[ "$(jq -r '.assertions_failed | length' "$H")" = 0 ] || fail "a clean run left failed assertions"
cmp -s "$H" "$EXA_DATA/operator/backups/cassini-archive-health.json" \
  || fail "the off-host heartbeat was not written to operator/backups/ (the only channel that leaves george)"
ok "health.json is valid, green, and mirrored to the operator/backups/ heartbeat path"

# an unacknowledged ALARM cannot be erased by a successful run
echo 'cassini-archive-ingest.service failed at 2026-08-28T00:00:00Z' > "$T/var/ALARM"
run_sync EXPECT_EXAPP=2 -- --apply --all
[ "$LAST_RC" = 7 ] || fail "a successful run reported green over an unacknowledged ALARM (exit $LAST_RC)"
log_has 'event=alarm.unacknowledged' || fail "the ALARM was not surfaced"
run_sync EXPECT_EXAPP=2 -- --apply --ack
[ "$LAST_RC" = 0 ] || fail "--ack exited $LAST_RC"
[ ! -f "$T/var/ALARM" ] || fail "--ack did not clear the ALARM"
compgen -G "$T/var/ALARM.acked-*" >/dev/null || fail "--ack deleted the alarm instead of filing it"
ok "an unacknowledged ALARM forces exit 7 until an explicit --ack files it away"

# snapshots
SNAPS=$(find "$T/snapshots" -mindepth 1 -maxdepth 1 -type d | wc -l)
[ "$SNAPS" -ge 1 ] || fail "no read-only snapshot was ever taken"
NEWEST=$(find "$T/snapshots" -mindepth 1 -maxdepth 1 -type d -printf '%P\n' | LC_ALL=C sort | tail -1)
[ -f "$T/snapshots/$NEWEST/index.jsonl" ] || fail "the snapshot does not contain index.jsonl"
ok "read-only snapshots are taken and carry the index ($SNAPS present)"

# =============================================================================
section "16. the reflink probe clones from INSIDE the archive"
# =============================================================================
# The probe used to be written into $WORKTMP — `mktemp -d`, i.e. /tmp, which on
# george is tmpfs (st_dev 38) while /mnt/data is btrfs (st_dev 48). FICLONE is
# EXDEV across filesystems and unimplemented on tmpfs, so that probe could only
# fail: every --apply run died in preflight claiming the ARCHIVE could not
# reflink, and pointed the operator at ALLOW_FULL_COPY=1 — the switch that turns
# the whole 123.6 GiB backfill into a real copy.
ARCH_PROBE="$T/archive-probe"
mkdir -p "$ARCH_PROBE"; : > "$ARCH_PROBE/.btrfs-subvol"
CPLOG="$LOGS/cp-probe.log"; : > "$CPLOG"
run_sync ARCHIVE_ROOT="$ARCH_PROBE" FAKE_CP_LOG="$CPLOG" -- --apply --backfill-old
[ "$LAST_RC" = 0 ] || fail "the probe run exited $LAST_RC"
grep -q -- "--reflink=always $ARCH_PROBE/state/.reflink-probe-src" "$CPLOG" \
  || fail "the reflink probe does not clone from a source inside the archive root: $(grep -- --reflink=always "$CPLOG" | head -2)"
grep -q -- '--reflink=always /tmp/cassini-archive\.' "$CPLOG" \
  && fail "the reflink probe still clones out of the scratch /tmp dir — cross-filesystem, so it can never succeed on george"
[ -z "$(find "$ARCH_PROBE" -maxdepth 2 -name '.reflink-probe-src-*' -o -maxdepth 1 -name '.probe-*' 2>/dev/null)" ] \
  || fail "the probe left files behind in the archive"
ok "the reflink probe clones archive->archive and cleans up after itself"

# =============================================================================
section "17. the run debounce is opt-in, so a hand-run pass is never skipped"
# =============================================================================
# The documented backfill is two commands seconds apart. With the debounce
# applied to every --apply run, the second one hit MIN_RUN_INTERVAL and exited 0
# having archived nothing — 43 ExApp captures silently skipped, green.
run_sync EXPECT_EXAPP=2 MIN_RUN_INTERVAL=300 -- --apply --sync-new
[ "$LAST_RC" = 0 ] || fail "the first of the two back-to-back passes exited $LAST_RC"
run_sync EXPECT_EXAPP=2 MIN_RUN_INTERVAL=300 -- --apply --sync-new
[ "$LAST_RC" = 0 ] || fail "the second back-to-back pass exited $LAST_RC"
log_lacks 'event=run.debounced' \
  || fail "a second hand-run pass was silently debounced — this is how the documented backfill skipped the whole ExApp era"
log_has 'event=pass.start pass=sync-new' || fail "the second pass did not actually run the ingest"
ok "two --apply passes seconds apart both run: nothing is silently skipped"

run_sync EXPECT_EXAPP=2 MIN_RUN_INTERVAL=300 -- --apply --sync-new --debounce
[ "$LAST_RC" = 0 ] || fail "--debounce exited $LAST_RC"
log_has 'event=run.debounced' || fail "--debounce did not debounce"
log_lacks 'event=pass.start' || fail "--debounce ran the pass anyway"
run_sync EXPECT_EXAPP=2 MIN_RUN_INTERVAL=300 -- --apply --sync-new --debounce --force
[ "$LAST_RC" = 0 ] || fail "--debounce --force exited $LAST_RC"
log_lacks 'event=run.debounced' || fail "--force did not override --debounce"
ok "--debounce (what the .path unit passes) skips; --force overrides it"

# =============================================================================
section "18. acceptance counts are asserted by whichever pass produced them"
# =============================================================================
# The README's runbook is two separate commands, so requiring backfill AND sync
# in ONE invocation left the 88/15/50/43 counts — the only automated backstop
# against a silent partial copy — permanently unevaluated.
run_sync EXPECT_CRON=99 -- --apply --backfill-old
[ "$LAST_RC" = 5 ] || fail "a backfill-only pass did not assert its own counts (exit $LAST_RC, expected 5)"
log_has 'era cron: 2 meetings archived, expected 99' || fail "the backfill-only acceptance mismatch was not reported"
log_lacks 'era=exapp' || fail "a backfill-only pass asserted the exapp count, which it cannot know"
ok "--apply --backfill-old alone asserts cron/legacy/hpb"

run_sync EXPECT_EXAPP=99 -- --apply --sync-new
[ "$LAST_RC" = 5 ] || fail "a sync-only pass did not assert the exapp floor (exit $LAST_RC, expected 5)"
log_has 'era exapp: 2 meetings archived, expected at least 99' || fail "the exapp floor was not reported"
ok "--apply --sync-new alone asserts the exapp floor (a count that may grow, never shrink)"

# =============================================================================
section "19. an empty source corpus is a failure, not a green zero"
# =============================================================================
# SRC_OLD present but EMPTY is exactly what a mountpoint whose mount did not
# come up looks like. It used to archive nothing and report
# status=ok meetings_total=0 with an empty assertion list.
EMPTY_SRC="$T/empty-src/recordings"; mkdir -p "$EMPTY_SRC"
ARCH_EMPTY="$T/archive-empty"; mkdir -p "$ARCH_EMPTY"; : > "$ARCH_EMPTY/.btrfs-subvol"
run_sync ARCHIVE_ROOT="$ARCH_EMPTY" SRC_OLD="$EMPTY_SRC" -- --apply --backfill-old
[ "$LAST_RC" = 3 ] || fail "a backfill of an empty source corpus exited $LAST_RC, expected 3"
log_has 'event=preflight.empty_source' || fail "the empty source corpus was not named"
log_lacks 'event=run.done status=ok' || fail "an empty-source backfill still reported a green run"
ok "a backfill whose old corpus is empty refuses (exit 3) instead of reporting 0 meetings green"

# =============================================================================
section "20. an empty operator DB and an empty current/ do not crash the pass"
# =============================================================================
# `${#ASSOC[@]}` on a `declare -A` that was never assigned is a FATAL unbound
# variable under `set -u`. An empty jobs table or a current/ with no .meeting
# aborted the run with a bash internal error and an undocumented exit 1 —
# before reconcile_reverse could report the very deletion that caused it.
NUL_DATA="$T/nul/_data"; mkdir -p "$NUL_DATA/operator/jobs/current" "$NUL_DATA/operator/jobs/runs" "$NUL_DATA/operator/backups"
python3 - "$NUL_DATA/operator/jobs.sqlite3" <<'PY2'
import sqlite3, sys
con = sqlite3.connect(sys.argv[1])
con.execute("""create table jobs(
  id text primary key, stage text, state text, artifact_run_path text,
  talk_binding text, request_json text, record_finished_at text,
  completed_at text, created_at text)""")
con.commit()
PY2
ARCH_NUL="$T/archive-nul"; mkdir -p "$ARCH_NUL"; : > "$ARCH_NUL/.btrfs-subvol"
mkdir -p "$T/nul/hpb"
run_sync ARCHIVE_ROOT="$ARCH_NUL" SRC_EXA_DATA="$NUL_DATA" SRC_EXA="$NUL_DATA/operator/jobs" \
         SRC_HPB="$T/nul/hpb" \
         OPERATOR_DB="$NUL_DATA/operator/jobs.sqlite3" EXPECT_EXAPP=0 -- --apply --sync-new
[ "$LAST_RC" = 0 ] || fail "an empty operator DB / empty current/ exited $LAST_RC, expected 0"
log_lacks 'unbound variable' || fail "an empty associative array aborted the run with a bash internal error"
log_has 'event=jobsdb.loaded rows=0' || fail "the empty jobs table was not loaded"
log_has 'event=derived.index_loaded entries=0' || fail "the empty derived index was not loaded"
log_has 'event=run.done status=ok' || fail "an empty but healthy ExApp volume did not finish cleanly"
ok "an empty jobs table and a current/ with no bundles complete the pass (exit 0), not a bash error"

# =============================================================================
section "21. an unreadable ARCHIVE/meeting.json fails the run and is named"
# =============================================================================
# The reindex used to drop the row with a bare stderr line: the meeting vanished
# from index.jsonl and INVENTORY.md while every byte of its media sat on disk,
# and --check could not see it either because it diffs against a rebuild that
# drops the same row.
cp -a "$M3/ARCHIVE/meeting.json" "$T/meeting.json.bak"
chmod u+w "$M3" "$M3/ARCHIVE" "$M3/ARCHIVE/meeting.json"
: > "$M3/ARCHIVE/meeting.json"
run_sync EXPECT_EXAPP=2 -- --apply --rebuild-index
[ "$LAST_RC" = 5 ] || fail "a torn meeting.json still produced a green run (exit $LAST_RC, expected 5)"
log_has 'event=index.unreadable' || fail "the unreadable meeting.json was not reported as an event"
log_has 'is unreadable, so' || fail "the lost index row was not raised as an assertion"
[ "$(jq -r '.assertions_failed | length' "$ARCHIVE/state/health.json")" -ge 1 ] \
  || fail "health.json does not carry the lost-row assertion"
cp -a "$T/meeting.json.bak" "$M3/ARCHIVE/meeting.json"
chmod a-w "$M3/ARCHIVE/meeting.json" "$M3/ARCHIVE" "$M3"
run_sync EXPECT_EXAPP=2 -- --apply --rebuild-index
[ "$LAST_RC" = 0 ] || fail "the run did not go green again after the meeting.json was restored"
[ "$(wc -l < "$IDX")" = "$(( $(meetings_list | wc -l) + 1 ))" ] \
  || fail "the restored index does not hold one row per meeting plus the job-only row"
ok "an unreadable meeting.json fails the run, names the lost row, and recovers when restored"

# =============================================================================
section "22. a crash between the attach and the manifest refresh is repaired"
# =============================================================================
# attach commits the component with a_mv and refreshes MANIFEST.sha256 +
# meeting.json afterwards. A kill in between left the component on disk and
# unrecorded — and the `[ -e ]` latch made that permanent, while `sha256sum -c`
# (which only checks the files it LISTS) kept reporting the bundle as good.
CRASH_MJ="$M4/ARCHIVE/meeting.json"
CRASH_MAN="$M4/ARCHIVE/MANIFEST.sha256"
chmod u+w "$M4" "$M4/ARCHIVE" "$CRASH_MJ" "$CRASH_MAN"
grep -v "  derived/$JOB_OK\.opus\$" "$CRASH_MAN" > "$T/man.tmp"
mv "$T/man.tmp" "$CRASH_MAN"
STALE_SHA=$(sha256sum "$CRASH_MAN" | cut -d' ' -f1)
jq --arg m "sha256:$STALE_SHA" \
   '.sha256_manifest=$m | .opus_attached=false | .derived=[.derived[] | select(.kind != "opus")]' \
   "$CRASH_MJ" > "$T/mj.tmp"
mv "$T/mj.tmp" "$CRASH_MJ"
chmod a-w "$CRASH_MJ" "$CRASH_MAN" "$M4/ARCHIVE" "$M4"
# The doctored bundle is internally consistent: this is why the integrity
# verifier can never notice it on its own.
( cd "$M4" && sha256sum -c --quiet ARCHIVE/MANIFEST.sha256 ) \
  || fail "the simulated crash state does not reproduce (the stale manifest should still verify)"
MJ_INO_BEFORE=$(stat -c %i "$CRASH_MJ")
run_sync EXPECT_EXAPP=2 -- --apply --sync-new
[ "$LAST_RC" = 0 ] || fail "the repair run exited $LAST_RC"
log_has 'event=attach.repair' || fail "the unrecorded component was not detected and repaired"
grep -q "derived/$JOB_OK.opus" "$CRASH_MAN" \
  || fail "the attached .opus is still missing from MANIFEST.sha256 after the repair"
[ "$(jq -r .opus_attached "$CRASH_MJ")" = true ] || fail "opus_attached is still false after the repair"
[ "$(jq -r '[.derived[] | select(.kind == "opus")] | length' "$CRASH_MJ")" = 1 ] \
  || fail "meeting.json still does not list the attached .opus"
[ "$(stat -c %i "$CRASH_MJ")" != "$MJ_INO_BEFORE" ] \
  || fail "meeting.json was rewritten in place — a_write must rename a temp file, or a kill mid-write tears it"
grep -q '"opus_attached":true' <(jq -c 'select(.dir=="meetings/'"$EXAPP_DIR"'")' "$IDX") \
  || fail "index.jsonl still reports opus_attached=false after the repair"
ok "a component on disk but absent from the manifest is repaired, not latched over forever"

# The same crash on the .mjr path, which has its own latch and no derived/ at all.
MJR_MAN="$M5/ARCHIVE/MANIFEST.sha256"
MJR_MJ="$M5/ARCHIVE/meeting.json"
chmod u+w "$M5" "$M5/ARCHIVE" "$MJR_MAN" "$MJR_MJ"
grep -v "  janus-mjr/" "$MJR_MAN" > "$T/man2.tmp"
mv "$T/man2.tmp" "$MJR_MAN"
jq --arg m "sha256:$(sha256sum "$MJR_MAN" | cut -d' ' -f1)" '.sha256_manifest=$m' "$MJR_MJ" > "$T/mj2.tmp"
mv "$T/mj2.tmp" "$MJR_MJ"
chmod a-w "$MJR_MAN" "$MJR_MJ" "$M5/ARCHIVE" "$M5"
( cd "$M5" && sha256sum -c --quiet ARCHIVE/MANIFEST.sha256 ) \
  || fail "the simulated .mjr crash state does not reproduce"
run_sync EXPECT_EXAPP=2 -- --apply --sync-new
[ "$LAST_RC" = 0 ] || fail "the .mjr repair run exited $LAST_RC"
grep -q "janus-mjr/videoroom-$MJR_US-audio-0.mjr" "$MJR_MAN" \
  || fail "the attached .mjr is still missing from MANIFEST.sha256 — the janus-mjr latch never re-checks"
ok "the .mjr attach has the same crash repair, on a bundle with no derived/ at all"

# =============================================================================
section "23. the rotating verifier actually detects a corrupted archived file"
# =============================================================================
VICTIM="$M2/sessions/$SID/streams/stream-1.rtplog"
cp -a "$VICTIM" "$T/victim.bak"
chmod u+w "$M2" "$M2/sessions" "$M2/sessions/$SID" "$M2/sessions/$SID/streams" "$VICTIM"
printf 'bitrot' | dd of="$VICTIM" bs=1 seek=17 conv=notrunc status=none
rm -f "$ARCHIVE/state/verify.json"      # verify walks from a cursor; start at the top
run_sync EXPECT_EXAPP=2 -- --apply --verify
[ "$LAST_RC" = 5 ] || fail "a corrupted archived file did not fail --verify (exit $LAST_RC, expected 5)"
log_has 'sha256 mismatch under meetings/' || fail "--verify did not name the corrupted bundle"
[ "$(jq -r .failed "$ARCHIVE/state/verify.json")" = 1 ] || fail "verify.json does not record the failure"
cp -a "$T/victim.bak" "$VICTIM"
chmod a-w "$VICTIM" "$M2/sessions/$SID/streams" "$M2/sessions/$SID" "$M2/sessions" "$M2"
rm -f "$ARCHIVE/state/verify.json"
run_sync EXPECT_EXAPP=2 -- --apply --verify
[ "$LAST_RC" = 0 ] || fail "--verify still fails after the corrupted byte was restored"
ok "one flipped byte in an archived file fails --verify (exit 5) and is recorded in verify.json"

# =============================================================================
section "24. the snapshot pruner's four brakes hold"
# =============================================================================
SNAP2="$T/snapshots2"; mkdir -p "$SNAP2"
mkdir -p "$SNAP2/000-baseline--2025-06-01T000000Z"
for d in 01 02 03 04 05 06 07 08 09 10; do mkdir -p "$SNAP2/2025-06-${d}T000000Z"; done
run_sync EXPECT_EXAPP=2 SNAPSHOT_ROOT="$SNAP2" -- --apply --snapshot
[ "$LAST_RC" = 0 ] || fail "the pruning snapshot run exited $LAST_RC"
log_has 'event=snapshot.retention_deferred' \
  || fail "SNAPSHOT_MAX_DELETES did not cap a large retention drop"
log_has 'event=snapshot.prune kept=' || fail "the pruner did not report what it kept"
DELETED=$(grep -c 'event=snapshot.delete' "$LAST_LOG" || true)
[ "$DELETED" = 3 ] || fail "the pruner deleted $DELETED snapshots in one pass, cap is SNAPSHOT_MAX_DELETES=3"
[ -d "$SNAP2/000-baseline--2025-06-01T000000Z" ] \
  || fail "the pruner deleted the pinned 000-baseline snapshot"
NEWEST2=$(find "$SNAP2" -mindepth 1 -maxdepth 1 -type d -printf '%P\n' | LC_ALL=C sort | tail -1)
[ -f "$SNAP2/$NEWEST2/index.jsonl" ] || fail "the newest snapshot was pruned or is not a real snapshot"
grep -q 'event=snapshot.delete.*000-baseline' "$LAST_LOG" && fail "the baseline was listed for deletion"
ok "pruner: baseline pinned, newest kept, at most SNAPSHOT_MAX_DELETES=3 deleted per pass"

# =============================================================================
section "25. a source tree containing a symlink is quarantined, never followed"
# =============================================================================
SYM="$T/symsrc"
mkdir -p "$SYM/recordings/daily-meeting-2026-02-03/session/streams" "$SYM/archive" "$SYM/secret"
: > "$SYM/archive/.btrfs-subvol"
echo 'do not copy me' > "$SYM/secret/private.txt"
echo '{"kind":"recording","state":"ready"}' > "$SYM/recordings/daily-meeting-2026-02-03/cassini.json"
blob "$SYM/recordings/daily-meeting-2026-02-03/recording.mkv" 2 a
session_json "$SYM/recordings/daily-meeting-2026-02-03/session/session.json" \
  mczuc3mb CodemyriadRecorder 2026-02-03T08:00:00.000000000Z
ln -s "$SYM/secret/private.txt" "$SYM/recordings/daily-meeting-2026-02-03/link-out"
run_sync ARCHIVE_ROOT="$SYM/archive" SRC_OLD="$SYM/recordings" SRC_HPB="$T/nul/hpb" \
         EXPECT_CRON=0 EXPECT_LEGACY=0 EXPECT_HPB=0 -- --apply --backfill-old
[ "$LAST_RC" = 5 ] || fail "a source containing a symlink did not fail the run (exit $LAST_RC, expected 5)"
log_has 'event=quarantine' || fail "the symlinked source was not quarantined"
log_has 'symlink' || fail "the quarantine reason does not name the symlink"
[ -z "$(cd "$SYM/archive" && find meetings -mindepth 1 -maxdepth 1 -type d -not -name '.*' 2>/dev/null || true)" ] \
  || fail "a source containing a symlink was ingested anyway"
grep -rq 'do not copy me' "$SYM/archive" && fail "the symlink was FOLLOWED and its target copied into the archive"
[ -L "$SYM/recordings/daily-meeting-2026-02-03/link-out" ] || fail "the source symlink was modified"
ok "a symlink in a source tree quarantines the bundle; the target is never read or copied"

# The same unresolved anomaly on the next pass is the SAME fact, not a new one.
run_sync ARCHIVE_ROOT="$SYM/archive" SRC_OLD="$SYM/recordings" SRC_HPB="$T/nul/hpb" \
         EXPECT_CRON=0 EXPECT_LEGACY=0 EXPECT_HPB=0 -- --apply --backfill-old
[ "$LAST_RC" = 5 ] || fail "the second pass over an unfixed anomaly exited $LAST_RC, expected 5"
[ "$(wc -l < "$SYM/archive/reports/anomalies.tsv")" = 1 ] \
  || fail "reports/anomalies.tsv grew a second row for the same unresolved anomaly ($(wc -l < "$SYM/archive/reports/anomalies.tsv") rows) — healthcheck A7 counts rows"
ok "an unresolved anomaly keeps failing the run but is recorded once, not once per pass"

# =============================================================================
section "26. a working reflink pair is not refused because st_dev differs"
# =============================================================================
# Regression: the probe used to prove the source->archive pair with
# `stat -c %d "$src" = stat -c %d "$ARCHIVE_ROOT"`. Under the units'
# ProtectSystem=strict + BindPaths namespace the same btrfs reports a different
# st_dev for the bind-mounted archive, so the check hard-refused (exit 3,
# reflink.different_filesystem, with no override) a pair that clones perfectly.
# Observed on george 2026-08-28: source_dev=48 archive_dev=1048653.
#
# Here: reflink WORKS (forced), but st_dev differs. The run must proceed.
DEVSPLIT="$T/devsplit"
mkdir -p "$DEVSPLIT/recordings/daily-meeting-2026-02-10/session/streams" "$DEVSPLIT/archive"
: > "$DEVSPLIT/archive/.btrfs-subvol"
blob "$DEVSPLIT/recordings/daily-meeting-2026-02-10/recording.mkv" 2 a
blob "$DEVSPLIT/recordings/daily-meeting-2026-02-10/session/streams/s_000001.rtplog" 1 b
blob "$DEVSPLIT/recordings/daily-meeting-2026-02-10/session/streams/s_000001.idx" 1 c
session_json "$DEVSPLIT/recordings/daily-meeting-2026-02-10/session/session.json" \
  mczuc3mb CodemyriadRecorder 2026-02-10T08:00:00.000000000Z

run_sync ARCHIVE_ROOT="$DEVSPLIT/archive" SRC_OLD="$DEVSPLIT/recordings" SRC_HPB="$T/nul/hpb" \
         ALLOW_FULL_COPY=0 FAKE_CP_FORCE_REFLINK=1 FAKE_STAT_SPLIT_DEV=1 \
         EXPECT_CRON=1 EXPECT_LEGACY=0 EXPECT_HPB=0 -- --apply --backfill-old
[ "$LAST_RC" = 0 ] \
  || fail "a reflinkable source/archive pair was refused because st_dev differs (exit $LAST_RC); log: $(grep -o 'event=reflink[^ ]*' "$LAST_LOG" | head -1)"
log_lacks 'reflink.different_filesystem' \
  || fail "the probe still reports different_filesystem for a pair that reflinks successfully"
[ -d "$DEVSPLIT/archive/meetings/2026-02-10T080000Z--mczuc3mb--cron--daily-meeting" ] \
  || fail "the meeting was not ingested across the differing-st_dev pair"
ok "a differing st_dev does not refuse a pair whose reflink actually succeeds"

# The genuine cross-filesystem case must still be refused, loudly, and must not
# silently fall back to writing real bytes.
mkdir -p "$DEVSPLIT/archive2"; : > "$DEVSPLIT/archive2/.btrfs-subvol"
run_sync ARCHIVE_ROOT="$DEVSPLIT/archive2" SRC_OLD="$DEVSPLIT/recordings" SRC_HPB="$T/nul/hpb" \
         ALLOW_FULL_COPY=0 FAKE_CP_NO_REFLINK=1 \
         EXPECT_CRON=1 EXPECT_LEGACY=0 EXPECT_HPB=0 -- --apply --backfill-old
[ "$LAST_RC" = 3 ] || fail "a genuinely un-reflinkable pair was not refused with exit 3 (got $LAST_RC)"
log_has 'reflink' || fail "the refusal does not name reflink as the reason"
ok "a source that genuinely cannot be cloned into the archive is still refused, not copied"

echo
echo "PASS: cassini-archive-sync.sh — $CHECKS assertions over a synthetic 6-shape corpus"
