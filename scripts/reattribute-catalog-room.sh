#!/usr/bin/env bash
# reattribute-catalog-room.sh — declare that two room ids are the same room.
#
# A meeting's room id is derived one-way from the room's identity. A recording
# made since Cassini started keeping the room derives its id from the Talk
# conversation token; one published before that has no token in its file, so the
# catalog backfill derives its id from the room NAME instead. The two
# derivations do not and cannot agree, so one real conversation shows up as two
# rooms:
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
# It is also the remedy when CASSINI_ROOM_ID_PEPPER changes: every id changes
# with it, and this is what maps the old ones onto the new.
#
# Options:
#   --from ID          the room id to replace (required)
#   --to ID            the room id to replace it with (required)
#   --name NAME        display name for the merged room (default: the target's)
#   --apply            write the updated catalog (without it, nothing is written)
#   --container NAME   app container (default: nc_app_gocassini, or $CASSINI_CONTAINER)
#   -h, --help         this text
#
# This is a DIFFERENT KIND OF ACT from the backfill, which is why it is a
# different command. The backfill recovers what a file already says and cannot
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
                                        [--apply] [--container NAME]

Declares that two room ids are the same room, and rewrites the catalog so they
are. Reports what it would change and writes nothing unless --apply is passed.

  --from ID          the room id to replace
  --to ID            the room id to replace it with
  --name NAME        display name for the merged room (default: the target's)
  --apply            write the updated catalog back to Nextcloud Files
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

It stopped before writing anything, so the catalog in Nextcloud Files is exactly
as it was and there is nothing to clean up. Fix the error reported above and run
it again.
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

It stopped part-way, after it had started writing. The catalog in Nextcloud
Files may hold the reattributed entries without the owner-only permissions being
re-applied, which would leave the full archive index readable by every
signed-in account.

Check Cassini/Recordings/catalog.json in the Files app and confirm it is shared
with nobody before re-running. Re-running this itself is safe: reattributing a
room that has already been reattributed simply finds nothing to do.
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
