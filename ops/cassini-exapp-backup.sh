#!/usr/bin/env bash
# cassini-exapp-backup — snapshot the production Cassini ExApp archive into restic on R2.
#
# WHERE THIS RUNS
#   George (Proxmox host), as root, installed at /usr/local/sbin/cassini-exapp-backup
#   and driven by cassini-exapp-backup.timer (daily). It reads the ExApp Docker
#   volume straight off the host btrfs filesystem — it does NOT enter CT 112, so a
#   stopped container does not stop the backup.
#
#   Execution stays on the host because restic chunks and hashes client-side: it has
#   to read all 57 GB locally to produce a ~2 GB delta. Verification and retention
#   run off-machine in CI (codemyriad/systems, .github/workflows/backup-*.yml) —
#   a watchdog running here could not tell you this host is dead.
#
# SAFETY CONTRACT (D-568, extended by D-576)
#   - This host only ever *writes*. It never calls forget, prune or unlock. Retention
#     is a monthly CI job holding a separate credential, so the delete capability does
#     not live on the production host at all.
#   - Production is opened read-only. The SQLite database is copied through SQLite's
#     own online-backup API with the source opened `mode=ro`, so a concurrent writer
#     cannot produce a torn file and the backup cannot create -wal/-shm sidecars in
#     the production directory. The *live* jobs.sqlite3 is deliberately excluded —
#     only the staged consistent copy is backed up.
#   - One `restic backup` invocation covers every path, so a run yields a single
#     atomic snapshot rather than several copies that can disagree with each other.
#   - flock prevents overlapping runs.
#
# SCOPE — see README.md for the full rationale.
#   site/published        the viewer's delivery path: catalog + portable .opus
#                         meetings, which embed both audio and transcript
#   operator/jobs/current the .meeting bundles the site is rebuilt from, minus
#                         bulk media (`cassini publish` regenerates from these)
#   operator/jobs/runs    per-attempt job history, text artefacts only
#   <stage>/              per-run point-in-time manifest + consistent SQLite copy
#
#   Deliberately excluded: *.rtplog, *.idx, *.mkv (raw capture intermediates) and the
#   per-attempt media in runs/ (superseded by site/published). That is ~55 GB of the
#   57 GB volume; nothing on the delivery path is excluded.
#
# CONFIG      /etc/default/cassini-exapp-backup (no secrets)
# CREDENTIALS /root/.config/restic/r2.env and /root/.config/restic/password, both 0600
# RESTORE     README.md
set -euo pipefail

CONFIG_FILE=${CONFIG_FILE:-/etc/default/cassini-exapp-backup}
# shellcheck disable=SC1090
[ -r "$CONFIG_FILE" ] && . "$CONFIG_FILE"

SRC=${SRC:-/mnt/data/cassini-exapp/docker/volumes/nc_app_gocassini_data/_data}
CREDENTIALS_FILE=${CREDENTIALS_FILE:-/root/.config/restic/r2.env}
RESTIC_PASSWORD_FILE=${RESTIC_PASSWORD_FILE:-/root/.config/restic/password}
PROVENANCE_FILE=${PROVENANCE_FILE:-/usr/local/share/cassini-exapp-backup/PROVENANCE}
LOCKFILE=${LOCKFILE:-/run/cassini-exapp-backup.lock}
WORKROOT=${WORKROOT:-/var/tmp/cassini-exapp-backup}
BACKUP_HOST=${BACKUP_HOST:-george-ct112}
export HOME=${HOME:-/root}

# Sourcing the config sets shell variables, not environment ones. restic is a child
# process and reads these from the environment, so they must be exported explicitly —
# under systemd `EnvironmentFile=` happens to cover this, but running the script by
# hand (as the runbook's dry run does) would otherwise fail with an unset repository.
export RESTIC_REPOSITORY RESTIC_PASSWORD_FILE

# R2 credentials (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY) live outside the config
# file so /etc/default stays secret-free, matching the contract D-568 established.
# shellcheck disable=SC1090
[ -r "$CREDENTIALS_FILE" ] && . "$CREDENTIALS_FILE"

# Stable path: restic records absolute paths, and a path that changed every run would
# defeat parent-snapshot matching and re-upload the staged files every night.
STAGE="$WORKROOT/snapshot"
TS=$(date -u +%Y-%m-%dT%H%M%SZ)

# Provenance, carried both in summary.txt and in the snapshot's tags. The tags are what
# CI actually reads: `restic snapshots --json` exposes them without dumping file
# content, so the watchdog works with a read-only token and no repository lock.
SCRIPT_SHA=$(sha256sum "$0" | cut -d' ' -f1)
INSTALLED_COMMIT=$(awk -F= '$1=="commit"{print $2}' "$PROVENANCE_FILE" 2>/dev/null || true)
INSTALLED_COMMIT=${INSTALLED_COMMIT:-unknown}

RESTIC_COMMON=(
  --verbose
  --retry-lock 5m
)

log() { printf '%s %s\n' "$(date -u +%FT%TZ)" "$*"; }
die() { log "FATAL: $*"; exit 1; }

