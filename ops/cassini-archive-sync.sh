#!/usr/bin/env bash
# cassini-archive-sync — build and maintain the unified Cassini meeting archive.
#
# WHAT THIS IS
#   One additive, never-mutating, never-deleting reflink mirror of every meeting
#   Cassini has ever recorded, across all four capture eras:
#
#     cron    /mnt/data/cassini/recordings/<dir>            CodemyriadRecorder, CT 107 cron
#     legacy  /mnt/data/cassini/recordings/<name>.mkv       bare mixdown, no raw
#     hpb     /mnt/data/cassini/hpb-talk-recordings/*.mkv   Talk/Janus server recorder
#     exapp   …/_data/operator/jobs/current/<ULID>.run      CassiniRecorder, Nextcloud ExApp
#
#   into ARCHIVE_ROOT/meetings/<ANCHOR>--<ROOM>--<ERA>--<SLUG>/, with an
#   index.jsonl row and a human INVENTORY.md alongside.
#
#   The archive is also a DETECTOR. The previous pipeline died silently on
#   2026-07-31 and cost three unrecoverable business days; cassini-ingest-batch
#   sat `failed` for three weeks unnoticed. Every run therefore writes
#   state/health.json plus an off-host heartbeat, and refuses to report green
#   while an unacknowledged ALARM exists.
#
# WHERE THIS RUNS
#   George (Proxmox host), as root, on the host filesystem. It does NOT enter
#   CT 112 and does NOT talk to docker, so a stopped or upgrading container does
#   not stop the archive. Root is mandatory: 42 of the old bundles carry
#   mode-0700 `artifact-remux-work` directories, and a non-root traversal skips
#   them, under-reports by 40,248,765,505 B, AND EXITS 0.
#
# WHAT IT TOUCHES
#   Writes ONLY under:  $ARCHIVE_ROOT, $SNAPSHOT_ROOT, $VAR_DIR, $HEARTBEAT_DIR
#   Reads  ONLY  from:  $SRC_OLD, $SRC_HPB, $SRC_EXA   (never opened for write;
#                       the operator DB is opened `mode=ro` and copied through
#                       SQLite's own online-backup API, exactly as
#                       /usr/local/sbin/cassini-exapp-backup does)
#   Every mutating operation goes through the a_* wrappers, and every one of
#   those calls guard_write() first. A path outside the writable roots is a
#   fatal error, not a warning. Under systemd this is belt-and-braces with
#   ReadOnlyPaths=/mnt/data/cassini /mnt/data/cassini-exapp.
#
#   It NEVER deletes from a source tree. It never deletes the orphaned
#   artifact-remux-work scratch either — that is a separate human decision; it
#   is excluded from the copy and accounted for by the byte in EXCLUDED.tsv.
#
# HOW TO RUN
#   Everything is a dry run unless you pass --apply. With no arguments at all
#   this prints the full plan and writes nothing.
#
#     sudo ops/cassini-archive-sync.sh                       # dry run of everything
#     sudo ops/cassini-archive-sync.sh --backfill-old        # dry run, old eras only
#     sudo ops/cassini-archive-sync.sh --apply --backfill-old --sync-new
#                                                     # the one-time backfill:
#                                                     # ONE invocation, both
#                                                     # flags — each pass only
#                                                     # asserts the eras it
#                                                     # touched
#     sudo ops/cassini-archive-sync.sh --apply --sync-new       # what the timer runs
#     sudo ops/cassini-archive-sync.sh --apply --all            # everything + snapshot
#     sudo ops/cassini-archive-sync.sh --apply --snapshot
#     sudo ops/cassini-archive-sync.sh --apply --verify
#     sudo ops/cassini-archive-sync.sh --apply --rebuild-index
#     sudo ops/cassini-archive-sync.sh --check                  # index drift only
#     sudo ops/cassini-archive-sync.sh --apply --ack            # clear an ALARM
#
#   The backfill reads ~123.6 GiB to hash it. Expect 1–3 h at Nice=10 with idle
#   I/O. It is resumable per meeting; re-running is cheap and idempotent.
#
# PREREQUISITES the operator creates by hand, once, before the first --apply:
#     btrfs subvolume create /mnt/data/cassini-archive
#     mkdir -p /mnt/data/cassini-archive-snapshots /var/lib/cassini-archive
#   This script deliberately does not create the subvolume: `btrfs subvolume
#   create` is the one irreversible layout decision here, and it belongs to a
#   human. Everything below it is created automatically.
#
# EXIT CODES
#   0 ok            2 usage / not root      3 preflight failed
#   4 operator DB unreadable                5 not green: a conflict, a failed
#                                             assertion, or a quarantined source
#   6 upstream quiet (no new capture in MAX_QUIET_WEEKDAYS weekdays)
#   7 unacknowledged ALARM
#
# CONFIG  /etc/default/cassini-archive (no secrets). Every knob below is
#         overridable from there or from the environment.

set -euo pipefail

# Sourcing this file defines every function without running a pass, so the
# harness tests can exercise the naming and identity logic directly. Executing
# it runs one pass.
_SOURCED=0
if [ "${BASH_SOURCE[0]}" != "$0" ]; then _SOURCED=1; fi

CONFIG_FILE=${CONFIG_FILE:-/etc/default/cassini-archive}
# shellcheck disable=SC1090
[ -r "$CONFIG_FILE" ] && . "$CONFIG_FILE"

# --- roots -------------------------------------------------------------------
ARCHIVE_ROOT=${ARCHIVE_ROOT:-/mnt/data/cassini-archive}
SNAPSHOT_ROOT=${SNAPSHOT_ROOT:-/mnt/data/cassini-archive-snapshots}
VAR_DIR=${VAR_DIR:-/var/lib/cassini-archive}

SRC_OLD=${SRC_OLD:-/mnt/data/cassini/recordings}
SRC_HPB=${SRC_HPB:-/mnt/data/cassini/hpb-talk-recordings}
SRC_EXA_DATA=${SRC_EXA_DATA:-/mnt/data/cassini-exapp/docker/volumes/nc_app_gocassini_data/_data}
SRC_EXA=${SRC_EXA:-$SRC_EXA_DATA/operator/jobs}
OPERATOR_DB=${OPERATOR_DB:-$SRC_EXA_DATA/operator/jobs.sqlite3}

# jobs.artifact_run_path is recorded as the CONTAINER path
# (/nc_app_gocassini_data/operator/jobs/current/<ULID>.run). To compare it with
# what we see on the host we have to translate the prefix. Both halves are
# configurable so a future volume move does not silently make every G3 gate fail
# closed and the ingest skip every new capture while exiting 0.
CONTAINER_DATA_PREFIX=${CONTAINER_DATA_PREFIX:-/nc_app_gocassini_data}

# The off-host heartbeat. This exact directory is in cassini-exapp-backup's
# `[ -e "$p" ] && paths+=("$p")` list and a .json matches none of its excludes,
# so the file ships to R2 nightly with ZERO edits to the production backup
# script. Do not "improve" this path.
HEARTBEAT_DIR=${HEARTBEAT_DIR:-$SRC_EXA_DATA/operator/backups}
HEARTBEAT_FILE=${HEARTBEAT_FILE:-$HEARTBEAT_DIR/cassini-archive-health.json}

# --- knobs -------------------------------------------------------------------
LOCKFILE=${LOCKFILE:-/run/lock/cassini-archive.lock}
MIN_FREE_BYTES=${MIN_FREE_BYTES:-214748364800}      # 200 GiB
MIN_RUN_INTERVAL=${MIN_RUN_INTERVAL:-300}           # seconds; --force overrides
VERIFY_BYTES_PER_RUN=${VERIFY_BYTES_PER_RUN:-4294967296}
MAX_QUIET_WEEKDAYS=${MAX_QUIET_WEEKDAYS:-3}
EXPECTED_QUIET_UNTIL=${EXPECTED_QUIET_UNTIL:-}
CONCURRENT_WINDOW_S=${CONCURRENT_WINDOW_S:-900}
KEEP_RUN_LOGS=${KEEP_RUN_LOGS:-90}
KEEP_DB_SNAPSHOTS=${KEEP_DB_SNAPSHOTS:-30}
SNAPSHOT_MAX_DELETES=${SNAPSHOT_MAX_DELETES:-3}
SNAPSHOT_KEEP_DAYS=${SNAPSHOT_KEEP_DAYS:-14}
SNAPSHOT_KEEP_WEEKS=${SNAPSHOT_KEEP_WEEKS:-8}
SNAPSHOT_KEEP_MONTHS=${SNAPSHOT_KEEP_MONTHS:-24}
LOCAL_TZ=${LOCAL_TZ:-Europe/Rome}

# `cp --reflink=always` is mandatory: a silent fallback to a full copy would
# write ~123.6 GiB nobody asked for. The single exception is small files, where
# btrfs may store the extent inline in metadata and refuse FICLONE. Those are
# copied for real, counted, and logged — never silently. Anything larger than
# SMALL_FILE_COPY_MAX that will not reflink is a hard failure.
SMALL_FILE_COPY_MAX=${SMALL_FILE_COPY_MAX:-1048576}
ALLOW_FULL_COPY=${ALLOW_FULL_COPY:-0}

# Acceptance numbers the backfill asserts against. A different number is a
# failed backfill, not a shrug. Override only with evidence.
EXPECT_CRON=${EXPECT_CRON:-88}
EXPECT_LEGACY=${EXPECT_LEGACY:-15}
EXPECT_HPB=${EXPECT_HPB:-50}
EXPECT_EXAPP=${EXPECT_EXAPP:-43}

SCHEMA=cassini.archive.index.v1

# --- derived paths -----------------------------------------------------------
MEETINGS_DIR="$ARCHIVE_ROOT/meetings"
STAGING_DIR="$MEETINGS_DIR/.staging"
STATE_DIR="$ARCHIVE_ROOT/state"
REPORTS_DIR="$ARCHIVE_ROOT/reports"
QUARANTINE_DIR="$ARCHIVE_ROOT/quarantine"
UNATTRIBUTED_DIR="$ARCHIVE_ROOT/unattributed"
BYDATE_DIR="$ARCHIVE_ROOT/by-date"
BYJOB_DIR="$ARCHIVE_ROOT/by-job-id"
VIEWER_DIR="$ARCHIVE_ROOT/viewer"
ALARM_FILE="$VAR_DIR/ALARM"
LAST_RUN_STAMP="$VAR_DIR/last-run-epoch"

INGESTED_TSV="$STATE_DIR/ingested.tsv"
PENDING_TSV="$STATE_DIR/pending.tsv"
CONFLICTS_TSV="$STATE_DIR/conflicts.tsv"
EXCLUDED_TSV="$STATE_DIR/EXCLUDED.tsv"
LOWCONF_TSV="$STATE_DIR/attach-low-confidence.tsv"
ANOMALIES_TSV="$REPORTS_DIR/anomalies.tsv"
VERIFY_JSON="$STATE_DIR/verify.json"
HEALTH_JSON="$STATE_DIR/health.json"
INDEX_JSONL="$ARCHIVE_ROOT/index.jsonl"

# =============================================================================
# logging — one structured, greppable line per event
# =============================================================================
RUN_ID=""
LOG_SINK=""

now_utc() { date -u +%FT%TZ; }

log() { # log <level> <event> [k=v ...]
  local level=$1 event=$2
  shift 2
  local line
  line="$(now_utc) level=$level run=${RUN_ID:--} event=$event${*:+ $*}"
  printf '%s\n' "$line"
  [ -n "$LOG_SINK" ] && printf '%s\n' "$line" >>"$LOG_SINK"
  return 0
}
info() { log info "$@"; }
warn() { log warn "$@" >&2; }
die() {
  local code=$1
  shift
  log fatal "$@" >&2
  exit "$code"
}

# kv() quotes a value so a log line stays parseable when it contains spaces.
kv() { printf '%s="%s"' "$1" "${2//\"/\\\"}"; }

# =============================================================================
# write guard — the structural half of "never touch the sources"
# =============================================================================
WRITABLE_ROOTS=()
WORKTMP=""

abspath() { readlink -m -- "$1"; }

guard_write() {
  local p
  p=$(abspath "$1")
  local r
  for r in "${WRITABLE_ROOTS[@]}"; do
    [ -n "$r" ] || continue
    if [ "$p" = "$r" ] || [ "${p#"$r"/}" != "$p" ]; then
      return 0
    fi
  done
  die 5 guard.violation "$(kv path "$p")" reason=outside-writable-roots
}

# nullglob save/restore. A callee that ends with a bare `shopt -u nullglob`
# silently turns the option OFF for its caller: backfill_old sets nullglob,
# calls ingest_one, and its NEXT glob (the hpb corpus) then expands to a
# literal `.../*.mkv` and gets quarantined. Pair every enable with a restore.
_NG_STACK=()
# NB `shopt -p nullglob` exits 1 when the option is OFF, which under `set -e`
# would abort the run inside the assignment.
push_nullglob() { _NG_STACK+=("$(shopt -p nullglob || true)"); shopt -s nullglob; }
pop_nullglob() {
  local n=${#_NG_STACK[@]}
  if [ "$n" -gt 0 ]; then eval "${_NG_STACK[$((n - 1))]}"; unset "_NG_STACK[$((n - 1))]"
  else shopt -u nullglob; fi
}

DRY_RUN=1

# a_* wrappers: the ONLY places this script mutates the filesystem. Keeping them
# in one block is what makes the read-only posture testable by grep.
a_mkdir() { guard_write "$1"; if [ "$DRY_RUN" = 1 ]; then info dry.mkdir "$(kv path "$1")"; else mkdir -p -- "$1"; fi; }
a_rm()    { guard_write "$1"; if [ "$DRY_RUN" = 1 ]; then info dry.rm "$(kv path "$1")"; else
              # The archive is written 0444/0555 on purpose, so lift our own
              # write bits before removing archive-owned scratch. Nothing
              # outside the writable roots can reach this line.
              [ -e "$1" ] && chmod -R u+w -- "$1" 2>/dev/null
              rm -rf -- "$1"
            fi; }
a_mv()    { guard_write "$1"; guard_write "$2"; if [ "$DRY_RUN" = 1 ]; then info dry.mv "$(kv from "$1")" "$(kv to "$2")"; else mv -T -- "$1" "$2"; fi; }
a_ln()    { guard_write "$2"; if [ "$DRY_RUN" = 1 ]; then info dry.symlink "$(kv link "$2")" "$(kv target "$1")"; else ln -sfn -- "$1" "$2"; fi; }
a_chmod() { local m=$1; shift; guard_write "$1"; if [ "$DRY_RUN" = 1 ]; then info dry.chmod "$(kv mode "$m")" "$(kv path "$1")"; else chmod "$m" -- "$1"; fi; }
a_chmod_r() { local m=$1; shift; guard_write "$1"; if [ "$DRY_RUN" = 1 ]; then info dry.chmod_r "$(kv mode "$m")" "$(kv path "$1")"; else chmod -R "$m" -- "$1"; fi; }
a_write() { # a_write <dest>   (content on stdin)
  guard_write "$1"
  if [ "$DRY_RUN" = 1 ]; then
    local n; n=$(wc -c)
    info dry.write "$(kv path "$1")" bytes="$n"
  else
    # Write to a sibling temp file and rename(2) it into place. `cat >"$1"` is
    # truncate-then-write, so a kill in the middle of rewriting meeting.json
    # (refresh_manifest does exactly that on every attach) leaves a zero-length
    # or half-written file. That file is still a regular file, so sweep_orphans
    # will not quarantine it, and the reindex simply drops the row: the meeting
    # vanishes from index.jsonl and from INVENTORY.md while all its media sit
    # untouched on disk. A rename is atomic, so the file is either the old
    # version or the new one, never a torn one.
    #
    # Archive files are sealed 0444, so the previous mode is carried over onto
    # the replacement; the rename itself needs the DIRECTORY's write bit, which
    # every caller lifts (refresh_manifest chmods u+w on the bundle and on
    # ARCHIVE/ before writing). guard_write() has already proved the path is ours.
    local d t mode=""
    d=$(dirname -- "$1")
    t="$d/.cassini-archive-write.$$.tmp"
    guard_write "$t"
    [ -e "$1" ] && mode=$(stat -c %a -- "$1")
    cat >"$t"
    [ -n "$mode" ] && chmod "$mode" -- "$t"
    mv -f -T -- "$t" "$1"
  fi
}
a_append() { # a_append <dest>  (one short line on stdin; O_APPEND, sub-PIPE_BUF)
  guard_write "$1"
  if [ "$DRY_RUN" = 1 ]; then
    local l; l=$(cat)
    info dry.append "$(kv path "$1")" "$(kv line "$l")"
  else
    cat >>"$1"
  fi
}

# a_cp_reflink <src-file> <dst-file>
# Prints the number of bytes that had to be copied for real (0 for a clean
# reflink) so the caller can report physical cost honestly.
a_cp_reflink() {
  local src=$1 dst=$2
  guard_write "$dst"
  if [ "$DRY_RUN" = 1 ]; then
    printf '0\n'
    return 0
  fi
  if cp --reflink=always --preserve=timestamps -- "$src" "$dst" 2>/dev/null; then
    printf '0\n'
    return 0
  fi
  local size
  size=$(stat -c %s -- "$src")
  if [ "$ALLOW_FULL_COPY" = 1 ]; then
    cp --reflink=auto --preserve=timestamps -- "$src" "$dst"
    printf '%s\n' "$size"
    return 0
  fi
  if [ "$size" -le "$SMALL_FILE_COPY_MAX" ]; then
    # btrfs can hold a small file's extent inline in metadata, where FICLONE is
    # refused. Copying it for real costs a bounded, logged handful of bytes.
    cp --preserve=timestamps -- "$src" "$dst"
    # stdout is this function's return channel (bytes physically written), so
    # the log line must not land on it.
    log info copy.smallfile.fallback "$(kv path "$src")" bytes="$size" >&2
    printf '%s\n' "$size"
    return 0
  fi
  die 3 reflink.unavailable "$(kv path "$src")" bytes="$size" \
    hint="refusing a full copy; set ALLOW_FULL_COPY=1 only if you really mean to write ~123.6 GiB"
}

# =============================================================================
# usage
# =============================================================================
usage() {
  sed -n '2,/^set -euo pipefail$/p' "$0" | sed -e 's/^# \{0,1\}//' -e '/^set -euo/d'
  cat <<'EOF'
OPTIONS
  --dry-run          plan only, write nothing (DEFAULT)
  --apply            actually write
  --backfill-old     ingest the cron / legacy / hpb corpora
  --sync-new         ingest ExApp .run captures, attach derived .meeting/.opus/.mjr
  --all              --backfill-old --sync-new + reindex + snapshot + verify slice
  --snapshot         take a read-only btrfs snapshot and run the fenced pruner
  --verify           re-hash the next VERIFY_BYTES_PER_RUN slice of the corpus
  --rebuild-index    rebuild index.jsonl / by-date / by-job-id / viewer / INVENTORY.md
  --check            rebuild the index into a temp file and diff it against the live one
  --ack              acknowledge and clear $VAR_DIR/ALARM
  --debounce         exit 0 when the previous run finished less than
                     MIN_RUN_INTERVAL seconds ago. ONLY for the unattended
                     trigger (cassini-archive-sync.path can fire many times a
                     minute); a hand-run pass must never be silently skipped
  --force            run even with --debounce
  --help             this text
EOF
}

# =============================================================================
# argument parsing
# =============================================================================
DO_BACKFILL=0 DO_SYNC=0 DO_SNAPSHOT=0 DO_VERIFY=0 DO_REINDEX=0 DO_CHECK=0 DO_ACK=0 FORCE=0
DEBOUNCE=0
ANY_ACTION=0

while [ "$_SOURCED" = 0 ] && [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY_RUN=1 ;;
    --apply) DRY_RUN=0 ;;
    --backfill-old) DO_BACKFILL=1; DO_REINDEX=1; ANY_ACTION=1 ;;
    --sync-new) DO_SYNC=1; DO_REINDEX=1; ANY_ACTION=1 ;;
    --all) DO_BACKFILL=1; DO_SYNC=1; DO_REINDEX=1; DO_SNAPSHOT=1; DO_VERIFY=1; ANY_ACTION=1 ;;
    --snapshot) DO_SNAPSHOT=1; ANY_ACTION=1 ;;
    --verify) DO_VERIFY=1; ANY_ACTION=1 ;;
    --rebuild-index) DO_REINDEX=1; ANY_ACTION=1 ;;
    --check) DO_CHECK=1; ANY_ACTION=1 ;;
    --ack) DO_ACK=1; ANY_ACTION=1 ;;
    --debounce) DEBOUNCE=1 ;;
    --force) FORCE=1 ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die 2 usage.unknown_option "$(kv arg "$1")" ;;
  esac
  shift
