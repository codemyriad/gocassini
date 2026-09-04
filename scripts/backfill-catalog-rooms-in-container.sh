#!/usr/bin/env bash
# backfill-catalog-rooms-in-container.sh — the half of the room backfill that
# runs INSIDE the Cassini app container.
#
# Not meant to be run directly on a host: scripts/backfill-catalog-rooms.sh
# streams it into the container over stdin. It is a tracked file rather than a
# heredoc inside that script so shellcheck covers it and so the offline test
# (scripts/test-backfill-catalog-rooms.sh) can run it with curl, ffprobe, node
# and cassini stubbed on PATH.
#
# WHERE A ROOM ID COMES FROM (D-640)
#
# The catalog entry's id IS the operator's job id — the sink publishes
# meetings/<jobID>.opus and the exporter derives the catalog id from that stem —
# and jobs.talk_binding still holds the Talk room token for exactly those jobs.
# Nothing deletes job rows. So for most of an archive the room's REAL identity is
# one lookup away, and deriving from the room's display name is not the fallback
# for "an old recording", it is the fallback for "this installation has no record
# of producing it".
#
#   entry.id ──strip --stt-*──▶ jobs.talk_binding
#                                     │
#              ┌── row with a token ──┴── no row ──┐
#              ▼                                   ▼
#     rm_<hmac(TOKEN)>                   the published file:
#     name from the binding              CASSINI_ROOM_ID, else
#     AUTHORITATIVE — overwrites         rm_<hmac(TITLE)>
#     whatever the entry carries         FILLS A BLANK ONLY — an operator's
#                                        reattribution is never overwritten
#
# Those two halves compose exactly: this script only overwrites what
# reattribute-catalog-room.sh is forbidden to touch, and vice versa, so a
# scheduled backfill and an operator's judgement cannot fight over an entry.
#
# WHAT IT WRITES
#
#   PUT       <archive root>/meetings/<id>.opus       re-tagged, unless --no-retag
#   PUT       <archive root>/catalog.json             (only with --apply)
#
# <archive root> is Cassini/Recordings under access control and
# CassiniNoACL/Recordings in the default mode; it is read from
# storage_settings.json rather than assumed (see resolve_archive_root).
#   PROPPATCH the owner-only ACL on catalog.json      (only with --apply)
#
# The artifact is re-tagged because the exporter re-derives a catalog entry's
# room from the FILE on every republish. A catalog-only backfill is silently
# undone by the next re-seal, which is the D-640 P0 this closes. `cassini retag`
# is used rather than an ffmpeg one-liner because the room lives in the gzipped
# CASSINI_PAYLOAD_* manifest and the plain tag is only a mirror of it.
#
# The .opus PUT is deliberately NOT followed by a PROPPATCH. A PUT onto an
# existing path answers 204 and preserves the fileid that the groupfolders ACL
# rows hang off, so leaving the ACL alone is what keeps it correct — while
# writing one would replace the meeting's real audience with whatever this
# script could guess. The status is asserted for exactly that reason: a 201
# means the file was NOT there and a fresh, ruleless fileid was minted, which
# inherits the container's grant to everyone.
#
# The catalog's PROPPATCH, by contrast, is not optional. A PUT that recreates
# catalog.json inherits that same grant, which would publish the unfiltered
# archive index — every meeting, including ones the reader may not open — to
# every signed-in account.
#
# Exit codes, which the wrapper turns into instructions. The line they draw is
# whether the CATALOG was written:
#   0  done
#   1  failed AFTER writing    — the catalog may be updated without its ACL
#   2  wrong usage/environment — never started
#   3  nothing to do           — nothing written
#   4  failed BEFORE writing   — the catalog is untouched, retry is safe
#
# Re-tagging happens before the catalog write, so an interrupted run leaves the
# catalog stale and a re-run selects the same entries again. That is why exit 4
# stays honest even when some artifacts were already re-tagged: re-tagging is
# idempotent and never touches an ACL, so the only cost of retrying is time.

set -euo pipefail

