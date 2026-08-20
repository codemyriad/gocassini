#!/usr/bin/env bash
# test-backfill-catalog-rooms.sh — offline regressions for the room backfill.
#
# Two halves, tested separately because they fail in different ways:
#
#   backfill-catalog-rooms.sh              reaches the right container and turns
#                                          the payload's exit code into an
#                                          instruction. `docker` is stubbed
#                                          through the DOCKER hook.
#   backfill-catalog-rooms-in-container.sh reads rooms out of the operator's job
#                                          database and out of published files,
#                                          merges them into the catalog, and
#                                          re-tags the artifacts. `curl`,
#                                          `ffprobe` and `cassini` are stubbed on
#                                          PATH, so this needs no Nextcloud and
#                                          no media. The job database is REAL —
#                                          a SQLite file built with node:sqlite,
#                                          because the schema and the JSON shape
#                                          of talk_binding are exactly what a
#                                          stub would get wrong.
#
# Neither needs a daemon, a network, or a Nextcloud.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WRAPPER="$SCRIPT_DIR/backfill-catalog-rooms.sh"
PAYLOAD="$SCRIPT_DIR/backfill-catalog-rooms-in-container.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

failures=0
fail() { echo "  FAIL: $*" >&2; failures=$((failures + 1)); }
ok() { echo "  ok: $*"; }

# check <description> <file> <pattern>
# Greps a file whose text is wrapped across lines, so an assertion never fails
# merely because a heredoc broke the phrase it is looking for.
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

echo "test-backfill-catalog-rooms.sh"

# ---------------------------------------------------------------------------
# the wrapper
# ---------------------------------------------------------------------------

# make_docker_stub <exit-code-for-exec> [running]
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

# --- dry run is the default: --apply must never be inferred ---
make_docker_stub 0
if ! run_wrapper; then
  fail "a successful run should exit 0"
fi
if grep -qx -- "--apply" "$WORK/exec-argv"; then
  fail "a bare invocation must not pass --apply; this rewrites a live archive"
else
  ok "dry run is the default"
fi

# Re-tagging is on by default under --apply, so a bare invocation must NOT pass
# --no-retag either: a catalog-only backfill is exactly the non-durable outcome
# this whole change exists to stop being the default.
if grep -qx -- "--no-retag" "$WORK/exec-argv"; then
  fail "a bare invocation must not pass --no-retag; re-tagging is what makes the room survive a republish"
else
  ok "re-tagging is the default"
fi

# --- the payload is what gets streamed in, not re-derived ---
if ! grep -q "backfill-catalog-rooms-in-container" "$WORK/exec-stdin"; then
  fail "the payload script should be streamed into the container over stdin"
else
  ok "the payload reaches the container over stdin"
fi

# --- flags are passed through ---
make_docker_stub 0
run_wrapper --apply --limit 5 --no-retag --jobs-db /tmp/jobs.sqlite3 \
  || fail "--apply --limit --no-retag --jobs-db should exit 0"
for expected in --apply --limit 5 --no-retag --jobs-db /tmp/jobs.sqlite3; do
  if ! grep -qx -- "$expected" "$WORK/exec-argv"; then
    fail "expected $expected to reach the container, got: $(tr '\n' ' ' <"$WORK/exec-argv")"
  fi
done
ok "flags are passed through"

make_docker_stub 0
run_wrapper --no-jobs-db || fail "--no-jobs-db should exit 0"
if ! grep -qx -- "--no-jobs-db" "$WORK/exec-argv"; then
  fail "--no-jobs-db should reach the container"
else
  ok "--no-jobs-db is passed through"
fi

# --- the container name is honoured ---
make_docker_stub 0
CASSINI_CONTAINER=nc_app_other run_wrapper || fail "an explicit container should exit 0"
if ! grep -qx "nc_app_other" "$WORK/exec-argv"; then
  fail "CASSINI_CONTAINER should select the container, got: $(tr '\n' ' ' <"$WORK/exec-argv")"
else
  ok "CASSINI_CONTAINER selects the container"
fi

# --- a missing or stopped container fails before doing anything ---
make_docker_stub 0
if run_wrapper --container missing-container; then
  fail "a missing container should not exit 0"
elif ! grep -q "not found" "$WORK/stderr"; then
  fail "a missing container should say so: $(cat "$WORK/stderr")"
else
  ok "a missing container is refused"
fi

make_docker_stub 0 false
if run_wrapper; then
  fail "a stopped container should not exit 0"
elif ! grep -q "is not running" "$WORK/stderr"; then
  fail "a stopped container should say so: $(cat "$WORK/stderr")"
else
  ok "a stopped container is refused"
fi

# --- every preflight refusal is exit 2, never 1 ---
# 1 is reserved for "failed after writing". A wrapper that returned it for a
# typo would send an admin to inspect a live archive nothing touched.
make_docker_stub 0
run_wrapper --bogus-flag && true
status=$?
if [[ $status -eq 2 ]]; then ok "an unknown option exits 2"; else fail "an unknown option should exit 2, got $status"; fi
make_docker_stub 0
run_wrapper --container missing-container && true
status=$?
if [[ $status -eq 2 ]]; then ok "a missing container exits 2"; else fail "a missing container should exit 2, got $status"; fi
make_docker_stub 0 false
run_wrapper && true
status=$?
if [[ $status -eq 2 ]]; then ok "a stopped container exits 2"; else fail "a stopped container should exit 2, got $status"; fi