done

if [ "$_SOURCED" = 0 ] && [ "$ANY_ACTION" = 0 ]; then
  DO_BACKFILL=1; DO_SYNC=1; DO_REINDEX=1; DO_SNAPSHOT=1; DO_VERIFY=1
fi

# =============================================================================
# preflight
# =============================================================================
RUN_STATUS=ok
ASSERTIONS_FAILED=()
COPY_MODE=reflink
# reconcile_reverse() and check_upstream_quiet() return their result HERE and not
# on stdout. Both call assert_fail(), and a `x=$(fn)` call site runs them in a
# subshell: the RUN_STATUS flip and the ASSERTIONS_FAILED append are discarded
# when it exits, so the reverse-reconciliation alarm and the whole of channel 3
# (upstream liveness) could never make a run exit non-zero. Their info lines were
# also captured into the caller's variable, which then went into health.json as
# a --argjson value.
VANISHED_SOURCES=0
QUIET_WEEKDAYS=0

assert_fail() { # record a failed assertion without aborting the whole pass
  RUN_STATUS=degraded
  ASSERTIONS_FAILED+=("$1")
  warn assert.failed "$(kv detail "$1")"
}

preflight() {
  if [ "$(id -u)" != 0 ]; then
    cat >&2 <<'EOF'
FATAL: cassini-archive-sync must run as root.

  42 of the old bundles carry `recording-segments-*/artifact-remux-work/`
  directories with mode 0700. A non-root traversal cannot enter them, so it
  silently skips 40,248,765,505 B — and `du`, `find` and `rsync` all still
  exit 0. A non-root run would therefore produce an archive that looks
  complete, passes its own byte-conservation check, and is missing 37.5 GiB.

  Run it under sudo, or via the systemd unit (User=root).
EOF
    exit 2
  fi

  for t in jq python3 sqlite3 sha256sum flock btrfs find stat cp; do
    command -v "$t" >/dev/null || die 3 preflight.missing_tool "$(kv tool "$t")"
  done
  command -v ffprobe >/dev/null || warn preflight.missing_tool tool=ffprobe \
    note="legacy room tokens and hpb durations cannot be read; those eras will be deferred"

  [ -d "$SRC_OLD" ] || die 3 preflight.missing_source "$(kv path "$SRC_OLD")"
  [ -d "$SRC_EXA" ] || die 3 preflight.missing_source "$(kv path "$SRC_EXA")"
  [ -d "$SRC_HPB" ] || warn preflight.missing_source "$(kv path "$SRC_HPB")"

  # A source root that EXISTS BUT IS EMPTY is the exact shape of a mountpoint
  # whose mount did not come up — and it is invisible: the pass walks nothing,
  # archives nothing, and writes `status=ok meetings_total=0` with an empty
  # assertions list. That is the silent-green failure this whole project exists
  # to stop, aimed at the half of the corpus that cannot be re-recorded.
  if [ "$DO_BACKFILL" = 1 ]; then
    local n_old n_hpb
    n_old=$(find "$SRC_OLD" -mindepth 1 -maxdepth 1 -printf . | wc -c)
    [ "$n_old" -gt 0 ] || die 3 preflight.empty_source "$(kv path "$SRC_OLD")" \
      hint="the old corpus holds 103 entries; an empty one means the filesystem is not mounted where it is expected. Refusing to record a zero-meeting backfill as a success."
    if [ "$EXPECT_HPB" != 0 ]; then
      n_hpb=0
      [ -d "$SRC_HPB" ] && n_hpb=$(find "$SRC_HPB" -mindepth 1 -maxdepth 1 -name '*.mkv' -printf . | wc -c)
      [ "$n_hpb" -gt 0 ] || die 3 preflight.empty_source "$(kv path "$SRC_HPB")" \
        hint="EXPECT_HPB=$EXPECT_HPB but the hpb corpus is empty or absent; set EXPECT_HPB=0 if that era is genuinely gone."
    fi
  fi

  local ar sr vr
  ar=$(abspath "$ARCHIVE_ROOT"); sr=$(abspath "$SNAPSHOT_ROOT"); vr=$(abspath "$VAR_DIR")
  local s
  for s in "$SRC_OLD" "$SRC_HPB" "$SRC_EXA_DATA"; do
    local sp; sp=$(abspath "$s")
    case "$ar" in "$sp"|"$sp"/*) die 3 preflight.nested_root "$(kv archive "$ar")" "$(kv source "$sp")" ;; esac
    case "$sp" in "$ar"|"$ar"/*) die 3 preflight.nested_root "$(kv archive "$ar")" "$(kv source "$sp")" ;; esac
  done

  WORKTMP=$(mktemp -d -t cassini-archive.XXXXXXXX)
  WRITABLE_ROOTS=("$ar" "$sr" "$vr" "$(abspath "$HEARTBEAT_DIR")" "$WORKTMP")

  [ -d "$ARCHIVE_ROOT" ] || die 3 preflight.missing_archive_root "$(kv path "$ARCHIVE_ROOT")" \
    hint="create it first:  btrfs subvolume create $ARCHIVE_ROOT"
  btrfs subvolume show "$ARCHIVE_ROOT" >/dev/null 2>&1 \
    || die 3 preflight.not_a_subvolume "$(kv path "$ARCHIVE_ROOT")" \
       hint="btrfs subvolume create $ARCHIVE_ROOT — snapshots are impossible otherwise"

  local free
  free=$(df -B1 --output=avail "$ARCHIVE_ROOT" | tail -1 | tr -d ' ')
  [ "$free" -ge "$MIN_FREE_BYTES" ] \
    || die 3 preflight.low_space free_bytes="$free" required_bytes="$MIN_FREE_BYTES"

  a_mkdir "$MEETINGS_DIR"; a_mkdir "$STAGING_DIR"; a_mkdir "$STATE_DIR"
  a_mkdir "$REPORTS_DIR"; a_mkdir "$QUARANTINE_DIR"
  a_mkdir "$UNATTRIBUTED_DIR/jobs"; a_mkdir "$UNATTRIBUTED_DIR/derived"; a_mkdir "$UNATTRIBUTED_DIR/hpb-mjr"
  a_mkdir "$STATE_DIR/runs"; a_mkdir "$STATE_DIR/jobs-db"; a_mkdir "$STATE_DIR/site"
  a_mkdir "$VAR_DIR"; a_mkdir "$SNAPSHOT_ROOT"

  if [ "$DRY_RUN" = 0 ]; then
    LOG_SINK="$STATE_DIR/runs/$RUN_ID.log"
    guard_write "$LOG_SINK"
    : >"$LOG_SINK"
  fi

  reflink_probe
  info preflight.ok "$(kv archive "$ARCHIVE_ROOT")" copy_mode="$COPY_MODE" \
    free_bytes="$free" dry_run="$DRY_RUN"
}

# Prove reflink works from EVERY source root into the archive before copying a
# single meeting. Same fs is necessary but not sufficient (nodatacow, a future
# separate device, an exotic mount option), and finding out halfway through a
# 123.6 GiB backfill is not acceptable.
#
# The probe SOURCE must live on the ARCHIVE's own filesystem. It used to be
# written into $WORKTMP, which is `mktemp -d` — i.e. /tmp, which on george is
# tmpfs (st_dev 38) while /mnt/data is btrfs (st_dev 48). FICLONE is EXDEV
# across filesystems and is not implemented by tmpfs at all, so that probe could
# only ever fail: EVERY --apply run died here telling the operator that reflink
# does not work into the archive and that ALLOW_FULL_COPY=1 is the way past it —
# which is exactly how a 123.6 GiB real copy gets authorised on a misdiagnosis.
# PrivateTmp=yes in the units makes that worse, not better.
reflink_probe() {
  local probe="$STATE_DIR/.reflink-probe-src-$RUN_ID"
  local dst="$ARCHIVE_ROOT/.probe-$RUN_ID"
  guard_write "$probe"
  guard_write "$dst"
  if [ "$DRY_RUN" = 1 ]; then
    info dry.reflink_probe note="skipped in dry run; --apply proves it before copying anything"
    return 0
  fi
  head -c 1048576 /dev/urandom >"$probe"

  # Step 1 -- does the ARCHIVE filesystem support reflink at all? This is the
  # more basic question, so it is asked first: if the answer is no, every
  # source pair would fail too, and "different filesystem" would be the wrong
  # diagnosis.
  if cp --reflink=always "$probe" "$dst" 2>/dev/null; then
    COPY_MODE=reflink
    rm -f -- "$probe" "$dst"
    info reflink.ok "$(kv probe_source "$probe")" note="probe source is on the archive filesystem"
  else
    rm -f -- "$probe" "$dst"
    [ "$ALLOW_FULL_COPY" = 1 ] || die 3 reflink.unavailable_on_archive \
      "$(kv archive "$ARCHIVE_ROOT")" \
      hint="cp --reflink=always failed inside the archive itself; refusing to write ~123.6 GiB. Set ALLOW_FULL_COPY=1 to override."
    COPY_MODE=copy
    warn reflink.degraded note="ALLOW_FULL_COPY=1: this run writes real bytes"
    return 0
  fi

  # Step 2 -- is each SOURCE on that same filesystem? Prove it by performing the
  # exact operation the backfill performs: clone a real source file into the
  # archive. This reads the source (never writes it) and writes only inside the
  # archive.
  #
  # Do NOT compare st_dev here. Under the units' mount namespace
  # (ProtectSystem=strict + BindPaths) the same underlying btrfs reports a
  # different st_dev for a bind-mounted path than for its original path
  # (observed on george 2026-08-28: source_dev=48 archive_dev=1048653), so an
  # st_dev equality test hard-refuses a pair that clones perfectly.
  local s sfile spair
  for s in "$SRC_OLD" "$SRC_HPB" "$SRC_EXA"; do
    [ -d "$s" ] || continue
    sfile=$(find "$s" -xdev -type f -print -quit 2>/dev/null) || sfile=""
    [ -n "$sfile" ] || continue
    spair="$ARCHIVE_ROOT/.probe-pair-$RUN_ID"
    guard_write "$spair"
    if cp --reflink=always "$sfile" "$spair" 2>/dev/null; then
      rm -f -- "$spair"
    else
      rm -f -- "$spair"
      [ "$ALLOW_FULL_COPY" = 1 ] || die 3 reflink.different_filesystem \
        "$(kv source "$s")" "$(kv probe_file "$sfile")" \
        hint="the archive supports reflink but cloning from this source into it failed; the two are not on one btrfs filesystem. This run would have to copy ~123.6 GiB. Set ALLOW_FULL_COPY=1 to override."
      COPY_MODE=copy
      warn reflink.degraded note="ALLOW_FULL_COPY=1: this run writes real bytes"
      return 0
    fi
  done
}

# =============================================================================
# §2 naming
# =============================================================================
slugify() {
  local s
  s=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]' | sed -e 's/[^a-z0-9]\+/-/g' -e 's/^-\+//' -e 's/-\+$//')
  s=${s:0:40}
  s=$(printf '%s' "$s" | sed -e 's/-\+$//')
  [ -n "$s" ] || s=untitled
  printf '%s' "$s"
}

# A slug that ends in -YYYY-MM-DD would be swallowed by describeMeeting's legacy
# branch and render a wrong date. Refuse rather than silently mangle.
assert_slug_ok() {
  local s=$1
  [[ $s =~ ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$ ]] \
    || die 5 slug.invalid "$(kv slug "$s")"
  [[ ! $s =~ -[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] \
    || die 5 slug.ends_with_date "$(kv slug "$s")"
}

json_get() { # json_get <file> <jq-filter>  -> empty string on null/missing
  jq -r "$2 // empty" "$1" 2>/dev/null || true
}

# ffprobe with an explicit file: protocol. The old bare mkv names contain colons
# (daily-meeting-2026-03-10--12:30.mkv) and ffmpeg otherwise reads "12" as a URL
# protocol and fails — which is exactly how the room tokens stayed "unknown" in
# three earlier designs.
ffprobe_title() {
  ffprobe -v error -show_entries format_tags=title -of default=nw=1:nk=1 -i "file:$1" 2>/dev/null || true
}
ffprobe_duration() {
  ffprobe -v error -show_entries format=duration -of default=nw=1:nk=1 -i "file:$1" 2>/dev/null || true
}

# Fields produced by identify(): every era fills all of them.
ID_ERA="" ID_LAYOUT="" ID_ROOM="" ID_ROOM_SOURCE="" ID_ANCHOR_ISO="" ID_ANCHOR_STAMP=""
ID_ANCHOR_KIND="" ID_ZONE_PROVEN="" ID_STARTED="" ID_STARTED_SOURCE="" ID_PRECISION=""
ID_SLUG="" ID_PREFIX="" ID_DIRNAME="" ID_KEY="" ID_DURATION="" ID_RECORDER=""
ID_CAPTURE_STATE="" ID_JOB_ID="" ID_SOURCE_ROOT="" ID_NOTES=""

reset_identity() {
  ID_ERA=""; ID_LAYOUT=""; ID_ROOM=""; ID_ROOM_SOURCE=""; ID_ANCHOR_ISO=""; ID_ANCHOR_STAMP=""
  ID_ANCHOR_KIND=""; ID_ZONE_PROVEN=""; ID_STARTED=""; ID_STARTED_SOURCE=""; ID_PRECISION=""
  ID_SLUG=""; ID_PREFIX=""; ID_DIRNAME=""; ID_KEY=""; ID_DURATION=""; ID_RECORDER=""
  ID_CAPTURE_STATE=""; ID_JOB_ID=""; ID_SOURCE_ROOT=""; ID_NOTES=""
}

# session.json lives at session/ (finalized) or sessions/<id>/ (crashed mid-run).
# Both layouts are first-class; the crashed ones hold the ONLY copy of 26 old
# meetings, so "failed" is never a reason to skip.
find_session_json() {
  local dir=$1
  if [ -f "$dir/session/session.json" ]; then
    printf '%s' "$dir/session/session.json"
    return 0
  fi
  local c
  c=$(find "$dir/sessions" -mindepth 2 -maxdepth 2 -name session.json -print 2>/dev/null | LC_ALL=C sort | head -1)
  [ -n "$c" ] && printf '%s' "$c"
  return 0
}

# Whole-second truncation. Millisecond precision is NOT stable: in 3 of the 42
# crashed bundles the sessions/<recording_…Z> directory name and
# session.json:started_wall_utc disagree at the millisecond (04-14, 05-13,
# 07-08) while agreeing to the second in all 42.
#
# `${1%%.*}` strips nothing when the input carries no fraction, so re-appending
# the Z unconditionally produced `2026-06-17T09:38:24ZZ` — which then reached
# ID_DIRNAME, `key`, `anchor_utc` and `date_local`. Go's RFC3339Nano drops
# trailing zeros, so a whole-second producer stamp is reachable even though
# nothing in today's corpus carries one.
truncate_second() {
  local s=${1%%.*}
  case "$1" in *Z) [ "$s" = "$1" ] || s="${s}Z" ;; esac
  printf '%s' "$s"
}

identify_session_dir() { # cron | exapp
  local dir=$1 era=$2
  local sj; sj=$(find_session_json "$dir")
  [ -n "$sj" ] || return 1
  ID_LAYOUT=sessions
  [ -f "$dir/session/session.json" ] && ID_LAYOUT=session
  ID_ROOM=$(json_get "$sj" '.platform.room')
  ID_ROOM_SOURCE="session.json"
  ID_RECORDER=$(json_get "$sj" '.platform.recorder_identity.display')
  ID_STARTED=$(json_get "$sj" '.started_wall_utc')
  ID_STARTED_SOURCE="session.json:started_wall_utc"
  ID_PRECISION=nanosecond
  [ -n "$ID_ROOM" ] || return 1
  [ -n "$ID_STARTED" ] || return 1
  ID_ANCHOR_ISO=$(truncate_second "$ID_STARTED")
  ID_ANCHOR_KIND="session-start"
  ID_ZONE_PROVEN=true
  ID_ERA=$era
  return 0
}

iso_to_stamp() { # 2026-07-31T10:30:12Z -> 2026-07-31T103012Z
  local iso=$1 z=""
  case "$iso" in *Z) z=Z; iso=${iso%Z} ;; esac
  printf '%s%s%s' "${iso%%T*}T" "$(printf '%s' "${iso#*T}" | tr -d ':')" "$z"
}

finish_identity() {
  assert_slug_ok "$ID_SLUG"
  ID_ANCHOR_STAMP=$(iso_to_stamp "$ID_ANCHOR_ISO")
  ID_PREFIX="$ID_ANCHOR_STAMP--$ID_ROOM--$ID_ERA"
  ID_DIRNAME="$ID_PREFIX--$ID_SLUG"
  ID_KEY="$ID_ANCHOR_ISO|$ID_ROOM|$ID_ERA"
}

identify() { # identify <path> <era-hint>; sets ID_*; returns 1 if it must be quarantined
  local p=$1 hint=$2
  reset_identity
  local base; base=$(basename "$p")

  case "$hint" in
    cron)
      ID_SOURCE_ROOT="old-recordings"
      identify_session_dir "$p" cron || return 1
      ID_CAPTURE_STATE=$(json_get "$p/cassini.json" '.state'); ID_CAPTURE_STATE=${ID_CAPTURE_STATE:-unknown}
      # basename with the FIRST date removed, dashes collapsed:
      #   daily-meeting-2026-03-30-part2 -> daily-meeting-part2
      ID_SLUG=$(slugify "$(printf '%s' "$base" | sed -e 's/[0-9]\{4\}-[0-9]\{2\}-[0-9]\{2\}//')")
      finish_identity
      ;;
    exapp)
      ID_SOURCE_ROOT="exapp-current"
      identify_session_dir "$p" exapp || return 1
      ID_CAPTURE_STATE=$(json_get "$p/cassini.json" '.state'); ID_CAPTURE_STATE=${ID_CAPTURE_STATE:-unknown}
      ID_JOB_ID=${base%.run}
      ID_LAYOUT=run
      local rn; rn=${JOB_ROOM_NAME[$ID_JOB_ID]:-}
      ID_SLUG=$(slugify "${rn:-untitled}")
      finish_identity
      ;;
    legacy)
      ID_SOURCE_ROOT="old-recordings"
      ID_ERA=legacy; ID_LAYOUT=bare-mkv; ID_CAPTURE_STATE=imported
      # The room token is EVIDENCE, not an assumption: all 15 bare mkv carry
      # TAG:title = "Cassini Go Recording <token>".
      local title; title=$(ffprobe_title "$p")
      if [[ $title =~ ^Cassini\ Go\ Recording\ ([a-z0-9]{8})$ ]]; then
        ID_ROOM=${BASH_REMATCH[1]}
        ID_ROOM_SOURCE="mkv-title-tag"
      else
        QUARANTINE_REASON="no room evidence in mkv title tag: '${title}'"
        return 1
      fi
      local stem=${base%.mkv} hh mm ss
      if [[ $stem =~ --([0-9]{2}):([0-9]{2})(:([0-9]{2}))? ]]; then
        hh=${BASH_REMATCH[1]}; mm=${BASH_REMATCH[2]}; ss=${BASH_REMATCH[4]:-00}
        ID_STARTED_SOURCE=filename
        ID_PRECISION=second
        [ -n "${BASH_REMATCH[4]:-}" ] || ID_PRECISION=minute
      else
        # 2026-03-26.mkv carries no time. The derived .meeting built from it
        # does (daily-meeting-2026-03-26--12:30), so borrow it rather than
        # inventing 00:00 and mis-sorting the day.
        local dm=${DERIVED_BY_SOURCE_BASENAME[$base]:-}
        if [ -z "$dm" ]; then
          QUARANTINE_REASON="no time in filename and no corroborating derived .meeting"
          return 1
        fi
        local dmb; dmb=$(basename "$dm" .meeting)
        if [[ $dmb =~ --([0-9]{2}):([0-9]{2}) ]]; then
          hh=${BASH_REMATCH[1]}; mm=${BASH_REMATCH[2]}; ss=00
          ID_STARTED_SOURCE="derived-meeting-basename"
          ID_PRECISION=minute
        else
          QUARANTINE_REASON="derived .meeting basename carries no time: $dmb"
          return 1
        fi
      fi
      local d
      if [[ $stem =~ ([0-9]{4}-[0-9]{2}-[0-9]{2}) ]]; then d=${BASH_REMATCH[1]}; else
        QUARANTINE_REASON="no date in bare mkv filename"; return 1
      fi
      # No Z: the zone of a cron-written filename was never measured, only
      # corroborated. An honest missing Z beats a confident wrong one.
      ID_ANCHOR_ISO="${d}T${hh}:${mm}:${ss}"
      ID_ANCHOR_KIND="filename-wallclock"
      ID_ZONE_PROVEN=false
      ID_STARTED="$ID_ANCHOR_ISO"
      ID_RECORDER=""
      local sl
      sl=$(printf '%s' "$stem" | sed -e 's/[0-9]\{4\}-[0-9]\{2\}-[0-9]\{2\}//' -e 's/[0-9]\{2\}:[0-9]\{2\}\(:[0-9]\{2\}\)\?//')
      sl=$(slugify "$sl")
      [ "$sl" = untitled ] && [ -n "${DERIVED_BY_SOURCE_BASENAME[$base]:-}" ] && {
        local dmb2; dmb2=$(basename "${DERIVED_BY_SOURCE_BASENAME[$base]}" .meeting)
        sl=$(slugify "$(printf '%s' "$dmb2" | sed -e 's/[0-9]\{4\}-[0-9]\{2\}-[0-9]\{2\}//' -e 's/[0-9]\{2\}:[0-9]\{2\}//')")
      }
      ID_SLUG=$sl
      finish_identity
      ;;
    hpb)
      ID_SOURCE_ROOT="hpb"
      ID_ERA=hpb; ID_LAYOUT=hpb-mkv; ID_CAPTURE_STATE=imported
      # Recording-<room>-YYYY-MM-DD_HH-MM-SS_<usec>.mkv
      if [[ $base =~ ^Recording-([a-z0-9]{8})-([0-9]{4}-[0-9]{2}-[0-9]{2})_([0-9]{2})-([0-9]{2})-([0-9]{2})_[0-9]+\.mkv$ ]]; then
        ID_ROOM=${BASH_REMATCH[1]}
        ID_ROOM_SOURCE="hpb-filename"
        ID_ANCHOR_ISO="${BASH_REMATCH[2]}T${BASH_REMATCH[3]}:${BASH_REMATCH[4]}:${BASH_REMATCH[5]}Z"
      else
        QUARANTINE_REASON="hpb filename does not match Recording-<room>-<date>_<time>_<usec>.mkv"
        return 1
      fi
      # The stamp is a FINALIZE time, proven UTC by the sidecar's generated_at
      # (1–22 s later, +00:00). It is deliberately the anchor rather than the
      # derived start: the filename is byte-stable, whereas
      # end − ffprobe_duration would silently re-key a meeting if ffprobe ever
      # changed its rounding.
      ID_ANCHOR_KIND="recording-finalized"
      ID_ZONE_PROVEN=true
      ID_DURATION=$(ffprobe_duration "$p")
      if [ -n "$ID_DURATION" ]; then
        ID_STARTED=$(python3 -c 'import sys,datetime
a=datetime.datetime.strptime(sys.argv[1],"%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=datetime.timezone.utc)
print((a-datetime.timedelta(seconds=float(sys.argv[2]))).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3]+"Z")' \
          "$ID_ANCHOR_ISO" "$ID_DURATION")
        ID_STARTED_SOURCE="hpb-filename-minus-duration"
        ID_PRECISION="approx-second"
      else
        ID_STARTED=""
        ID_STARTED_SOURCE="hpb-filename-minus-duration"
        ID_PRECISION="approx-second"
        ID_NOTES="ffprobe-duration-unavailable"
      fi
      ID_SLUG="talk-recording"
      ID_RECORDER=""
      finish_identity
      ;;
    *) die 5 identify.unknown_era "$(kv hint "$hint")" ;;
  esac
  return 0
}

# =============================================================================
# source enumeration, exclusions, fingerprint
# =============================================================================
# Every omission is named, byte-counted and auditable. Nothing is ever deleted
# from a source to make an exclusion true.
is_excluded_rel() { # is_excluded_rel <relpath> -> prints reason, or nothing
  local rel=$1
  case "$rel" in
    artifact-remux-work/*|*/artifact-remux-work/*) printf 'orphan-remux-scratch'; return 0 ;;
    .*|*/.*) printf 'operator-transient-dotfile'; return 0 ;;
    *.backup) printf 'operator-transient-backup'; return 0 ;;
  esac
  return 0
}

