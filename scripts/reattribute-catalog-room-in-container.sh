#!/usr/bin/env bash
# reattribute-catalog-room-in-container.sh — the half of the room reattribution
# that runs INSIDE the Cassini app container.
#
# Not meant to be run directly on a host: scripts/reattribute-catalog-room.sh
# streams it in over stdin. It is a tracked file rather than a heredoc so the
# lint gate covers it, and so the offline test can run it with curl stubbed on
# PATH.
#
#   GET       Cassini/Recordings/catalog.json     (WebDAV, as the recordings owner)
#   rewrite   every entry whose roomId is --from, to --to (+ the merged name)
#   REFUSE    if any of them has a recorded room binding                (D-640)
#   PUT       each moved meeting's re-tagged .opus  (only with --apply)
#   PUT       the catalog back                      (only with --apply)
#   PROPPATCH the owner-only ACL back               (only with --apply)
#
# THE LINEAGE GUARD (D-640). Reattribution asserts an identity the data does not
# support. Where the data DOES support one — a job row in the operator's database
# carrying the Talk room token this recording actually came from — asserting a
# different one manufactures a meeting whose recorded lineage and published room
# permanently disagree, and nothing downstream can tell which is right.
#
# So a meeting the operator has lineage for is refused, and the operator is
# pointed at ./scripts/backfill-catalog-rooms.sh, which for that population is
# not a workaround but the better answer: it derives the true id from the token.
# --force overrides, loudly.
#
# The guard runs BEFORE the dry-run early exit, so a dry run refuses too. The dry
# run is the review surface this command's whole design rests on, and a guard
# that only fires on --apply teaches an operator that the dry run is a preview of
# something else.
#
# It compares DERIVED IDS and prints derived ids. The token is a capability — for
# a public conversation /call/<token> is the join link — so it is hashed inside
# the node program and never logged, written or put on a command line.
#
# IT ALSO RE-TAGS EACH MOVED RECORDING, because the exporter re-derives a catalog
# entry's room from the FILE on every republish; a catalog-only merge is undone
# by the next re-seal. The .opus PUT is never followed by a PROPPATCH: an
# overwrite answers 204 and preserves the fileid the groupfolders rules hang off,
# so leaving the ACL alone is what keeps it correct. A 201 means the file was not
# there and a fresh, ruleless fileid was minted, which is a hard failure.
#
# The catalog's PROPPATCH, by contrast, is not optional: a PUT that recreates
# catalog.json inherits the containing folder's grant to the virtual "everyone"
# group, which would publish the unfiltered archive index to every signed-in
# account.
#
# Exit codes, which the wrapper turns into instructions. The line they draw is
# whether the catalog was WRITTEN:
#   0  done
#   1  failed AFTER writing    — the catalog may be updated without its ACL
#   2  wrong usage/environment — never started
#   3  nothing to do           — no entry carries the --from id
#   4  failed BEFORE writing   — nothing written, retry is safe
#   5  refused                 — a recorded room binding contradicts this merge

set -euo pipefail

APPLY=0
FORCE=0
RETAG=1
USE_JOBS_DB=1
JOBS_DB=
FROM=""
TO=""
NAME=""

OWNER="cassini"
ROOT="Cassini/Recordings"

# The image default and the AppAPI-relative path, mirroring exAppDataPathDefault
# in the operator and effective_data_path in deployment/exapp-start.sh. Reading
# $CASSINI_OPERATOR_DB_PATH literally would miss on every real install: the baked
# env value IS the image default, and the operator redirects it.
JOBS_DB_IMAGE_DEFAULT="/var/lib/cassini-operator/jobs.sqlite3"
JOBS_DB_PERSIST_REL="operator/jobs.sqlite3"