APPLY=0
RETAG=1
USE_JOBS_DB=1
JOBS_DB=
# Empty means "no limit". It is deliberately NOT 0: 0 is a value an operator can
# type, and reading it as "unlimited" would make the most cautious-looking flag
# the one that writes the most.
LIMIT=

# The archive layout is fixed (D-529): the recordings owner, the root, and the
# per-meeting file name, which is always the catalog entry's id.
OWNER="cassini"
# ROOT is resolved from the recorded storage mode, not hard-coded (D-616
# followups). The two models keep their archives in different places on purpose,
# so that neither can shadow the other:
#
#	default            CassiniNoACL/Recordings   the owner's own private tree
#	access controlled  Cassini/Recordings        inside the Cassini Team folder
#
# A script pinned to one of them reads an empty catalog on an instance running
# the other, and — with --apply — would WRITE that empty catalog back over the
# archive's only index. resolve_archive_root below is what stops that.
ROOT=""
# An override for a deployment whose persistent volume is somewhere unusual.
STORAGE_SETTINGS="${CASSINI_STORAGE_SETTINGS_PATH:-}"
STORAGE_SETTINGS_REL="storage_settings.json"

# The image default, and the relative path AppAPI's persistent volume puts it
# under. Both mirror exAppDataPathDefault in the operator and effective_data_path
# in deployment/exapp-start.sh; a guard that reads $CASSINI_OPERATOR_DB_PATH
# literally would read a non-existent file on every real install, because the
# baked env value IS the image default and the operator redirects it.
JOBS_DB_IMAGE_DEFAULT="/var/lib/cassini-operator/jobs.sqlite3"
JOBS_DB_PERSIST_REL="operator/jobs.sqlite3"

log() { echo "backfill-catalog-rooms: $*"; }
fail_before() { echo "error: $*" >&2; exit 4; }
fail_after() { echo "error: $*" >&2; exit 1; }
fail_usage() { echo "error: $*" >&2; exit 2; }