# --- exit codes become instructions, and only exit 1 mentions cleanup ---
# "nothing was written, retry is safe" and "something was written, check it"
# are opposite instructions; giving the wrong one sends an admin to inspect a
# live archive after a run that touched nothing.
make_docker_stub 3
run_wrapper && true
[[ $? -eq 3 ]] || fail "exit 3 should be relayed"
refute "exit 3 relays 'nothing to do' without alarm" "$WORK/stderr" "readable by every signed-in account"

make_docker_stub 4
run_wrapper && true
[[ $? -eq 4 ]] || fail "exit 4 should be relayed"
check "exit 4 says the catalog was not written" "$WORK/stderr" "exactly as it was"
# Re-tagging happens before the catalog write, so exit 4 has to admit some
# artifacts may already have been rewritten — while saying why that is harmless.
check "exit 4 admits artifacts may already be re-tagged, and why that is safe" "$WORK/stderr" \
  "never touches its permissions"

make_docker_stub 1
run_wrapper && true
[[ $? -eq 1 ]] || fail "exit 1 should be relayed"
check "exit 1 warns that the catalog may be exposed" "$WORK/stderr" "readable by every signed-in account"
check "exit 1 also covers the created-artifact case" "$WORK/stderr" "CREATED rather than overwritten"

make_docker_stub 137
run_wrapper && true
[[ $? -eq 137 ]] || fail "an unknown exit code should be relayed verbatim"
check "an unknown exit code is relayed and admitted" "$WORK/stderr" "is unknown"

# ---------------------------------------------------------------------------
# the payload
# ---------------------------------------------------------------------------

STUBS="$WORK/stubs"
mkdir -p "$STUBS"

# node:sqlite is unflagged from Node 22.13 and flagged before it. The payload
# retries with the flag; this harness needs the same, or the fixture cannot be
# built on an older runtime.
node_sqlite() {
  if node -e 'require("node:sqlite")' >/dev/null 2>&1; then
    node "$@"
  else
    node --experimental-sqlite "$@"
  fi
}

if ! node -e 'require("node:sqlite")' >/dev/null 2>&1 \
   && ! node --experimental-sqlite -e 'require("node:sqlite")' >/dev/null 2>&1; then
  echo "  SKIP: this node has no node:sqlite; the jobs-table half cannot be exercised" >&2
  echo "PASS (partial)"
  exit 0
fi

JOBS_DB="$WORK/jobs.sqlite3"

# write_jobs_db builds a REAL operator job database from a TSV of
# <job id>\t<room token>\t<room name>. The talk_binding column is the exact JSON
# the operator writes (talkJobBinding in talk_backend.go), because a stub of it
# would agree with whatever the payload happened to expect.
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
      const [id, token, roomName] = line.split("\t");
      const binding = {
        backend_url: "https://cloud.test/",
        room_token: token,
        owner: "alice",
        room_public: false,
      };
      if (roomName) binding.room_name = roomName;
      insert.run(id, JSON.stringify(binding));
    }
    db.close();
  ' "$JOBS_DB" "$1"
}

# write_fake_archive builds the fake Nextcloud the curl stub serves:
#   $WORK/archive/catalog.json          what a GET of the catalog returns
#   $WORK/archive/tags/<id>.json        what ffprobe reports for that meeting
# A meeting with no tags file is one whose .opus is missing (404).
write_fake_archive() {
  rm -rf "$WORK/archive"
  mkdir -p "$WORK/archive/tags"
  cat >"$WORK/archive/catalog.json"
}

write_tags() {
  local id="$1"
  cat >"$WORK/archive/tags/$id.json"
}

# The curl stub speaks just enough of the interface the payload uses: -o, -w
# '%{http_code}', -X <method>, and --data-binary @<file>. It records every
# write so the test can assert what would have been sent.
#
# OPUS_PUT_STATUS lets a test make an overwrite answer 201 instead of 204 — the
# one status that means a fresh, ruleless fileid was minted.
cat >"$STUBS/curl" <<EOF
#!/usr/bin/env bash
set -euo pipefail
out=""; method="GET"; data=""; url=""
while [[ \$# -gt 0 ]]; do
  case "\$1" in
    -o) out="\$2"; shift 2 ;;
    -X) method="\$2"; shift 2 ;;
    -H) shift 2 ;;
    # The real script passes the auth headers in a 0600 config file rather than
    # on argv, so the stub has to skip it the same way curl would.
    --config) shift 2 ;;
    --data-binary) data="\${2#@}"; shift 2 ;;
    -sS|-w) shift ;;
    '%{http_code}') shift ;;
    *) url="\$1"; shift ;;
  esac