log() { echo "reattribute-catalog-room: $*"; }
fail_before() { echo "error: $*" >&2; exit 4; }
fail_after() { echo "error: $*" >&2; exit 1; }
fail_usage() { echo "error: $*" >&2; exit 2; }
fail_refused() { echo "error: $*" >&2; exit 5; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --apply) APPLY=1; shift ;;
    --force) FORCE=1; shift ;;
    --no-retag) RETAG=0; shift ;;
    --no-jobs-db) USE_JOBS_DB=0; shift ;;
    --jobs-db)
      [[ $# -ge 2 ]] || fail_usage "--jobs-db needs a value"
      JOBS_DB="$2"; shift 2 ;;
    --jobs-db=*) JOBS_DB="${1#--jobs-db=}"; shift ;;
    --from)
      [[ $# -ge 2 ]] || fail_usage "--from needs a value"
      FROM="$2"; shift 2 ;;
    --from=*) FROM="${1#--from=}"; shift ;;
    --to)
      [[ $# -ge 2 ]] || fail_usage "--to needs a value"
      TO="$2"; shift 2 ;;
    --to=*) TO="${1#--to=}"; shift ;;
    --name)
      [[ $# -ge 2 ]] || fail_usage "--name needs a value"
      NAME="$2"; shift 2 ;;
    --name=*) NAME="${1#--name=}"; shift ;;
    *) fail_usage "unknown option: $1" ;;
  esac
done

[[ -n "$FROM" ]] || fail_usage "--from is required"
[[ -n "$TO" ]] || fail_usage "--to is required"
[[ "$FROM" != "$TO" ]] || fail_usage "--from and --to are the same id ($FROM); nothing to reattribute"
# Both must LOOK like derived room ids. The one input that must never get
# through is a raw Talk conversation token pasted where an id belongs: it is a
# short alphanumeric string that looks perfectly plausible, and --to is written
# verbatim into every moved entry's roomId — so with --no-retag it would be
# published in catalog.json, which is the one thing the derivation exists to
# prevent. `cassini retag` refuses it on the other path; this closes the path
# that skips retag.
for id_flag in from to; do
  id_value="$FROM"
  [[ "$id_flag" == "to" ]] && id_value="$TO"
  [[ "$id_value" =~ ^rm_[0-9a-f]{16}$ ]] || fail_usage "--$id_flag is not a derived room id (want rm_ followed by 16 hex characters), got: $id_value
Copy the room= value from 'cassini meetings rooms' exactly. If what you have
looks like a Talk conversation token, it is not a room id and must not be
published."
done
if [[ -n "$JOBS_DB" && "$USE_JOBS_DB" == "0" ]]; then
  fail_usage "--jobs-db and --no-jobs-db contradict each other"
fi

for tool in curl node base64; do
  command -v "$tool" >/dev/null 2>&1 || fail_usage "$tool is not available in this container"
done
if [[ "$RETAG" == "1" && "$APPLY" == "1" ]]; then
  command -v cassini >/dev/null 2>&1 \
    || fail_usage "cassini is not available in this container, so the merged recordings cannot be re-tagged. Pass --no-retag to write only the catalog — but a catalog-only merge is reverted by the next republish."
fi

# Checked by hand rather than with "${VAR:?msg}": that form aborts with status 1
# under `set -e`, and 1 is the code reserved for "failed AFTER writing".
for var in NEXTCLOUD_URL APP_SECRET APP_ID APP_VERSION; do
  if [[ -z "${!var:-}" ]]; then
    fail_usage "$var is not set — this must run inside the Cassini app container, where AppAPI injects it"
  fi
done

BASE="${NEXTCLOUD_URL%/}"
AUTH="$(printf '%s' "$OWNER:$APP_SECRET" | base64 | tr -d '\n')"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# The auth header goes in a 0600 config file, never on curl's argv: APP_SECRET
# authorises acting as any Nextcloud user, and an argv is readable from /proc.
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

# dav runs one WebDAV request as the recordings owner, printing the HTTP status
# and leaving the body in $3. --fail is deliberately not used: it collapses every
# error status into exit 22, and the status is what the callers branch on.
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
  200) ;;
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

