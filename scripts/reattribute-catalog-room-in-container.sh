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
#   PUT       the catalog back                    (only with --apply)
#   PROPPATCH the owner-only ACL back             (only with --apply)
#
# The PROPPATCH is not optional: a PUT that recreates catalog.json inherits the
# containing folder's grant to the virtual "everyone" group, which would publish
# the unfiltered archive index to every signed-in account.
#
# Unlike the backfill, this reads no recordings — the change is entirely inside
# the catalog, so nothing is downloaded and the run is quick.
#
# Exit codes, which the wrapper turns into instructions. The line they draw is
# whether the catalog was WRITTEN:
#   0  done
#   1  failed AFTER writing    — the catalog may be updated without its ACL
#   2  wrong usage/environment — never started
#   3  nothing to do           — no entry carries the --from id
#   4  failed BEFORE writing   — nothing written, retry is safe

set -euo pipefail

APPLY=0
FROM=""
TO=""
NAME=""

OWNER="cassini"
ROOT="Cassini/Recordings"

log() { echo "reattribute-catalog-room: $*"; }
fail_before() { echo "error: $*" >&2; exit 4; }
fail_after() { echo "error: $*" >&2; exit 1; }
fail_usage() { echo "error: $*" >&2; exit 2; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --apply) APPLY=1; shift ;;
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

for tool in curl node base64; do
  command -v "$tool" >/dev/null 2>&1 || fail_usage "$tool is not available in this container"
done

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
set +e
# shellcheck disable=SC2016 # the ${...} below are JS template literals, evaluated by node, not by the shell
node -e '
  const fs = require("fs");
  const [, catalogPath, outPath, reportPath, from, to, wantedName] = process.argv;
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
  let moved = 0;
  let renamed = 0;
  catalog.meetings = meetings.map((entry) => {
    if (!entry || typeof entry.id !== "string") return entry;
    const wasName = typeof entry.roomName === "string" ? entry.roomName : "";
    if (entry.roomId === from) {
      const updated = { ...entry, roomId: to };
      if (mergedName) updated.roomName = mergedName;
      moved += 1;
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
  // Two-space indent and a trailing newline, matching every other writer of
  // this file, so a hand diff against an exported site stays readable.
  fs.writeFileSync(outPath, JSON.stringify(catalog, null, 2) + "\n");
  process.stderr.write(`${moved} moved, ${renamed} already-present meeting(s) renamed, merged name ${JSON.stringify(mergedName || "")}\n`);
  process.exit(moved === 0 ? 3 : 0);
' "$CATALOG" "$UPDATED" "$REPORT" "$FROM" "$TO" "$NAME"
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

if [[ "$APPLY" != "1" ]]; then
  log "dry run: nothing was written. Re-run with --apply to reattribute these $moved meeting(s)."
  exit 0
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
exit 0