# Bulk capture intermediates, wherever they appear.
EXCLUDES=(
  --exclude '*.rtplog'
  --exclude '*.idx'
  --exclude '*.mkv'
  # runs/ additionally holds per-attempt republished audio/video, byte-identical to
  # what site/published already carries. Anchored so current/ keeps its media.
  --exclude "$SRC/operator/jobs/runs/**/*.opus"
  --exclude "$SRC/operator/jobs/runs/**/*.webm"
  # Transient staging directories the operator writes mid-publish.
  --exclude '.staging'
  --exclude 'published.staging'
)

stage_snapshot() {
  rm -rf "$STAGE"
  mkdir -p "$STAGE"

  log "stage: jobs.sqlite3 (online backup, source read-only)"
  sqlite3 "file:${SRC}/operator/jobs.sqlite3?mode=ro" ".backup '$STAGE/jobs.sqlite3'" \
    || die "sqlite backup failed"
  sqlite3 "$STAGE/jobs.sqlite3" 'pragma integrity_check;' | grep -qx ok \
    || die "sqlite backup failed integrity_check"
  {
    printf 'jobs\t%s\n'         "$(sqlite3 "$STAGE/jobs.sqlite3" 'select count(*) from jobs;')"
    printf 'job_attempts\t%s\n' "$(sqlite3 "$STAGE/jobs.sqlite3" 'select count(*) from job_attempts;')"
  } > "$STAGE/jobs.counts.tsv"

  log "stage: delivery manifest (sha256 of every published file)"
  ( cd "$SRC/site/published" && find . -type f -print0 | sort -z \
      | xargs -0 -r sha256sum ) > "$STAGE/published.sha256"
  ( cd "$SRC/site/published" && find . -type f -printf '%P\t%s\n' | sort ) > "$STAGE/published.manifest.tsv"
  cp -a "$SRC/site/published/catalog.json" "$STAGE/catalog.json"
  cp -a "$SRC/site/published/cassini.json" "$STAGE/cassini.json"
  for f in settings.json app-state.json; do
    [ -f "$SRC/operator/$f" ] && cp -a "$SRC/operator/$f" "$STAGE/$f"
  done

  {
    echo "snapshot_ts=$TS"
    echo "host=$(hostname)"
    echo "source=$SRC"
    echo "script_sha256=$SCRIPT_SHA"
    echo "installed_commit=$INSTALLED_COMMIT"
    echo "restic_version=$(restic version | head -1)"
    echo "published_files=$(wc -l < "$STAGE/published.manifest.tsv")"
    echo "published_bytes=$(awk -F'\t' '{s+=$2} END {print s+0}' "$STAGE/published.manifest.tsv")"
    echo "catalog_meetings=$(python3 -c 'import json,sys;print(len(json.load(open(sys.argv[1]))["meetings"]))' "$STAGE/catalog.json" 2>/dev/null || echo unknown)"
    echo "jobs_rows=$(awk -F'\t' '$1=="jobs"{print $2}' "$STAGE/jobs.counts.tsv")"
    echo "exapp_image=$(pct exec 112 -- docker inspect nc_app_gocassini --format '{{.Config.Image}}' 2>/dev/null || echo unknown)"
  } > "$STAGE/summary.txt"
  log "snapshot summary:"; sed 's/^/    /' "$STAGE/summary.txt"
}

main() {
  log "=== cassini-exapp-backup start ts=$TS repo=${RESTIC_REPOSITORY:-<unset>} src=$SRC"

  [ -d "$SRC" ] || die "source not found: $SRC"
  [ -d "$SRC/site/published" ] || die "delivery path missing: $SRC/site/published"
  [ -f "$SRC/operator/jobs.sqlite3" ] || die "job database missing: $SRC/operator/jobs.sqlite3"
  command -v restic >/dev/null || die "restic not on PATH"
  command -v sqlite3 >/dev/null || die "sqlite3 not on PATH"
  [ -n "${RESTIC_REPOSITORY:-}" ] || die "RESTIC_REPOSITORY unset (see $CONFIG_FILE)"
  [ -r "$RESTIC_PASSWORD_FILE" ] || die "restic password unreadable: $RESTIC_PASSWORD_FILE"
  [ -n "${AWS_ACCESS_KEY_ID:-}" ] || die "R2 credentials unset (see $CREDENTIALS_FILE)"
  restic cat config >/dev/null 2>&1 || die "cannot open restic repo: ${RESTIC_REPOSITORY}"

  stage_snapshot

  # Paths that always exist, plus the optional operator artefacts. Passing a missing
  # path to restic is a hard error, so build the list from what is actually there.
  local paths=("$STAGE" "$SRC/site/published" "$SRC/operator/jobs/current" "$SRC/operator/jobs/runs")
  for p in "$SRC/operator/settings.json" "$SRC/operator/app-state.json" "$SRC/operator/backups"; do
    [ -e "$p" ] && paths+=("$p")
  done

  log "backup: one snapshot over ${#paths[@]} paths"
  restic "${RESTIC_COMMON[@]}" backup \
    --host "$BACKUP_HOST" \
    --tag cassini-exapp --tag "ts=$TS" \
    --tag "sha=$SCRIPT_SHA" --tag "commit=$INSTALLED_COMMIT" \
    "${EXCLUDES[@]}" \
    "${paths[@]}"

  log "=== cassini-exapp-backup done ts=$TS"
}

exec 9>"$LOCKFILE"
flock -n 9 || { log "another run holds $LOCKFILE; exiting"; exit 0; }
main "$@"
