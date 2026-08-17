#!/usr/bin/env bash
# backfill-catalog-rooms-in-container.sh — the half of the room backfill that
# runs INSIDE the Cassini app container.
#
# Not meant to be run directly on a host: scripts/backfill-catalog-rooms.sh
# streams it into the container over stdin. It is a tracked file rather than a
# heredoc inside that script so shellcheck covers it and so the offline test
# (scripts/test-backfill-catalog-rooms.sh) can run it with curl and ffprobe
# stubbed on PATH.
#
# What it does, per published recording that has no roomId/roomName yet:
#
#   GET  Cassini/Recordings/catalog.json          (WebDAV, as the recordings owner)
#   GET  Cassini/Recordings/meetings/<id>.opus    for each entry needing a room
#   ffprobe the file  →  CASSINI_ROOM_ID / CASSINI_ROOM_NAME, else TITLE
#   merge into the catalog, preserving every other field and the version
#   PUT       the catalog back              (only with --apply)
#   PROPPATCH the owner-only ACL back       (only with --apply)
#
# The PROPPATCH is not optional. A PUT that recreates catalog.json inherits the
# containing folder's grant to the virtual "everyone" group, which would publish
# the unfiltered archive index — every meeting, including ones the reader may
# not open — to every signed-in account. The operator re-applies the same rules
# after every write it makes, and so must this.
#
# Exit codes, which the wrapper turns into instructions. The line they draw is
# whether the catalog was WRITTEN:
#   0  done
#   1  failed AFTER writing    — the catalog may be updated without its ACL
#   2  wrong usage/environment — never started
#   3  nothing to do           — nothing written
#   4  failed BEFORE writing   — nothing written, retry is safe

set -euo pipefail

APPLY=0
LIMIT=0

# The archive layout is fixed (D-529): the recordings owner, the root, and the
# per-meeting file name, which is always the catalog entry's id.
OWNER="cassini"
ROOT="Cassini/Recordings"

log() { echo "backfill-catalog-rooms: $*"; }
fail_before() { echo "error: $*" >&2; exit 4; }
fail_after() { echo "error: $*" >&2; exit 1; }
fail_usage() { echo "error: $*" >&2; exit 2; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --apply) APPLY=1; shift ;;
    --limit)
      [[ $# -ge 2 ]] || fail_usage "--limit needs a value"
      LIMIT="$2"; shift 2 ;;
    --limit=*) LIMIT="${1#--limit=}"; shift ;;
    *) fail_usage "unknown option: $1" ;;
  esac
done
[[ "$LIMIT" =~ ^[0-9]+$ ]] || fail_usage "--limit must be a non-negative integer, got: $LIMIT"

for tool in curl ffprobe node base64; do
  command -v "$tool" >/dev/null 2>&1 || fail_usage "$tool is not available in this container"
done

: "${NEXTCLOUD_URL:?NEXTCLOUD_URL is not set — this must run inside the Cassini app container, where AppAPI injects it}"
: "${APP_SECRET:?APP_SECRET is not set — this must run inside the Cassini app container, where AppAPI injects it}"
: "${APP_ID:?APP_ID is not set — this must run inside the Cassini app container, where AppAPI injects it}"
: "${APP_VERSION:?APP_VERSION is not set — this must run inside the Cassini app container, where AppAPI injects it}"

BASE="${NEXTCLOUD_URL%/}"
AUTH="$(printf '%s' "$OWNER:$APP_SECRET" | base64 | tr -d '\n')"

WORK="$(mktemp -d)"
# Cleaned on every path: the .opus downloads are whole recordings, and leaving
# them behind would fill the container's disk with a second copy of the archive.
trap 'rm -rf "$WORK"' EXIT

# AA_VERSION is optional — AppAPI sets it only on some versions — so it is
# carried as an array element rather than an unquoted ${VAR:+...} expansion,
# which would word-split "AA-VERSION: 2.0" into two arguments and send a
# malformed header.
DAV_HEADERS=(
  -H "AUTHORIZATION-APP-API: $AUTH"
  -H "EX-APP-ID: $APP_ID"
  -H "EX-APP-VERSION: $APP_VERSION"
)
if [[ -n "${AA_VERSION:-}" ]]; then
  DAV_HEADERS+=(-H "AA-VERSION: $AA_VERSION")