# resolve_archive_root reads which storage model this install is in, and returns
# the root that model keeps its archive under.
#
# The mode lives in storage_settings.json beside the jobs database, on the same
# AppAPI persistent volume (cassini-operator/internal/operator/storage_settings.go).
# It is read rather than guessed because guessing wrong is not a failed run: with
# --apply, a script that read an empty catalog from the wrong root would write
# that empty catalog back over the archive's only index.
#
# A file that exists but says nothing usable is FATAL rather than defaulted, for
# the same reason: "I could not tell which mode this is" and "this is the default
# mode" are different answers, and only one of them is safe to act on.
resolve_archive_root() {
  local settings="$STORAGE_SETTINGS" enabled=""

  if [[ -z "$settings" ]]; then
    settings="${APP_PERSISTENT_STORAGE:-}"
    if [[ -n "$settings" ]]; then
      settings="${settings%/}/$STORAGE_SETTINGS_REL"
    else
      # No persistent volume in the environment: fall back to the directory the
      # jobs database resolved to, which is where the operator puts both.
      settings="$(dirname -- "${JOBS_DB:-$JOBS_DB_IMAGE_DEFAULT}")/$STORAGE_SETTINGS_REL"
    fi
  fi

  if [[ ! -f "$settings" ]]; then
    # No recorded decision at all. That is the ordinary state of an install that
    # has never had its enabled edge run, and the operator's own fallback there
    # is the default model — so it is the honest answer, not a guess.
    ROOT="CassiniNoACL/Recordings"
    log "no storage settings at $settings; assuming the default storage mode ($ROOT)"
    return 0
  fi

  enabled="$(jq -r 'if has("access_control_enabled") then (.access_control_enabled|tostring) else "" end' "$settings" 2>/dev/null || true)"
  case "$enabled" in
    true)  ROOT="Cassini/Recordings" ;;
    false) ROOT="CassiniNoACL/Recordings" ;;
    *)
      fail_before "could not read access_control_enabled from $settings — refusing to guess which storage mode this instance is in, because writing to the wrong root would overwrite the archive's index"
      ;;
  esac

  if [[ "$(jq -r 'if has("migration_clean") then (.migration_clean|tostring) else "true" end' "$settings" 2>/dev/null || echo true)" == "false" ]]; then
    # The recorded mode still names a complete archive — that is the operator's
    # invariant — so this is safe to run. It is worth saying out loud because the
    # OTHER root also holds recordings, and an operator looking at both would
    # otherwise think this script had missed some.
    log "note: a storage mode switch did not finish tidying up. $ROOT is complete; the other root holds a leftover copy that Cassini does not read."
  fi
  log "storage mode from $settings: archive root is $ROOT"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --apply) APPLY=1; shift ;;
    --no-retag) RETAG=0; shift ;;
    --no-jobs-db) USE_JOBS_DB=0; shift ;;
    --jobs-db)
      [[ $# -ge 2 ]] || fail_usage "--jobs-db needs a value"
      JOBS_DB="$2"; shift 2 ;;
    --jobs-db=*) JOBS_DB="${1#--jobs-db=}"; shift ;;
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
if [[ -n "$JOBS_DB" && "$USE_JOBS_DB" == "0" ]]; then
  fail_usage "--jobs-db and --no-jobs-db contradict each other"
fi

for tool in curl ffprobe node base64; do
  command -v "$tool" >/dev/null 2>&1 || fail_usage "$tool is not available in this container"
done
# Only a run that will actually re-tag needs it: a dry run never reaches the
# call, and refusing to even REPORT on a container without the binary would deny
# an operator the one thing that is always safe to do.
if [[ "$RETAG" == "1" && "$APPLY" == "1" ]]; then
  command -v cassini >/dev/null 2>&1 \
    || fail_usage "cassini is not available in this container, so published files cannot be re-tagged. Pass --no-retag to write only the catalog — but a catalog-only room is reverted by the next republish."
fi

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
# capture — for every request, and this makes several per meeting examined.
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

# run_node runs a node program, retrying once with --experimental-sqlite.
#
# node:sqlite is unflagged from Node 22.13 and flagged before it, and the image
# tracks node:22-*, so which one is present depends on when the image was built.
# Probing the version string would mean parsing it; running the program and
# retrying on the one error that flag fixes is shorter and cannot misread a
# version scheme.
#
# stderr is captured rather than passed through, because the FIRST attempt's
# stderr is a false alarm whenever the retry is the one that works. It is
# forwarded on both outcomes: on failure it is the diagnosis, and on success it
# carries the count of job rows found — which is the one line that tells an
# operator whether their job history was actually there.
run_node() {
  local program="$1"
  shift
  if node -e "$program" "$@" 2>"$WORK/node.err"; then
    cat "$WORK/node.err" >&2
    return 0
  fi
  if grep -q "node:sqlite" "$WORK/node.err" 2>/dev/null; then
    # Start the log over: the first attempt's module error is noise once the
    # retry explains itself.
    : > "$WORK/node.err"
    if node --experimental-sqlite -e "$program" "$@" 2>"$WORK/node.err"; then
      cat "$WORK/node.err" >&2
      return 0
    fi
  fi
  cat "$WORK/node.err" >&2
  return 1
}

# ---------------------------------------------------------------------------
# The jobs table: catalog id -> the room the operator RECORDED it in.
# ---------------------------------------------------------------------------

JOBS_TSV="$WORK/jobs.tsv"
: > "$JOBS_TSV"
jobs_source="none"

if [[ "$USE_JOBS_DB" == "1" ]]; then
  if [[ -z "$JOBS_DB" ]]; then
    JOBS_DB="${CASSINI_OPERATOR_DB_PATH:-}"
    if [[ -n "${APP_PERSISTENT_STORAGE:-}" && ( -z "$JOBS_DB" || "$JOBS_DB" == "$JOBS_DB_IMAGE_DEFAULT" ) ]]; then
      JOBS_DB="${APP_PERSISTENT_STORAGE%/}/$JOBS_DB_PERSIST_REL"
    fi
    JOBS_DB="${JOBS_DB:-$JOBS_DB_IMAGE_DEFAULT}"
  fi

  if [[ ! -f "$JOBS_DB" ]]; then
    # Not fatal, and not silent. An installation whose job history is genuinely
    # gone still gets the name-derived pass — but the operator has to be told,
    # because the ids it produces are the weaker kind and a later run that DOES
    # find the database will move those meetings to different ids.
    log "no operator job database at $JOBS_DB — every room will be derived from the published file's name, which is a weaker identity."
    log "  pass --jobs-db PATH if it lives elsewhere, or --no-jobs-db to silence this."
  else
    log "reading room bindings from $JOBS_DB"
    # Read-only, and the ONLY thing taken out is the derived id and the name.
    # The token is a capability — for a public conversation /call/<token> is the
    # join link — so it is hashed inside this program and never printed, never
    # written to a file, and never put on a command line.
    #
    # Pinned against the Go implementation by
    # TestRoomIDMatchesTheNodeImplementation, and re-implemented rather than
    # shared with the name-domain derivation below so a divergence fails a test
    # instead of agreeing with itself.
    # shellcheck disable=SC2016 # the ${...} below are JS template literals, evaluated by node, not by the shell
    run_node '
      const crypto = require("crypto");
      const { DatabaseSync } = require("node:sqlite");
      const [, dbPath] = process.argv;
      const db = new DatabaseSync(dbPath, { readOnly: true });
      const pepper = Buffer.from(process.env.CASSINI_ROOM_ID_PEPPER || "", "utf8");
      const flat = (value) => String(value == null ? "" : value).replace(/[\t\r\n]+/g, " ").trim();
      let rows;
      try {
        rows = db.prepare("SELECT id, talk_binding FROM jobs WHERE talk_binding IS NOT NULL").all();
      } finally {
        db.close();
      }
      const lines = [];
      for (const row of rows) {
        let binding;
        try {
          binding = JSON.parse(row.talk_binding);
        } catch {
          continue;
        }
        const token = typeof binding?.room_token === "string" ? binding.room_token.trim() : "";
        if (token === "") continue;
        const mac = crypto.createHmac("sha256", pepper);
        mac.update("cassini.room.token.v1\u0000", "utf8");
        mac.update(token, "utf8");
        const roomId = "rm_" + mac.digest("hex").slice(0, 16);
        const roomName = typeof binding?.room_name === "string" ? binding.room_name.trim() : "";
        lines.push(`${flat(row.id)}\t${roomId}\t${flat(roomName)}`);
      }
      process.stdout.write(lines.length ? lines.join("\n") + "\n" : "");
      process.stderr.write(`${lines.length} job(s) carry a room binding\n`);
    ' "$JOBS_DB" > "$JOBS_TSV" || fail_before "could not read the operator job database at $JOBS_DB.
This is an outage or a permissions problem, not a verdict on any recording —
nothing has been written. Fix it and run this again, or pass --no-jobs-db to
fall back to deriving every room from its published file's name."
    jobs_source="$JOBS_DB"
  fi
fi

# ---------------------------------------------------------------------------
# The catalog, and which of its entries need work.
# ---------------------------------------------------------------------------

CATALOG="$WORK/catalog.json"
resolve_archive_root

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

# The plan: one line per entry that needs work, six fields separated by US
# (U+001F), not by tabs.
#   <id> <url-encoded opus basename> <roomId> <roomName> <source> <needsProbe>
#
# The delimiter is load-bearing. Bash treats TAB as IFS *whitespace*, so a `read`
# over a tab-separated record collapses a run of tabs into one and shifts every
# later field left — and two of these six fields are EMPTY on exactly the path
# that most needs them read correctly (an entry with no job row, whose roomId and
# roomName both come from the file). US is not IFS whitespace, so empty fields
# survive. The planner strips control characters from every value it emits, so
# the delimiter cannot appear inside one.
#
# A blank roomId with needsProbe=1 means "the file has to tell us"; a filled one
# means the jobs table already did.
PLAN="$WORK/plan.tsv"
# shellcheck disable=SC2016 # the ${...} below are JS template literals, evaluated by node, not by the shell
node -e '
  const fs = require("fs");
  const [, catalogPath, jobsPath, limitRaw] = process.argv;
  const limit = Number(limitRaw) || 0;
  let catalog;
  try {
    catalog = JSON.parse(fs.readFileSync(catalogPath, "utf8"));
  } catch (err) {
    process.stderr.write(`the catalog is not readable JSON: ${err.message}\n`);
    process.exit(4);
  }

  const jobs = new Map();
  const jobsRaw = fs.readFileSync(jobsPath, "utf8");
  for (const line of jobsRaw.split("\n")) {
    if (line.trim() === "") continue;
    const [jobId, roomId, roomName] = line.split("\t");
    jobs.set(jobId, { roomId, roomName: roomName || "" });
  }

  // An STT-variant export has a catalog id like "<ulid>--stt-parakeet" while
  // the job it came from is the bare "<ulid>". Mirrors stripVariantSuffix in
  // export-static-meetings.mjs; re-implemented rather than shared, like every
  // other cross-language rule here.
  const stripVariant = (id) => id.replace(/--stt-[A-Za-z0-9._-]+$/, "");
  const str = (value) => (typeof value === "string" ? value.trim() : "");
  const US = "\u001f";
  // Every C0 control, not just tab and newline: the record separator is one of
  // them, and a room name is user-controlled text.
  const flat = (value) => String(value).replace(/[\u0000-\u001f]+/g, " ").trim();

  const meetings = Array.isArray(catalog.meetings) ? catalog.meetings : [];
  const lines = [];
  let reconciled = 0;
  let filled = 0;
  for (const entry of meetings) {
    if (!entry || str(entry.id) === "") continue;
    const id = str(entry.id);
    // The published file is named for the entry id (the exporter derives the
    // catalog id from the .opus stem), so audioPath only confirms the meeting
    // is in the single-file format at all. An entry with only an artifactPath
    // predates it and has no file to read or to re-tag.
    if (str(entry.audioPath) === "") continue;

    const job = jobs.get(stripVariant(id));
    const entryRoomId = str(entry.roomId);
    const entryRoomName = str(entry.roomName);

    if (job) {
      // The machine owns this one. The token is what the operator recorded, so
      // it wins over whatever the entry carries — that is what heals a room
      // split across a token-derived and a name-derived id, and what makes
      // rotating CASSINI_ROOM_ID_PEPPER a re-run rather than a manual merge.
      const roomName = job.roomName || entryRoomName;
      // Skip when the id already matches AND the binding has no name to add.
      // Testing `roomName !== ""` instead would re-examine, re-download and
      // re-upload — on every single run, forever — the one shape that has a
      // room but genuinely no label anywhere: a job whose Talk room-name lookup
      // never succeeded and whose file carries no usable title either. There is
      // nothing left for this script to find for those, and saying so is what
      // makes a scheduled run idempotent.
      if (entryRoomId === job.roomId && (job.roomName === "" || job.roomName === entryRoomName)) {
        continue;
      }
      // Probe only when the name is still unknown: the id came from the token
      // and needs no file at all, but a room with no label reads as anonymous
      // in every listing, and the TITLE tag on the file is the last place to look.
      const needsProbe = roomName === "" ? 1 : 0;
      lines.push([flat(id), encodeURIComponent(id + ".opus"), job.roomId, flat(roomName), "job-binding", needsProbe].join(US));
      reconciled += 1;
    } else {
      // The operator owns this one: no job row means this installation has no
      // record of producing it, so anything already there was put there by a
      // person and must not be overwritten.
      if (entryRoomId !== "" || entryRoomName !== "") continue;
      lines.push([flat(id), encodeURIComponent(id + ".opus"), "", "", "file", 1].join(US));
      filled += 1;
    }
    if (limit > 0 && lines.length >= limit) break;
  }
  process.stdout.write(lines.length ? lines.join("\n") + "\n" : "");
  process.stderr.write(`${meetings.length} entr(y/ies) in the catalog, ${lines.length} to examine (${reconciled} from the jobs table, ${filled} from their published file)\n`);
' "$CATALOG" "$JOBS_TSV" "${LIMIT:-0}" > "$PLAN" || fail_before "could not read the catalog"

if [[ ! -s "$PLAN" ]]; then
  log "every published recording already carries the room its job recorded"
  exit 3
fi

# ---------------------------------------------------------------------------
# Resolve each entry, and re-tag its artifact.
# ---------------------------------------------------------------------------

# One line per resolved room: <id>\t<roomId>\t<roomName>\t<source>
RESOLVED="$WORK/resolved.tsv"
: > "$RESOLVED"
missing=0
retagged=0
while IFS=$'\x1f' read -r id opus want_room_id want_room_name source needs_probe; do
  [[ -n "$id" ]] || continue
  room_id="$want_room_id"
  room_name="$want_room_name"
  target="$WORK/meeting.opus"
  have_file=0

  # The file is fetched when it has something to tell us, and when it is about
  # to be re-tagged. A room recovered from the jobs table needs neither, so a
  # re-run with --no-retag over an archive with intact job history downloads
  # nothing at all.
  if [[ "$needs_probe" == "1" || ( "$APPLY" == "1" && "$RETAG" == "1" ) ]]; then
    status="$(dav GET "$ROOT/meetings/$opus" "$target")" || status="000"
    case "$status" in
      200) have_file=1 ;;
      404)
        # Genuinely absent: the catalog names a file the archive does not have.
        #
        # Gated on whether a room is already KNOWN, not on whether the file was
        # going to be probed. needs_probe means only "the room NAME is still
        # unknown" — for an entry recovered from the jobs table the id came from
        # the token and needs no file at all, so treating a missing file as
        # fatal there would throw away an id the operator's own records
        # determined, and report the meeting as unplaceable when it is not.
        if [[ -z "$room_id" ]]; then
          log "  $id: no file at $ROOT/meetings/$opus (HTTP 404) — left without a room"
          missing=$((missing + 1))
          continue
        fi
        log "  $id: no file at $ROOT/meetings/$opus (HTTP 404) — the catalog entry is updated, the artifact cannot be"
        ;;
      *)
        # An outage, an auth failure, or a proxy error — NOT a permanent verdict
        # on this recording. A backfill over a few hundred meetings downloads
        # each one in full and runs for a long time, so a Nextcloud restart
        # part-way through would otherwise silently classify every remaining
        # entry as unfixable and still report "done". Stop instead: the catalog
        # has not been written, so re-running costs only time.
        fail_before "reading $ROOT/meetings/$opus returned HTTP $status.