PLAN_SRC=() PLAN_DST=() PLAN_BYTES=0
EXCL_LINES=() EXCL_BYTES=0 EXCL_FILES=0
TOTAL_FILE_BYTES=0 TOTAL_FILES=0 SYMLINK_COUNT=0

# plan_dir <srcdir>: fills PLAN_* / EXCL_* / TOTAL_* for a directory source.
plan_dir() {
  local root=$1
  PLAN_SRC=(); PLAN_DST=(); PLAN_BYTES=0
  EXCL_LINES=(); EXCL_BYTES=0; EXCL_FILES=0
  TOTAL_FILE_BYTES=0; TOTAL_FILES=0; SYMLINK_COUNT=0
  local rel size reason
  while IFS=$'\t' read -r -d '' size rel; do
    TOTAL_FILES=$((TOTAL_FILES + 1))
    TOTAL_FILE_BYTES=$((TOTAL_FILE_BYTES + size))
    reason=$(is_excluded_rel "$rel")
    if [ -n "$reason" ]; then
      EXCL_LINES+=("$rel"$'\t'"$size"$'\t'"$reason")
      EXCL_BYTES=$((EXCL_BYTES + size))
      EXCL_FILES=$((EXCL_FILES + 1))
    else
      PLAN_SRC+=("$root/$rel"); PLAN_DST+=("$rel")
      PLAN_BYTES=$((PLAN_BYTES + size))
    fi
  done < <(find "$root" -type f -printf '%s\t%P\0')
  SYMLINK_COUNT=$(find "$root" -type l | wc -l)
}

