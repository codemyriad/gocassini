#!/usr/bin/env bash
# test-reattribute-catalog-room.sh — offline regressions for the room merge.
#
# This command asserts an identity the data does not support, and is not
# reversible except by running it the other way — so the things worth pinning
# are the guards, not the happy path: that it is a dry run by default, that it
# names every meeting it would move, that it refuses a no-op merge rather than
# reporting success, and that a write is always followed by the ACL that keeps
# the archive index private.
#
# `docker` is stubbed through the DOCKER hook for the wrapper; `curl` is stubbed
# on PATH for the payload. No daemon, no network, no Nextcloud.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WRAPPER="$SCRIPT_DIR/reattribute-catalog-room.sh"
PAYLOAD="$SCRIPT_DIR/reattribute-catalog-room-in-container.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

failures=0
fail() { echo "  FAIL: $*" >&2; failures=$((failures + 1)); }
ok() { echo "  ok: $*"; }

check() {
  local what="$1" file="$2" pattern="$3"
  if tr '\n' ' ' <"$file" | grep -q -- "$pattern"; then
    ok "$what"
  else
    fail "$what — expected /$pattern/ in: $(tr '\n' ' ' <"$file")"
  fi
}

# refute <description> <file> <pattern>
refute() {
  local what="$1" file="$2" pattern="$3"
  if tr '\n' ' ' <"$file" | grep -q -- "$pattern"; then
    fail "$what — did not expect /$pattern/ in: $(tr '\n' ' ' <"$file")"
  else
    ok "$what"
  fi
}

echo "test-reattribute-catalog-room.sh"

FROM_ID="rm_11bb22cc33dd44ee"
TO_ID="rm_9f2a1c3d4e5b6a70"

# ---------------------------------------------------------------------------
# the wrapper
# ---------------------------------------------------------------------------

make_docker_stub() {
  local exec_status="$1" running="${2:-true}"
  rm -f "$WORK/exec-argv" "$WORK/exec-stdin"
  cat >"$WORK/docker" <<EOF
#!/usr/bin/env bash
case "\$1" in
  inspect)
    if [[ "\$2" == "-f" ]]; then echo "$running"; exit 0; fi
    [[ "\$2" == "missing-container" ]] && exit 1
    exit 0
    ;;
  exec)
    shift
    printf '%s\n' "\$@" >"$WORK/exec-argv"
    cat >"$WORK/exec-stdin"
    exit $exec_status
    ;;
esac
exit 0
EOF
  chmod +x "$WORK/docker"
}

run_wrapper() {
  DOCKER="$WORK/docker" "$WRAPPER" "$@" >"$WORK/stdout" 2>"$WORK/stderr"
}

# --- both ids are required, and they may not be the same ---
make_docker_stub 0
run_wrapper --to "$TO_ID" && true
status=$?
if [[ $status -eq 2 ]]; then ok "a missing --from exits 2"; else fail "a missing --from should exit 2, got $status"; fi

run_wrapper --from "$FROM_ID" && true
status=$?
if [[ $status -eq 2 ]]; then ok "a missing --to exits 2"; else fail "a missing --to should exit 2, got $status"; fi

# Merging a room into itself means one of the two ids is not the one that was
# meant. Reporting "0 entries changed" would look like the merge was already
# done, which is the wrong conclusion to leave someone with.
run_wrapper --from "$TO_ID" --to "$TO_ID" && true
status=$?
if [[ $status -eq 2 ]]; then ok "merging a room into itself is refused"; else fail "a self-merge should exit 2, got $status"; fi
check "the self-merge error says why" "$WORK/stderr" "are the same id"

# --- dry run is the default ---
make_docker_stub 0
run_wrapper --from "$FROM_ID" --to "$TO_ID" || fail "a successful run should exit 0"
if grep -qx -- "--apply" "$WORK/exec-argv"; then
  fail "a bare invocation must not pass --apply; this rewrites a live archive"
else
  ok "dry run is the default"