done
: >"\${out:-/dev/null}"
case "\$method:\$url" in
  GET:*catalog.json)
    cat "$WORK/archive/catalog.json" >"\$out"; echo 200 ;;
  GET:*meetings/*)
    id="\${url##*/meetings/}"; id="\${id%.opus}"
    if [[ -f "$WORK/archive/tags/\$id.json" ]]; then
      # Stands in for the recording; ffprobe is stubbed too, so the bytes only
      # need to name which meeting this is.
      printf '%s' "\$id" >"\$out"; echo 200
    else
      echo 404
    fi ;;
  PUT:*meetings/*)
    id="\${url##*/meetings/}"; id="\${id%.opus}"
    printf '%s\t%s\n' "\$id" "\$(cat "\$data")" >>"$WORK/opus-puts.tsv"
    echo "\${OPUS_PUT_STATUS:-204}" ;;
  PROPPATCH:*meetings/*)
    # Never legitimate: writing an ACL onto a recording would replace the
    # meeting's real audience with whatever this script could guess. Recorded so
    # a test can assert it does not happen.
    printf '%s\n' "\$url" >>"$WORK/opus-proppatch.txt"
    echo 207 ;;
  PUT:*catalog.json)
    cp "\$data" "$WORK/put-body.json"; echo 204 ;;
  PROPPATCH:*catalog.json)
    cp "\$data" "$WORK/proppatch-body.xml"
    printf '<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:propstat><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>' >"\$out"
    echo 207 ;;
  *) echo 500 ;;
esac
EOF

# ffprobe reads the stand-in body written above to find which meeting it is,
# then emits that meeting's pre-baked tag report.
cat >"$STUBS/ffprobe" <<EOF
#!/usr/bin/env bash
set -euo pipefail
for arg in "\$@"; do last="\$arg"; done
id="\$(cat "\$last")"
cat "$WORK/archive/tags/\$id.json"
EOF

# The cassini stub stands in for `cassini retag`. It records every invocation so
# a test can assert which room id and which lineage were written, and copies the
# input through so the PUT that follows has a body to send.
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
if [[ -n "\${RETAG_FAILS:-}" ]]; then echo "retag: simulated failure" >&2; exit 1; fi
cp "\$in" "\$out"
# The --json summary the payload reads to decide whether an upload is needed.
# RETAG_CHANGES=none makes it report that the file already named this room.
if [[ "\${RETAG_CHANGES:-some}" == "none" ]]; then
  printf '{"input":"%s","output":"%s","meetingId":"m","changes":[]}\n' "\$in" "\$out"
else
  printf '{"input":"%s","output":"%s","meetingId":"m","changes":[{"field":"roomId","from":null,"to":"rm_x"}]}\n' "\$in" "\$out"
fi
EOF
chmod +x "$STUBS/curl" "$STUBS/ffprobe" "$STUBS/cassini"
cp "$STUBS/curl" "$STUBS/curl.real"

run_payload() {
  rm -f "$WORK/put-body.json" "$WORK/proppatch-body.xml" \
        "$WORK/opus-puts.tsv" "$WORK/opus-proppatch.txt" "$WORK/retag-argv"
  env PATH="$STUBS:$PATH" \
    NEXTCLOUD_URL="https://cloud.test" APP_SECRET="s3cret" APP_ID="gocassini" APP_VERSION="0.2.0" \
    CASSINI_ROOM_ID_PEPPER="test-pepper" \
    OPUS_PUT_STATUS="${OPUS_PUT_STATUS:-204}" \
    RETAG_FAILS="${RETAG_FAILS:-}" \
    RETAG_CHANGES="${RETAG_CHANGES:-some}" \
    bash "$PAYLOAD" --jobs-db "$JOBS_DB" "$@" >"$WORK/stdout" 2>"$WORK/stderr"
}

# room_id_for_name / room_id_for_token derive the ids the payload must produce,
# the same way the Go recorder and the payload both do. Written out here rather
# than shared, so a change to either side fails this test instead of silently
# agreeing with itself. The two domains are deliberately separate: without them
# a room whose NAME equalled another room's TOKEN would derive the same id.
room_id_for_name() {
  node -e '
    const crypto = require("crypto");
    const mac = crypto.createHmac("sha256", Buffer.from(process.argv[2] || "", "utf8"));
    mac.update("cassini.room.name.v1\u0000", "utf8");
    mac.update(process.argv[1].trim(), "utf8");
    process.stdout.write("rm_" + mac.digest("hex").slice(0, 16));
  ' "$1" "test-pepper"
}
room_id_for_token() {
  node -e '
    const crypto = require("crypto");
    const mac = crypto.createHmac("sha256", Buffer.from(process.argv[2] || "", "utf8"));
    mac.update("cassini.room.token.v1\u0000", "utf8");
    mac.update(process.argv[1].trim(), "utf8");
    process.stdout.write("rm_" + mac.digest("hex").slice(0, 16));
  ' "$1" "test-pepper"
}

write_fake_archive <<'EOF'
{
  "version": "cassini.viewer.catalog.v1",
  "meetings": [
    {"id": "JOBROW", "title": "Weekly Sync", "dateLabel": "2026-08-11 10:32",
     "audioPath": "./meetings/JOBROW.opus", "speakerCount": 3},
    {"id": "SPLIT", "title": "Old Standup", "dateLabel": "2026-08-10 09:00",
     "audioPath": "./meetings/SPLIT.opus",
     "roomId": "rm_1111111111111111", "roomName": "Old Standup"},
    {"id": "NONAMEJOB", "title": "NONAMEJOB", "dateLabel": "2026-08-09 09:00",
     "audioPath": "./meetings/NONAMEJOB.opus"},
    {"id": "LEGACY", "title": "Retro", "dateLabel": "2026-07-02 09:00",
     "audioPath": "./meetings/LEGACY.opus"},
    {"id": "DEFAULTTITLE", "title": "Untitled meeting", "dateLabel": "2026-06-01 08:00",
     "audioPath": "./meetings/DEFAULTTITLE.opus"},
    {"id": "GONE", "title": "Missing file", "dateLabel": "2026-05-01 08:00",
     "audioPath": "./meetings/GONE.opus"},
    {"id": "ALREADY", "title": "Done", "dateLabel": "2026-04-01 08:00",
     "audioPath": "./meetings/ALREADY.opus",
     "roomId": "rm_deadbeefdeadbeef", "roomName": "Chosen By A Person"}
  ]
}
EOF
write_tags JOBROW <<'EOF'
{"format":{"tags":{"TITLE":"Weekly Sync"}},"streams":[]}
EOF
write_tags SPLIT <<'EOF'
{"format":{},"streams":[{"tags":{"TITLE":"Old Standup"}}]}
EOF
write_tags NONAMEJOB <<'EOF'
{"format":{"tags":{"TITLE":"NONAMEJOB"}},"streams":[]}
EOF
write_tags LEGACY <<'EOF'
{"format":{},"streams":[{"tags":{"TITLE":"Retro"}}]}
EOF
write_tags DEFAULTTITLE <<'EOF'
{"format":{"tags":{"TITLE":"Cassini Meeting"}},"streams":[]}
EOF
write_tags ALREADY <<'EOF'
{"format":{"tags":{"TITLE":"Done"}},"streams":[]}
EOF

# JOBROW and SPLIT were recorded by this installation; LEGACY, DEFAULTTITLE,
# GONE and ALREADY were not. NONAMEJOB has a job row whose room-name lookup
# never succeeded, which is the state a Nextcloud outage during recording
# leaves behind.
printf 'JOBROW\ttok-jobrow\tWeekly Sync\nSPLIT\ttok-split\t\nNONAMEJOB\ttok-noname\t\n' >"$WORK/jobs.tsv"
write_jobs_db "$WORK/jobs.tsv"

JOBROW_ROOM_ID="$(room_id_for_token "tok-jobrow")"
SPLIT_ROOM_ID="$(room_id_for_token "tok-split")"
NONAME_ROOM_ID="$(room_id_for_token "tok-noname")"
LEGACY_ROOM_ID="$(room_id_for_name "Retro")"

# --- dry run reports and writes nothing ---
run_payload || fail "a dry run should exit 0: $(cat "$WORK/stderr")"
if [[ -f "$WORK/put-body.json" ]]; then
  fail "a dry run must not PUT the catalog"
else
  ok "a dry run writes nothing"
fi
if [[ -f "$WORK/opus-puts.tsv" ]]; then
  fail "a dry run must not re-tag any published file"
else
  ok "a dry run re-tags nothing"
fi
check "a dry run says it wrote nothing" "$WORK/stdout" "dry run: nothing was written"

# --- the jobs table is the authority ---
check "a job row gives the room the operator actually recorded" "$WORK/stdout" \
  "JOBROW: room id=$JOBROW_ROOM_ID name=Weekly Sync (from job-binding)"

# The heal: an entry already carrying a NAME-derived id is moved to the
# TOKEN-derived one, which is what merges a conversation that an earlier pass
# split into two rooms. This is the whole point of D-640.
check "a name-derived id is reconciled to the token-derived one" "$WORK/stdout" \
  "SPLIT: room id=$SPLIT_ROOM_ID name=Old Standup (from job-binding)"
refute "the split entry does not keep its old id" "$WORK/stdout" "rm_1111111111111111"

# A job row with no room name at all still resolves: the id comes from the
# token, and a room with no label beats no room.
check "a job row with no name still yields an id" "$WORK/stdout" \
  "NONAMEJOB: room id=$NONAME_ROOM_ID name=- (from job-binding)"

# --- no job row: the file, and only to fill a blank ---
check "a meeting with no job row falls back to its file's name" "$WORK/stdout" \
  "LEGACY: room id=$LEGACY_ROOM_ID name=Retro (from title+id-from-name)"

if [[ "$LEGACY_ROOM_ID" == rm_* && ${#LEGACY_ROOM_ID} -eq 19 ]]; then
  ok "the derived id is an opaque rm_ value, not a room name or token"
else
  fail "derived id has the wrong shape: $LEGACY_ROOM_ID"
fi

check "packer default titles are not mistaken for room names" "$WORK/stdout" \
  "DEFAULTTITLE: the published file carries no room name"

check "a missing published file is reported and skipped" "$WORK/stdout" "GONE: no file at"

# The other half of the ownership rule: with no job row, whatever is there was
# put there by a person — usually with reattribute-catalog-room.sh — and a
# scheduled backfill must never undo it.
refute "a room chosen by a person, with no job row, is left alone" "$WORK/stdout" "ALREADY:"

# The token is a join capability for a public conversation. It is hashed inside
# the node program and must never reach a log, a file, or an argv.
for token in tok-jobrow tok-split tok-noname; do
  refute "the raw token $token never appears in the output" "$WORK/stdout" "$token"
  refute "the raw token $token never appears on stderr" "$WORK/stderr" "$token"
done

# --- --apply writes the catalog, re-tags the artifacts, restores the ACL ---
run_payload --apply || fail "--apply should exit 0: $(cat "$WORK/stderr")"
[[ -f "$WORK/put-body.json" ]] || fail "--apply should PUT the catalog"

if node -e '
  const fs = require("fs");
  const catalog = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
  const byId = Object.fromEntries(catalog.meetings.map((m) => [m.id, m]));
  // node -e puts the FIRST user argument at argv[1], not argv[2].
  const [, , jobrow, split, noname, legacy] = process.argv;
  const problems = [];
  if (catalog.version !== "cassini.viewer.catalog.v1") problems.push("the version must be carried through untouched");
  if (catalog.meetings.length !== 7) problems.push("every entry must survive, not just the updated ones");
  if (byId.JOBROW.roomId !== jobrow || byId.JOBROW.roomName !== "Weekly Sync") problems.push("JOBROW room not written from the job binding");
  if (byId.JOBROW.speakerCount !== 3) problems.push("unrelated fields must be preserved verbatim");
  if (byId.SPLIT.roomId !== split) problems.push("SPLIT was not reconciled to the token-derived id");
  if (byId.SPLIT.roomName !== "Old Standup") problems.push("SPLIT should keep the name it already had");
  if (byId.NONAMEJOB.roomId !== noname) problems.push("NONAMEJOB should get an id from its token");
  if ("roomName" in byId.NONAMEJOB) problems.push("NONAMEJOB has no name anywhere and must not be given one");
  if (byId.LEGACY.roomId !== legacy) problems.push("LEGACY roomId is not the name-derived id");
  if (byId.LEGACY.roomName !== "Retro") problems.push("LEGACY roomName not written");
  if ("roomId" in byId.DEFAULTTITLE || "roomName" in byId.DEFAULTTITLE) problems.push("DEFAULTTITLE must stay roomless");
  if (byId.ALREADY.roomId !== "rm_deadbeefdeadbeef") problems.push("a room chosen by a person must not be overwritten");
  if (problems.length) { console.error(problems.join("; ")); process.exit(1); }
' "$WORK/put-body.json" "$JOBROW_ROOM_ID" "$SPLIT_ROOM_ID" "$NONAME_ROOM_ID" "$LEGACY_ROOM_ID"; then
  ok "the written catalog keeps every other field, and the version"
else
  fail "the written catalog is wrong (see above)"
fi

# The durability half. Without this the exporter re-derives each entry's room
# from an untouched file on the next republish and the whole backfill is undone.
for id in JOBROW SPLIT NONAMEJOB LEGACY; do
  if grep -q "^$id	" "$WORK/opus-puts.tsv" 2>/dev/null; then
    ok "$id's published file was re-tagged and uploaded"
  else
    fail "$id should have been re-tagged: $(cat "$WORK/opus-puts.tsv" 2>/dev/null)"
  fi
done
check "the re-tag writes the resolved room id into the file" "$WORK/retag-argv" \
  -- "--room-id $JOBROW_ROOM_ID"
# A meeting recovered from the jobs table has a lineage by definition; one
# recovered from its own file does not, and none is invented for it.
check "a job-recovered meeting is also stamped with its job id" "$WORK/retag-argv" \
  -- "--job-id JOBROW"
if grep -q -- "--job-id LEGACY" "$WORK/retag-argv"; then
  fail "a meeting with no job row must not be stamped with an invented job id"
else
  ok "a meeting with no job row is not given a job id"
fi

# Never PROPPATCH a recording. An overwrite keeps the fileid the groupfolders
# rules hang off, so leaving the ACL alone is what keeps it correct — while
# writing one would replace the meeting's real audience with a guess.
if [[ -f "$WORK/opus-proppatch.txt" ]]; then
  fail "a recording's permissions must never be rewritten: $(cat "$WORK/opus-proppatch.txt")"
else
  ok "no recording's permissions were touched"
fi

# Byte format matters: every other writer of this file emits a two-space indent
# and a trailing newline, and deviating makes every future hand diff noisy.
if [[ "$(tail -c 1 "$WORK/put-body.json")" == "" ]] && grep -q '^  "version"' "$WORK/put-body.json"; then
  ok "the catalog keeps the two-space indent and trailing newline"
else
  fail "the catalog should be two-space indented and end with a newline"
fi

# The PROPPATCH is the difference between a private archive index and one every
# signed-in account can read, so it must happen on every write.
if [[ -f "$WORK/proppatch-body.xml" ]]; then
  check "--apply restores the owner-only permissions" "$WORK/proppatch-body.xml" \
    "<nc:acl-mapping-id>everyone</nc:acl-mapping-id><nc:acl-mask>31</nc:acl-mask><nc:acl-permissions>0</nc:acl-permissions>"
else
  fail "--apply must restore the owner-only ACL"
fi

# --- --no-retag writes only the catalog, and says the change is temporary ---
run_payload --apply --no-retag || fail "--apply --no-retag should exit 0: $(cat "$WORK/stderr")"
[[ -f "$WORK/put-body.json" ]] || fail "--no-retag should still PUT the catalog"
if [[ -f "$WORK/opus-puts.tsv" ]]; then
  fail "--no-retag must not upload any recording"
else
  ok "--no-retag leaves published files untouched"
fi
check "--no-retag warns that the change does not survive a republish" "$WORK/stdout" \
  "will revert them"

# --- a 201 on a recording is a hard failure: a new fileid has no permissions ---
# A leaf under meetings/ with no rule of its own inherits the container's grant
# to the virtual everyone group, so a "successful" create is a world-readable
# recording.
OPUS_PUT_STATUS=201 run_payload --apply && true
status=$?
unset OPUS_PUT_STATUS
if [[ $status -eq 1 ]]; then
  ok "a created (201) recording exits 1, the code that means 'check it now'"
else
  fail "a 201 on a recording upload should exit 1, got $status"
fi
check "the 201 failure explains the permissions consequence" "$WORK/stderr" \
  "inherits the folder's grant to every signed-in account"
if [[ -f "$WORK/put-body.json" ]]; then
  fail "a failed re-tag must stop before the catalog is written"
else
  ok "a failed re-tag stops before the catalog is written"
fi

# --- a failing retag stops before anything is uploaded ---
RETAG_FAILS=1 run_payload --apply && true
status=$?
unset RETAG_FAILS
if [[ $status -eq 4 ]]; then
  ok "a failing re-tag exits 4 (nothing written, retry safe)"
else
  fail "a failing re-tag should exit 4, got $status"
fi
if [[ -f "$WORK/opus-puts.tsv" || -f "$WORK/put-body.json" ]]; then
  fail "a failing re-tag must upload nothing at all"
else
  ok "a failing re-tag uploads nothing"
fi

# --- a rejected ACL is a failure AFTER writing, not a success ---
# Outside a group folder with advanced ACL, nc:acl-list is not settable and
# Nextcloud answers 207 with a 403 propstat. Reading only the 207 is how a file
# ends up with no ACL while every response looks fine (D-585).
cat >"$STUBS/curl.reject" <<EOF
#!/usr/bin/env bash
if printf '%s\n' "\$@" | grep -q PROPPATCH; then
  for a in "\$@"; do [[ "\$prev" == "-o" ]] && out="\$a"; prev="\$a"; done
  printf '<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:propstat><d:status>HTTP/1.1 403 Forbidden</d:status></d:propstat></d:response></d:multistatus>' >"\$out"
  echo 207
  exit 0
fi
exec "$STUBS/curl.real" "\$@"
EOF
chmod +x "$STUBS/curl.reject"
cp "$STUBS/curl.reject" "$STUBS/curl"
run_payload --apply && true
status=$?
cp "$STUBS/curl.real" "$STUBS/curl"
if [[ $status -ne 1 ]]; then
  fail "a rejected ACL should exit 1 (written, may be exposed), got $status"
elif ! grep -q "rejected the catalog permissions" "$WORK/stderr"; then
  fail "a rejected ACL should say so: $(cat "$WORK/stderr")"
else
  ok "a rejected ACL is a failure after writing, not a silent success"
fi

# --- an entry whose file already names the room is not re-uploaded ---
# Re-tagging is local and cheap; uploading a whole sealed recording is not. An
# entry can be selected purely because its room NAME changed, and the artifact
# carries the id and not the name — so there is nothing new to send.
RETAG_CHANGES=none run_payload --apply && true
status=$?
unset RETAG_CHANGES
if [[ $status -eq 0 ]]; then ok "a no-op re-tag still succeeds"; else fail "expected exit 0, got $status: $(cat "$WORK/stderr")"; fi
if [[ -f "$WORK/opus-puts.tsv" ]]; then
  fail "a recording whose file already names the room must not be re-uploaded: $(cat "$WORK/opus-puts.tsv")"
else
  ok "a recording whose file already names the room is not re-uploaded"
fi
check "it says why nothing was uploaded" "$WORK/stdout" "already names this room"
# ...and the catalog is still written, because the entry itself did change.
[[ -f "$WORK/put-body.json" ]] || fail "the catalog must still be written when only the entry changed"

# --- an unreadable change summary is a failure, not "nothing moved" ---
# A two-way test would read a malformed summary as "no changes" and silently
# skip the durability step this whole branch exists for.
cat >"$STUBS/cassini.broken" <<EOF
#!/usr/bin/env bash
set -euo pipefail
in="\$2"; out=""
shift 2
while [[ \$# -gt 0 ]]; do
  case "\$1" in
    --out) out="\$2"; shift 2 ;;
    *) shift ;;
  esac
done
cp "\$in" "\$out"
printf 'not json at all'
EOF
chmod +x "$STUBS/cassini.broken"
cp "$STUBS/cassini" "$STUBS/cassini.real"
cp "$STUBS/cassini.broken" "$STUBS/cassini"
run_payload --apply && true
status=$?
cp "$STUBS/cassini.real" "$STUBS/cassini"
if [[ $status -eq 4 ]]; then
  ok "an unreadable re-tag summary exits 4 rather than skipping the upload"
else
  fail "an unreadable summary should exit 4, got $status"
fi
if [[ -f "$WORK/put-body.json" ]]; then fail "it must not write the catalog"; else ok "and writes nothing"; fi

# --- a dry run works on a container with no cassini binary ---
# Reporting is always safe, so refusing to report because the re-tag binary is
# absent would deny an operator the one thing they can always do.
mv "$STUBS/cassini" "$STUBS/cassini.hidden"
run_payload && true
status=$?
mv "$STUBS/cassini.hidden" "$STUBS/cassini"
if [[ $status -eq 0 ]]; then
  ok "a dry run does not require the cassini binary"
else
  fail "a dry run should not need cassini, got $status: $(cat "$WORK/stderr")"
fi

# --- a re-run after a successful pass changes nothing ---
# Idempotence is what makes it safe to schedule. The catalog written above is
# fed back in as the archive, and the second pass must resolve nothing and
# upload nothing.
#
# Note it exits 0 rather than 3: this fixture keeps two entries that neither the
# jobs table nor their files can ever place, and reporting those as "nothing
# needed changing" would be a lie.
run_payload --apply >/dev/null 2>&1 || true
cp "$WORK/put-body.json" "$WORK/archive/catalog.json"
run_payload --apply && true
status=$?
if [[ $status -eq 0 ]]; then
  ok "a second pass over an already-backfilled archive succeeds"
else
  fail "a second pass should succeed, got $status: $(cat "$WORK/stderr")"
fi
check "the second pass resolves nothing" "$WORK/stdout" "0 entr(y/ies) can be given a room"
if [[ -f "$WORK/opus-puts.tsv" ]]; then
  fail "a second pass must not re-upload any recording: $(cat "$WORK/opus-puts.tsv")"
else
  ok "a second pass re-uploads nothing"
fi
# Specifically the shape that used to churn: a job row with a token and no room
# name anywhere. There is nothing left to find for it, and re-downloading and
# re-uploading its recording on every scheduled run would be pure cost.
refute "a job-bound meeting with no name anywhere is not re-examined" "$WORK/stdout" "NONAMEJOB:"

# --- the jobs database is not optional silently ---
# A run that cannot read it must not quietly fall back to name derivation: the
# same room would get one id when the database is reachable and a different one
# when it is not, which is exactly the split this design forbids.
write_fake_archive <<'EOF'
{"version":"cassini.viewer.catalog.v1","meetings":[
  {"id":"JOBROW","title":"Weekly Sync","dateLabel":"2026-08-11 10:32","audioPath":"./meetings/JOBROW.opus"}]}
EOF
write_tags JOBROW <<'EOF'
{"format":{"tags":{"TITLE":"Weekly Sync"}},"streams":[]}
EOF
printf 'not a sqlite database at all' >"$WORK/broken.sqlite3"
env PATH="$STUBS:$PATH" \
  NEXTCLOUD_URL="https://cloud.test" APP_SECRET="s3cret" APP_ID="gocassini" APP_VERSION="0.2.0" \
  CASSINI_ROOM_ID_PEPPER="test-pepper" \
  bash "$PAYLOAD" --jobs-db "$WORK/broken.sqlite3" >"$WORK/stdout" 2>"$WORK/stderr" && true
status=$?
if [[ $status -eq 4 ]]; then
  ok "an unreadable jobs database exits 4 rather than falling back silently"
else
  fail "an unreadable jobs database should exit 4, got $status: $(cat "$WORK/stderr")"
fi
if [[ -f "$WORK/put-body.json" ]]; then
  fail "an unreadable jobs database must write nothing"
else
  ok "an unreadable jobs database writes nothing"
fi

# --- but an ABSENT database is a warning, not a failure ---
# An installation whose job history is genuinely gone still deserves the
# name-derived pass; it just has to be told that is what it is getting.
env PATH="$STUBS:$PATH" \
  NEXTCLOUD_URL="https://cloud.test" APP_SECRET="s3cret" APP_ID="gocassini" APP_VERSION="0.2.0" \
  CASSINI_ROOM_ID_PEPPER="test-pepper" \
  bash "$PAYLOAD" --jobs-db "$WORK/does-not-exist.sqlite3" >"$WORK/stdout" 2>"$WORK/stderr" && true
status=$?
if [[ $status -eq 0 ]]; then
  ok "an absent jobs database still runs the name-derived pass"
else
  fail "an absent jobs database should still run, got $status: $(cat "$WORK/stderr")"
fi
check "an absent jobs database says the ids will be weaker" "$WORK/stdout" "a weaker identity"

# --- --no-jobs-db is the explicit form of the same thing ---
# Run WITHOUT the harness's usual --jobs-db, which would contradict it.
env PATH="$STUBS:$PATH" \
  NEXTCLOUD_URL="https://cloud.test" APP_SECRET="s3cret" APP_ID="gocassini" APP_VERSION="0.2.0" \
  CASSINI_ROOM_ID_PEPPER="test-pepper" \
  bash "$PAYLOAD" --no-jobs-db >"$WORK/stdout" 2>"$WORK/stderr" && true
status=$?
if [[ $status -eq 0 ]]; then
  ok "--no-jobs-db runs the file-only pass"
else
  fail "--no-jobs-db should exit 0, got $status: $(cat "$WORK/stderr")"
fi
check "--no-jobs-db derives from the name instead of the token" "$WORK/stdout" "id-from-name"

# --- --jobs-db and --no-jobs-db contradict each other ---
run_payload --no-jobs-db --jobs-db "$JOBS_DB" && true
status=$?
if [[ $status -eq 2 ]]; then ok "--jobs-db with --no-jobs-db is a usage error"; else fail "contradictory jobs flags should exit 2, got $status"; fi

# --- an archive with nothing to do exits 3 ---
write_fake_archive <<'EOF'
{"version":"cassini.viewer.catalog.v1","meetings":[
  {"id":"ALREADY","title":"Done","dateLabel":"2026-04-01 08:00","audioPath":"./meetings/ALREADY.opus",
   "roomId":"rm_deadbeefdeadbeef","roomName":"Existing"}]}
EOF
run_payload && true
status=$?
if [[ $status -eq 3 ]]; then ok "an archive with nothing to do exits 3"; else fail "an archive where every entry has a room should exit 3, got $status"; fi

# --- a missing AppAPI environment is exit 2, not 1 ---
# Running the payload outside the app container (or against the wrong one) must
# not report "the catalog may be publicly readable" for a run that made no
# request at all.
rm -f "$WORK/put-body.json"
env PATH="$STUBS:$PATH" APP_SECRET="" NEXTCLOUD_URL="" APP_ID="" APP_VERSION="" \
  bash "$PAYLOAD" >"$WORK/stdout" 2>"$WORK/stderr" && true
status=$?
if [[ $status -eq 2 ]]; then ok "a missing AppAPI environment exits 2"; else fail "a missing environment should exit 2, got $status"; fi
check "the environment error names the variable" "$WORK/stderr" "NEXTCLOUD_URL is not set"

# --- a transport or auth failure is not a verdict on the recording ---
# A backfill runs for a long time; a Nextcloud restart part-way through must not
# silently mark every remaining meeting permanently unfixable and exit 0.
write_fake_archive <<'EOF'
{"version":"cassini.viewer.catalog.v1","meetings":[
  {"id":"OUTAGE","title":"t","dateLabel":"2026-08-11 10:32","audioPath":"./meetings/OUTAGE.opus"}]}
EOF
cat >"$STUBS/curl" <<EOF
#!/usr/bin/env bash
for a in "\$@"; do [[ "\$prev" == "-o" ]] && out="\$a"; prev="\$a"; done
if printf '%s\n' "\$@" | grep -q catalog.json; then
  cat "$WORK/archive/catalog.json" >"\$out"; echo 200; exit 0
fi
: >"\$out"; echo 503
EOF
chmod +x "$STUBS/curl"
run_payload && true
status=$?
if [[ $status -eq 4 ]]; then ok "an outage on a meeting GET exits 4 (nothing written, retry safe)"; else fail "an outage should exit 4, got $status"; fi
check "the outage says it is not a verdict on the recording" "$WORK/stderr" "not a verdict on that recording"
cp "$STUBS/curl.real" "$STUBS/curl"

# --- "every candidate failed" must not be reported as "nothing needed doing" ---
write_fake_archive <<'EOF'
{"version":"cassini.viewer.catalog.v1","meetings":[
  {"id":"GONE","title":"t","dateLabel":"2026-08-11 10:32","audioPath":"./meetings/GONE.opus"}]}
EOF
run_payload && true
status=$?
if [[ $status -eq 0 ]]; then ok "unresolvable candidates exit 0, not 3"; else fail "unresolvable candidates should not claim 'nothing needed doing', got $status"; fi
check "it says neither source can supply a room" "$WORK/stdout" "can supply one"

# --- --limit caps how many entries are examined ---
write_fake_archive <<'EOF'
{"version":"cassini.viewer.catalog.v1","meetings":[
  {"id":"LIMIT1","title":"One","dateLabel":"2026-08-11 10:32","audioPath":"./meetings/LIMIT1.opus"},
  {"id":"LIMIT2","title":"Two","dateLabel":"2026-08-12 10:32","audioPath":"./meetings/LIMIT2.opus"}]}
EOF
write_tags LIMIT1 <<'EOF'
{"format":{"tags":{"TITLE":"One"}},"streams":[]}
EOF
write_tags LIMIT2 <<'EOF'
{"format":{"tags":{"TITLE":"Two"}},"streams":[]}
EOF
run_payload --limit 1 || fail "--limit 1 should exit 0: $(cat "$WORK/stderr")"
check "--limit examines the first entry" "$WORK/stdout" "LIMIT1:"
refute "--limit stops before the second" "$WORK/stdout" "LIMIT2:"

# --- --limit 0 is refused rather than silently meaning "no limit" ---
run_payload --limit 0 && true
status=$?
if [[ $status -eq 2 ]]; then ok "--limit 0 is refused"; else fail "--limit 0 should exit 2, got $status"; fi

# --- a bad --limit is a usage error, before any request ---
run_payload --limit not-a-number && true
status=$?
if [[ $status -eq 2 ]]; then ok "a malformed --limit is a usage error"; else fail "a non-numeric --limit should exit 2, got $status"; fi

if [[ "$failures" -ne 0 ]]; then
  echo "FAILED: $failures check(s)" >&2
  exit 1
fi
echo "PASS"