# plan_files <dstdir-relative-name>=<abs src> ... for the file-shaped eras
plan_files() {
  PLAN_SRC=(); PLAN_DST=(); PLAN_BYTES=0
  EXCL_LINES=(); EXCL_BYTES=0; EXCL_FILES=0
  TOTAL_FILE_BYTES=0; TOTAL_FILES=0; SYMLINK_COUNT=0
  local pair src dst size
  for pair in "$@"; do
    dst=${pair%%=*}; src=${pair#*=}
    [ -f "$src" ] || continue
    size=$(stat -c %s -- "$src")
    PLAN_SRC+=("$src"); PLAN_DST+=("$dst")
    PLAN_BYTES=$((PLAN_BYTES + size))
    TOTAL_FILES=$((TOTAL_FILES + 1)); TOTAL_FILE_BYTES=$((TOTAL_FILE_BYTES + size))
  done
}

# The fingerprint is over (relpath, size, mtime) of exactly the files we ingest.
# It is what makes a re-run free: an unchanged meeting costs one glob and one
# find, and reads zero media bytes.
#
# %Y (epoch seconds), never %y: %y renders the mtime in the CALLER's TZ, so a
# run with a different TZ from the previous one would report every already-
# ingested source as `source-mutated` — a corpus-wide false conflict that looks
# exactly like the alarm you most want to trust.
fingerprint_plan() {
  local i
  for i in "${!PLAN_SRC[@]}"; do
    printf '%s\t%s\n' "${PLAN_DST[$i]}" "$(stat -c '%s|%Y' -- "${PLAN_SRC[$i]}")"
  done | LC_ALL=C sort | sha256sum | cut -d' ' -f1
}

ledger_fingerprint() { # ledger_fingerprint <key>
  [ -f "$INGESTED_TSV" ] || return 0
  awk -F'\t' -v k="$1" '$1==k{fp=$4} END{if(fp!="")print fp}' "$INGESTED_TSV"
}

record_conflict() {
  local key=$1 a=$2 b=$3 reason=$4
  printf '%s\t%s\t%s\t%s\t%s\n' "$key" "$a" "$b" "$reason" "$(now_utc)" | a_append "$CONFLICTS_TSV"
  RUN_STATUS=degraded
  CONFLICT_COUNT=$((CONFLICT_COUNT + 1))
  warn conflict "$(kv key "$key")" "$(kv a "$a")" "$(kv b "$b")" "$(kv reason "$reason")"
}

quarantine_source() {
  local p=$1 reason=$2
  # An anomaly nobody has fixed is re-detected on EVERY pass. Appending a fresh
  # row each time grows reports/anomalies.tsv without bound and inflates
  # healthcheck A7's count with one fact repeated N times, which is how a report
  # stops being read. The row is the fact, not the sighting; the run log carries
  # the per-run timestamps.
  local seen=0
  if [ -f "$ANOMALIES_TSV" ] \
     && awk -F'\t' -v p="$p" -v r="$reason" '$2 == p && $3 == r { found = 1 } END { exit !found }' "$ANOMALIES_TSV"; then
    seen=1
  fi
  [ "$seen" = 0 ] && printf '%s\t%s\t%s\n' "$(now_utc)" "$p" "$reason" | a_append "$ANOMALIES_TSV"
  RUN_STATUS=degraded
  warn quarantine "$(kv path "$p")" "$(kv reason "$reason")"
}

# =============================================================================
# §5.4 per-meeting transaction
# =============================================================================
STAT_NEW=0 STAT_SKIP=0 STAT_DEFER=0 STAT_QUAR=0 CONFLICT_COUNT=0 PHYSICAL_BYTES=0
# source path -> directory name a dry run WOULD have created (§6: the plan has
# to be as resolvable as the result, or the plan alarms about itself).
declare -A PLANNED_MEETING=()
# dirname -> anchor|started|has_mix|era for the same planned captures. The
# date-uniqueness resolver and the .mjr window matcher read meeting.json out of
# committed directories, of which a dry run has none — so without this the very
# first `--dry-run --all`, the plan the operator is told to review before
# authorising 123.6 GiB, reports the 3 /work/work/*-recovered.mkv bundles as
# unattributable and all 7 .mjr as having zero candidate windows, and exits 5
# on ten alarms about its own dry-ness.
declare -A PLANNED_META=()

ingest_one() { # ingest_one <srcpath> <era-hint> [extra plan_files pairs...]
  local src=$1 hint=$2
  shift 2
  QUARANTINE_REASON=""
  if ! identify "$src" "$hint"; then
    quarantine_source "$src" "${QUARANTINE_REASON:-unidentifiable}"
    STAT_QUAR=$((STAT_QUAR + 1))
    return 0
  fi

  if [ $# -gt 0 ]; then plan_files "$@"; else plan_dir "$src"; fi

  if [ "$SYMLINK_COUNT" != 0 ]; then
    quarantine_source "$src" "source contains $SYMLINK_COUNT symlink(s); refusing to guess what they mean"
    STAT_QUAR=$((STAT_QUAR + 1))
    return 0
  fi

  local fp; fp=$(fingerprint_plan)

  push_nullglob
  local hits=("$MEETINGS_DIR/$ID_PREFIX--"*)
  pop_nullglob
  if [ "${#hits[@]}" -gt 1 ]; then
    record_conflict "$ID_KEY" "$src" "${hits[*]}" ambiguous-prefix
    return 0
  fi
  if [ "${#hits[@]}" -eq 1 ]; then
    local old; old=$(ledger_fingerprint "$ID_KEY")
    if [ "$old" = "$fp" ]; then
      STAT_SKIP=$((STAT_SKIP + 1))
      info ingest.skip "$(kv key "$ID_KEY")" reason=unchanged
      return 0
    fi
    if [ -z "$old" ]; then
      # Crash between rename and ledger append: the directory is real, the
      # ledger line is not. Re-derive the line; never re-copy.
      printf '%s\t%s\t%s\t%s\n' "$ID_KEY" "$(basename "${hits[0]}")" "$(now_utc)" "$fp" | a_append "$INGESTED_TSV"
      info ingest.ledger_repaired "$(kv key "$ID_KEY")"
      STAT_SKIP=$((STAT_SKIP + 1))
      return 0
    fi
    # A changed fingerprint on an already-ingested source is a conflict, never a
    # silent heal. The archived copy is not touched.
    record_conflict "$ID_KEY" "$src" "${hits[0]}" "source-mutated old_fp=$old new_fp=$fp"
    return 0
  fi

  local total=$((PLAN_BYTES + EXCL_BYTES))
  if [ "$total" != "$TOTAL_FILE_BYTES" ]; then
    quarantine_source "$src" "byte conservation failed: copied=$PLAN_BYTES excluded=$EXCL_BYTES total=$TOTAL_FILE_BYTES"
    STAT_QUAR=$((STAT_QUAR + 1))
    return 0
  fi

  info ingest.plan "$(kv key "$ID_KEY")" "$(kv dir "$ID_DIRNAME")" era="$ID_ERA" \
    files="${#PLAN_SRC[@]}" bytes="$PLAN_BYTES" excluded_files="$EXCL_FILES" \
    excluded_bytes="$EXCL_BYTES" layout="$ID_LAYOUT" room_source="$ID_ROOM_SOURCE"

  if [ "$DRY_RUN" = 1 ]; then
    STAT_NEW=$((STAT_NEW + 1))
    PLANNED_MEETING[$src]="$ID_DIRNAME"
    local pmix=false
    case "$ID_LAYOUT" in
      # bare-mkv / hpb-mkv get the uniform recording.mkv read path as a symlink;
      # every other layout has a real recording.mkv in the plan or has no mix.
      bare-mkv|hpb-mkv) pmix=true ;;
      *) printf '%s\n' "${PLAN_DST[@]}" | grep -qx 'recording.mkv' && pmix=true ;;
    esac
    PLANNED_META[$ID_DIRNAME]="$ID_ANCHOR_ISO|$ID_STARTED|$pmix|$ID_ERA"
    return 0
  fi

  local stage="$STAGING_DIR/$ID_DIRNAME.partial"
  a_rm "$stage"
  a_mkdir "$stage"

  local i d phys=0
  for i in "${!PLAN_SRC[@]}"; do
    d="${PLAN_DST[$i]}"
    case "$d" in
      ARCHIVE|ARCHIVE/*|derived|derived/*)
        quarantine_source "$src" "source entry collides with an archive-owned name: $d"
        a_rm "$stage"; STAT_QUAR=$((STAT_QUAR + 1)); return 0 ;;
    esac
    a_mkdir "$stage/$(dirname "$d")"
    local n; n=$(a_cp_reflink "${PLAN_SRC[$i]}" "$stage/$d")
    phys=$((phys + n))
  done
  PHYSICAL_BYTES=$((PHYSICAL_BYTES + phys))

  # Additive, reversible, self-evidently ours: a relative symlink that gives
  # every era one uniform read path. The archive never fabricates a producer
  # file, so the manifest stays a pure function of the source.
  case "$ID_LAYOUT" in
    sessions)
      local sid; sid=$(basename "$(dirname "$(find_session_json "$src")")")
      a_ln "sessions/$sid" "$stage/session" ;;
    bare-mkv|hpb-mkv)
      a_ln "$(basename "$src")" "$stage/recording.mkv" ;;
  esac

  a_mkdir "$stage/ARCHIVE"
  ( cd "$stage" && find . -type f -not -path './ARCHIVE/*' -printf '%P\0' | LC_ALL=C sort -z \
      | xargs -0 -r sha256sum ) > "$WORKTMP/manifest.$$"
  a_write "$stage/ARCHIVE/MANIFEST.sha256" < "$WORKTMP/manifest.$$"
  {
    printf '# source\t%s\n' "$src"
    printf '# ingested_at\t%s\n' "$(now_utc)"
    local j
    for j in "${!PLAN_SRC[@]}"; do printf '%s\t%s\n' "${PLAN_DST[$j]}" "${PLAN_SRC[$j]}"; done
  } | a_write "$stage/ARCHIVE/source.tsv"
  if [ "${#EXCL_LINES[@]}" -gt 0 ]; then
    printf '%s\n' "${EXCL_LINES[@]}" | a_write "$stage/ARCHIVE/EXCLUDED.tsv"
    local e
    for e in "${EXCL_LINES[@]}"; do printf '%s/%s\n' "$src" "$e"; done | a_append "$EXCLUDED_TSV"
  else
    : | a_write "$stage/ARCHIVE/EXCLUDED.tsv"
  fi

  write_meeting_json "$stage" "$src" "$fp" derived_pending

  a_chmod_r a-w "$stage"
  # rename(2) of a directory into a different parent rewrites its ".." entry and
  # therefore needs the directory's own write bit. Seal the top level after the
  # commit, never before it.
  a_chmod u+w "$stage"
  sync -f "$stage/ARCHIVE/MANIFEST.sha256" 2>/dev/null || sync

  # The ONE commit point. meetings/<name> existing IS the completeness proof;
  # there is no separate marker to fall out of sync with it.
  a_mv "$stage" "$MEETINGS_DIR/$ID_DIRNAME"
  a_chmod a-w "$MEETINGS_DIR/$ID_DIRNAME"
  printf '%s\t%s\t%s\t%s\n' "$ID_KEY" "$ID_DIRNAME" "$(now_utc)" "$fp" | a_append "$INGESTED_TSV"

  STAT_NEW=$((STAT_NEW + 1))
  info ingest.done "$(kv key "$ID_KEY")" "$(kv dir "$ID_DIRNAME")" bytes="$PLAN_BYTES" physical_bytes="$phys"
}

# Note: <dir> is the STAGING directory (…​.partial); the row's `dir` field is
# built from ID_DIRNAME, which is the committed name.
write_meeting_json() { # write_meeting_json <dir> <srcpath> <fingerprint> <state>
  local dir=$1 src=$2 fp=$3 state=$4
  local manifest_sha=""
  [ -f "$dir/ARCHIVE/MANIFEST.sha256" ] && manifest_sha=$(sha256sum "$dir/ARCHIVE/MANIFEST.sha256" | cut -d' ' -f1)
  local rtplog_count raw_bytes mix_bytes has_mix raw_kind
  rtplog_count=$(printf '%s\n' "${PLAN_DST[@]}" | grep -c '\.rtplog$' || true)
  raw_bytes=$(raw_bytes_of_plan)
  mix_bytes=0
  [ -f "$dir/recording.mkv" ] && mix_bytes=$(stat -Lc %s "$dir/recording.mkv" 2>/dev/null || echo 0)
  has_mix=false; [ "$mix_bytes" != 0 ] && has_mix=true
  raw_kind=none
  [ "$rtplog_count" != 0 ] && raw_kind=rtplog
  local has_raw=false; [ "$raw_kind" != none ] && has_raw=true
  local date_local; date_local=$(TZ="$LOCAL_TZ" date -d "${ID_ANCHOR_ISO/Z/+00:00}" +%F 2>/dev/null || printf '%s' "${ID_ANCHOR_ISO%%T*}")

  jq -n \
    --arg schema "$SCHEMA" --arg id "$ID_PREFIX" --arg key "$ID_KEY" \
    --arg dir "meetings/$ID_DIRNAME" --arg era "$ID_ERA" \
    --arg recorder "$ID_RECORDER" --arg room "$ID_ROOM" --arg room_source "$ID_ROOM_SOURCE" \
    --arg room_name "${JOB_ROOM_NAME[${ID_JOB_ID:-_}]:-}" --arg owner "${JOB_OWNER[${ID_JOB_ID:-_}]:-}" \
    --arg anchor "$ID_ANCHOR_ISO" --arg anchor_kind "$ID_ANCHOR_KIND" \
    --argjson zone_proven "$ID_ZONE_PROVEN" \
    --arg started "$ID_STARTED" --arg started_source "$ID_STARTED_SOURCE" \
    --arg precision "$ID_PRECISION" --arg date_local "$date_local" \
    --arg ended "${JOB_RECORD_FINISHED[${ID_JOB_ID:-_}]:-}" --arg duration "$ID_DURATION" \
    --arg capture_state "$ID_CAPTURE_STATE" --arg job_id "$ID_JOB_ID" \
    --arg job_state "${JOB_STATE[${ID_JOB_ID:-_}]:-}" --arg job_stage "${JOB_STAGE[${ID_JOB_ID:-_}]:-}" \
    --arg source_root "$ID_SOURCE_ROOT" --arg layout "$ID_LAYOUT" --arg source_path "$src" \
    --arg fp "sha256:$fp" --argjson has_raw "$has_raw" --arg raw_kind "$raw_kind" \
    --argjson rtplog_count "$rtplog_count" --argjson raw_bytes "$raw_bytes" \
    --argjson has_mix "$has_mix" --argjson mix_bytes "$mix_bytes" \
    --argjson bytes_total "$PLAN_BYTES" --argjson files_total "${#PLAN_SRC[@]}" \
    --argjson excluded_bytes "$EXCL_BYTES" --argjson excluded_files "$EXCL_FILES" \
    --arg manifest_sha "sha256:$manifest_sha" --arg copy_mode "$COPY_MODE" \
    --arg state "$state" --arg notes "$ID_NOTES" \
    --arg ingested_at "$(now_utc)" --arg run_id "$RUN_ID" --arg tool_sha "$TOOL_SHA" '
    {
      schema:$schema, id:$id, key:$key, dir:$dir, era:$era,
      recorder_display: (if $recorder=="" then null else $recorder end),
      room:$room, room_source:$room_source,
      room_name: (if $room_name=="" then null else $room_name end),
      owner: (if $owner=="" then null else $owner end),
      anchor_utc:$anchor, anchor_kind:$anchor_kind, anchor_zone_proven:$zone_proven,
      started_at_utc: (if $started=="" then null else $started end),
      started_at_source:$started_source, start_precision:$precision,
      tz_hypothesis: null,
      ended_at_utc: (if $ended=="" then null else $ended end),
      duration_s: (if $duration=="" then null else ($duration|tonumber) end),
      date_local:$date_local, capture_state:$capture_state,
      job_id: (if $job_id=="" then null else $job_id end),
      job_state: (if $job_state=="" then null else $job_state end),
      job_stage: (if $job_stage=="" then null else $job_stage end),
      attempt: null,
      source_root:$source_root, source_layout:$layout, source_paths:[$source_path],
      source_present:true, source_fingerprint:$fp,
      has_raw:$has_raw, raw_kind:$raw_kind, rtplog_count:$rtplog_count, raw_bytes:$raw_bytes,
      has_mix:$has_mix, mix_bytes:$mix_bytes,
      media: (if $has_mix then [{path:"recording.mkv", bytes:$mix_bytes, kind:"mkv-multitrack"}] else [] end),
      derived: [], opus_attached:false,
      buildable:$has_mix, remuxable:$has_raw, concurrent_with: [],
      bytes_total:$bytes_total, files_total:$files_total,
      excluded_bytes:$excluded_bytes, excluded_files:$excluded_files,
      sha256_manifest:$manifest_sha, last_verified_utc:$ingested_at,
      copy_mode:$copy_mode, state:$state,
      notes: (if $notes=="" then [] else [$notes] end),
      ingested_at_utc:$ingested_at, ingest_run_id:$run_id, tool_sha256:$tool_sha
    }' | a_write "$dir/ARCHIVE/meeting.json"
}

raw_bytes_of_plan() {
  local i n=0
  for i in "${!PLAN_DST[@]}"; do
    case "${PLAN_DST[$i]}" in
      *.rtplog|*.idx) n=$((n + $(stat -c %s -- "${PLAN_SRC[$i]}"))) ;;
    esac
  done
  printf '%s' "$n"
}

# =============================================================================
# operator DB (§7 G3, §9)
# =============================================================================
# `declare -A X` alone leaves X UNSET, and `${#X[@]}` on an unset associative
# array is an unbound-variable FATAL under `set -u` (bash 5.2: `${!X[@]}` and
# indexed arrays are fine, this one form is not). An empty operator DB or a
# current/ with no .meeting bundles would therefore abort the whole pass with a
# bash internal error and an undocumented exit 1 — turning "the ExApp volume was
# recreated", the standing risk §5.6 exists to REPORT, into a crash before
# reconcile_reverse ever runs. Assigning an empty literal defines them.
declare -A JOB_STAGE=() JOB_STATE=() JOB_RUNPATH=() JOB_ROOM=() JOB_ROOM_NAME=() JOB_OWNER=()
declare -A JOB_RECORD_FINISHED=() JOB_COMPLETED=()
JOBS_LOADED=0
DB_SNAPSHOT=""

load_jobs_db() {
  [ "$JOBS_LOADED" = 0 ] || return 0
  [ -f "$OPERATOR_DB" ] || die 4 jobsdb.missing "$(kv path "$OPERATOR_DB")"
  DB_SNAPSHOT="$WORKTMP/jobs.sqlite3"
  guard_write "$DB_SNAPSHOT"
  # Exactly the recipe cassini-exapp-backup uses: the live DB is opened mode=ro
  # and copied through SQLite's own online-backup API, so a concurrent writer
  # cannot produce a torn file and no -wal/-shm sidecar can appear in
  # production. If this fails we ingest NO .run and exit 4 — we never fall back
  # to guessing job state from the filesystem.
  sqlite3 "file:${OPERATOR_DB}?mode=ro" ".backup '$DB_SNAPSHOT'" \
    || die 4 jobsdb.backup_failed "$(kv path "$OPERATOR_DB")"
  sqlite3 "$DB_SNAPSHOT" 'pragma integrity_check;' | grep -qx ok \
    || die 4 jobsdb.integrity_check_failed

  # US (0x1f), NOT tab: tab is an IFS *whitespace* character, so bash collapses
  # runs of it and drops empty fields even when IFS is set to tab alone. Every
  # NULL column in the middle of the row therefore shifted all the later columns
  # left by one: a job with a NULL artifact_run_path got the room token as its
  # run path (so the 7 zero-media jobs never got their index row at all), and a
  # job with a NULL room_name got its completed_at read as record_finished_at —
  # which is G3 phase 1's whole gate, i.e. a bundle that had not finished
  # recording could be ingested as complete.
  local id stage state runp room roomname owner recfin comp
  while IFS=$'\x1f' read -r id stage state runp room roomname owner recfin comp; do
    JOB_STAGE[$id]=$stage; JOB_STATE[$id]=$state
    JOB_RUNPATH[$id]=$runp
    JOB_ROOM[$id]=$room; JOB_ROOM_NAME[$id]=$roomname; JOB_OWNER[$id]=$owner
    JOB_RECORD_FINISHED[$id]=$recfin; JOB_COMPLETED[$id]=$comp
  done < <(sqlite3 -separator $'\x1f' "$DB_SNAPSHOT" "
    select id,
           stage,
           state,
           coalesce(artifact_run_path,''),
           coalesce(json_extract(talk_binding,'\$.room_token'), json_extract(request_json,'\$.roomToken'), ''),
           coalesce(json_extract(talk_binding,'\$.room_name'),''),
           coalesce(json_extract(talk_binding,'\$.owner'),''),
           coalesce(record_finished_at,''),
           coalesce(completed_at,'')
      from jobs order by created_at;")

  JOBS_LOADED=1
  if [ "$DRY_RUN" = 0 ]; then
    local ts; ts=$(date -u +%Y-%m-%dT%H%M%SZ)
    guard_write "$STATE_DIR/jobs-db/$ts.sqlite3"
    cp -- "$DB_SNAPSHOT" "$STATE_DIR/jobs-db/$ts.sqlite3"
    prune_keep "$STATE_DIR/jobs-db" "$KEEP_DB_SNAPSHOTS"
  fi
  info jobsdb.loaded rows="${#JOB_STAGE[@]}"
}

# jobs.artifact_run_path is a container path; translate it to compare with what
# we see on the host.
host_path_of() {
  local p=$1
  case "$p" in
    "$CONTAINER_DATA_PREFIX"/*) printf '%s%s' "$SRC_EXA_DATA" "${p#"$CONTAINER_DATA_PREFIX"}" ;;
    *) printf '%s' "$p" ;;
  esac
}

prune_keep() { # keep the newest N entries in a directory of timestamped files
  local dir=$1 keep=$2 f
  push_nullglob
  local all=("$dir"/*)
  pop_nullglob
  [ "${#all[@]}" -le "$keep" ] && return 0
  local n=$(( ${#all[@]} - keep ))
  # NUL-delimited: a `for f in $(...)` here word-splits and globs. Today these
  # names are generated by this script (<UTC>.sqlite3, <RUN_ID>.log) and are
  # safe, but nothing structural says the next caller's will be.
  while IFS= read -r -d '' f; do
    a_rm "$f"
  done < <(printf '%s\0' "${all[@]}" | LC_ALL=C sort -z | head -z -n "$n")
}

# =============================================================================
# derived index (§5.5)
# =============================================================================
declare -A DERIVED_BY_SOURCE=() DERIVED_BY_SOURCE_BASENAME=()
DERIVED_LOADED=0

load_derived_index() {
  [ "$DERIVED_LOADED" = 0 ] || return 0
  push_nullglob
  local m sp
  for m in "$SRC_EXA/current"/*.meeting; do
    sp=$(json_get "$m/cassini.json" '.source_path')
    [ -n "$sp" ] || continue
    DERIVED_BY_SOURCE[$sp]=$m
    DERIVED_BY_SOURCE_BASENAME[$(basename "$sp")]=$m
  done
  pop_nullglob
  DERIVED_LOADED=1
  info derived.index_loaded entries="${#DERIVED_BY_SOURCE[@]}"
}

# =============================================================================
# ingest passes
# =============================================================================
sweep_staging() {
  push_nullglob
  local p
  for p in "$STAGING_DIR"/*.partial; do
    # A crash before the commit rename leaves exactly this. The source is
    # untouched, so discarding and re-ingesting is always safe — and there is no
    # resume logic to get subtly wrong.
    info staging.sweep "$(kv path "$p")"
    a_rm "$p"
  done
  pop_nullglob
}

sweep_orphans() {
  push_nullglob
  local d
  for d in "$MEETINGS_DIR"/*/; do
    d=${d%/}
    [ -f "$d/ARCHIVE/meeting.json" ] && continue
    local q
    q="$QUARANTINE_DIR/$(basename "$d").orphan-$(date -u +%Y%m%dT%H%M%SZ)"
    warn orphan.quarantined "$(kv path "$d")"
    a_mv "$d" "$q"
    RUN_STATUS=degraded
    ASSERTIONS_FAILED+=("meeting dir without ARCHIVE/meeting.json: $(basename "$d")")
  done
  pop_nullglob
}