fi

# dav runs one WebDAV request as the recordings owner. It prints the HTTP status
# on stdout and leaves the body in $3; the caller decides what a status means,
# because "404 on the catalog" and "404 on a meeting" are different stories.
#
# The path is appended already percent-encoded: the fixed prefix has no special
# characters, and the per-meeting segment is encoded by the node step that
# produced it, because a catalog id is server data and may contain anything.
#
# --fail is deliberately NOT used: it collapses every error status into exit 22
# and discards the body, and the status is exactly what the callers branch on.
dav() {
  local method="$1" rel="$2" out="$3"
  shift 3
  curl -sS -o "$out" -w '%{http_code}' \
    -X "$method" \
    "${DAV_HEADERS[@]}" \
    "$@" \
    "$BASE/remote.php/dav/files/$OWNER/$rel"
}

CATALOG="$WORK/catalog.json"
log "reading $ROOT/catalog.json"
status="$(dav GET "$ROOT/catalog.json" "$CATALOG")" || fail_before "could not reach Nextcloud at $BASE"
case "$status" in
  200)
    ;;
  404)
    log "no catalog at $ROOT/catalog.json — this installation has published nothing yet"
    exit 3
    ;;
  401|403)
    fail_before "Nextcloud refused the recordings owner ($status). Is the app enabled, and is $OWNER provisioned?"
    ;;
  *)
    fail_before "reading the catalog returned HTTP $status"
    ;;
esac

# Which entries need a room, and what file to read it from. One line per entry:
#   <id>\t<url-encoded opus basename>
# Entries that already carry a roomId or a roomName are skipped — the backfill
# is a repair, not a rewrite, and must never overwrite what publishing wrote.
NEEDED="$WORK/needed.tsv"
# shellcheck disable=SC2016 # the ${...} below are JS template literals, evaluated by node, not by the shell
node -e '
  const fs = require("fs");
  const [, catalogPath, limitRaw] = process.argv;
  const limit = Number(limitRaw) || 0;
  let catalog;
  try {
    catalog = JSON.parse(fs.readFileSync(catalogPath, "utf8"));
  } catch (err) {
    process.stderr.write(`the catalog is not readable JSON: ${err.message}\n`);
    process.exit(4);
  }
  const meetings = Array.isArray(catalog.meetings) ? catalog.meetings : [];
  const lines = [];
  for (const entry of meetings) {
    if (!entry || typeof entry.id !== "string" || entry.id.trim() === "") continue;
    const hasRoom =
      (typeof entry.roomId === "string" && entry.roomId.trim() !== "") ||
      (typeof entry.roomName === "string" && entry.roomName.trim() !== "");
    if (hasRoom) continue;
    // The published file is named for the entry id (the exporter derives the
    // catalog id from the .opus stem), so audioPath only confirms the meeting
    // is in the single-file format at all. An entry with only an artifactPath
    // predates it and has no file to read.
    if (typeof entry.audioPath !== "string" || entry.audioPath.trim() === "") continue;
    const id = entry.id.trim();
    // Encoded here, not in the shell: a catalog id is server data and may hold
    // spaces or anything else that has to survive into a URL path.
    lines.push(`${id}\t${encodeURIComponent(id + ".opus")}`);
    if (limit > 0 && lines.length >= limit) break;
  }
  process.stdout.write(lines.length ? lines.join("\n") + "\n" : "");
  process.stderr.write(`${meetings.length} entr(y/ies) in the catalog, ${lines.length} to examine\n`);
' "$CATALOG" "$LIMIT" > "$NEEDED" || fail_before "could not read the catalog"

if [[ ! -s "$NEEDED" ]]; then
  log "every published recording already carries its room"
  exit 3
fi