This is an outage or an auth failure, not a verdict on that recording — the
catalog has not been written, so fix it and run this again."
        ;;
    esac
  fi

  if [[ "$needs_probe" == "1" && "$have_file" == "1" ]]; then
    # Ogg puts comments on the stream and other muxers on the format; ask for
    # both and let the resolve step take whichever carries the tag.
    tags="$WORK/tags.json"
    if ! ffprobe -v error -show_entries format_tags:stream_tags -of json "$target" > "$tags" 2>/dev/null; then
      if [[ -z "$room_id" ]]; then
        log "  $id: the published file could not be probed — left without a room"
        missing=$((missing + 1))
        continue
      fi
      log "  $id: the published file could not be probed — keeping the room its job recorded, with no name"
    else
      # shellcheck disable=SC2016 # the ${...} below are JS template literals, evaluated by node, not by the shell
      line="$(node -e '
        const fs = require("fs");
        const [, tagsPath, meetingId, knownRoomId, knownRoomName] = process.argv;
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
        // Anything the jobs table already told us outranks the file: the token
        // is what the operator recorded, and the file is a copy of what it
        // happened to know at pack time.
        let roomId = knownRoomId || tag("CASSINI_ROOM_ID");
        let roomName = knownRoomName || tag("CASSINI_ROOM_NAME");
        let source = knownRoomId ? "job-binding" : (roomId || roomName ? "room-tag" : "");
        if (!roomName) {
          // The Talk room name has been the embedded title since D-462. Packer
          // defaults are not names: a title that echoes the meeting id, or the
          // generic fallback, tells us nothing about a room.
          const title = tag("TITLE");
          // Mirrors preferredPortableTitle (export-static-meetings.mjs),
          // including its variant arm: an STT-variant export has a catalog id
          // like "<ulid>--stt-parakeet" while the embedded title is the bare
          // "<ulid>", so the plain inequality alone would stamp a ULID in as a
          // room name.
          const isIdEcho = title === meetingId || meetingId.startsWith(title + "--");
          if (title && !isIdEcho && title !== "Cassini Meeting") {
            roomName = title;
            source = source || "title";
          }
        }
        if (!roomId && !roomName) process.exit(1);
        if (!roomId) {
          // No id from the jobs table and none in the file, so derive one from
          // the name with the SAME one-way function the recorder applies to a
          // Talk token — different domain, so a name can never collide with a
          // token belonging to some other room. Pinned against the Go
          // implementation by TestRoomIDMatchesTheNodeImplementation.
          const crypto = require("crypto");
          const mac = crypto.createHmac("sha256", Buffer.from(process.env.CASSINI_ROOM_ID_PEPPER || "", "utf8"));
          mac.update("cassini.room.name.v1\u0000", "utf8");
          mac.update(roomName.trim(), "utf8");
          roomId = "rm_" + mac.digest("hex").slice(0, 16);
          // Say so. A derived-from-name id is a weaker identity than a
          // derived-from-token one, and the operator reading a dry run is the
          // only person in a position to notice when it is wrong.
          source = (source || "title") + "+id-from-name";
        }
        // Tabs and newlines would break the TSV this is read back as, and a
        // room name is user-controlled text.
        const flat = (value) => String(value).replace(/[\u0000-\u001f]+/g, " ").trim();
        process.stdout.write([flat(roomId), flat(roomName), source].join("\u001f"));
      ' "$tags" "$id" "$room_id" "$room_name")" || line=""
      if [[ -z "$line" ]]; then
        if [[ -z "$room_id" ]]; then
          log "  $id: the published file carries no room name — left without a room"
          missing=$((missing + 1))
          continue
        fi
      else
        IFS=$'\x1f' read -r room_id room_name source <<< "$line"
      fi
    fi
  fi

  if [[ -z "$room_id" ]]; then
    log "  $id: no room could be resolved — left without a room"
    missing=$((missing + 1))
    continue
  fi

  # Re-tag the artifact BEFORE the catalog is written. An interrupted run then
  # leaves the catalog stale, so a re-run selects the same entries and finishes
  # the job; the reverse order would leave the catalog claiming a room the files
  # do not carry, and a re-run would skip them forever.
  retag_note=""
  if [[ "$APPLY" == "1" && "$RETAG" == "1" && "$have_file" == "1" ]]; then
    retagged_path="$WORK/retagged.opus"
    rm -f "$retagged_path"
    retag_args=(retag "$target" --out "$retagged_path" --room-id "$room_id")
    # The lineage, while the file is open anyway. A meeting recovered from the
    # jobs table has one by definition; one recovered from its own file does
    # not, and nothing is invented for it.
    if [[ "$source" == job-binding* ]]; then
      retag_args+=(--job-id "${id%%--stt-*}")
    fi
    retag_args+=(--json)
    if ! cassini "${retag_args[@]}" >"$WORK/retag.json" 2>"$WORK/retag.err"; then
      fail_before "re-tagging $id failed:
$(cat "$WORK/retag.err")
The catalog has not been written and no artifact was uploaded, so re-running is
safe. Pass --no-retag to update only the catalog."
    fi
    # Re-tagging is local and cheap; UPLOADING a whole sealed recording is not.
    # An entry can be selected purely because its room NAME changed, and the
    # artifact carries the id and not the name — so for those there is nothing
    # new to send, and sending it anyway would re-upload the archive on every
    # run. retag's own change report is the authority on whether the file moved,
    # which avoids probing it a second time to ask.
    # Three outcomes, and they must not be collapsed: 0 "it moved, upload it",
    # 10 "nothing moved, skip the upload", anything else "the summary could not
    # be read". A two-way test would read an unparseable summary as "nothing
    # moved" and silently skip the durability step this whole branch exists for.
    set +e
    node -e '
      const fs = require("fs");
      const summary = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
      if (!Array.isArray(summary.changes)) throw new Error("summary has no changes array");
      process.exit(summary.changes.length > 0 ? 0 : 10);
    ' "$WORK/retag.json" 2>"$WORK/retag-summary.err"
    summary_status=$?
    set -e
    case "$summary_status" in
      0) ;;
      10)
        log "  $id: room id=$room_id name=${room_name:--} (from ${source}; the published file already names this room)"
        printf '%s\t%s\t%s\t%s\n' "$id" "$room_id" "$room_name" "$source" >> "$RESOLVED"
        continue
        ;;
      *)
        fail_before "could not read what re-tagging $id changed:
