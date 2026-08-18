#!/usr/bin/env bash
# backfill-catalog-rooms.sh — add room metadata to already-published recordings.
#
# Since D-622 every recording carries the conversation it came from, and the
# catalog entry it publishes into carries roomId/roomName. Recordings published
# before that carry neither, so they appear in no room: `cassini meetings rooms`
# does not list them, and `cassini meetings list --room` cannot reach them.
#
# This adds back what the published files still hold. Run it ONCE, by hand,
# after updating. Nothing else needs it: every recording published since the
# update writes its room at publish time.
#
# WHAT IT CAN AND CANNOT RECOVER. A recording published before D-622 was written
# with no room id. The Talk room token was never in the file and cannot be
# derived from it, so those entries get a room NAME and no id — which is enough
# for `cassini meetings rooms` to list them and for `--room "name:<name>"` to
# select them, and is honest about what is actually known. No id is ever
# invented: a made-up token would look real and would silently group meetings
# that were never in the same room.
#
# WHERE THE NAME COMES FROM, in order:
#   1. the file's CASSINI_ROOM_NAME tag  (written since D-622)
#   2. the file's TITLE tag              (the Talk room name, since D-462)
# TITLE is used only when it is a real name: a title that merely echoes the
# meeting id, or the packer's "Cassini Meeting" default, is left alone. A
# recording packed by hand from a file rather than recorded from a call could
# still have a TITLE that is a file name, not a room — which is why this runs as
# a dry run by default and prints the source of every value before it writes.
#
#   ./scripts/backfill-catalog-rooms.sh              # report only, change nothing
#   ./scripts/backfill-catalog-rooms.sh --apply      # write the catalog back
#
# Options:
#   --apply            write the updated catalog (without it, nothing is written)
#   --limit N          examine at most N entries that need a room (default: all)
#   --container NAME   app container (default: nc_app_gocassini, or $CASSINI_CONTAINER)
#   -h, --help         this text
#
# The work happens inside the app container because the catalog in Nextcloud
# Files is readable and writable only by the recordings owner, whose credential
# AppAPI injects into that container and which is not available from here.
#
# Run it while nothing is publishing. Publishing rewrites the same catalog with
# a read-merge-write and no lock, so a recording finishing mid-backfill can lose
# one of the two writes. Re-running is always safe — the backfill is idempotent
# per entry.

set -euo pipefail

CONTAINER="${CASSINI_CONTAINER:-nc_app_gocassini}"
DOCKER="${DOCKER:-docker}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PAYLOAD="$SCRIPT_DIR/backfill-catalog-rooms-in-container.sh"
ARGS=()

# Exit 2, not 1. Every guard below runs before the container is touched, and 1
# is the code this script's own contract reserves for "failed AFTER writing —
# the catalog may be exposed". A typo in a flag must not send someone to inspect
# a live archive.
die() { echo "error: $*" >&2; exit 2; }

usage() {
  cat >&2 <<'EOF'
Usage:
  ./scripts/backfill-catalog-rooms.sh [--apply] [--limit N] [--container NAME]

Adds room metadata to recordings published before Cassini recorded the room.
Reports what it would change and writes nothing unless --apply is passed.

  --apply            write the updated catalog back to Nextcloud Files
  --limit N          examine at most N entries that need a room
  --container NAME   app container (default: nc_app_gocassini)

Environment:
  CASSINI_CONTAINER  same as --container
  DOCKER             container CLI to use (e.g. podman)
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --apply) ARGS+=(--apply); shift ;;
    --limit)
      [[ $# -ge 2 ]] || die "--limit needs a value"
      ARGS+=(--limit "$2"); shift 2 ;;
    --limit=*) ARGS+=(--limit "${1#--limit=}"); shift ;;
    --container)
      [[ $# -ge 2 ]] || die "--container needs a value"
      CONTAINER="$2"; shift 2 ;;
    --container=*) CONTAINER="${1#--container=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage; die "unknown option: $1" ;;
  esac
done

[[ -r "$PAYLOAD" ]] || die "cannot read $PAYLOAD — run this from a Cassini checkout with both scripts present."

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
Cassini can reach the recordings folder this reads and writes."
fi

echo "backfill-catalog-rooms: using container $CONTAINER"

# The payload is streamed in over stdin rather than copied into the image. It
# stays a tracked script that shellcheck covers and that the offline test runs
# directly, which an inline heredoc would not be.
#
# The codes distinguish whether the catalog was WRITTEN, because "retry is safe"
# and "retry may make it worse" are opposite instructions and must never be
# given for the same situation.
#
#   0  done
#   3  nothing to do          — nothing written
#   4  failed before writing  — nothing written, retry is safe
#   2  wrong usage/environment — nothing written, it never started
#   1  failed after writing   — the catalog may be half-updated
set +e
"$DOCKER" exec -i "$CONTAINER" bash -s -- ${ARGS[@]+"${ARGS[@]}"} < "$PAYLOAD"
status=$?
set -e

case "$status" in
  0) ;;
  3)
    cat >&2 <<'EOF'

Nothing was changed, and nothing needed to be. Either every published recording
already carries its room, or this installation has no published recordings.

If you expected meetings to be missing a room, check `cassini meetings list`
first: an entry this cannot fix is one whose published file carries no room name
either, and no amount of re-running will change that.
EOF
    ;;
  4)
    cat >&2 <<'EOF'

It stopped before writing anything, so the catalog in Nextcloud Files is exactly
as it was and there is nothing to clean up. Fix the error reported above and run
it again.
EOF
    ;;
  2)
    cat >&2 <<'EOF'

It never started: the arguments were rejected, or the app container is not set
up the way it expects. Nothing was written.

This runs inside the Cassini app container because the catalog is readable only
by the recordings owner, whose credential AppAPI injects there. Check that you
are pointing at that container and that the app is enabled in Nextcloud.
EOF
    ;;
  1)
    cat >&2 <<'EOF'

It stopped part-way, after it had started writing. The catalog in Nextcloud
Files may hold the updated entries without the owner-only permissions being
re-applied, which would leave the full archive index readable by every
signed-in account.

Check Cassini/Recordings/catalog.json in the Files app and confirm it is shared
with nobody before re-running. Re-running the backfill itself is safe: it is
idempotent per entry.
EOF
    ;;
  *)
    # Not one of the payload's own codes — the container died, or the container
    # CLI failed. Whether anything was written is genuinely unknown, so say that
    # rather than guess in either direction.
    cat >&2 <<EOF

It ended unexpectedly (exit $status). That is not one of the codes it reports
for itself, so the container or the container CLI failed rather than the
backfill.

Check Cassini/Recordings/catalog.json in the Files app before re-running:
whether anything was written is unknown.
EOF
    ;;
esac

exit "$status"