UPDATED="$WORK/catalog.updated.json"
REPORT="$WORK/report.txt"
MOVED_IDS="$WORK/moved-ids.txt"
set +e
# shellcheck disable=SC2016 # the ${...} below are JS template literals, evaluated by node, not by the shell
node -e '
  const fs = require("fs");
  const [, catalogPath, outPath, reportPath, movedPath, from, to, wantedName] = process.argv;
  let catalog;
  try {
    catalog = JSON.parse(fs.readFileSync(catalogPath, "utf8"));
  } catch (err) {
    process.stderr.write(`the catalog is not readable JSON: ${err.message}\n`);
    process.exit(4);
  }
  const meetings = Array.isArray(catalog.meetings) ? catalog.meetings : [];

  // The merged room takes one display name, so the result reads as one room
  // rather than as one id with two names. An explicit --name wins; otherwise
  // the TARGET room supplies it, since --to is the identity being kept.
  let mergedName = (wantedName || "").trim();
  if (!mergedName) {
    for (const entry of meetings) {
      if (entry && entry.roomId === to && typeof entry.roomName === "string" && entry.roomName.trim() !== "") {
        mergedName = entry.roomName.trim();
        break;
      }
    }
  }

  const lines = [];
  // The ids the guard and the re-tagger need, as data. $REPORT is prose that
  // embeds user-controlled room names and is not a safe parse source.
  const movedIds = [];
  let moved = 0;
  let renamed = 0;
  catalog.meetings = meetings.map((entry) => {
    if (!entry || typeof entry.id !== "string") return entry;
    const wasName = typeof entry.roomName === "string" ? entry.roomName : "";
    if (entry.roomId === from) {
      const updated = { ...entry, roomId: to };
      if (mergedName) updated.roomName = mergedName;
      moved += 1;
      movedIds.push(entry.id);
      lines.push(`  ${entry.id}: ${from} -> ${to}` + (mergedName && mergedName !== wasName ? ` (name ${JSON.stringify(wasName)} -> ${JSON.stringify(mergedName)})` : ""));
      return updated;
    }
    // Meetings ALREADY in the target room are renamed too. The merged room has
    // to end up with exactly one display name — renaming only the arrivals
    // would leave one room id reading under two names in every listing, which
    // is the confusion this command exists to remove.
    if (entry.roomId === to && mergedName && wasName !== mergedName) {
      renamed += 1;
      lines.push(`  ${entry.id}: stays in ${to} (name ${JSON.stringify(wasName)} -> ${JSON.stringify(mergedName)})`);
      return { ...entry, roomName: mergedName };
    }
    return entry;
  });

  fs.writeFileSync(reportPath, lines.join("\n") + (lines.length ? "\n" : ""));
  fs.writeFileSync(movedPath, movedIds.join("\n") + (movedIds.length ? "\n" : ""));
  // Two-space indent and a trailing newline, matching every other writer of
  // this file, so a hand diff against an exported site stays readable.
  fs.writeFileSync(outPath, JSON.stringify(catalog, null, 2) + "\n");
  process.stderr.write(`${moved} moved, ${renamed} already-present meeting(s) renamed, merged name ${JSON.stringify(mergedName || "")}\n`);
  process.exit(moved === 0 ? 3 : 0);
' "$CATALOG" "$UPDATED" "$REPORT" "$MOVED_IDS" "$FROM" "$TO" "$NAME"
merge_status=$?
set -e

case "$merge_status" in
  0) ;;
  3)
    log "no meeting carries the room id $FROM; nothing to reattribute"
    exit 3
    ;;
  *) fail_before "could not rewrite the catalog" ;;
esac

# Print every affected meeting, not just a count. This asserts an identity the
# data does not support, and the list is the only chance to notice it is wrong
# before it stops being recoverable.
while IFS= read -r line; do
  [[ -n "$line" ]] && log "$line"
