#!/usr/bin/env bash
# cassini-exapp-backup — replicate the production Cassini ExApp archive to Cloudflare R2.
#
# WHERE THIS RUNS
#   George (Proxmox host), as root, installed at /usr/local/sbin/cassini-exapp-backup
#   and driven by cassini-exapp-backup.timer (daily). It reads the ExApp Docker
#   volume straight off the host btrfs filesystem — it does NOT enter CT 112, so a
#   stopped container does not stop the backup.
#
# SAFETY CONTRACT (D-568)
#   - Additive only. Uses `rclone copy` exclusively. It never calls sync, delete,
#     move, purge or cleanup, in either direction. Production is opened read-only.
#   - The SQLite database is copied through SQLite's own online-backup API with the
#     source opened `mode=ro`, so a concurrent writer cannot produce a torn file and
#     the backup cannot create -wal/-shm sidecars in the production directory.
#
# SCOPE — see docs/prod-backup.md for the full rationale.
#   delivery/  site/published (the viewer's delivery path: catalog + portable .opus
#              meetings, which embed both audio and transcript) + operator config
#   sources/   operator/jobs/current minus bulk media — the .meeting bundles the
#              site is rebuilt from (`cassini publish`)
#   history/   operator/jobs/runs, text artefacts only — per-attempt job history
#   snapshots/ per-run point-in-time manifest + consistent SQLite copy
#
#   Deliberately excluded: *.rtplog, *.idx, *.mkv (raw capture intermediates,
#   ~20 GB in current/ and byte-identical copies again in runs/) and the media in
#   runs/ (per-attempt .opus/.webm, ~14 GB, superseded by delivery/ + sources/).
#   That is ~55 GB of the 57 GB volume; nothing on the delivery path is excluded.
#
# CONFIG   /etc/default/cassini-exapp-backup (no secrets — rclone credentials live
#          in root-only /root/.config/rclone/rclone.conf, mode 0600).
# RESTORE  docs/prod-backup.md
set -euo pipefail

CONFIG_FILE=${CONFIG_FILE:-/etc/default/cassini-exapp-backup}
# shellcheck disable=SC1090
[ -r "$CONFIG_FILE" ] && . "$CONFIG_FILE"

SRC=${SRC:-/mnt/data/cassini-exapp/docker/volumes/nc_app_gocassini_data/_data}
RCLONE_REMOTE=${RCLONE_REMOTE:-r2}
BUCKET=${BUCKET:-cassini-exapp-backups}
PREFIX=${PREFIX:-george-ct112}
LOCKFILE=${LOCKFILE:-/run/cassini-exapp-backup.lock}
WORKROOT=${WORKROOT:-/var/tmp/cassini-exapp-backup}
export HOME=${HOME:-/root}

DEST="${RCLONE_REMOTE}:${BUCKET}/${PREFIX}"
TS=$(date -u +%Y-%m-%dT%H%M%SZ)

RCLONE_COMMON=(
  --config /root/.config/rclone/rclone.conf
  --transfers 8 --checkers 16 --fast-list
  --retries 3 --low-level-retries 10
  --s3-chunk-size 32M
  --stats 60s --stats-one-line
)

# Bulk capture intermediates. Present in current/ and duplicated verbatim in runs/.
MEDIA_EXCLUDES=(--exclude '*.rtplog' --exclude '*.idx' --exclude '*.mkv')
# runs/ additionally holds per-attempt republished audio, superseded by delivery/.
RUNS_EXCLUDES=("${MEDIA_EXCLUDES[@]}" --exclude '*.opus' --exclude '*.webm')
# Transient staging directories the operator writes mid-publish.
STAGING_EXCLUDES=(--exclude '.staging/**' --exclude '**/.staging/**')

WORK=""
cleanup() { [ -n "$WORK" ] && rm -rf "$WORK"; return 0; }

log() { printf '%s %s\n' "$(date -u +%FT%TZ)" "$*"; }
die() { log "FATAL: $*"; exit 1; }