# One line per resolved room: <id>\t<roomId>\t<roomName>\t<source>
RESOLVED="$WORK/resolved.tsv"
: > "$RESOLVED"
missing=0
while IFS=$'\t' read -r id opus; do
  [[ -n "$id" ]] || continue
  target="$WORK/meeting.opus"
  status="$(dav GET "$ROOT/meetings/$opus" "$target")" || status="000"
  if [[ "$status" != "200" ]]; then
    log "  $id: no readable file at $ROOT/meetings/$opus (HTTP $status) — left without a room"
    missing=$((missing + 1))
    continue
  fi
  # Ogg puts comments on the stream and other muxers on the format; ask for both
  # and let the merge step take whichever carries the tag.
  tags="$WORK/tags.json"
  if ! ffprobe -v error -show_entries format_tags:stream_tags -of json "$target" > "$tags" 2>/dev/null; then
    log "  $id: the published file could not be probed — left without a room"
    missing=$((missing + 1))
    continue
  fi
  # shellcheck disable=SC2016 # the ${...} below are JS template literals, evaluated by node, not by the shell
  line="$(node -e '
    const fs = require("fs");
    const [, tagsPath, meetingId] = process.argv;
    const probed = JSON.parse(fs.readFileSync(tagsPath, "utf8"));
    const maps = [probed.format && probed.format.tags].concat(
      (probed.streams || []).map((stream) => stream.tags),
    );
    const tag = (name) => {
      for (const map of maps) {
        if (!map) continue;
        for (const [key, value] of Object.entries(map)) {
          if (key.toLowerCase() === name.toLowerCase() && typeof value === "string" && value.trim() !== "") {
            return value.trim();
          }
        }
      }
      return "";
    };
    const roomId = tag("CASSINI_ROOM_ID");
    let roomName = tag("CASSINI_ROOM_NAME");
    let source = roomId || roomName ? "room-tag" : "";
    if (!roomName) {
      // The Talk room name has been the embedded title since D-462. Packer
      // defaults are not names: a title that echoes the meeting id, or the
      // generic fallback, tells us nothing about a room.
      const title = tag("TITLE");
      if (title && title !== meetingId && title !== "Cassini Meeting") {
        roomName = title;
        source = source || "title";
      }
    }
    if (!roomId && !roomName) process.exit(1);
    // Tabs and newlines would break the TSV this is read back as, and a room
    // name is user-controlled text.
    const flat = (value) => value.replace(/[\t\r\n]+/g, " ").trim();
    process.stdout.write(`${meetingId}\t${flat(roomId)}\t${flat(roomName)}\t${source}`);
  ' "$tags" "$id")" || {
    log "  $id: the published file carries no room name — left without a room"
    missing=$((missing + 1))
    continue
  }
  printf '%s\n' "$line" >> "$RESOLVED"
  # Rendered with awk, not `read`: bash treats tab as IFS whitespace, so a
  # `read` over this record collapses the two tabs around an EMPTY roomId into
  # one and shifts every later field left — which is exactly the case a legacy
  # recording produces, and it would report a name as an id.
  printf '%s' "$line" | awk -F'\t' '{ printf "  %s: room id=%s name=%s (from %s)\n", $1, ($2 == "" ? "-" : $2), ($3 == "" ? "-" : $3), $4 }' \
    | while IFS= read -r rendered; do log "$rendered"; done
done < "$NEEDED"

resolved_count="$(wc -l < "$RESOLVED" | tr -d ' ')"
log "$resolved_count entr(y/ies) can be given a room; $missing cannot"

if [[ "$resolved_count" == "0" ]]; then
  log "nothing to write"
  exit 3
fi