$(cat "$WORK/retag-summary.err")
Nothing has been uploaded and the catalog has not been written, so re-running is
safe."
        ;;
    esac
    # Refuse to CREATE. A PUT onto a path that is not there mints a fresh fileid
    # with no ACL rows, and a leaf under meetings/ with no rule of its own
    # inherits the container's grant to the virtual everyone group — a
    # world-readable recording. The GET above already proved it exists; this is
    # the second half of the same guard, on the response rather than the request.
    status="$(dav PUT "$ROOT/meetings/$opus" "$WORK/put-opus.out" -H "Content-Type: audio/ogg" --data-binary "@$retagged_path")" \
      || fail_before "uploading the re-tagged $id could not be sent.
The file's ACL is unaffected — an overwrite keeps the fileid its permissions
hang off — but the recording may be truncated. Check that $id still plays, then
run this again."
    case "$status" in
      204) ;;
      201)
        fail_after "uploading the re-tagged $id answered HTTP 201, meaning the file was CREATED rather than overwritten.
A newly created file under $ROOT/meetings/ has no permissions of its own and
inherits the folder's grant to every signed-in account. Check the permissions on
$ROOT/meetings/$opus in the Files app before doing anything else."
        ;;
      *)
        fail_before "uploading the re-tagged $id returned HTTP $status (an overwrite answers 204).