main() {
  log "=== cassini-exapp-backup start ts=$TS dest=$DEST src=$SRC"

  [ -d "$SRC" ] || die "source not found: $SRC"
  [ -d "$SRC/site/published" ] || die "delivery path missing: $SRC/site/published"
  [ -f "$SRC/operator/jobs.sqlite3" ] || die "job database missing: $SRC/operator/jobs.sqlite3"
  command -v rclone >/dev/null || die "rclone not on PATH"
  command -v sqlite3 >/dev/null || die "sqlite3 not on PATH"
  rclone "${RCLONE_COMMON[@]}" lsd "${RCLONE_REMOTE}:${BUCKET}" --max-depth 1 >/dev/null \
    || die "cannot reach ${RCLONE_REMOTE}:${BUCKET}"

  local work="$WORKROOT/$TS"
  WORK="$work"
  mkdir -p "$work"
  trap cleanup EXIT

  # --- consistent copy of the job database -----------------------------------
  log "snapshot: jobs.sqlite3 (online backup, source read-only)"
  sqlite3 "file:${SRC}/operator/jobs.sqlite3?mode=ro" ".backup '$work/jobs.sqlite3'" \
    || die "sqlite backup failed"
  sqlite3 "$work/jobs.sqlite3" 'pragma integrity_check;' | grep -qx ok \
    || die "sqlite backup failed integrity_check"
  {
    printf 'jobs\t%s\n'         "$(sqlite3 "$work/jobs.sqlite3" 'select count(*) from jobs;')"
    printf 'job_attempts\t%s\n' "$(sqlite3 "$work/jobs.sqlite3" 'select count(*) from job_attempts;')"
  } > "$work/jobs.counts.tsv"

  # --- point-in-time manifest of the delivery path ----------------------------
  log "snapshot: delivery manifest (sha256 of every published file)"
  ( cd "$SRC/site/published" && find . -type f -print0 | sort -z \
      | xargs -0 -r sha256sum ) > "$work/published.sha256"
  ( cd "$SRC/site/published" && find . -type f -printf '%P\t%s\n' | sort ) > "$work/published.manifest.tsv"
  cp -a "$SRC/site/published/catalog.json" "$work/catalog.json"
  cp -a "$SRC/site/published/cassini.json" "$work/cassini.json"
  for f in settings.json app-state.json; do
    [ -f "$SRC/operator/$f" ] && cp -a "$SRC/operator/$f" "$work/$f"
  done

  {
    echo "snapshot_ts=$TS"
    echo "host=$(hostname)"
    echo "source=$SRC"
    echo "published_files=$(wc -l < "$work/published.manifest.tsv")"
    echo "published_bytes=$(awk -F'\t' '{s+=$2} END {print s+0}' "$work/published.manifest.tsv")"
    echo "catalog_meetings=$(python3 -c 'import json,sys;print(len(json.load(open(sys.argv[1]))["meetings"]))' "$work/catalog.json" 2>/dev/null || echo unknown)"
    echo "jobs_rows=$(awk -F'\t' '$1=="jobs"{print $2}' "$work/jobs.counts.tsv")"
    echo "exapp_image=$(pct exec 112 -- docker inspect nc_app_gocassini --format '{{.Config.Image}}' 2>/dev/null || echo unknown)"
  } > "$work/summary.txt"
  log "snapshot summary:"; sed 's/^/    /' "$work/summary.txt"

  # --- replicate (copy only, never sync/delete) -------------------------------
  log "copy: delivery/site/published"
  rclone "${RCLONE_COMMON[@]}" copy "$SRC/site/published" "$DEST/delivery/site/published"

  log "copy: delivery/operator (config + operator-side backups)"
  rclone "${RCLONE_COMMON[@]}" copy "$SRC/operator" "$DEST/delivery/operator" \
    --include 'settings.json' --include 'app-state.json' \
    --include 'jobs.sqlite3*' --include 'backups/**'

  log "copy: sources/operator/jobs/current (minus bulk media)"
  rclone "${RCLONE_COMMON[@]}" copy "$SRC/operator/jobs/current" "$DEST/sources/operator/jobs/current" \
    "${MEDIA_EXCLUDES[@]}" "${STAGING_EXCLUDES[@]}"

  log "copy: history/operator/jobs/runs (text artefacts only)"
  rclone "${RCLONE_COMMON[@]}" copy "$SRC/operator/jobs/runs" "$DEST/history/operator/jobs/runs" \
    "${RUNS_EXCLUDES[@]}" "${STAGING_EXCLUDES[@]}"

  log "copy: snapshots/$TS"
  rclone "${RCLONE_COMMON[@]}" copy "$work" "$DEST/snapshots/$TS"
  printf '%s\n' "$TS" | rclone "${RCLONE_COMMON[@]}" rcat "$DEST/LATEST"

  log "=== cassini-exapp-backup done ts=$TS"
}

exec 9>"$LOCKFILE"
flock -n 9 || { log "another run holds $LOCKFILE; exiting"; exit 0; }
main "$@"
