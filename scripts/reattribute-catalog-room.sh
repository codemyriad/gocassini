#!/usr/bin/env bash
# reattribute-catalog-room.sh — declare that two room ids are the same room.
#
# A meeting's room id is derived one-way from the room's identity. A recording
# this installation produced derives its id from the Talk conversation token —
# from the file when it was recorded, from the operator's job database when it is
# backfilled. A recording this installation has NO job row for has neither, so
# its id is derived from the room NAME instead. The two derivations do not and
# cannot agree, so one real conversation can show up as two rooms:
#
#   cassini meetings rooms
#     room=rm_9f2a1c3d4e5b6a70  name=Weekly Sync  meetings=12   <- from the token
#     room=rm_11bb22cc33dd44ee  name=Weekly Sync  meetings=40   <- from the name
#
# Nothing in the data proves those are the same room. Two conversations can
# share a display name, and a room can be renamed between recordings. Only a
# person knows. This is how that person says so:
#
#   ./scripts/reattribute-catalog-room.sh \
#       --from rm_11bb22cc33dd44ee --to rm_9f2a1c3d4e5b6a70            # dry run
#   ./scripts/reattribute-catalog-room.sh \
#       --from rm_11bb22cc33dd44ee --to rm_9f2a1c3d4e5b6a70 --apply
#
# Every catalog entry whose roomId is --from is rewritten to --to, and takes on
# the target room's display name so the merged room reads as one room rather
# than as one id with two names. Pass --name to set that name explicitly when
# the target has none, or when neither name is the one you want to keep.
#
# IT ONLY TOUCHES MEETINGS THE OPERATOR HAS NO LINEAGE FOR (D-640). If a meeting
# this would move has a job row carrying the room token it was actually recorded
# in, the merge is REFUSED — for those the recorded truth is recoverable and
# asserting a different id would leave a recording whose lineage and published
# room permanently disagree. The right tool for that population is the backfill,
# which derives the real id from the token:
#
#   ./scripts/backfill-catalog-rooms.sh --apply
#
# That also means a CASSINI_ROOM_ID_PEPPER rotation is no longer a manual merge
# for most of an archive: re-running the backfill re-derives every meeting whose
# job row survives. This command is for the rest — and --force is there for the
# case where the recorded binding is genuinely the wrong one.
#
# It also re-tags each moved recording, because the exporter re-derives a catalog
# entry's room from the file on every republish, so a catalog-only merge is undone
# by the next re-seal.
#
# Options:
#   --from ID          the room id to replace (required)
#   --to ID            the room id to replace it with (required)
#   --name NAME        display name for the merged room (default: the target's)
#   --apply            write the updated catalog (without it, nothing is written)
#   --force            move meetings even when the operator recorded a different room
#   --no-retag         update only the catalog; do not re-tag any published file
#   --jobs-db PATH     the operator job database (default: resolved as the app does)
#   --no-jobs-db       do not check the recorded lineage at all
#   --container NAME   app container (default: nc_app_gocassini, or $CASSINI_CONTAINER)
#   -h, --help         this text
#
# This is a DIFFERENT KIND OF ACT from the backfill, which is why it is a
# different command. The backfill recovers what the operator recorded and cannot
# be wrong about identity. This asserts an identity the data does not support.
# It is also not reversible except by running it again the other way — and once
# two rooms are merged, which meetings came from which is no longer recorded
# anywhere. Read the dry run.

set -euo pipefail

CONTAINER="${CASSINI_CONTAINER:-nc_app_gocassini}"
DOCKER="${DOCKER:-docker}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PAYLOAD="$SCRIPT_DIR/reattribute-catalog-room-in-container.sh"
ARGS=()
FROM=""
TO=""

# Exit 2, not 1: every guard here runs before the container is touched, and 1 is
# the code this contract reserves for "failed AFTER writing".
die() { echo "error: $*" >&2; exit 2; }