done < "$REPORT"

moved="$(grep -c -- "-> $TO$\|-> $TO " "$REPORT" | tr -d ' ')"
log "$moved meeting(s) would move from $FROM to $TO"
if grep -q "stays in $TO (name" "$REPORT" 2>/dev/null; then
  # The merged room takes one display name, including on meetings already in the
  # target — but a NAME is not this tool's to keep. Since D-640 the operator
  # stamps it from the job's Talk binding on every publish, and the backfill
  # restores it from the same source. So the merged label holds until one of
  # those runs for a meeting that has a job row.
  log "note: the merged room's display name is not durable for any meeting the"
  log "  operator has a job row for — publishing or backfilling one restores the"
  log "  name recorded against its job. The room ID is what this merge makes stick."
fi

# ---------------------------------------------------------------------------
# The lineage guard. Runs in a dry run too — see the header.
# ---------------------------------------------------------------------------

if [[ "$USE_JOBS_DB" == "1" ]]; then
  if [[ -z "$JOBS_DB" ]]; then
    JOBS_DB="${CASSINI_OPERATOR_DB_PATH:-}"
    if [[ -n "${APP_PERSISTENT_STORAGE:-}" && ( -z "$JOBS_DB" || "$JOBS_DB" == "$JOBS_DB_IMAGE_DEFAULT" ) ]]; then
      JOBS_DB="${APP_PERSISTENT_STORAGE%/}/$JOBS_DB_PERSIST_REL"
    fi
    JOBS_DB="${JOBS_DB:-$JOBS_DB_IMAGE_DEFAULT}"
  fi

  if [[ ! -f "$JOBS_DB" ]]; then
    # No database is not the same as no lineage, but it is the same OUTCOME: an
    # installation with no job history has nothing to contradict the merge, and
    # that is exactly the population this command exists for.
    log "no operator job database at $JOBS_DB — nothing here can contradict this merge."
  else
    CONTRADICTS="$WORK/contradicts.txt"
    # node:sqlite is unflagged from Node 22.13 and flagged before it; which one
    # the image has depends on when it was built. Run, and retry once with the
    # flag on the single error it fixes.
    # stderr is captured because the FIRST attempt's module error is a false
    # alarm whenever the retry is the one that works — and forwarded on every
    # failure path, including the retry's. Without that, the whole population
    # the retry exists for (a flagged Node, 22.5 to 22.12) reports every guard
    # failure with no diagnostic at all, under a message asserting a cause that
    # may not be the real one.
    run_guard() {
      if node -e "$1" "$JOBS_DB" "$MOVED_IDS" "$TO" 2>"$WORK/guard.err"; then
        return 0
      fi
      if grep -q "node:sqlite" "$WORK/guard.err" 2>/dev/null; then
        : > "$WORK/guard.err"
        if node --experimental-sqlite -e "$1" "$JOBS_DB" "$MOVED_IDS" "$TO" 2>"$WORK/guard.err"; then
          return 0
        fi
      fi
      cat "$WORK/guard.err" >&2
      return 1
    }
    # shellcheck disable=SC2016 # the ${...} below are JS template literals, evaluated by node, not by the shell
    run_guard '
      const fs = require("fs");
      const crypto = require("crypto");
      const { DatabaseSync } = require("node:sqlite");
      const [, dbPath, movedPath, to] = process.argv;
      const wanted = new Set(
        fs.readFileSync(movedPath, "utf8").split("\n").map((l) => l.trim()).filter(Boolean),
      );
      // The catalog id may carry an STT-variant suffix while the job it came
      // from is the bare ULID. Mirrors stripVariantSuffix in
      // export-static-meetings.mjs.
      const stripVariant = (id) => id.replace(/--stt-[A-Za-z0-9._-]+$/, "");
      // One job can have produced several catalog entries — an STT-variant
      // export publishes <ulid>--stt-<model> beside <ulid>, and both strip to
      // the same job id. Keyed to a LIST, because a map of one would name and
      // count only the last of them and under-report the contradiction.
      const byJob = new Map();
      for (const id of wanted) {
        const job = stripVariant(id);
        if (!byJob.has(job)) byJob.set(job, []);
        byJob.get(job).push(id);
      }

      const db = new DatabaseSync(dbPath, { readOnly: true });
      let rows;
      try {
        rows = db.prepare("SELECT id, talk_binding FROM jobs WHERE talk_binding IS NOT NULL").all();
      } finally {
        db.close();
      }
      const pepper = Buffer.from(process.env.CASSINI_ROOM_ID_PEPPER || "", "utf8");
      const lines = [];
      for (const row of rows) {
        const meetingIds = byJob.get(String(row.id));
        if (!meetingIds) continue;
        let binding;
        try { binding = JSON.parse(row.talk_binding); } catch { continue; }
        const token = typeof binding?.room_token === "string" ? binding.room_token.trim() : "";
        if (token === "") continue;
        const mac = crypto.createHmac("sha256", pepper);
        mac.update("cassini.room.token.v1\u0000", "utf8");
        mac.update(token, "utf8");
        const derived = "rm_" + mac.digest("hex").slice(0, 16);
        // Only a CONTRADICTION is a reason to refuse. When the recorded room
        // derives to exactly the merge target, the operator is asking for the
        // thing the data already says — which is the ordinary shape of this
        // upgrade: an entry left carrying a stale name-derived id, being moved
        // onto the token-derived one. Refusing that would block the correct
        // merge, and would do it with a message reading "derives to X, not X".
        if (derived === to) continue;
        // Derived ids only. The token is a join capability and must not reach a
        // log, a file or an argv.
        for (const meetingId of meetingIds) {
          lines.push(`  ${meetingId}: its recorded room derives to ${derived}, not ${to}`);
        }
      }
      process.stdout.write(lines.length ? lines.join("\n") + "\n" : "");
    ' > "$CONTRADICTS" || fail_before "could not read the operator job database at $JOBS_DB.
