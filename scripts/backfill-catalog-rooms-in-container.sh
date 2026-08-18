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
# Empty means "no limit". It is deliberately NOT 0: 0 is a value an operator can
# type, and reading it as "unlimited" would make the most cautious-looking flag
# the one that writes the most.
LIMIT=

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
if [[ -n "$LIMIT" ]]; then
  [[ "$LIMIT" =~ ^[0-9]+$ ]] || fail_usage "--limit must be a positive integer, got: $LIMIT"
  [[ "$LIMIT" != "0" ]] || fail_usage "--limit 0 would examine nothing; omit --limit to examine everything"
fi

for tool in curl ffprobe node base64; do
  command -v "$tool" >/dev/null 2>&1 || fail_usage "$tool is not available in this container"
done

# Checked by hand rather than with "${VAR:?msg}": that form aborts with status 1
# under `set -e`, and 1 is the code this contract reserves for "failed AFTER
# writing". A run that never made a request would otherwise tell the operator
# their catalog may be publicly readable.
for var in NEXTCLOUD_URL APP_SECRET APP_ID APP_VERSION; do
  if [[ -z "${!var:-}" ]]; then
    fail_usage "$var is not set — this must run inside the Cassini app container, where AppAPI injects it"
  fi
done

BASE="${NEXTCLOUD_URL%/}"
AUTH="$(printf '%s' "$OWNER:$APP_SECRET" | base64 | tr -d '\n')"

WORK="$(mktemp -d)"
# Cleaned on every path: the .opus downloads are whole recordings, and leaving
# them behind would fill the container's disk with a second copy of the archive.
trap 'rm -rf "$WORK"' EXIT

# The auth header goes in a 0600 config file, never on curl's argv. APP_SECRET
# is the ExApp shared secret that authorises acting as any Nextcloud user, and
# an argv is readable from /proc, from `docker top`, and from any crash or audit
# capture — for every request, and this makes one per meeting examined.
#
# AA_VERSION is optional (AppAPI sets it only on some versions) and is written
# conditionally rather than expanded inline, which would word-split
# "AA-VERSION: 2.0" into two arguments and send a malformed header.
CURL_CONFIG="$WORK/curl.conf"
(umask 077; : > "$CURL_CONFIG")
{
  printf 'header = "AUTHORIZATION-APP-API: %s"\n' "$AUTH"
  printf 'header = "EX-APP-ID: %s"\n' "$APP_ID"
  # shellcheck disable=SC2153 # APP_VERSION is an AppAPI-injected variable, not a typo for AA_VERSION; both are used here
  printf 'header = "EX-APP-VERSION: %s"\n' "$APP_VERSION"
  if [[ -n "${AA_VERSION:-}" ]]; then
    printf 'header = "AA-VERSION: %s"\n' "$AA_VERSION"
  fi
} >> "$CURL_CONFIG"

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
    --config "$CURL_CONFIG" \
    -X "$method" \
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
' "$CATALOG" "${LIMIT:-0}" > "$NEEDED" || fail_before "could not read the catalog"

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
  case "$status" in
    200) ;;
    404)
      # Genuinely absent: the catalog names a file the archive does not have.
      # Nothing will ever recover a room for it.
      log "  $id: no file at $ROOT/meetings/$opus (HTTP 404) — left without a room"
      missing=$((missing + 1))
      continue
      ;;
    *)
      # An outage, an auth failure, or a proxy error — NOT a permanent verdict
      # on this recording. A backfill over a few hundred meetings downloads each
      # one in full and runs for a long time, so a Nextcloud restart part-way
      # through would otherwise silently classify every remaining entry as
      # unfixable and still report "done". Stop instead: nothing has been
      # written, so re-running costs only time.
      fail_before "reading $ROOT/meetings/$opus returned HTTP $status.
This is an outage or an auth failure, not a verdict on that recording — nothing
has been written, so fix it and run this again."
      ;;
  esac
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
      // Mirrors preferredPortableTitle (export-static-meetings.mjs), including
      // its variant arm: an STT-variant export has a catalog id like
      // "<ulid>--stt-parakeet" while the embedded title is the bare "<ulid>",
      // so the plain inequality alone would stamp a ULID in as a room name.
      const isIdEcho = title === meetingId || meetingId.startsWith(title + "--");
      if (title && !isIdEcho && title !== "Cassini Meeting") {
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
  if [[ "$missing" != "0" ]]; then
    # NOT exit 3. The wrapper renders 3 as "nothing needed to be changed", and
    # that is false here: entries do need a room and their published files do
    # not carry one. Exit 0 with the count, so the operator hears the true
    # answer — that this is as far as the files themselves can get.
    log "nothing to write: $missing entr(y/ies) need a room and their published files carry none"
    exit 0
  fi
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
# A transport failure here is NOT "nothing was written": curl can fail after
# Nextcloud has already committed the write (a proxy timing out while the
# response is read), and the recreated catalog.json would then be live with the
# container's inherited grant to everyone and no PROPPATCH to remove it. Exit 1
# — "may be written, check it" — is the only honest answer.
status="$(dav PUT "$ROOT/catalog.json" "$WORK/put.out" -H "Content-Type: application/json" --data-binary "@$UPDATED")" \
  || fail_after "the catalog upload could not be sent, and it may still have been applied"
# 2xx only, matching the operator's own uploader (webdav_upload.go). A 3xx here
# means curl — invoked without -L — did NOT store the body at the target, so
# treating a redirect as success would report a write that never happened.
case "$status" in
  20*) ;;
  *) fail_before "writing the catalog returned HTTP $status (only a 2xx means it was stored)" ;;
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