usage() {
  cat >&2 <<'EOF'
Usage:
  ./scripts/reattribute-catalog-room.sh --from ID --to ID [--name NAME]
                                        [--apply] [--force] [--no-retag]
                                        [--jobs-db PATH | --no-jobs-db]
                                        [--container NAME]

Declares that two room ids are the same room, and rewrites the catalog and the
recordings so they are. Reports what it would change and writes nothing unless
--apply is passed.

Meetings the operator has a recorded room binding for are REFUSED: for those the
real room id is recoverable, and ./scripts/backfill-catalog-rooms.sh is the tool
that recovers it.

  --from ID          the room id to replace
  --to ID            the room id to replace it with
  --name NAME        display name for the merged room (default: the target's)
  --apply            write the catalog and the re-tagged recordings
  --force            move meetings even when the operator recorded a different room
  --no-retag         update only the catalog, leaving published files untouched
  --jobs-db PATH     the operator job database, if not where the app puts it
  --no-jobs-db       do not check the recorded lineage at all
  --container NAME   app container (default: nc_app_gocassini)

Find the ids with `cassini meetings rooms`.

Environment:
  CASSINI_CONTAINER  same as --container
  DOCKER             container CLI to use (e.g. podman)
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --from)
      [[ $# -ge 2 ]] || die "--from needs a value"
      FROM="$2"; shift 2 ;;
    --from=*) FROM="${1#--from=}"; shift ;;
    --to)
      [[ $# -ge 2 ]] || die "--to needs a value"
      TO="$2"; shift 2 ;;
    --to=*) TO="${1#--to=}"; shift ;;
    --name)
      [[ $# -ge 2 ]] || die "--name needs a value"
      ARGS+=(--name "$2"); shift 2 ;;
    --name=*) ARGS+=(--name "${1#--name=}"); shift ;;
    --apply) ARGS+=(--apply); shift ;;
    --force) ARGS+=(--force); shift ;;
    --no-retag) ARGS+=(--no-retag); shift ;;
    --no-jobs-db) ARGS+=(--no-jobs-db); shift ;;
    --jobs-db)
      [[ $# -ge 2 ]] || die "--jobs-db needs a value"
      ARGS+=(--jobs-db "$2"); shift 2 ;;
    --jobs-db=*) ARGS+=(--jobs-db "${1#--jobs-db=}"); shift ;;
    --container)
      [[ $# -ge 2 ]] || die "--container needs a value"
      CONTAINER="$2"; shift 2 ;;
    --container=*) CONTAINER="${1#--container=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage; die "unknown option: $1" ;;
  esac
done

[[ -n "$FROM" ]] || { usage; die "--from is required"; }
[[ -n "$TO" ]] || { usage; die "--to is required"; }
# Refused here rather than treated as a no-op: asking to merge a room into
# itself means one of the two ids is not the one that was meant, and quietly
# reporting "0 entries changed" would look like the merge had already been done.
[[ "$FROM" != "$TO" ]] || die "--from and --to are the same id ($FROM); nothing to reattribute"
ARGS=(--from "$FROM" --to "$TO" "${ARGS[@]+"${ARGS[@]}"}")

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

echo "reattribute-catalog-room: using container $CONTAINER"

#   0  done
#   3  nothing to do          — no entry carries the --from id
#   4  failed before writing  — nothing written, retry is safe
#   5  refused                — a recorded room binding contradicts the merge
#   2  wrong usage/environment — nothing written, it never started
#   1  failed after writing   — the catalog may be exposed
set +e
"$DOCKER" exec -i "$CONTAINER" bash -s -- "${ARGS[@]}" < "$PAYLOAD"
status=$?
set -e

case "$status" in
  0) ;;
  3)
    cat >&2 <<EOF

Nothing was changed: no meeting in the catalog carries the room id $FROM.

Check it against \`cassini meetings rooms\` — the value in the room= column is
what this takes, copied exactly. If the room is listed but this found nothing,
you may have the --from and --to the wrong way round.
EOF
    ;;
  4)
    cat >&2 <<'EOF'

It stopped before writing the catalog, so the catalog in Nextcloud Files is
exactly as it was. Some recordings may already have been re-tagged; that is safe
to leave and safe to repeat, because re-tagging changes nothing but the room a
file names and never touches its permissions. Fix the error reported above and
run it again.
EOF
    ;;
  5)
    cat >&2 <<EOF

Nothing was written, and this is a refusal rather than a failure.

Some of the meetings this would move have a room recorded in the operator's job
database — the conversation they were actually recorded in. Moving them to $TO
would leave that recorded lineage and their published room permanently
disagreeing, and nothing afterwards could tell which one is right.

For those meetings the real room id is recoverable, so asserting one is the
wrong tool. Run the backfill instead, which derives it from what was recorded:

  ./scripts/backfill-catalog-rooms.sh --apply

If you are certain the recorded binding is the wrong one — a job that recorded
the wrong conversation, say — re-run this with --force. The meetings it names
above are the ones that would move.
EOF
    ;;
  2)
    cat >&2 <<'EOF'

It never started: the arguments were rejected, or the app container is not set
up the way it expects. Nothing was written.
EOF
    ;;
  1)
    cat >&2 <<'EOF'

It stopped part-way, after it had started writing, and something may now be
readable that should not be. There are two shapes of this and the error above
says which:

  * the catalog was written without its owner-only permissions being re-applied,
    which would leave the full archive index readable by every signed-in account
    — check Cassini/Recordings/catalog.json in the Files app; or
  * a re-tagged recording was CREATED rather than overwritten, meaning it has no
    permissions of its own and inherits the folder's grant to every signed-in
    account — the error names the file.

Confirm the named file is shared with nobody before re-running. Re-running this
itself is safe: reattributing a room that has already been reattributed simply
finds nothing to do.
EOF
    ;;
  *)
    cat >&2 <<EOF

It ended unexpectedly (exit $status). That is not one of the codes it reports
for itself, so the container or the container CLI failed rather than the
reattribution.

Check Cassini/Recordings/catalog.json in the Files app before re-running:
whether anything was written is unknown.
EOF
    ;;
esac

exit "$status"
