#!/usr/bin/env bash
# backfill-nc-files.sh — move a legacy in-app recording archive into Nextcloud Files.
#
# Run this ONCE, by hand, after updating an installation that recorded meetings
# before Cassini stored them in Nextcloud Files. Normal publishing needs nothing
# from this script: every recording has gone straight into Nextcloud Files since
# the delivery became a mandatory publish stage. What it fixes is the one case
# nothing else can — meetings published by an older version, which live only on
# the app's own volume and will never be published again.
#
# Cassini used to do this by itself, on every enable/install/update, by
# re-uploading the entire archive. That was unbounded work under a fixed
# deadline: past a certain archive size it ran out of time before writing the
# index, and the recording list in Nextcloud froze permanently. The migration is
# now something you run deliberately, once, and watch (D-613).
#
# It refuses to run if Nextcloud Files already holds recordings, because the only
# archive it is correct to write into is an empty one. If you see that refusal,
# your installation is already past the migration point and there is nothing to
# do.
#
# It also refuses on an installation running the DEFAULT storage mode (D-616).
# Everything it writes is protected by Team-folder ACL rules, and those mean
# nothing in the service account's own home — which is where the default mode
# keeps recordings, readable by every signed-in account by design. If you meant
# to move an archive INTO the Team folder, switch storage modes in the app's
# Setup tab instead: that moves the recordings that are already published.
#
#   ./scripts/backfill-nc-files.sh --dry-run      # check first, change nothing
#   ./scripts/backfill-nc-files.sh                # migrate; recordings stay private
#   ./scripts/backfill-nc-files.sh --public       # migrate; everyone may read them
#
# Options:
#   --dry-run          report what would be uploaded, write nothing
#   --public           let every signed-in account read the migrated recordings
#   --container NAME   app container (default: nc_app_gocassini, or $CASSINI_CONTAINER)
#   --site-root DIR    archive path inside the container (default: the app's own)
#   -h, --help         this text
#
# The work happens inside the app container because that is the only place the
# credential for writing to Nextcloud as the recordings owner exists — AppAPI
# injects it into the container's environment and it is not readable from here.

set -euo pipefail

CONTAINER="${CASSINI_CONTAINER:-nc_app_gocassini}"
DOCKER="${DOCKER:-docker}"
ARGS=()

die() { echo "error: $*" >&2; exit 1; }

usage() {
  cat >&2 <<'EOF'
Usage:
  ./scripts/backfill-nc-files.sh [--dry-run] [--public]
                                 [--container NAME] [--site-root DIR]

Migrates recordings published by an older version of the app out of the app's
own storage and into Nextcloud Files. Run it once, after updating.

  --dry-run          report what would be uploaded, write nothing
  --public           let every signed-in account read the migrated recordings;
                     without it they are readable only by the Cassini service
                     account and you grant access from the Files UI
  --container NAME   app container (default: nc_app_gocassini)
  --site-root DIR    archive path inside the container

Environment:
  CASSINI_CONTAINER  same as --container
  DOCKER             container CLI to use (e.g. podman)
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) ARGS+=(--dry-run); shift ;;
    --public)  ARGS+=(--public); shift ;;
    --container)
      [[ $# -ge 2 ]] || die "--container needs a value"
      CONTAINER="$2"; shift 2 ;;
    --container=*) CONTAINER="${1#--container=}"; shift ;;
    --site-root)
      [[ $# -ge 2 ]] || die "--site-root needs a value"
      ARGS+=(--site-root "$2"); shift 2 ;;
    --site-root=*) ARGS+=(--site-root "${1#--site-root=}"); shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage; die "unknown option: $1" ;;
  esac
done

command -v "$DOCKER" >/dev/null 2>&1 \
  || die "$DOCKER not found. Run this on the host where the Cassini app container runs."

if ! "$DOCKER" inspect "$CONTAINER" >/dev/null 2>&1; then
  die "container '$CONTAINER' not found.
The Cassini app container is named nc_app_<app-id> — usually nc_app_gocassini.
List candidates with: $DOCKER ps --format '{{.Names}}' | grep nc_app_
Then pass it with --container NAME."
fi

if [[ "$("$DOCKER" inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null)" != "true" ]]; then
  die "container '$CONTAINER' is not running. Enable the app in Nextcloud first, so that
Cassini has provisioned the recordings folder this migration writes into."
fi

echo "backfill-nc-files: using container $CONTAINER"

# The command reports its own progress and decides its own outcome; this script
# only translates it into what to do next. The codes distinguish whether
# anything was WRITTEN, because "retry is safe" and "retry makes it worse" are
# opposite instructions and must never be given for the same situation.
#
#   0  migrated
#   3  nothing to migrate      — nothing written
#   4  failed before writing   — nothing written, retry is safe
#   2  wrong usage/environment — nothing written, the command never started
#   1  failed after writing    — may be half-written, do not just retry
#
# Only 1 may suggest cleanup. Every other code means nothing was written, so the
# catch-all below must stay narrow: sending someone to delete recordings after a
# run that wrote nothing points them at their live archive.
set +e
# "${ARGS[@]+...}" rather than a bare "${ARGS[@]}": under `set -u`, bash 3.2 —
# still the system bash on macOS — treats an empty array as unset and aborts,
# which is exactly the no-flags invocation this script documents first.
"$DOCKER" exec "$CONTAINER" cassini-operator backfill-nc-files ${ARGS[@]+"${ARGS[@]}"}
status=$?
set -e

case "$status" in
  0) ;;
  3)
    cat >&2 <<'EOF'

Nothing was changed, and nothing needed to be. Either this installation already
keeps its recordings in Nextcloud Files, or it has no older archive to migrate.
Both mean there is nothing for this migration to do.

If you expected recordings to be missing, they are missing for some other
reason — check the app's status page rather than re-running this.
EOF
    ;;
  4)
    cat >&2 <<'EOF'

The migration stopped before writing anything, so nothing in Nextcloud Files was
touched and there is nothing to clean up. Fix the error reported above and run
it again.
EOF
    ;;
  2)
    cat >&2 <<'EOF'

The migration never started: the command rejected how it was invoked, or the app
container is not set up the way it expects. Nothing in Nextcloud Files was
touched and there is nothing to clean up.

Check that you are pointing at the Cassini app container and that the app is
enabled in Nextcloud, then run it again.
EOF
    ;;
  1)
    cat >&2 <<'EOF'

The migration stopped part-way, after it had started writing. It uploads
recordings before writing the index, so what is in Nextcloud Files now may be
recordings that nothing links to yet.

Do not simply re-run it: the guard will refuse, because Nextcloud Files now
holds recordings. Fix the error reported above, then remove the recordings this
run uploaded from Cassini/Recordings/ in the Files app before trying again.
EOF
    ;;
  *)
    # Not one of the command's own codes — the container died, or the container
    # CLI itself failed. Whether anything was written is genuinely unknown, so
    # say that rather than guess in either direction.
    cat >&2 <<EOF

The migration ended unexpectedly (exit $status). That is not one of the codes it
reports for itself, so the container or the container CLI failed rather than the
migration.

Check Cassini/Recordings/ in the Files app before re-running: whether anything
was written is unknown.
EOF
    ;;
esac

exit "$status"