This is an outage or a permissions problem, and a guard that degrades silently is
not a guard — nothing has been written. Fix it and run this again, or pass
--no-jobs-db to merge without checking the recorded lineage."

    if [[ -s "$CONTRADICTS" ]]; then
      contradicting="$(wc -l < "$CONTRADICTS" | tr -d ' ')"
      while IFS= read -r line; do
        [[ -n "$line" ]] && log "$line"
      done < "$CONTRADICTS"
      if [[ "$FORCE" != "1" ]]; then
        fail_refused "refused: $contradicting of the $moved meeting(s) this would move have a room recorded in the operator.
Those recordings can track their lineage to the conversation that produced them,
and moving them would leave that lineage and their published room permanently
disagreeing. Nothing has been written.

For those meetings the right tool is the backfill, which derives the real id
from the recorded token rather than asserting one:

  ./scripts/backfill-catalog-rooms.sh --apply

Re-run with --force if you are certain the recorded binding is the wrong one."
      fi
      log "overridden by --force: $contradicting meeting(s) with a recorded room binding will be moved anyway."
    fi
  fi
fi

if [[ "$APPLY" != "1" ]]; then
  log "dry run: nothing was written. Re-run with --apply to reattribute these $moved meeting(s)."
  exit 0
fi

# ---------------------------------------------------------------------------
# Re-tag each moved recording, before the catalog is written.
# ---------------------------------------------------------------------------
#
# This order is deliberate: an interrupted run leaves the catalog untouched, so
# a re-run selects the same meetings and finishes the job. The reverse would
# leave the catalog claiming a room the files do not carry, and the next
# republish would revert exactly those entries.