The catalog has not been written, so re-running is safe."
        ;;
    esac
    retagged=$((retagged + 1))
    retag_note=", retagged"
  fi

  printf '%s\t%s\t%s\t%s\n' "$id" "$room_id" "$room_name" "$source" >> "$RESOLVED"
  log "  $id: room id=$room_id name=${room_name:--} (from ${source}${retag_note})"
done < "$PLAN"

resolved_count="$(wc -l < "$RESOLVED" | tr -d ' ')"
log "$resolved_count entr(y/ies) can be given a room; $missing cannot"
if [[ "$APPLY" == "1" && "$RETAG" == "1" ]]; then
  log "$retagged artifact(s) re-tagged so the room survives a republish"
fi

if [[ "$resolved_count" == "0" ]]; then
  if [[ "$missing" != "0" ]]; then
    # NOT exit 3. The wrapper renders 3 as "nothing needed to be changed", and
    # that is false here: entries do need a room and neither the jobs table nor
    # their published files can supply one. Exit 0 with the count, so the
    # operator hears the true answer.
    log "nothing to write: $missing entr(y/ies) need a room and neither $jobs_source nor their published files can supply one"
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
  log "$missing entr(y/ies) could not be given one; neither the jobs table nor their published files carry a room"
fi
if [[ "$RETAG" != "1" ]]; then
  log "--no-retag was set, so the published files were not updated. The next republish of these meetings will revert them."
fi
exit 0