fi
for expected in --from "$FROM_ID" --to "$TO_ID"; do
  grep -qx -- "$expected" "$WORK/exec-argv" || fail "expected $expected to reach the container"
done
ok "the ids are passed through"

make_docker_stub 0
run_wrapper --from "$FROM_ID" --to "$TO_ID" --name "Weekly Sync" --apply || fail "--apply should exit 0"
for expected in --apply --name "Weekly Sync"; do
  grep -qx -- "$expected" "$WORK/exec-argv" || fail "expected $expected to reach the container"
done
ok "--name and --apply are passed through"

# --- exit codes become instructions ---
make_docker_stub 3
run_wrapper --from "$FROM_ID" --to "$TO_ID" && true
status=$?
if [[ $status -eq 3 ]]; then ok "exit 3 is relayed"; else fail "exit 3 should be relayed, got $status"; fi
check "exit 3 suggests the ids may be the wrong way round" "$WORK/stderr" "wrong way round"

make_docker_stub 1
run_wrapper --from "$FROM_ID" --to "$TO_ID" && true
status=$?
if [[ $status -eq 1 ]]; then ok "exit 1 is relayed"; else fail "exit 1 should be relayed, got $status"; fi
check "exit 1 warns that the catalog may be exposed" "$WORK/stderr" "readable by every signed-in account"
check "exit 1 also covers the created-recording case" "$WORK/stderr" "CREATED rather than overwritten"

# Exit 5 needs an arm of its own. Without one it falls to the catch-all, which
# says "whether anything was written is unknown" — the single most wrong thing
# that could be said about a refusal, which by construction wrote nothing.
make_docker_stub 5
run_wrapper --from "$FROM_ID" --to "$TO_ID" && true
status=$?
if [[ $status -eq 5 ]]; then ok "exit 5 is relayed"; else fail "exit 5 should be relayed, got $status"; fi
check "exit 5 says nothing was written" "$WORK/stderr" "Nothing was written"
check "exit 5 names the backfill as the right tool" "$WORK/stderr" "backfill-catalog-rooms.sh --apply"
check "exit 5 names the override" "$WORK/stderr" -- "--force"
refute "exit 5 does not claim the outcome is unknown" "$WORK/stderr" "is unknown"

# --- the new flags reach the container, and are not inferred ---
make_docker_stub 0
run_wrapper --from "$FROM_ID" --to "$TO_ID" --force --no-retag --no-jobs-db \
  || fail "the new flags should exit 0"
for expected in --force --no-retag --no-jobs-db; do
  if ! grep -qx -- "$expected" "$WORK/exec-argv"; then
    fail "expected $expected to reach the container, got: $(tr '\n' ' ' <"$WORK/exec-argv")"
  fi
done
ok "--force, --no-retag and --no-jobs-db are passed through"

make_docker_stub 0
run_wrapper --from "$FROM_ID" --to "$TO_ID" || fail "a bare invocation should exit 0"
for forbidden in --force --no-retag --no-jobs-db; do
  if grep -qx -- "$forbidden" "$WORK/exec-argv"; then
    fail "a bare invocation must not pass $forbidden"
  fi
done
ok "the guard and the re-tag are never inferred"

# ---------------------------------------------------------------------------
# the payload
# ---------------------------------------------------------------------------

STUBS="$WORK/stubs"
mkdir -p "$STUBS"

write_fake_catalog() { cat >"$WORK/catalog.json"; }

# The stub branches on the URL, not just the method: since D-640 this command
# reads and writes recordings as well as the catalog, and a method-only stub
# would answer a recording GET with the catalog.
cat >"$STUBS/curl" <<EOF
#!/usr/bin/env bash
set -euo pipefail
out=""; method="GET"; data=""; url=""
while [[ \$# -gt 0 ]]; do
  case "\$1" in
    -o) out="\$2"; shift 2 ;;
    -X) method="\$2"; shift 2 ;;
    -H) shift 2 ;;
    --config) shift 2 ;;
    --data-binary) data="\${2#@}"; shift 2 ;;
    -sS|-w) shift ;;
    '%{http_code}') shift ;;
    *) url="\$1"; shift ;;
  esac