if [[ "$RETAG" == "1" ]]; then
  retagged=0
  while IFS= read -r meeting_id; do
    [[ -n "$meeting_id" ]] || continue
    opus="$(node -e 'process.stdout.write(encodeURIComponent(process.argv[1] + ".opus"))' "$meeting_id")"
    target="$WORK/meeting.opus"
    status="$(dav GET "$ROOT/meetings/$opus" "$target")" || status="000"
    case "$status" in
      200) ;;
      404)
        log "  $meeting_id: no file at $ROOT/meetings/$opus — the catalog entry will move, the artifact cannot"
        continue
        ;;
      *)
        fail_before "reading $ROOT/meetings/$opus returned HTTP $status.
This is an outage or an auth failure — the catalog has not been written, so fix
it and run this again."
        ;;
    esac

    retagged_path="$WORK/retagged.opus"
    rm -f "$retagged_path"
    if ! cassini retag "$target" --out "$retagged_path" --room-id "$TO" >/dev/null 2>"$WORK/retag.err"; then
      fail_before "re-tagging $meeting_id failed:
$(cat "$WORK/retag.err")
The catalog has not been written and no artifact was uploaded, so re-running is
safe. Pass --no-retag to update only the catalog."
    fi
    # Refuse to CREATE. A PUT onto a path that is not there mints a fresh fileid
    # with no ACL rows, and a leaf under meetings/ with no rule of its own
    # inherits the container grant to the virtual everyone group.
    status="$(dav PUT "$ROOT/meetings/$opus" "$WORK/put-opus.out" -H "Content-Type: audio/ogg" --data-binary "@$retagged_path")" \
      || fail_before "uploading the re-tagged $meeting_id could not be sent.
The file permissions are unaffected — an overwrite keeps the fileid they hang
off — but the recording may be truncated. Check that $meeting_id still plays,
then run this again."
    case "$status" in
      204) retagged=$((retagged + 1)) ;;
      201)
        fail_after "uploading the re-tagged $meeting_id answered HTTP 201, meaning the file was CREATED rather than overwritten.
A newly created file under $ROOT/meetings/ has no permissions of its own and
inherits the folder grant to every signed-in account. Check the permissions on
$ROOT/meetings/$opus in the Files app before doing anything else."
        ;;
      *)
        fail_before "uploading the re-tagged $meeting_id returned HTTP $status (an overwrite answers 204).
The catalog has not been written, so re-running is safe."
        ;;
    esac
  done < "$MOVED_IDS"
  log "$retagged recording(s) re-tagged so the merge survives a republish"
fi

log "writing $ROOT/catalog.json"
# A transport failure here is NOT "nothing was written": curl can fail after
# Nextcloud has committed the write, leaving the recreated catalog.json with the
# container's inherited grant and no PROPPATCH to remove it.
status="$(dav PUT "$ROOT/catalog.json" "$WORK/put.out" -H "Content-Type: application/json" --data-binary "@$UPDATED")" \
  || fail_after "the catalog upload could not be sent, and it may still have been applied"
# 2xx only: a 3xx means curl, invoked without -L, did not store the body at the
# target, so treating a redirect as success would report a write that never was.
case "$status" in
  20*) ;;
  *) fail_before "writing the catalog returned HTTP $status (only a 2xx means it was stored)" ;;
esac

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
# A PROPPATCH answers 207, and a REJECTED property lands in the per-property
# status inside it — the 207 alone only says the request was understood (D-585).
if grep -qE 'HTTP/1\.[01] [45][0-9][0-9]' "$WORK/acl.out"; then
  fail_after "Nextcloud rejected the catalog permissions:
$(cat "$WORK/acl.out")
The catalog is written but may be readable by every signed-in account. Check
$ROOT/catalog.json in the Files app."
fi

log "done: $moved meeting(s) now belong to $TO"
if [[ "$RETAG" != "1" ]]; then
  log "--no-retag was set, so the published files still name the old room. The next republish of these meetings will revert them."
fi
exit 0
