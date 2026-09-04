#!/usr/bin/env bash
# backfill-catalog-rooms.sh — give already-published recordings the room their
# job actually recorded.
#
# Since D-622 every recording carries the conversation it came from. Recordings
# published before that carry nothing, so they appear in no room: `cassini
# meetings rooms` does not list them and `--room` cannot reach them.
#
# WHERE THE ROOM COMES FROM (D-640). The catalog entry's id IS the operator's
# job id — the sink publishes meetings/<jobID>.opus — and the operator's job
# database still holds the Talk room token for exactly those jobs. Nothing
# deletes job rows. So the room a recording came out of is usually recoverable
# EXACTLY, not approximately:
#
#   1. jobs.talk_binding.room_token  →  the real room id, authoritative.
#      Overwrites whatever the entry carries, which is what merges a room that
#      an earlier pass split across a token-derived and a name-derived id.
#   2. no job row  →  the published file: its CASSINI_ROOM_ID tag, else an id
#      derived from its room NAME (CASSINI_ROOM_NAME, else the TITLE tag).
#      This only FILLS A BLANK — it never overwrites a room a person chose with
#      ./scripts/reattribute-catalog-room.sh.
#
# A name-derived id is a weaker identity: two conversations sharing a display
# name derive one id, and renaming a room derives a new one. Nothing is ever
# invented, though — an entry that neither source can place is left with no room
# rather than a plausible guess. TITLE is used only when it is a real name: one
# that merely echoes the meeting id, or the packer's "Cassini Meeting" default,
# is ignored. A recording packed by hand could still have a TITLE that is a file
# name, which is why this is a dry run by default and prints the source of every
# value before it writes.
#
# IT ALSO RE-TAGS THE PUBLISHED FILE, because the exporter re-derives a catalog
# entry's room from the file on every republish — so a catalog-only backfill is
# silently undone by the next re-seal. Overwriting an existing file preserves the
# permissions already on it; the payload refuses to CREATE one, which is the case
# that would not. Pass --no-retag to skip it and accept that the change is
# temporary.
#
#   ./scripts/backfill-catalog-rooms.sh              # report only, change nothing
#   ./scripts/backfill-catalog-rooms.sh --apply      # write it back
#
# Options:
#   --apply            write the catalog and the re-tagged files (without it, nothing is written)
#   --no-retag         update only the catalog; do not re-tag any published file
#   --jobs-db PATH     the operator job database (default: resolved as the app does)
#   --no-jobs-db       ignore the job database; derive every room from its file
#   --limit N          examine at most N entries that need a room (default: all)
#   --container NAME   app container (default: nc_app_gocassini, or $CASSINI_CONTAINER)
#   -h, --help         this text
#
# The work happens inside the app container because both things it reads live
# there: the catalog in Nextcloud Files is readable only by the recordings owner,
# whose credential AppAPI injects into that container, and the job database is on
# that container's own volume.
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
  ./scripts/backfill-catalog-rooms.sh [--apply] [--no-retag] [--limit N]
                                      [--jobs-db PATH | --no-jobs-db]
                                      [--container NAME]

Gives already-published recordings the room their job recorded, reading the Talk
room token out of the operator's job database and falling back to the published
file only where no job row survives. Reports what it would change and writes
nothing unless --apply is passed.

  --apply            write the catalog and the re-tagged files
  --no-retag         update only the catalog, leaving published files untouched
                     (the change is then reverted by the next republish)
  --jobs-db PATH     the operator job database, if it is not where the app puts it
  --no-jobs-db       ignore the job database and derive every room from its file
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
    --no-retag) ARGS+=(--no-retag); shift ;;
    --no-jobs-db) ARGS+=(--no-jobs-db); shift ;;
    --jobs-db)
      [[ $# -ge 2 ]] || die "--jobs-db needs a value"
      ARGS+=(--jobs-db "$2"); shift 2 ;;
    --jobs-db=*) ARGS+=(--jobs-db "${1#--jobs-db=}"); shift ;;
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
already carries the room its job recorded, or this installation has no published
recordings.

If you expected meetings to be missing a room, check `cassini meetings list`
first. An entry this cannot fix is one with no job row AND no room name in its
published file, and no amount of re-running will change that — those are the
ones ./scripts/reattribute-catalog-room.sh exists for.
EOF
    ;;
  4)
    cat >&2 <<'EOF'

It stopped before writing the catalog, so the catalog in Nextcloud Files is
exactly as it was. Some published files may already have been re-tagged; that is
safe to leave and safe to repeat, because re-tagging changes nothing but the
room a file names and never touches its permissions. Fix the error reported
above and run it again.
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

It stopped part-way, after it had started writing, and something may now be
readable that should not be. There are two shapes of this and the error above
says which:

  * the catalog was written without its owner-only permissions being re-applied,
    which would leave the full archive index readable by every signed-in account
    — check <archive root>/catalog.json in the Files app; or
  * a re-tagged recording was CREATED rather than overwritten, meaning it has no
    permissions of its own and inherits the folder's grant to every signed-in
    account — the error names the file; check it in the Files app.

Confirm the named file is shared with nobody before re-running. Re-running the
backfill itself is safe: it is idempotent per entry.
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

Check <archive root>/catalog.json in the Files app before re-running:
whether anything was written is unknown.
EOF
    ;;
esac

exit "$status"