done
: >"\${out:-/dev/null}"
case "\$method:\$url" in
  GET:*catalog.json) cat "$WORK/catalog.json" >"\$out"; echo 200 ;;
  GET:*meetings/*)
    id="\${url##*/meetings/}"; id="\${id%.opus}"
    printf '%s' "\$id" >"\$out"; echo 200 ;;
  PUT:*meetings/*)
    id="\${url##*/meetings/}"; id="\${id%.opus}"
    printf '%s\n' "\$id" >>"$WORK/opus-puts.txt"
    echo "\${OPUS_PUT_STATUS:-204}" ;;
  PROPPATCH:*meetings/*)
    # Never legitimate: writing an ACL onto a recording would replace the
    # meeting's real audience with a guess. Recorded so a test can assert it.
    printf '%s\n' "\$url" >>"$WORK/opus-proppatch.txt"
    echo 207 ;;
  PUT:*catalog.json) cp "\$data" "$WORK/put-body.json"; echo 204 ;;
  PROPPATCH:*catalog.json)
    cp "\$data" "$WORK/proppatch-body.xml"
    printf '<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:propstat><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>' >"\$out"
    echo 207 ;;
  *) echo 500 ;;
esac
EOF

# The cassini stub stands in for `cassini retag`: it records the room id written
# into each file and copies the input through so the PUT has a body.
cat >"$STUBS/cassini" <<EOF
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "\$*" >>"$WORK/retag-argv"
[[ "\$1" == "retag" ]] || { echo "unexpected cassini subcommand: \$1" >&2; exit 2; }
in="\$2"; out=""
shift 2
while [[ \$# -gt 0 ]]; do
  case "\$1" in
    --out) out="\$2"; shift 2 ;;
    *) shift ;;
  esac
done
cp "\$in" "\$out"
EOF
chmod +x "$STUBS/curl" "$STUBS/cassini"
cp "$STUBS/curl" "$STUBS/curl.real"

# node:sqlite is unflagged from Node 22.13 and flagged before it.
node_sqlite() {
  if node -e 'require("node:sqlite")' >/dev/null 2>&1; then
    node "$@"
  else
    node --experimental-sqlite "$@"
  fi
}

if ! node -e 'require("node:sqlite")' >/dev/null 2>&1 \
   && ! node --experimental-sqlite -e 'require("node:sqlite")' >/dev/null 2>&1; then
  echo "  SKIP: this node has no node:sqlite; the lineage-guard half cannot be exercised" >&2
  echo "PASS (partial)"
  exit 0
fi

JOBS_DB="$WORK/jobs.sqlite3"

# write_jobs_db builds a REAL operator job database from a TSV of
# <job id>\t<room token>. The talk_binding column holds the exact JSON the
# operator writes (talkJobBinding in talk_backend.go), because a stub of it would
# agree with whatever the guard happened to expect.
write_jobs_db() {
  rm -f "$JOBS_DB"
  node_sqlite -e '
    const fs = require("fs");
    const { DatabaseSync } = require("node:sqlite");
    const [, dbPath, rowsPath] = process.argv;
    const db = new DatabaseSync(dbPath);
    db.exec("CREATE TABLE jobs (id TEXT PRIMARY KEY NOT NULL, talk_binding TEXT)");
    const insert = db.prepare("INSERT INTO jobs (id, talk_binding) VALUES (?, ?)");
    for (const line of fs.readFileSync(rowsPath, "utf8").split("\n")) {
      if (line.trim() === "") continue;
      const [id, token] = line.split("\t");
      insert.run(id, JSON.stringify({
        backend_url: "https://cloud.test/",
        room_token: token,
        owner: "alice",
        room_public: false,
      }));
    }
    db.close();
  ' "$JOBS_DB" "$1"
}

run_payload() {
  rm -f "$WORK/put-body.json" "$WORK/proppatch-body.xml" \
        "$WORK/opus-puts.txt" "$WORK/opus-proppatch.txt" "$WORK/retag-argv"
  env PATH="$STUBS:$PATH" \
    NEXTCLOUD_URL="https://cloud.test" APP_SECRET="s3cret" APP_ID="gocassini" APP_VERSION="0.2.0" \
    CASSINI_ROOM_ID_PEPPER="test-pepper" \
    OPUS_PUT_STATUS="${OPUS_PUT_STATUS:-204}" \
    bash "$PAYLOAD" --jobs-db "$JOBS_DB" "$@" >"$WORK/stdout" 2>"$WORK/stderr"
}

# By default nothing in the fixture has a recorded lineage: these are exactly the
# meetings this command exists for. Individual tests add rows.
: >"$WORK/jobs.tsv"
write_jobs_db "$WORK/jobs.tsv"

write_fake_catalog <<EOF
{
  "version": "cassini.viewer.catalog.v1",
  "meetings": [
    {"id": "NEW1", "title": "Weekly Sync", "dateLabel": "2026-08-11 10:32",
     "audioPath": "./meetings/NEW1.opus", "speakerCount": 3,
     "roomId": "$TO_ID", "roomName": "Weekly Sync"},
    {"id": "OLD1", "title": "Weekly Sync", "dateLabel": "2026-05-02 10:30",
     "audioPath": "./meetings/OLD1.opus",
     "roomId": "$FROM_ID", "roomName": "weekly sync"},
    {"id": "OLD2", "title": "Weekly Sync", "dateLabel": "2026-04-25 10:30",
     "audioPath": "./meetings/OLD2.opus",
     "roomId": "$FROM_ID", "roomName": "weekly sync"},
    {"id": "OTHER", "title": "Retro", "dateLabel": "2026-04-01 10:30",
     "audioPath": "./meetings/OTHER.opus",
     "roomId": "rm_ffffffffffffffff", "roomName": "Retro"}
  ]
}
EOF

# --- dry run names every meeting it would move, and writes nothing ---
run_payload --from "$FROM_ID" --to "$TO_ID" || fail "a dry run should exit 0: $(cat "$WORK/stderr")"
if [[ -f "$WORK/put-body.json" ]]; then
  fail "a dry run must not PUT the catalog"
else
  ok "a dry run writes nothing"
fi
# The per-meeting list is the only chance to notice a wrong merge before it
# stops being recoverable, so a bare count is not enough.
check "the dry run names each meeting it would move" "$WORK/stdout" "OLD1: $FROM_ID -> $TO_ID"
check "and the second one" "$WORK/stdout" "OLD2: $FROM_ID -> $TO_ID"
check "it reports the total" "$WORK/stdout" "2 meeting(s) would move"
check "it says nothing was written" "$WORK/stdout" "dry run: nothing was written"

# --- apply rewrites only the matching entries, and adopts the target's name ---
run_payload --from "$FROM_ID" --to "$TO_ID" --apply || fail "--apply should exit 0: $(cat "$WORK/stderr")"
[[ -f "$WORK/put-body.json" ]] || fail "--apply should PUT the catalog"

# shellcheck disable=SC2016 # the ${...} below are JS template literals, evaluated by node, not by the shell
if node -e '
  const fs = require("fs");
  const [, path, from, to] = process.argv;
  const catalog = JSON.parse(fs.readFileSync(path, "utf8"));
  const byId = Object.fromEntries(catalog.meetings.map((m) => [m.id, m]));
  const problems = [];
  if (catalog.version !== "cassini.viewer.catalog.v1") problems.push("the version must be carried through untouched");
  if (catalog.meetings.length !== 4) problems.push("every entry must survive the merge");
  for (const id of ["OLD1", "OLD2"]) {
    if (byId[id].roomId !== to) problems.push(`${id} was not reattributed`);
    // The merged room takes ONE name — the target the operator chose to keep —
    // or the room reads as one id with two names in every listing.
    if (byId[id].roomName !== "Weekly Sync") problems.push(`${id} kept its old room name`);
  }
  if (byId.NEW1.roomId !== to) problems.push("the target room must be untouched");
  if (byId.OTHER.roomId === to) problems.push("an unrelated room was swept into the merge");
  if (byId.NEW1.speakerCount !== 3) problems.push("unrelated fields must be preserved verbatim");
  if (problems.length) { console.error(problems.join("; ")); process.exit(1); }
' "$WORK/put-body.json" "$FROM_ID" "$TO_ID"; then
  ok "only the matching entries move, and they adopt the target room name"
else
  fail "the written catalog is wrong (see above)"
fi

if [[ "$(tail -c 1 "$WORK/put-body.json")" == "" ]] && grep -q '^  "version"' "$WORK/put-body.json"; then
  ok "the catalog keeps the two-space indent and trailing newline"
else
  fail "the catalog should be two-space indented and end with a newline"
fi

# A write that recreates catalog.json inherits the folder's grant to everyone,
# which would publish the unfiltered archive index. The ACL is not optional.
if [[ -f "$WORK/proppatch-body.xml" ]]; then
  check "--apply restores the owner-only permissions" "$WORK/proppatch-body.xml" \
    "<nc:acl-mapping-id>everyone</nc:acl-mapping-id><nc:acl-mask>31</nc:acl-mask><nc:acl-permissions>0</nc:acl-permissions>"
else
  fail "--apply must restore the owner-only ACL"
fi

# --- an explicit --name wins over the target's ---
run_payload --from "$FROM_ID" --to "$TO_ID" --name "Weekly Sync (renamed)" --apply \
  || fail "--name should exit 0: $(cat "$WORK/stderr")"
# shellcheck disable=SC2016 # the ${...} below are JS template literals, evaluated by node, not by the shell
if node -e '
  const fs = require("fs");
  const catalog = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
  const names = new Set(catalog.meetings.filter((m) => m.roomId === process.argv[2]).map((m) => m.roomName));
  if (names.size !== 1 || !names.has("Weekly Sync (renamed)")) {
    console.error("merged room names: " + JSON.stringify([...names]));
    process.exit(1);
  }
' "$WORK/put-body.json" "$TO_ID"; then
  ok "--name renames every meeting in the merged room, including the target's own"
else
  fail "--name did not produce one name for the merged room"
fi

# --- an id nothing carries is exit 3, not a silent success ---
run_payload --from "rm_00000000deadbeef" --to "$TO_ID" && true
status=$?
if [[ $status -eq 3 ]]; then ok "an unmatched --from exits 3"; else fail "an unmatched --from should exit 3, got $status"; fi
if [[ -f "$WORK/put-body.json" ]]; then
  fail "an unmatched --from must not write the catalog"
else
  ok "an unmatched --from writes nothing"
fi

# ---------------------------------------------------------------------------
# The lineage guard (D-640)
# ---------------------------------------------------------------------------

# room_id_for_token derives what the guard must derive, written out here rather
# than shared so a change to either side fails this test instead of agreeing
# with itself.
room_id_for_token() {
  node -e '
    const crypto = require("crypto");
    const mac = crypto.createHmac("sha256", Buffer.from("test-pepper", "utf8"));
    mac.update("cassini.room.token.v1\u0000", "utf8");
    mac.update(process.argv[1].trim(), "utf8");
    process.stdout.write("rm_" + mac.digest("hex").slice(0, 16));
  ' "$1"
}

# OLD1 was recorded by this installation, in a conversation whose token derives
# to something that is NOT the merge target. OLD2 was not — it has no job row at
# all, which is the population this command exists for.
printf 'OLD1\ttok-old1\n' >"$WORK/jobs.tsv"
write_jobs_db "$WORK/jobs.tsv"
OLD1_RECORDED_ID="$(room_id_for_token "tok-old1")"

# --- the guard refuses, and refuses in a DRY RUN ---
# The dry run is the review surface this command's whole design rests on. A
# guard that only fired on --apply would teach an operator that the dry run is a
# preview of something else.
run_payload --from "$FROM_ID" --to "$TO_ID" && true
status=$?
if [[ $status -eq 5 ]]; then
  ok "a recorded room binding refuses the merge, in a dry run"
else
  fail "a contradicting lineage should exit 5, got $status: $(cat "$WORK/stderr")"
fi
check "the refusal names the meeting and what its lineage derives to" "$WORK/stdout" \
  "OLD1: its recorded room derives to $OLD1_RECORDED_ID"
check "the refusal points at the backfill as the right tool" "$WORK/stderr" \
  "backfill-catalog-rooms.sh --apply"
if [[ -f "$WORK/put-body.json" || -f "$WORK/opus-puts.txt" ]]; then
  fail "a refusal must write nothing at all"
else
  ok "a refusal writes nothing"
fi

# The token is a join capability for a public conversation. The guard compares
# derived ids and must never print, log or write the token itself.
refute "the refusal never prints the raw token" "$WORK/stdout" "tok-old1"
refute "and not on stderr either" "$WORK/stderr" "tok-old1"

# --- it refuses with --apply too, before anything is written ---
run_payload --from "$FROM_ID" --to "$TO_ID" --apply && true
status=$?
if [[ $status -eq 5 ]]; then ok "the guard also refuses --apply"; else fail "--apply with a contradicting lineage should exit 5, got $status"; fi
if [[ -f "$WORK/put-body.json" ]]; then fail "a refused --apply must not write the catalog"; else ok "a refused --apply writes nothing"; fi

# --- --force overrides, and says so loudly ---
run_payload --from "$FROM_ID" --to "$TO_ID" --apply --force && true
status=$?
if [[ $status -eq 0 ]]; then ok "--force overrides the guard"; else fail "--force should exit 0, got $status: $(cat "$WORK/stderr")"; fi
check "--force records the override in the transcript" "$WORK/stdout" "overridden by --force"
[[ -f "$WORK/put-body.json" ]] || fail "--force should write the catalog"

# --- a meeting with no job row is never refused ---
: >"$WORK/jobs.tsv"
write_jobs_db "$WORK/jobs.tsv"
run_payload --from "$FROM_ID" --to "$TO_ID" && true
status=$?
if [[ $status -eq 0 ]]; then
  ok "meetings with no recorded lineage merge freely — the population this exists for"
else
  fail "a merge with no contradicting lineage should exit 0, got $status: $(cat "$WORK/stderr")"
fi

# --- an unreadable jobs database fails closed ---
# A guard that degrades silently is not a guard.
printf 'not a sqlite database at all' >"$WORK/broken.sqlite3"
env PATH="$STUBS:$PATH" \
  NEXTCLOUD_URL="https://cloud.test" APP_SECRET="s3cret" APP_ID="gocassini" APP_VERSION="0.2.0" \
  CASSINI_ROOM_ID_PEPPER="test-pepper" \
  bash "$PAYLOAD" --jobs-db "$WORK/broken.sqlite3" --from "$FROM_ID" --to "$TO_ID" --apply \
  >"$WORK/stdout" 2>"$WORK/stderr" && true
status=$?
if [[ $status -eq 4 ]]; then ok "an unreadable jobs database exits 4 rather than merging unchecked"; else fail "an unreadable jobs database should exit 4, got $status"; fi
if [[ -f "$WORK/put-body.json" ]]; then fail "a failed guard must write nothing"; else ok "a failed guard writes nothing"; fi

# --- --no-jobs-db skips the check explicitly ---
printf 'OLD1\ttok-old1\n' >"$WORK/jobs.tsv"
write_jobs_db "$WORK/jobs.tsv"
env PATH="$STUBS:$PATH" \
  NEXTCLOUD_URL="https://cloud.test" APP_SECRET="s3cret" APP_ID="gocassini" APP_VERSION="0.2.0" \
  CASSINI_ROOM_ID_PEPPER="test-pepper" \
  bash "$PAYLOAD" --no-jobs-db --from "$FROM_ID" --to "$TO_ID" >"$WORK/stdout" 2>"$WORK/stderr" && true
status=$?
if [[ $status -eq 0 ]]; then ok "--no-jobs-db skips the lineage check"; else fail "--no-jobs-db should exit 0, got $status: $(cat "$WORK/stderr")"; fi

# ---------------------------------------------------------------------------
# Re-tagging the moved recordings (D-640)
# ---------------------------------------------------------------------------

: >"$WORK/jobs.tsv"
write_jobs_db "$WORK/jobs.tsv"

run_payload --from "$FROM_ID" --to "$TO_ID" --apply || fail "--apply should exit 0: $(cat "$WORK/stderr")"
# Without this the exporter re-derives each entry's room from an untouched file
# on the next republish, and the merge is undone.
for id in OLD1 OLD2; do
  if grep -qx "$id" "$WORK/opus-puts.txt" 2>/dev/null; then
    ok "$id's recording was re-tagged and uploaded"
  else
    fail "$id should have been re-tagged: $(cat "$WORK/opus-puts.txt" 2>/dev/null)"
  fi
done
if grep -qx "NEW1" "$WORK/opus-puts.txt" 2>/dev/null; then
  fail "a meeting that did not move must not be re-tagged"
else
  ok "meetings that did not move are left alone"
fi
check "the re-tag writes the TARGET room id into the file" "$WORK/retag-argv" -- "--room-id $TO_ID"

# Never PROPPATCH a recording: an overwrite keeps the fileid its permissions
# hang off, so leaving the ACL alone is what keeps it correct.
if [[ -f "$WORK/opus-proppatch.txt" ]]; then
  fail "a recording's permissions must never be rewritten: $(cat "$WORK/opus-proppatch.txt")"
else
  ok "no recording's permissions were touched"
fi

# --- a 201 on a recording is a hard failure ---
# A newly created leaf under meetings/ has no rules of its own and inherits the
# container's grant to the virtual everyone group.
OPUS_PUT_STATUS=201 run_payload --from "$FROM_ID" --to "$TO_ID" --apply && true
status=$?
unset OPUS_PUT_STATUS
if [[ $status -eq 1 ]]; then ok "a created (201) recording exits 1"; else fail "a 201 on a recording upload should exit 1, got $status"; fi
check "the 201 failure explains the permissions consequence" "$WORK/stderr" \
  "inherits the folder grant to every signed-in account"
if [[ -f "$WORK/put-body.json" ]]; then
  fail "a failed re-tag must stop before the catalog is written"
else
  ok "a failed re-tag stops before the catalog is written"
fi

# --- --no-retag writes only the catalog, and says the merge is temporary ---
run_payload --from "$FROM_ID" --to "$TO_ID" --apply --no-retag \
  || fail "--no-retag should exit 0: $(cat "$WORK/stderr")"
[[ -f "$WORK/put-body.json" ]] || fail "--no-retag should still write the catalog"
if [[ -f "$WORK/opus-puts.txt" ]]; then
  fail "--no-retag must not upload any recording"
else
  ok "--no-retag leaves recordings untouched"
fi
check "--no-retag warns that the merge does not survive a republish" "$WORK/stdout" \
  "will revert them"

# --- --jobs-db and --no-jobs-db contradict each other ---
run_payload --from "$FROM_ID" --to "$TO_ID" --no-jobs-db && true
status=$?
if [[ $status -eq 2 ]]; then ok "--jobs-db with --no-jobs-db is a usage error"; else fail "contradictory jobs flags should exit 2, got $status"; fi

# --- a missing AppAPI environment is exit 2, not 1 ---
env PATH="$STUBS:$PATH" APP_SECRET="" NEXTCLOUD_URL="" APP_ID="" APP_VERSION="" \
  bash "$PAYLOAD" --from "$FROM_ID" --to "$TO_ID" >"$WORK/stdout" 2>"$WORK/stderr" && true
status=$?
if [[ $status -eq 2 ]]; then ok "a missing AppAPI environment exits 2"; else fail "a missing environment should exit 2, got $status"; fi

if [[ "$failures" -ne 0 ]]; then
  echo "FAILED: $failures check(s)" >&2
  exit 1
fi
echo "PASS"