backfill_old() {
  load_derived_index
  info pass.start pass=backfill-old
  push_nullglob
  local e
  for e in "$SRC_OLD"/*; do
    if [ -d "$e" ]; then
      ingest_one "$e" cron
    elif [[ $e == *.mkv ]]; then
      ingest_one "$e" legacy "$(basename "$e")=$e"
    else
      quarantine_source "$e" "unrecognised entry in $SRC_OLD"
    fi
  done
  local mkv sidecar
  for mkv in "$SRC_HPB"/*.mkv; do
    sidecar="${mkv%.mkv}.json"
    if [ -f "$sidecar" ]; then
      ingest_one "$mkv" hpb "$(basename "$mkv")=$mkv" "$(basename "$sidecar")=$sidecar"
    else
      ingest_one "$mkv" hpb "$(basename "$mkv")=$mkv"
    fi
  done
  pop_nullglob
  info pass.done pass=backfill-old new="$STAT_NEW" skipped="$STAT_SKIP" quarantined="$STAT_QUAR"
}

# §7: four ANDed gates. No sleeps, no mtime quiet windows — every gate is a fact
# a producer wrote, or an ordering against a concrete prior observation.
defer() { # defer <path> <gate> <fingerprint>
  printf '%s\t%s\t%s\t%s\n' "$(now_utc)" "$1" "$2" "$3" | a_append "$PENDING_TSV"
  STAT_DEFER=$((STAT_DEFER + 1))
  info ingest.deferred "$(kv path "$1")" gate="$2"
}

pending_fingerprint() {
  [ -f "$PENDING_TSV" ] || return 0
  awk -F'\t' -v p="$1" '$2==p{fp=$4} END{if(fp!="")print fp}' "$PENDING_TSV"
}

sync_new() {
  load_jobs_db
  load_derived_index
  info pass.start pass=sync-new
  push_nullglob
  local r base job
  for r in "$SRC_EXA/current"/*.run; do
    base=$(basename "$r"); job=${base%.run}

    # G1 rename gate: only entries directly under current/ are candidates, and
    # any dot-prefixed component is hard-skipped. promoteDirectory copies into
    # current/.staging and only then renames into place, so a name visible here
    # has already survived that rename and is byte-complete.
    case "$base" in .*) continue ;; esac

    # G2 manifest gate: cassini's own readiness predicate, not one we invented.
    local kind state
    kind=$(json_get "$r/cassini.json" '.kind')
    state=$(json_get "$r/cassini.json" '.state')
    if [ "$kind" != run ] || [ "$state" != ready ]; then
      defer "$r" "G2:cassini.json kind=$kind state=$state" ""
      continue
    fi

    # G3 phase 1: raw is archived the moment recording finished, whatever the
    # job did afterwards. A job that records fine and dies in build must still
    # get its raw archived that night — that is the failure this exists for.
    if [ -n "${JOB_STAGE[$job]:-}" ]; then
      local recfin runp
      recfin=${JOB_RECORD_FINISHED[$job]:-}
      runp=$(host_path_of "${JOB_RUNPATH[$job]:-}")
      if [ -z "$recfin" ]; then
        defer "$r" "G3:record_finished_at is null" ""
        continue
      fi
      if [ "$runp" != "$r" ]; then
        defer "$r" "G3:artifact_run_path mismatch db='${JOB_RUNPATH[$job]:-}' host='$r'" ""
        continue
      fi
    else
      # G4: a .run with no row at all (DB reset, manual import). Ordering
      # against a concrete prior observation, not a wall-clock wait.
      plan_dir "$r"
      local fp_now; fp_now=$(fingerprint_plan)
      local fp_prev; fp_prev=$(pending_fingerprint "$r")
      if [ "$fp_prev" != "$fp_now" ]; then
        defer "$r" "G4:no job row; first observation" "$fp_now"
        continue
      fi
      info ingest.g4_stable "$(kv path "$r")"
    fi

    ingest_one "$r" exapp
  done
  pop_nullglob

  index_job_only_rows
  attach_derived
  attach_mjr
  info pass.done pass=sync-new new="$STAT_NEW" skipped="$STAT_SKIP" deferred="$STAT_DEFER"
}

# The 7 failed, zero-media jobs get a row of their own. A recorded loss is data:
# the gaps must be visible, not absent.
index_job_only_rows() {
  local job
  for job in "${!JOB_STAGE[@]}"; do
    [ -z "${JOB_RUNPATH[$job]}" ] || continue
    local d="$UNATTRIBUTED_DIR/jobs/$job"
    [ -f "$d/ARCHIVE/meeting.json" ] && continue
    info jobonly.row job="$job" state="${JOB_STATE[$job]}"
    [ "$DRY_RUN" = 1 ] && continue
    a_mkdir "$d/ARCHIVE"
    local anchor date_local
    anchor=$(truncate_second "${JOB_RECORD_FINISHED[$job]}")
    # date_local must be the LOCAL date, exactly as write_meeting_json computes
    # it. Splitting the UTC anchor on "T" put a job that finished after 22:00
    # UTC into by-date/ a day before every meeting row of the same evening.
    date_local=$(TZ="$LOCAL_TZ" date -d "${anchor/Z/+00:00}" +%F 2>/dev/null || printf '%s' "${anchor%%T*}")
    jq -n --arg schema "$SCHEMA" --arg job "$job" \
      --arg room "${JOB_ROOM[$job]}" --arg owner "${JOB_OWNER[$job]}" \
      --arg room_name "${JOB_ROOM_NAME[$job]}" \
      --arg anchor "$anchor" --arg date_local "$date_local" \
      --arg state "${JOB_STATE[$job]}" --arg stage "${JOB_STAGE[$job]}" \
      --arg run_id "$RUN_ID" --arg tool_sha "$TOOL_SHA" --arg at "$(now_utc)" '
      {schema:$schema, id:("job-"+$job), key:("job|"+$job), dir:null, era:"exapp",
       recorder_display:"CassiniRecorder", room:$room, room_source:"jobs.request_json",
       room_name:(if $room_name=="" then null else $room_name end),
       owner:(if $owner=="" then null else $owner end),
       anchor_utc:$anchor, anchor_kind:"job-record-finished", anchor_zone_proven:true,
       started_at_utc:null, started_at_source:"jobs.record_finished_at",
       start_precision:"second", tz_hypothesis:null, ended_at_utc:$anchor,
       duration_s:null, date_local:$date_local, capture_state:"failed",
       job_id:$job, job_state:$state, job_stage:$stage, attempt:null,
       source_root:"exapp-runs", source_layout:"none", source_paths:[], source_present:false,
       source_fingerprint:null, has_raw:false, raw_kind:"none", rtplog_count:0, raw_bytes:0,
       has_mix:false, mix_bytes:0, media:[], derived:[], opus_attached:false,
       buildable:false, remuxable:false, concurrent_with:[], bytes_total:0, files_total:0,
       excluded_bytes:0, excluded_files:0, sha256_manifest:null, last_verified_utc:$at,
       copy_mode:"none", state:"complete", notes:["no-media-recovered"],
       ingested_at_utc:$at, ingest_run_id:$run_id, tool_sha256:$tool_sha}' \
      | a_write "$d/ARCHIVE/meeting.json"
  done
}

# meeting_dir_for_key <key-prefix> -> the single matching meetings/ dir, or empty
meeting_dir_for_prefix() {
  push_nullglob
  local hits=("$MEETINGS_DIR/$1--"*)
  pop_nullglob
  [ "${#hits[@]}" = 1 ] && printf '%s' "${hits[0]}"
  return 0
}

# meeting_dir_by_source <abs source path> — resolve through ARCHIVE/source.tsv
declare -A SOURCE_TO_MEETING=()
SOURCE_MAP_LOADED=0
load_source_map() {
  [ "$SOURCE_MAP_LOADED" = 0 ] || return 0
  push_nullglob
  local d sp
  for d in "$MEETINGS_DIR"/*/; do
    d=${d%/}
    [ -f "$d/ARCHIVE/source.tsv" ] || continue
    sp=$(awk -F'\t' '$1=="# source"{print $2; exit}' "$d/ARCHIVE/source.tsv")
    [ -n "$sp" ] && SOURCE_TO_MEETING[$sp]=$d
  done
  pop_nullglob
  # A dry run commits nothing, so without this every derived bundle whose
  # capture this same pass would have created resolves to nothing and is
  # reported as unattributable — i.e. the very first `--dry-run` on an empty
  # archive raises a false alarm and exits 5. Planned captures count.
  local src
  for src in "${!PLANNED_MEETING[@]}"; do
    [ -n "${SOURCE_TO_MEETING[$src]:-}" ] || SOURCE_TO_MEETING[$src]="$MEETINGS_DIR/${PLANNED_MEETING[$src]}"
  done
  SOURCE_MAP_LOADED=1
}