# Merge. Entries are rewritten in place, keeping their order and every other
# field: the exporter owns this shape and this has no business normalising
# fields it does not understand. The version is carried through untouched — five
# unlinked readers check it for exact equality.
UPDATED="$WORK/catalog.updated.json"
# shellcheck disable=SC2016 # the ${...} below are JS template literals, evaluated by node, not by the shell
node -e '
  const fs = require("fs");
  const [, catalogPath, resolvedPath, outPath] = process.argv;
  const catalog = JSON.parse(fs.readFileSync(catalogPath, "utf8"));
  const rooms = new Map();
  for (const line of fs.readFileSync(resolvedPath, "utf8").split("\n")) {
    if (line.trim() === "") continue;
    const [id, roomId, roomName] = line.split("\t");
    rooms.set(id, { roomId, roomName });
  }
  let changed = 0;
  catalog.meetings = (catalog.meetings || []).map((entry) => {
    if (!entry || typeof entry.id !== "string") return entry;
    const room = rooms.get(entry.id.trim());
    if (!room) return entry;
    const updated = { ...entry };
    // Absent, never empty: an entry with roomId: "" would read as "this meeting
    // has a room whose id is the empty string", and every consumer would have
    // to check presence AND emptiness.
    if (room.roomId) updated.roomId = room.roomId;
    if (room.roomName) updated.roomName = room.roomName;
    changed += 1;
    return updated;
  });
  // Two-space indent and a trailing newline, matching every other writer of
  // this file, so a hand diff against an exported site stays readable and the
  // next publish does not rewrite the whole document.
  fs.writeFileSync(outPath, JSON.stringify(catalog, null, 2) + "\n");
  process.stderr.write(`${changed} entr(y/ies) updated\n`);
' "$CATALOG" "$RESOLVED" "$UPDATED" || fail_before "could not merge the room metadata into the catalog"

if [[ "$APPLY" != "1" ]]; then
  log "dry run: nothing was written. Re-run with --apply to write these $resolved_count entr(y/ies)."
  exit 0
fi

log "writing $ROOT/catalog.json"
status="$(dav PUT "$ROOT/catalog.json" "$WORK/put.out" -H "Content-Type: application/json" --data-binary "@$UPDATED")" \
  || fail_before "the catalog upload could not be sent"
case "$status" in
  20*|30*) ;;
  *) fail_before "writing the catalog returned HTTP $status" ;;
esac

# From here on the catalog IS written, so every failure is exit 1: the file may
# be sitting there with the containing folder's grant to everyone.
log "restoring the owner-only permissions on $ROOT/catalog.json"
ACL_BODY="$WORK/acl.xml"
cat > "$ACL_BODY" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<d:propertyupdate xmlns:d="DAV:" xmlns:nc="http://nextcloud.org/ns"><d:set><d:prop><nc:acl-list><nc:acl><nc:acl-mapping-type>group</nc:acl-mapping-type><nc:acl-mapping-id>everyone</nc:acl-mapping-id><nc:acl-mask>31</nc:acl-mask><nc:acl-permissions>0</nc:acl-permissions></nc:acl><nc:acl><nc:acl-mapping-type>user</nc:acl-mapping-type><nc:acl-mapping-id>$OWNER</nc:acl-mapping-id><nc:acl-mask>31</nc:acl-mask><nc:acl-permissions>31</nc:acl-permissions></nc:acl></nc:acl-list></d:prop></d:set></d:propertyupdate>
EOF
status="$(dav PROPPATCH "$ROOT/catalog.json" "$WORK/acl.out" \
  -H "Content-Type: application/xml; charset=utf-8" --data-binary "@$ACL_BODY")" \
  || fail_after "the permissions request could not be sent, and the catalog is already written"
case "$status" in
  20*) ;;
  *) fail_after "restoring the catalog permissions returned HTTP $status" ;;
esac
# A PROPPATCH answers 207 Multi-Status, and a REJECTED property lands in the
# per-property status inside it — the 207 alone only says the request was
# understood. Not reading it is how a file ends up with no ACL while every
# response looks fine (D-585).
if grep -qE 'HTTP/1\.[01] [45][0-9][0-9]' "$WORK/acl.out"; then
  fail_after "Nextcloud rejected the catalog permissions:
$(cat "$WORK/acl.out")
The catalog is written but may be readable by every signed-in account. Check
$ROOT/catalog.json in the Files app."
fi

log "done: $resolved_count entr(y/ies) now carry a room"
if [[ "$missing" != "0" ]]; then
  log "$missing entr(y/ies) could not be given one; their published files carry no room name"
fi
exit 0
