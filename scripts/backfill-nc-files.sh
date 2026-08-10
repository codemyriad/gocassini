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

# The command reports its own progress and refuses on its own terms; this script
# only translates the outcome into something an admin can act on. Exit 3 is the
# guard's refusal, which is a legitimate answer rather than a breakage.
set +e
"$DOCKER" exec "$CONTAINER" cassini-operator backfill-nc-files "${ARGS[@]}"
status=$?
set -e

case "$status" in
  0) ;;
  3)
    cat >&2 <<'EOF'

Nothing was changed. This installation already stores its recordings in
Nextcloud Files, so there is no legacy archive to migrate. If you expected
recordings to be missing, they are missing for another reason — check the app's
status page rather than re-running this.
EOF
    ;;
  *)
    cat >&2 <<'EOF'

The migration did not finish. It uploads recordings before writing the index,
so a partial run leaves files that nothing links to rather than an index
pointing at recordings that are not there.

Re-running is NOT the fix: the guard will now refuse, because Nextcloud Files
holds recordings. Resolve the reported error first, then remove the partially
uploaded files from Cassini/Recordings/ in the Files UI before trying again.
EOF
    ;;
esac

exit "$status"