# The container source_path is mapped onto the HOST entry that was ingested,
# which is the FIRST path component under the era's mount point — not the
# basename. 45 of the 77 imported .meeting bundles carry
# `/recordings/daily-meeting-2026-04-15/recording.mkv`: a path INTO a cron
# bundle. Its basename is the literal string `recording.mkv`, so the old
# basename lookup asked for `<recordings>/recording.mkv`, which is not a path
# and is never a `# source` line — every one of those 45 landed in
# unattributed/derived/, raised an assertion, and made every run exit 5 forever
# from the very first sync. Mapping the first component finds the cron
# directory, which is exactly what was ingested.
resolve_derived_target() { # resolve_derived_target <container source_path> -> dir<TAB>method<TAB>confidence
  local sp=$1 base; base=$(basename "$sp")
  load_source_map
  local cand="" rel="" root="" first=""
  case "$sp" in
    "$CONTAINER_DATA_PREFIX"/operator/jobs/current/*.run)
      cand=${SOURCE_TO_MEETING[$(host_path_of "$sp")]:-}
      [ -n "$cand" ] && { printf '%s\texact-path\thigh' "$cand"; return 0; } ;;
    /recordings/*|/src/recordings/*|/src/daily-meeting-*|/src/hpb-talk-recordings/*)
      case "$sp" in
        /recordings/*)              rel=${sp#/recordings/};              root=$SRC_OLD ;;
        /src/recordings/*)          rel=${sp#/src/recordings/};          root=$SRC_OLD ;;
        /src/hpb-talk-recordings/*) rel=${sp#/src/hpb-talk-recordings/}; root=$SRC_HPB ;;
        /src/daily-meeting-*)       rel=${sp#/src/};                     root=$SRC_OLD ;;
      esac
      first=${rel%%/*}
      if [ -n "$first" ]; then
        cand=${SOURCE_TO_MEETING[$root/$first]:-}
        if [ -n "$cand" ]; then
          if [ "$first" = "$rel" ]; then printf '%s\texact-path\thigh' "$cand"
          else printf '%s\tsource-parent-dir\thigh' "$cand"; fi
          return 0
        fi
      fi
      # Last resort for a shape nobody has seen yet: the basename, in either
      # old-era root. Reported as `basename` so an unexpected match is visible.
      cand=${SOURCE_TO_MEETING[$SRC_OLD/$base]:-}
      [ -z "$cand" ] && cand=${SOURCE_TO_MEETING[$SRC_HPB/$base]:-}
      if [ -n "$cand" ]; then printf '%s\tbasename\thigh' "$cand"; return 0; fi ;;
    /work/work/*-recovered.mkv)
      # /work no longer exists in any container and no script in the repo
      # performs that recovery. The date is the only evidence, so attach only
      # when the whole corpus has exactly ONE meeting with a mixdown that day —
      # and mark it low-confidence forever.
      local d=${base%-recovered.mkv}
      local hits; hits=$(date_unique_meeting "$d")
      if [ -n "$hits" ]; then printf '%s\tdate-unique\tlow' "$hits"; return 0; fi ;;
  esac
  return 1
}

date_unique_meeting() {
  local d=$1 n hit
  push_nullglob
  local m=("$MEETINGS_DIR/${d}T"*)
  pop_nullglob
  n=0; hit=""
  local x
  for x in "${m[@]}"; do
    [ -f "$x/ARCHIVE/meeting.json" ] || continue
    [ "$(json_get "$x/ARCHIVE/meeting.json" '.has_mix')" = true ] || continue
    n=$((n + 1)); hit=$x
  done
  # Captures this same dry run WOULD have committed count too, or the plan
  # alarms about its own dry-ness (see PLANNED_META).
  local name meta pm
  for name in "${!PLANNED_META[@]}"; do
    case "$name" in "${d}T"*) ;; *) continue ;; esac
    [ -d "$MEETINGS_DIR/$name" ] && continue
    meta=${PLANNED_META[$name]}
    IFS='|' read -r _ _ pm _ <<<"$meta"
    [ "$pm" = true ] || continue
    n=$((n + 1)); hit="$MEETINGS_DIR/$name"
  done
  [ "$n" = 1 ] && printf '%s' "$hit"
  return 0
}

# Is <relpath> inside <meeting-dir> fully RECORDED — present in
# ARCHIVE/MANIFEST.sha256, and that manifest the one ARCHIVE/meeting.json was
# written against? Attaching is a_mv-then-refresh_manifest, and a kill between
# the two (SIGKILL, power cut, TimeoutStartSec=6h) commits the component with
# neither the manifest nor meeting.json mentioning it. The `[ -e ]` latch then
# made that permanent: the next pass returned early, so `derived`,
# `opus_attached` and `state` stayed wrong forever while `sha256sum -c` — which
# only checks files it LISTS — kept saying MANIFEST VERIFIES OK, and an operator
# reading index.jsonl would conclude the opus was never produced.
attach_recorded() { # attach_recorded <meeting-dir> <relpath>
  local target=$1 rel=$2
  local man="$target/ARCHIVE/MANIFEST.sha256" mj="$target/ARCHIVE/meeting.json"
  [ -f "$man" ] && [ -f "$mj" ] || return 1
  # sha256sum lines are "<64 hex><2 spaces><path>"; a prefix match covers both a
  # file component and every file under a directory component.
  cut -c 67- -- "$man" | awk -v p="$rel" 'index($0, p) == 1 { found = 1 } END { exit !found }' || return 1
  local recorded actual
  recorded=$(json_get "$mj" '.sha256_manifest')
  actual="sha256:$(sha256sum "$man" | cut -d' ' -f1)"
  [ "$recorded" = "$actual" ]
}

# Re-run refresh_manifest over a bundle whose component landed but whose
# bookkeeping did not. Idempotent, so a false positive costs one re-hash.
repair_attach() { # repair_attach <meeting-dir> <relpath>
  warn attach.repair "$(kv target "$(basename "$1")")" "$(kv component "$2")" \
    reason="component is on disk but not recorded in MANIFEST.sha256 / meeting.json"
  [ "$DRY_RUN" = 1 ] && return 0
  refresh_manifest "$1"
  a_chmod a-w "$1"
}

attach_one() { # attach_one <meeting-dir> <src component dir-or-file> <dest basename> <method> <conf>
  local target=$1 src=$2 name=$3 method=$4 conf=$5
  if [ -e "$target/derived/$name" ]; then
    attach_recorded "$target" "derived/$name" || repair_attach "$target" "derived/$name"
    return 0
  fi
  info attach.plan "$(kv target "$(basename "$target")")" "$(kv component "$name")" method="$method" confidence="$conf"
  [ "$conf" = low ] && printf '%s\t%s\t%s\t%s\n' "$(now_utc)" "$src" "$(basename "$target")" "$method" | a_append "$LOWCONF_TSV"
  [ "$DRY_RUN" = 1 ] && return 0

  local stage="$target/.add-$name"
  a_chmod u+w "$target"
  a_rm "$stage"
  if [ -d "$src" ]; then
    a_mkdir "$stage"
    local rel
    while IFS= read -r -d '' rel; do
      a_mkdir "$stage/$(dirname "$rel")"
      a_cp_reflink "$src/$rel" "$stage/$rel" >/dev/null
    done < <(cd "$src" && find . -type f -printf '%P\0')
  else
    a_mkdir "$(dirname "$stage")"
    a_cp_reflink "$src" "$stage" >/dev/null
  fi
  a_chmod_r a-w "$stage"
  [ -d "$stage" ] && a_chmod u+w "$stage"
  a_mkdir "$target/derived"
  a_mv "$stage" "$target/derived/$name"
  [ -d "$target/derived/$name" ] && a_chmod a-w "$target/derived/$name"
  refresh_manifest "$target"
  a_chmod a-w "$target"
}

refresh_manifest() {
  local target=$1
  a_chmod u+w "$target"
  a_chmod u+w "$target/ARCHIVE"
  ( cd "$target" && find . -type f -not -path './ARCHIVE/*' -printf '%P\0' | LC_ALL=C sort -z \
      | xargs -0 -r sha256sum ) > "$WORKTMP/manifest.$$"
  a_write "$target/ARCHIVE/MANIFEST.sha256" < "$WORKTMP/manifest.$$"
  local msha; msha=$(sha256sum "$target/ARCHIVE/MANIFEST.sha256" | cut -d' ' -f1)
  # `find derived` EXITS 1 when derived/ does not exist. 2>/dev/null hides the
  # message, not the status; `set -o pipefail` propagates it; and a plain
  # assignment is not an AND-OR list, so `set -e` killed the run mid-attach —
  # after MANIFEST.sha256 had been rewritten but before meeting.json, and before
  # reindex and write_health ever ran. The only caller that reaches here with no
  # derived/ is attach_mjr_one, which is precisely the real hpb corpus: no
  # imported .meeting points at a June hpb mkv, so all 7 .mjr attach to bundles
  # that have no derived/ at all.
  local derived_json opus derived_list=""
  if [ -d "$target/derived" ]; then
    derived_list=$(cd "$target" && find derived -mindepth 1 -maxdepth 1 -print | LC_ALL=C sort)
  fi
  derived_json=$(printf '%s\n' "$derived_list" | jq -R . \
    | jq -s 'map(select(length > 0)) | map({kind:(if endswith(".opus") then "opus" else "meeting" end), path:.})')
  opus=false
  compgen -G "$target/derived/*.opus" >/dev/null && opus=true
  jq --arg m "sha256:$msha" --argjson d "$derived_json" --argjson o "$opus" \
     --arg at "$(now_utc)" \
     '.sha256_manifest=$m | .derived=$d | .opus_attached=$o | .last_verified_utc=$at
      | .state=(if ($d|length)>0 then "complete" else .state end)' \
     "$target/ARCHIVE/meeting.json" > "$WORKTMP/mj.$$"
  a_write "$target/ARCHIVE/meeting.json" < "$WORKTMP/mj.$$"
  a_chmod a-w "$target/ARCHIVE"
}

attach_derived() {
  push_nullglob
  local m base job sp res target method conf
  for m in "$SRC_EXA/current"/*.meeting; do
    base=$(basename "$m"); job=${base%.meeting}
    if [[ $job =~ ^[0-7][0-9ABCDEFGHJKMNPQRSTVWXYZ]{25}$ ]]; then
      # G3 phase 2. SetMeetingBundleTitle rewrites cassini.json during
      # build->publish, so a bundle is only stable once the job is done. Gating
      # on stage='done' is what removes the need for any title-stamp mutation
      # whitelist.
      if [ "${JOB_STAGE[$job]:-}" != "done" ] || [ -z "${JOB_COMPLETED[$job]:-}" ]; then
        defer "$m" "G3phase2:stage=${JOB_STAGE[$job]:-none}" ""
        continue
      fi
      target=$(meeting_dir_for_job "$job")
      method=exact-path; conf=high
      [ -n "$target" ] || { unattributed_derived "$m" "job $job has no archived capture"; continue; }
    else
      sp=$(json_get "$m/cassini.json" '.source_path')
      if ! res=$(resolve_derived_target "$sp"); then
        unattributed_derived "$m" "unresolvable source_path=$sp"
        continue
      fi
      IFS=$'\t' read -r target method conf <<<"$res"
    fi
    attach_one "$target" "$m" "$base" "$method" "$conf"
    # .opus is packed by an async goroutine off the stage machine and only 8 of
    # 50 job rows carry artifact_opus_path, so the DB cannot gate it. One stat()
    # per pass, forever, with no completion latch.
    local o="$SRC_EXA/current/${job}.opus"
    [ -f "$o" ] && attach_one "$target" "$o" "${job}.opus" "$method" "$conf"
  done
  pop_nullglob
}

# The capture directory this job's .run was ingested into. Resolved through
# ARCHIVE/source.tsv (the recorded provenance), never by re-deriving the
# identity prefix from the directory name: `sed 's/--[^-]*$//'` only strips the
# trailing slug when the slug has no dash in it, so every real slug
# ("daily-standup-meeting") survived the strip, matched nothing, and sent the
# whole exapp era to unattributed/derived/.
meeting_dir_for_job() {
  load_source_map
  printf '%s' "${SOURCE_TO_MEETING[$SRC_EXA/current/$1.run]:-}"
}

unattributed_derived() {
  local m=$1 reason=$2
  warn derived.unattributed "$(kv path "$m")" "$(kv reason "$reason")"
  RUN_STATUS=degraded
  ASSERTIONS_FAILED+=("unattributed derived: $(basename "$m") ($reason)")
  local d
  d="$UNATTRIBUTED_DIR/derived/$(basename "$m")"
  [ -e "$d" ] && return 0
  [ "$DRY_RUN" = 1 ] && return 0
  a_mkdir "$d"
  local rel
  while IFS= read -r -d '' rel; do
    a_mkdir "$d/$(dirname "$rel")"
    a_cp_reflink "$m/$rel" "$d/$rel" >/dev/null
  done < <(cd "$m" && find . -type f -printf '%P\0')
}

# The 7 .mjr are the only pre-mix data of the hpb era. Their filename carries a
# microsecond epoch; an .mjr whose instant falls inside exactly one hpb
# meeting's [start-300s, anchor] window attaches to it. 0 or >1 matches is an
# alarm, never a guess.
attach_mjr() {
  push_nullglob
  local f base usec ts a s
  for f in "$SRC_HPB"/*.mjr; do
    base=$(basename "$f")
    if [[ $base =~ -([0-9]{13,19})-[a-z]+-[0-9]+\.mjr$ ]]; then
      usec=${BASH_REMATCH[1]}
    else
      warn mjr.unparseable "$(kv path "$f")"; continue
    fi
    ts=$((usec / 1000000))
    local hits=() d
    for d in "$MEETINGS_DIR"/*--hpb--*/; do
      d=${d%/}
      [ -f "$d/ARCHIVE/meeting.json" ] || continue
      a=$(json_get "$d/ARCHIVE/meeting.json" '.anchor_utc')
      s=$(json_get "$d/ARCHIVE/meeting.json" '.started_at_utc')
      if [ -z "$a" ] || [ -z "$s" ]; then continue; fi
      mjr_window_hit "$ts" "$a" "$s" && hits+=("$d")
    done
    # Windows this same dry run WOULD have committed count too, or every .mjr is
    # reported as having zero candidates on the very first plan (PLANNED_META).
    local name pera
    for name in "${!PLANNED_META[@]}"; do
      [ -d "$MEETINGS_DIR/$name" ] && continue
      IFS='|' read -r a s _ pera <<<"${PLANNED_META[$name]}"
      [ "$pera" = hpb ] || continue
      if [ -z "$a" ] || [ -z "$s" ]; then continue; fi
      mjr_window_hit "$ts" "$a" "$s" && hits+=("$MEETINGS_DIR/$name")
    done
    if [ "${#hits[@]}" = 1 ]; then
      attach_mjr_one "${hits[0]}" "$f" "$base"
    else
      warn mjr.unattributable "$(kv path "$f")" candidates="${#hits[@]}"
      RUN_STATUS=degraded
      ASSERTIONS_FAILED+=("unattributable .mjr: $base (${#hits[@]} candidate windows)")
      if [ "$DRY_RUN" = 0 ] && [ ! -e "$UNATTRIBUTED_DIR/hpb-mjr/$base" ]; then
        a_cp_reflink "$f" "$UNATTRIBUTED_DIR/hpb-mjr/$base" >/dev/null
      fi
    fi
  done
  pop_nullglob
}

# An .mjr instant belongs to an hpb capture when it falls inside
# [started - 300 s, anchor]. Shared by the committed and the planned scan so the
# dry run and the apply can never disagree about which window an .mjr is in.
mjr_window_hit() { # mjr_window_hit <epoch> <anchor-iso> <started-iso>
  local ts=$1 ae se
  ae=$(date -u -d "$2" +%s) || return 1
  se=$(date -u -d "${3%Z}+00:00" +%s) || return 1
  [ "$ts" -ge $((se - 300)) ] && [ "$ts" -le "$ae" ]
}

attach_mjr_one() {
  local target=$1 src=$2 name=$3
  # Janus MJR is not rtplog: it lives in janus-mjr/, never session/, so nothing
  # downstream mistakes it for something `cassini remux` can read.
  if [ -e "$target/janus-mjr/$name" ]; then
    attach_recorded "$target" "janus-mjr/$name" || repair_attach "$target" "janus-mjr/$name"
    return 0
  fi
  info attach.mjr "$(kv target "$(basename "$target")")" "$(kv component "$name")"
  [ "$DRY_RUN" = 1 ] && return 0
  a_chmod u+w "$target"
  a_mkdir "$target/janus-mjr"
  a_chmod u+w "$target/janus-mjr"
  a_cp_reflink "$src" "$target/janus-mjr/.add-$name" >/dev/null
  a_mv "$target/janus-mjr/.add-$name" "$target/janus-mjr/$name"
  a_chmod a-w "$target/janus-mjr/$name"
  refresh_manifest "$target"
  a_chmod a-w "$target/janus-mjr"
  a_chmod a-w "$target"
}

# §5.6 reverse reconciliation. Zero today; ANY non-zero count means something
# has started deleting from current/, the single largest standing risk, and the
# archive is what would notice first.
reconcile_reverse() {
  VANISHED_SOURCES=0
  push_nullglob
  local d gone=0 sp
  for d in "$MEETINGS_DIR"/*/; do
    d=${d%/}
    [ -f "$d/ARCHIVE/source.tsv" ] || continue
    sp=$(awk -F'\t' '$1=="# source"{print $2; exit}' "$d/ARCHIVE/source.tsv")
    [ -n "$sp" ] || continue
    [ -e "$sp" ] && continue
    gone=$((gone + 1))
    warn source.vanished "$(kv dir "$(basename "$d")")" "$(kv source "$sp")"
  done
  pop_nullglob
  if [ "$gone" -gt 0 ]; then
    assert_fail "$gone archived source path(s) have vanished — something is deleting from the source trees"
  fi
  VANISHED_SOURCES=$gone
}

# =============================================================================
# reindex, viewer symlinks, INVENTORY.md
# =============================================================================
reindex() { # reindex [--check]
  local mode=${1:-}
  local out="$WORKTMP/index.jsonl" links="$WORKTMP/links.tsv" inv="$WORKTMP/INVENTORY.md"
  local unreadable="$WORKTMP/unreadable.tsv"
  : >"$unreadable"
  ARCHIVE_ROOT="$ARCHIVE_ROOT" OUT="$out" LINKS="$links" INV="$inv" UNREADABLE="$unreadable" \
    CONCURRENT_WINDOW_S="$CONCURRENT_WINDOW_S" LOCAL_TZ="$LOCAL_TZ" \
    python3 - <<'PY'
import json, os, sys, datetime, collections

root = os.environ["ARCHIVE_ROOT"]
win  = int(os.environ["CONCURRENT_WINDOW_S"])

FIELDS = ["schema","id","key","dir","era","recorder_display","room","room_source","room_name",
          "owner","anchor_utc","anchor_kind","anchor_zone_proven","started_at_utc",
          "started_at_source","start_precision","tz_hypothesis","ended_at_utc","duration_s",
          "date_local","capture_state","job_id","job_state","job_stage","attempt","source_root",
          "source_layout","source_paths","source_present","source_fingerprint","has_raw",
          "raw_kind","rtplog_count","raw_bytes","has_mix","mix_bytes","media","derived",
          "opus_attached","buildable","remuxable","concurrent_with","bytes_total","files_total",
          "excluded_bytes","excluded_files","sha256_manifest","last_verified_utc","copy_mode",
          "state","notes","ingested_at_utc","ingest_run_id","tool_sha256"]

rows = []
# A meeting.json that will not parse is NOT a row to skip. a_write now renames
# into place so a torn file should be impossible, but a truncated, corrupted or
# hand-edited one still silently DELETED the meeting from index.jsonl and from
# INVENTORY.md while every byte of its media sat on disk — and `--check` could
# not see it either, because it diffs the live index against a rebuild that
# drops exactly the same row. Every one is named on stderr AND recorded, and the
# caller turns that into a failed assertion.
unreadable = []
for base in ("meetings", "unattributed/jobs"):
    d = os.path.join(root, base)
    if not os.path.isdir(d):
        continue
    for name in sorted(os.listdir(d)):
        if name.startswith("."):
            continue
        p = os.path.join(d, name, "ARCHIVE", "meeting.json")
        if not os.path.isfile(p):
            continue
        try:
            with open(p, encoding="utf-8") as fh:
                rows.append(json.load(fh))
        except Exception as exc:                                  # noqa: BLE001
            print(f"reindex: unreadable {p}: {exc}", file=sys.stderr)
            unreadable.append((os.path.join(base, name), str(exc)))

def secs(iso):
    if not iso:
        return None
    t = iso.rstrip("Z")
    t = t.split(".")[0]
    try:
        return int(datetime.datetime.strptime(t, "%Y-%m-%dT%H:%M:%S")
                   .replace(tzinfo=datetime.timezone.utc).timestamp())
    except ValueError:
        return None

# concurrent_with: same room, |delta| < window, different era. The 19 doubled
# dates are two genuinely different captures, so both are kept and linked —
# this is a provenance label, never a quality judgement.
for a in rows:
    a["concurrent_with"] = []
for i, a in enumerate(rows):
    ta = secs(a.get("anchor_utc"))
    if ta is None or not a.get("dir"):
        continue
    for b in rows[i + 1:]:
        tb = secs(b.get("anchor_utc"))
        if tb is None or not b.get("dir"):
            continue
        if a.get("room") == b.get("room") and a.get("era") != b.get("era") and abs(ta - tb) < win:
            a["concurrent_with"].append(b["id"])
            b["concurrent_with"].append(a["id"])

rows.sort(key=lambda r: (r.get("dir") or "~" + (r.get("id") or "")))

with open(os.environ["OUT"], "w", encoding="utf-8") as fh:
    for r in rows:
        # Every field always present, explicit null, never omitted — a jq
        # filter can then never silently miss a row.
        fh.write(json.dumps({k: r.get(k, None) for k in FIELDS},
                            sort_keys=False, separators=(",", ":")) + "\n")

# --- derived link farms ------------------------------------------------------
links = []
for r in rows:
    d = r.get("dir")
    if not d:
        continue
    name = os.path.basename(d)
    links.append((f"by-date/{r['date_local']}/{name}", f"../../{d}"))
    if r.get("job_id"):
        links.append((f"by-job-id/{r['job_id']}", f"../{d}"))
    # Viewer symlink farm: describeMeeting's modernStamp branch is
    # ^(.*)--(\d{8})T(\d{2})(\d{2})(\d{2})$ — no colons needed, and the era
    # token keeps the 19 doubled dates from rendering as two identical entries.
    stamp = r["anchor_utc"].rstrip("Z").replace("-", "").replace(":", "")
    stamp = stamp.replace("T", "T")
    slug = name.split("--")[-1]
    vname = f"{slug}-{r['era']}--{stamp}"
    for comp in (r.get("derived") or []):
        cp = comp.get("path") or ""
        if cp.endswith(".meeting"):
            links.append((f"viewer/{vname}.meeting", f"../{d}/{cp}"))

with open(os.environ["LINKS"], "w", encoding="utf-8") as fh:
    for a, b in links:
        fh.write(f"{a}\t{b}\n")

# --- INVENTORY.md ------------------------------------------------------------
by_era = collections.Counter(r["era"] for r in rows if r.get("dir"))
bytes_era = collections.Counter()
raw_era = collections.Counter()
for r in rows:
    if r.get("dir"):
        bytes_era[r["era"]] += r.get("bytes_total") or 0
        raw_era[r["era"]] += r.get("raw_bytes") or 0
by_date = collections.defaultdict(list)
for r in rows:
    if r.get("dir"):
        by_date[r["date_local"]].append(r)
doubles = sorted(d for d, v in by_date.items() if len(v) > 1)
excluded = sum(r.get("excluded_bytes") or 0 for r in rows)
total = sum(r.get("bytes_total") or 0 for r in rows)
noraw = [r for r in rows if r.get("dir") and not r.get("has_raw")]

def gb(n):
    return f"{n/1024**3:.2f} GiB"

L = []
L.append("# Cassini unified meeting archive — INVENTORY")
L.append("")
L.append(f"Generated {datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')} "
         f"by cassini-archive-sync.sh. Regenerated in full every run; edit nothing here.")
L.append("")
L.append("## WARNING — this archive has no off-host copy")
L.append("")
L.append("Local btrfs snapshots are not a backup. `/dev/sda1` dying loses the raw corpus, the")
L.append("archive, the index and every snapshot in one event. The nightly restic job")
L.append("(`cassini-exapp-backup`) explicitly excludes `*.rtplog`, `*.idx` and `*.mkv`, so the")
L.append("pre-mix corpus preserved here exists exactly once, on one device.")
L.append("")
L.append("## Totals")
L.append("")
L.append("| era | meetings | bytes | of which raw |")
L.append("|---|---:|---:|---:|")
for era in ("cron", "legacy", "hpb", "exapp"):
    L.append(f"| `{era}` | {by_era.get(era,0)} | {gb(bytes_era.get(era,0))} | {gb(raw_era.get(era,0))} |")
L.append(f"| **total** | **{sum(by_era.values())}** | **{gb(total)}** | **{gb(sum(raw_era.values()))}** |")
L.append("")
L.append(f"Index rows (meetings + job-only): **{len(rows)}**.")
L.append(f"Excluded and NOT copied (still present in the sources, never deleted): **{gb(excluded)}**.")
L.append("")
L.append("## Anchors — what the timestamp in a directory name means")
L.append("")
L.append("| era | anchor_kind | source | `Z` |")
L.append("|---|---|---|---|")
L.append("| `cron` | session-start | `session/session.json → started_wall_utc` | yes |")
L.append("| `exapp` | session-start | same | yes |")
L.append("| `hpb` | recording-**finalized** | the Talk recorder's filename stamp | yes |")
L.append("| `legacy` | filename-wallclock | the bare mkv basename; zone unproven | **no** |")
L.append("")
L.append("A missing `Z` is honest, not sloppy: those 15 wall-clock stamps were never measured")
L.append("in a known zone, only corroborated. The date is right; the time may be 1 h off.")
L.append("The hpb anchor is an END time, chosen because the filename is byte-stable whereas")
L.append("`end − ffprobe_duration` would silently re-key a meeting on an ffprobe upgrade.")
L.append("")
L.append("## Dates with more than one capture")
L.append("")
if doubles:
    L.append(f"{len(doubles)} date(s). Both captures are kept: they are different recordings by")
    L.append("different recorders, not duplicates. Nobody has compared their content.")
    L.append("")
    L.append("| date | captures |")
    L.append("|---|---|")
    for d in doubles:
        L.append("| " + d + " | " + ", ".join(f"`{os.path.basename(r['dir'])}`" for r in sorted(by_date[d], key=lambda r: r["anchor_utc"])) + " |")
else:
    L.append("None.")
L.append("")
L.append("## Meetings with no pre-mix raw")
L.append("")
L.append(f"{len(noraw)} of {sum(by_era.values())}. For these the mixdown (or nothing) is all")
L.append("that survives; per-speaker separation is gone for good.")
L.append("")
L.append("## Coverage calendar")
L.append("")
if by_date:
    months = collections.defaultdict(list)
    for d in sorted(by_date):
        months[d[:7]].append(d)
    L.append("| month | dates covered | meetings |")
    L.append("|---|---:|---:|")
    for m in sorted(months):
        L.append(f"| {m} | {len(months[m])} | {sum(len(by_date[d]) for d in months[m])} |")
    L.append("")
    first, last = min(by_date), max(by_date)
    d0 = datetime.date.fromisoformat(first)
    d1 = datetime.date.fromisoformat(last)
    holes = []
    cur = d0
    while cur <= d1:
        if cur.weekday() < 5 and cur.isoformat() not in by_date:
            holes.append(cur.isoformat())
        cur += datetime.timedelta(days=1)
    L.append(f"### Business days between {first} and {last} with no capture at all")
    L.append("")
    L.append(f"{len(holes)} day(s). These are gaps in the record, not gaps in the archive:")
    L.append("")
    L.append("```")
    for i in range(0, len(holes), 6):
        L.append("  " + "  ".join(holes[i:i + 6]))
    L.append("```")
L.append("")
with open(os.environ["INV"], "w", encoding="utf-8") as fh:
    fh.write("\n".join(L) + "\n")

with open(os.environ["UNREADABLE"], "w", encoding="utf-8") as fh:
    for name, exc in unreadable:
        fh.write(f"{name}\t{exc}\n")
PY

  # An unreadable meeting.json is a lost row, not a warning on stderr.
  if [ -s "$unreadable" ]; then
    local u
    while IFS=$'\t' read -r u _; do
      warn index.unreadable "$(kv path "$u")"
      assert_fail "ARCHIVE/meeting.json is unreadable, so $u is missing from index.jsonl"
    done <"$unreadable"
  fi

  if [ "$mode" = --check ]; then
    if [ -f "$INDEX_JSONL" ] && diff -q "$INDEX_JSONL" "$out" >/dev/null; then
      info index.check ok=true
      return 0
    fi
    warn index.check ok=false note="index.jsonl differs from a freshly derived rebuild"
    diff "$INDEX_JSONL" "$out" | head -40 >&2 || true
    assert_fail "index.jsonl drifted from the ARCHIVE/meeting.json files"
    return 0
  fi

  local rows; rows=$(wc -l <"$out")
  info index.rebuilt rows="$rows"
  if [ "$DRY_RUN" = 1 ]; then return 0; fi

  a_write "$INDEX_JSONL" <"$out"
  sha256sum "$INDEX_JSONL" | sed "s|$ARCHIVE_ROOT/||" | a_write "$INDEX_JSONL.sha256"
  a_write "$ARCHIVE_ROOT/INVENTORY.md" <"$inv"
  write_archive_md

  # Rebuilt from scratch every run so they can never drift.
  a_rm "$BYDATE_DIR"; a_rm "$BYJOB_DIR"; a_rm "$VIEWER_DIR"
  a_mkdir "$BYDATE_DIR"; a_mkdir "$BYJOB_DIR"; a_mkdir "$VIEWER_DIR"
  local link target
  while IFS=$'\t' read -r link target; do
    a_mkdir "$(dirname "$ARCHIVE_ROOT/$link")"
    a_ln "$target" "$ARCHIVE_ROOT/$link"
  done <"$links"
}

write_archive_md() {
  a_write "$ARCHIVE_ROOT/ARCHIVE.md" <<EOF
# Cassini unified meeting archive

Every meeting Cassini has ever recorded, in one place, in four capture eras.
Written only by \`ops/cassini-archive-sync.sh\` running as root on the host.

    meetings/<ANCHOR>--<ROOM>--<ERA>--<SLUG>/
        <source bytes, verbatim, at the top level>
        derived/     .meeting / .opus bundles built from this capture
        ARCHIVE/     meeting.json, MANIFEST.sha256, source.tsv, EXCLUDED.tsv
    index.jsonl      one NDJSON row per meeting; a pure derived cache
    INVENTORY.md     the human view; regenerated every run
    by-date/ by-job-id/ viewer/    symlink farms, rebuilt from scratch every run
    state/           ledgers, conflicts, health, per-run logs, operator DB copies
    quarantine/      anything unrecognised; written to, never deleted from

\`<ANCHOR>--<ROOM>--<ERA>\` is the identity: recomputable from immutable producer
facts, unique across the corpus. The trailing slug is decoration and is never
used for lookup.

## How to verify a meeting

    cd meetings/<name> && sha256sum -c ARCHIVE/MANIFEST.sha256

## How to restore one

The directory is the restore. Source bytes sit verbatim at its top level;
\`recording.mkv\` is either the real file or a symlink to the original name.
\`ARCHIVE/source.tsv\` records where every file came from.

## What is deliberately NOT here

\`recording-segments-*/artifact-remux-work/\` — 40,248,765,505 B of orphaned
crashed-remux scratch — is excluded, byte-counted in \`state/EXCLUDED.tsv\`, and
**still present in the source**. Deleting it is a separate human decision.

## The thing to remember

There is no off-host copy of the raw corpus. Snapshots are not a backup.
EOF
}

# =============================================================================
# §8 snapshots — deletion is the only destructive operation here, so it is fenced
# =============================================================================
take_snapshot() {
  local ts; ts=$(date -u +%Y-%m-%dT%H%M%SZ)
  local dst="$SNAPSHOT_ROOT/$ts"
  guard_write "$dst"
  info snapshot.take "$(kv path "$dst")"
  if [ "$DRY_RUN" = 0 ]; then
    btrfs subvolume snapshot -r "$ARCHIVE_ROOT" "$dst" >/dev/null \
      || { assert_fail "btrfs snapshot failed"; return 0; }
  fi
  prune_snapshots
}

prune_snapshots() {
  push_nullglob
  local all=() e
  for e in "$SNAPSHOT_ROOT"/*; do
    [ -d "$e" ] || continue
    all+=("$(basename "$e")")
  done
  pop_nullglob
  # Brake 3: never prune from a suspiciously thin or stale history.
  if [ "${#all[@]}" -lt 3 ]; then
    info snapshot.prune skipped=true reason="fewer than 3 snapshots"
    return 0
  fi
  local newest; newest=$(printf '%s\n' "${all[@]}" | LC_ALL=C sort | tail -1)
  local newest_epoch now
  newest_epoch=$(snapshot_epoch "$newest") || { info snapshot.prune skipped=true reason="newest name unparseable"; return 0; }
  now=$(date -u +%s)
  if [ $((now - newest_epoch)) -gt 108000 ]; then
    info snapshot.prune skipped=true reason="newest snapshot older than 30 h; the pruner is not the right tool for that"
    return 0
  fi

  local keep=() drop=() name
  local -A seen_week seen_month
  for name in $(printf '%s\n' "${all[@]}" | LC_ALL=C sort -r); do
    # Brake 1+2: only exactly-shaped names, never the newest, never the baseline.
    case "$name" in
      000-baseline--*) keep+=("$name"); continue ;;
    esac
    if [[ ! $name =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{6}Z$ ]]; then
      keep+=("$name"); continue
    fi
    [ "$name" = "$newest" ] && { keep+=("$name"); continue; }
    local ep age_days week month
    ep=$(snapshot_epoch "$name") || { keep+=("$name"); continue; }
    age_days=$(( (now - ep) / 86400 ))
    week=$(date -u -d "@$ep" +%G-W%V); month=$(date -u -d "@$ep" +%Y-%m)
    if [ "$age_days" -le "$SNAPSHOT_KEEP_DAYS" ]; then keep+=("$name"); continue; fi
    if [ "$age_days" -le $((SNAPSHOT_KEEP_WEEKS * 7)) ] && [ -z "${seen_week[$week]:-}" ]; then
      seen_week[$week]=1; keep+=("$name"); continue
    fi
    if [ "$age_days" -le $((SNAPSHOT_KEEP_MONTHS * 31)) ] && [ -z "${seen_month[$month]:-}" ]; then
      seen_month[$month]=1; keep+=("$name"); continue
    fi
    drop+=("$name")
  done

  # Brake 4: a date-math bug cannot wipe the history in one pass.
  if [ "${#drop[@]}" -gt "$SNAPSHOT_MAX_DELETES" ]; then
    info snapshot.retention_deferred wanted="${#drop[@]}" cap="$SNAPSHOT_MAX_DELETES"
    drop=("${drop[@]:0:$SNAPSHOT_MAX_DELETES}")
  fi
  for name in "${drop[@]:-}"; do
    [ -n "$name" ] || continue
    local p="$SNAPSHOT_ROOT/$name" lines="?" sha="?"
    [ -f "$p/index.jsonl" ] && { lines=$(wc -l <"$p/index.jsonl"); sha=$(sha256sum "$p/index.jsonl" | cut -d' ' -f1); }
    info snapshot.delete "$(kv name "$name")" index_rows="$lines" index_sha256="$sha"
    guard_write "$p"
    [ "$DRY_RUN" = 0 ] && btrfs subvolume delete "$p" >/dev/null
  done
  info snapshot.prune kept="${#keep[@]}" deleted="${#drop[@]}"
}

snapshot_epoch() {
  local n=$1
  [[ $n =~ ^([0-9]{4}-[0-9]{2}-[0-9]{2})T([0-9]{2})([0-9]{2})([0-9]{2})Z$ ]] || return 1
  date -u -d "${BASH_REMATCH[1]}T${BASH_REMATCH[2]}:${BASH_REMATCH[3]}:${BASH_REMATCH[4]}Z" +%s
}

# =============================================================================
# rotating integrity re-verify
# =============================================================================
verify_slice() {
  local cursor="" budget=$VERIFY_BYTES_PER_RUN
  [ -f "$VERIFY_JSON" ] && cursor=$(json_get "$VERIFY_JSON" '.cursor')
  push_nullglob
  local dirs=("$MEETINGS_DIR"/*/)
  pop_nullglob
  local checked=0 bad=0 bytes=0 last="$cursor" started=0 d
  for d in "${dirs[@]}"; do
    d=${d%/}
    local name; name=$(basename "$d")
    if [ -n "$cursor" ] && [ "$started" = 0 ]; then
      [[ "$name" > "$cursor" ]] || continue
    fi
    started=1
    [ -f "$d/ARCHIVE/MANIFEST.sha256" ] || { assert_fail "missing MANIFEST.sha256: $name"; continue; }
    local sz; sz=$(json_get "$d/ARCHIVE/meeting.json" '.bytes_total'); sz=${sz:-0}
    if ( cd "$d" && sha256sum -c --quiet ARCHIVE/MANIFEST.sha256 >/dev/null 2>&1 ); then
      checked=$((checked + 1))
    else
      bad=$((bad + 1))
      assert_fail "sha256 mismatch under meetings/$name"
    fi
    bytes=$((bytes + sz)); last=$name
    [ "$bytes" -ge "$budget" ] && break
  done
  # Wrap the cursor when the corpus has been walked end to end.
  [ "$last" = "$cursor" ] && last=""
  info verify.slice checked="$checked" failed="$bad" bytes="$bytes" cursor="${last:-<wrapped>}"
  [ "$DRY_RUN" = 1 ] && return 0
  jq -n --arg c "$last" --arg at "$(now_utc)" --argjson n "$checked" --argjson b "$bad" \
    '{cursor:$c, last_run_utc:$at, checked:$n, failed:$b}' | a_write "$VERIFY_JSON"
}

# =============================================================================
# §9 channel 3 — upstream liveness. The 2026-08-03/04/05 loss was no new
# recordings with every unit healthy, so a perfectly working ingest that sees
# nothing new for MAX_QUIET_WEEKDAYS weekdays FAILS THE RUN.
# =============================================================================
newest_capture_utc() {
  [ -f "$INDEX_JSONL" ] || { printf ''; return 0; }
  jq -rs 'map(select(.dir != null) | .anchor_utc) | sort | last // ""' "$INDEX_JSONL" 2>/dev/null || printf ''
}

weekdays_between() { # weekdays_between <iso-date> <iso-date>
  python3 -c 'import sys,datetime
a=datetime.date.fromisoformat(sys.argv[1]); b=datetime.date.fromisoformat(sys.argv[2])
n=0; d=a
while d<b:
    d+=datetime.timedelta(days=1)
    if d.weekday()<5: n+=1
print(n)' "$1" "$2"
}

check_upstream_quiet() {
  QUIET_WEEKDAYS=0
  local newest; newest=$(newest_capture_utc)
  [ -n "$newest" ] || return 0
  local today; today=$(date -u +%F)
  if [ -n "$EXPECTED_QUIET_UNTIL" ] && [[ "$today" < "$EXPECTED_QUIET_UNTIL" || "$today" == "$EXPECTED_QUIET_UNTIL" ]]; then
    info quiet.suppressed until="$EXPECTED_QUIET_UNTIL"
    return 0
  fi
  local n; n=$(weekdays_between "${newest%%T*}" "$today")
  QUIET_WEEKDAYS=$n
  if [ "$n" -ge "$MAX_QUIET_WEEKDAYS" ]; then
    assert_fail "no new capture in $n weekdays (newest=$newest) — recording may have stopped upstream"
    return 0
  fi
  info quiet.ok quiet_weekdays="$n" threshold="$MAX_QUIET_WEEKDAYS"
}

# =============================================================================
# health.json + off-host heartbeat
# =============================================================================
write_health() {
  local started=$1 status=$2 quiet=$3 vanished=$4
  local meetings index_rows bytes_total newest snapshot_newest snapshot_count free conflicts
  push_nullglob
  local mdirs=("$MEETINGS_DIR"/*/); meetings=${#mdirs[@]}
  local snaps=("$SNAPSHOT_ROOT"/*/); snapshot_count=${#snaps[@]}
  pop_nullglob
  index_rows=0; [ -f "$INDEX_JSONL" ] && index_rows=$(wc -l <"$INDEX_JSONL")
  bytes_total=0
  [ -f "$INDEX_JSONL" ] && bytes_total=$(jq -s 'map(.bytes_total // 0) | add // 0' "$INDEX_JSONL")
  newest=$(newest_capture_utc)
  snapshot_newest=""
  [ "$snapshot_count" -gt 0 ] && snapshot_newest=$(basename "$(printf '%s\n' "${snaps[@]}" | LC_ALL=C sort | tail -1)")
  free=$(df -B1 --output=avail "$ARCHIVE_ROOT" | tail -1 | tr -d ' ')
  conflicts=0; [ -f "$CONFLICTS_TSV" ] && conflicts=$(wc -l <"$CONFLICTS_TSV")

  local db_max=""
  if [ "$JOBS_LOADED" = 1 ]; then
    db_max=$(sqlite3 "$DB_SNAPSHOT" "select coalesce(max(record_finished_at),'') from jobs;")
  fi
  local lag=0
  if [ -n "$db_max" ] && [ -n "$newest" ]; then
    lag=$(( $(date -u -d "$db_max" +%s) - $(date -u -d "$newest" +%s) ))
    [ "$lag" -lt 0 ] && lag=0
  fi

  local assertions
  assertions=$(printf '%s\n' "${ASSERTIONS_FAILED[@]:-}" | jq -R . | jq -s 'map(select(length>0))')

  jq -n --arg schema cassini.archive.health.v1 --arg run "$RUN_ID" \
    --arg started "$started" --arg finished "$(now_utc)" --arg status "$status" \
    --argjson meetings "$meetings" --argjson index_rows "$index_rows" \
    --argjson bytes_total "$bytes_total" --argjson new "$STAT_NEW" \
    --argjson deferred "$STAT_DEFER" --argjson conflicts "$conflicts" \
    --arg newest "$newest" --arg db_max "$db_max" --argjson lag "$lag" \
    --arg snap "$snapshot_newest" --argjson snap_n "$snapshot_count" \
    --argjson free "$free" --arg copy_mode "$COPY_MODE" \
    --argjson quiet "$quiet" --argjson vanished "$vanished" \
    --argjson assertions "$assertions" --arg tool_sha "$TOOL_SHA" \
    --arg next "$(date -u -d '+30 hours' +%FT%TZ)" '
    {schema:$schema, ingest_run_id:$run,
     last_run_started_utc:$started, last_run_finished_utc:$finished, last_run_status:$status,
     meetings_total:$meetings, index_rows:$index_rows, bytes_total:$bytes_total,
     new_this_run:$new, deferred_this_run:$deferred, conflicts:$conflicts,
     newest_capture_utc:(if $newest=="" then null else $newest end),
     operator_db_max_record_finished_at:(if $db_max=="" then null else $db_max end),
     capture_lag_seconds:$lag, quiet_weekdays:$quiet, vanished_sources:$vanished,
     newest_snapshot_utc:(if $snap=="" then null else $snap end), snapshot_count:$snap_n,
     free_bytes:$free, copy_mode:$copy_mode,
     expected_next_run_before_utc:$next,
     assertions_failed:$assertions, script_sha256:$tool_sha}' | a_write "$HEALTH_JSON"

  # The heartbeat's whole point is to leave george. operator/backups/ is already
  # in cassini-exapp-backup's path list and a .json matches none of its
  # excludes, so this ships to R2 nightly with zero edits to that script.
  if [ "$DRY_RUN" = 0 ] && [ -d "$HEARTBEAT_DIR" ]; then
    guard_write "$HEARTBEAT_FILE"
    cp -- "$HEALTH_JSON" "$HEARTBEAT_FILE"
    info heartbeat.written "$(kv path "$HEARTBEAT_FILE")"
  elif [ ! -d "$HEARTBEAT_DIR" ]; then
    warn heartbeat.missing_dir "$(kv path "$HEARTBEAT_DIR")" \
      note="the only off-host detection channel is not being written"
  fi
}

# An alarm cannot be erased by the next successful run. Clearing it is an
# explicit human act.
check_alarm() {
  [ -f "$ALARM_FILE" ] || return 0
  warn alarm.unacknowledged "$(kv path "$ALARM_FILE")"
  sed 's/^/    /' "$ALARM_FILE" >&2
  return 1
}

# =============================================================================
# main
# =============================================================================
main() {
  local started; started=$(now_utc)
  RUN_ID=$(date -u +%Y%m%dT%H%M%SZ)-$$
  TOOL_SHA=$(sha256sum "$0" | cut -d' ' -f1)

  info run.start mode_backfill="$DO_BACKFILL" mode_sync="$DO_SYNC" mode_snapshot="$DO_SNAPSHOT" \
    mode_verify="$DO_VERIFY" mode_reindex="$DO_REINDEX" dry_run="$DRY_RUN" tool_sha256="$TOOL_SHA"
  [ "$DRY_RUN" = 1 ] && info run.dry_run note="nothing will be written; pass --apply to act"

  preflight

  if [ "$DO_ACK" = 1 ]; then
    if [ -f "$ALARM_FILE" ]; then
      a_mv "$ALARM_FILE" "$VAR_DIR/ALARM.acked-$RUN_ID"
      info alarm.acked
    else
      info alarm.none
    fi
    [ "$ANY_ACTION" = 1 ] && [ "$DO_BACKFILL$DO_SYNC$DO_SNAPSHOT$DO_VERIFY$DO_REINDEX$DO_CHECK" = "000000" ] && exit 0
  fi

  # The debounce protects cassini-archive-sync.path, which can fire several
  # times a minute while a capture is promoted. It used to apply to EVERY
  # --apply run, which silently broke the documented two-step backfill: the
  # second command ran seconds after the first, hit MIN_RUN_INTERVAL=300, and
  # exited 0 having archived nothing at all — 43 ExApp captures / 20.3 GiB
  # skipped, green, with the operator told to pin the baseline snapshot next.
  # It is now opt-in, and only the unit passes it.
  if [ "$DEBOUNCE" = 1 ] && [ "$FORCE" = 0 ] && [ -f "$LAST_RUN_STAMP" ] && [ "$DRY_RUN" = 0 ]; then
    local prev now_s
    prev=$(cat "$LAST_RUN_STAMP"); now_s=$(date -u +%s)
    if [ $((now_s - prev)) -lt "$MIN_RUN_INTERVAL" ]; then
      info run.debounced since="$((now_s - prev))" min_interval="$MIN_RUN_INTERVAL"
      exit 0
    fi
  fi

  sweep_staging
  sweep_orphans

  [ "$DO_BACKFILL" = 1 ] && backfill_old
  [ "$DO_SYNC" = 1 ] && sync_new

  if [ "$DO_BACKFILL" = 1 ] || [ "$DO_SYNC" = 1 ]; then
    reconcile_reverse
  fi

  [ "$DO_REINDEX" = 1 ] && reindex
  [ "$DO_CHECK" = 1 ] && reindex --check
  [ "$DO_VERIFY" = 1 ] && verify_slice
  [ "$DO_SNAPSHOT" = 1 ] && take_snapshot

  [ "$DO_SYNC" = 1 ] && check_upstream_quiet

  if [ "$DO_BACKFILL" = 1 ] || [ "$DO_SYNC" = 1 ]; then
    assert_backfill_acceptance
  fi

  [ "$DRY_RUN" = 0 ] && prune_keep "$STATE_DIR/runs" "$KEEP_RUN_LOGS"

  local status=$RUN_STATUS
  check_alarm || status=alarm
  write_health "$started" "$status" "$QUIET_WEEKDAYS" "$VANISHED_SOURCES"

  if [ "$DRY_RUN" = 0 ]; then
    guard_write "$LAST_RUN_STAMP"
    date -u +%s >"$LAST_RUN_STAMP"
  fi

  info run.done status="$status" new="$STAT_NEW" skipped="$STAT_SKIP" deferred="$STAT_DEFER" \
    quarantined="$STAT_QUAR" conflicts="$CONFLICT_COUNT" physical_bytes="$PHYSICAL_BYTES"

  [ "$status" = alarm ] && exit 7
  # RUN_STATUS is part of the condition, not just the two counters.
  # quarantine_source() sets degraded WITHOUT appending an assertion — so an
  # unidentifiable bundle, a symlinked source, a failed byte-conservation check
  # or a source entry colliding with an archive-owned name wrote
  # `last_run_status: degraded` into health.json and then EXITED 0: systemd saw
  # success, OnFailure never fired, no ALARM was latched, and the only thing
  # that would ever notice was the healthcheck up to 12 h later.
  if [ "$CONFLICT_COUNT" -gt 0 ] || [ "${#ASSERTIONS_FAILED[@]}" -gt 0 ] || [ "$RUN_STATUS" != ok ]; then
    warn run.not_green status="$RUN_STATUS" conflicts="$CONFLICT_COUNT" \
      assertions="${#ASSERTIONS_FAILED[@]}" quarantined="$STAT_QUAR"
    [ "$QUIET_WEEKDAYS" -ge "$MAX_QUIET_WEEKDAYS" ] && exit 6
    exit 5
  fi
  exit 0
}

# The acceptance numbers are asserted, not hoped for — and they are asserted by
# whichever pass could have produced them, not only by the single combined
# invocation nobody was told to run. The README's own two-step runbook never
# reached this function, which left the 88/15/50/43 counts (the only automated
# backstop against a silent partial copy of the irreplaceable half of the
# corpus) permanently unevaluated.
#
# cron / legacy / hpb are FROZEN corpora: the count is exact, forever, and a
# drop below it means something ate archived meetings. exapp keeps growing, so
# it is a FLOOR: never fewer than were there at backfill time.
assert_backfill_acceptance() {
  [ -f "$INDEX_JSONL" ] || return 0
  local era n want
  local eras=""
  [ "$DO_BACKFILL" = 1 ] && eras="cron legacy hpb"
  [ "$DO_SYNC" = 1 ] && eras="$eras exapp"
  for era in $eras; do
    case "$era" in
      cron) want=$EXPECT_CRON ;; legacy) want=$EXPECT_LEGACY ;;
      hpb) want=$EXPECT_HPB ;; exapp) want=$EXPECT_EXAPP ;;
    esac
    n=$(jq -rs --arg e "$era" 'map(select(.dir != null and .era == $e)) | length' "$INDEX_JSONL")
    if [ "$era" = exapp ]; then
      if [ "$n" -lt "$want" ]; then
        assert_fail "era exapp: $n meetings archived, expected at least $want"
      else
        info acceptance.ok era=exapp count="$n" floor="$want"
      fi
    elif [ "$n" != "$want" ]; then
      assert_fail "era $era: $n meetings archived, expected $want"
    else
      info acceptance.ok era="$era" count="$n"
    fi
  done
}

# The EXIT trap must preserve the exit status: a bare `[ x ] && rm` whose test
# fails returns 1 and silently rewrites every documented exit code to 1.
# shellcheck disable=SC2317  # invoked by the EXIT trap below
cleanup() {
  local rc=$?
  if [ -n "$WORKTMP" ] && [ -d "$WORKTMP" ]; then rm -rf -- "$WORKTMP"; fi
  return "$rc"
}

if [ "$_SOURCED" = 0 ]; then
  trap cleanup EXIT
  mkdir -p "$(dirname "$LOCKFILE")" 2>/dev/null || true
  # flock, exactly as /usr/local/sbin/cassini-exapp-backup does: two ingests
  # racing over the same staging directory is the one way this design could
  # corrupt itself.
  exec 9>"$LOCKFILE"
  flock -n 9 || { log warn run.locked "$(kv lockfile "$LOCKFILE")" note="another run holds the lock"; exit 0; }
  main
fi
