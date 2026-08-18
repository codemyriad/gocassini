#!/usr/bin/env bash
# test-backfill-catalog-rooms.sh — offline regressions for the room backfill.
#
# Two halves, tested separately because they fail in different ways:
#
#   backfill-catalog-rooms.sh              reaches the right container and turns
#                                          the payload's exit code into an
#                                          instruction. `docker` is stubbed
#                                          through the DOCKER hook.
#   backfill-catalog-rooms-in-container.sh reads rooms out of published files
#                                          and merges them into the catalog.
#                                          `curl` and `ffprobe` are stubbed on
#                                          PATH, so this needs no Nextcloud and
#                                          no media.
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

# --- the payload is what gets streamed in, not re-derived ---
if ! grep -q "backfill-catalog-rooms-in-container" "$WORK/exec-stdin"; then
  fail "the payload script should be streamed into the container over stdin"
else
  ok "the payload reaches the container over stdin"
fi

# --- flags are passed through ---
make_docker_stub 0
run_wrapper --apply --limit 5 || fail "--apply --limit should exit 0"
for expected in --apply --limit 5; do
  if ! grep -qx -- "$expected" "$WORK/exec-argv"; then
    fail "expected $expected to reach the container, got: $(tr '\n' ' ' <"$WORK/exec-argv")"
  fi
done
ok "flags are passed through"

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
check "exit 4 says nothing was written" "$WORK/stderr" "nothing to clean up"

make_docker_stub 1
run_wrapper && true
[[ $? -eq 1 ]] || fail "exit 1 should be relayed"
check "exit 1 warns that the catalog may be exposed" "$WORK/stderr" "readable by every signed-in account"

make_docker_stub 137
run_wrapper && true
[[ $? -eq 137 ]] || fail "an unknown exit code should be relayed verbatim"
check "an unknown exit code is relayed and admitted" "$WORK/stderr" "is unknown"

# ---------------------------------------------------------------------------
# the payload
# ---------------------------------------------------------------------------

STUBS="$WORK/stubs"
mkdir -p "$STUBS"

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
chmod +x "$STUBS/curl" "$STUBS/ffprobe"

run_payload() {
  rm -f "$WORK/put-body.json" "$WORK/proppatch-body.xml"
  env PATH="$STUBS:$PATH" \
    NEXTCLOUD_URL="https://cloud.test" APP_SECRET="s3cret" APP_ID="gocassini" APP_VERSION="0.2.0" \
    CASSINI_ROOM_ID_PEPPER="test-pepper" \
    bash "$PAYLOAD" "$@" >"$WORK/stdout" 2>"$WORK/stderr"
}

# room_id_for_name derives the id the payload must produce for a name-only
# recording, the same way the Go recorder and the payload both do. Written out
# here rather than shared, so a change to either side fails this test instead of
# silently agreeing with itself.
room_id_for_name() {
  node -e '
    const crypto = require("crypto");
    const mac = crypto.createHmac("sha256", Buffer.from(process.argv[2] || "", "utf8"));
    mac.update("cassini.room.name.v1\u0000", "utf8");
    mac.update(process.argv[1].trim(), "utf8");
    process.stdout.write("rm_" + mac.digest("hex").slice(0, 16));
  ' "$1" "test-pepper"
}

write_fake_archive <<'EOF'
{
  "version": "cassini.viewer.catalog.v1",
  "meetings": [
    {"id": "TAGGED", "title": "Weekly Sync (Parakeet)", "dateLabel": "2026-08-11 10:32",
     "audioPath": "./meetings/TAGGED.opus", "speakerCount": 3},
    {"id": "LEGACY", "title": "Old Standup", "dateLabel": "2026-07-02 09:00",
     "audioPath": "./meetings/LEGACY.opus"},
    {"id": "DEFAULTTITLE", "title": "Untitled meeting", "dateLabel": "2026-06-01 08:00",
     "audioPath": "./meetings/DEFAULTTITLE.opus"},
    {"id": "GONE", "title": "Missing file", "dateLabel": "2026-05-01 08:00",
     "audioPath": "./meetings/GONE.opus"},
    {"id": "ALREADY", "title": "Done", "dateLabel": "2026-04-01 08:00",
     "audioPath": "./meetings/ALREADY.opus", "roomId": "existing-token", "roomName": "Existing"}
  ]
}
EOF
write_tags TAGGED <<'EOF'
{"format":{"tags":{"TITLE":"Weekly Sync","CASSINI_ROOM_ID":"a7bc3k9x","CASSINI_ROOM_NAME":"Weekly Sync"}},"streams":[]}
EOF
write_tags LEGACY <<'EOF'
{"format":{},"streams":[{"tags":{"TITLE":"Old Standup"}}]}
EOF
write_tags DEFAULTTITLE <<'EOF'
{"format":{"tags":{"TITLE":"Cassini Meeting"}},"streams":[]}
EOF
write_tags ALREADY <<'EOF'
{"format":{"tags":{"TITLE":"Done","CASSINI_ROOM_ID":"should-not-be-read"}},"streams":[]}
EOF

# --- dry run reports and writes nothing ---
run_payload || fail "a dry run should exit 0: $(cat "$WORK/stderr")"
if [[ -f "$WORK/put-body.json" ]]; then
  fail "a dry run must not PUT the catalog"
else
  ok "a dry run writes nothing"
fi
check "a dry run says it wrote nothing" "$WORK/stdout" "dry run: nothing was written"

# --- what it can and cannot recover, and where each value came from ---
check "room tags are read from the file" "$WORK/stdout" \
  "TAGGED: room id=a7bc3k9x name=Weekly Sync (from room-tag)"

# A recording published before room ids existed keeps its name and gets NO id.
# A fabricated token would look real and would group meetings that were never
# in the same room.
LEGACY_ROOM_ID="$(room_id_for_name "Old Standup")"
check "a legacy recording gets an id derived from its name" "$WORK/stdout" \
  "LEGACY: room id=$LEGACY_ROOM_ID name=Old Standup (from title+id-from-name)"

# The whole point of the derivation: the published id must not be, or contain,
# anything that could be pasted into a Talk join link.
if [[ "$LEGACY_ROOM_ID" == rm_* && ${#LEGACY_ROOM_ID} -eq 19 ]]; then
  ok "the derived id is an opaque rm_ value, not a room name or token"
else
  fail "derived id has the wrong shape: $LEGACY_ROOM_ID"
fi

check "packer default titles are not mistaken for room names" "$WORK/stdout" \
  "DEFAULTTITLE: the published file carries no room name"

check "a missing published file is reported and skipped" "$WORK/stdout" "GONE: no file at"

refute "entries that already carry a room are left alone" "$WORK/stdout" "ALREADY:"

# --- --apply writes the catalog, then restores the ACL ---
run_payload --apply || fail "--apply should exit 0: $(cat "$WORK/stderr")"
[[ -f "$WORK/put-body.json" ]] || fail "--apply should PUT the catalog"

if node -e '
  const fs = require("fs");
  const catalog = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
  const byId = Object.fromEntries(catalog.meetings.map((m) => [m.id, m]));
  const problems = [];
  if (catalog.version !== "cassini.viewer.catalog.v1") problems.push("the version must be carried through untouched");
  if (catalog.meetings.length !== 5) problems.push("every entry must survive, not just the updated ones");
  if (byId.TAGGED.roomId !== "a7bc3k9x" || byId.TAGGED.roomName !== "Weekly Sync") problems.push("TAGGED room not written");
  if (byId.TAGGED.speakerCount !== 3) problems.push("unrelated fields must be preserved verbatim");
  if (byId.LEGACY.roomId !== process.argv[2]) problems.push("LEGACY roomId is not the name-derived id");
  if (byId.LEGACY.roomName !== "Old Standup") problems.push("LEGACY roomName not written");
  if ("roomId" in byId.DEFAULTTITLE || "roomName" in byId.DEFAULTTITLE) problems.push("DEFAULTTITLE must stay roomless");
  if (byId.ALREADY.roomId !== "existing-token") problems.push("an existing room must not be overwritten");
  if (problems.length) { console.error(problems.join("; ")); process.exit(1); }
' "$WORK/put-body.json" "$LEGACY_ROOM_ID"; then
  ok "the written catalog keeps every other field, and the version"
else
  fail "the written catalog is wrong (see above)"
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
cp "$STUBS/curl" "$STUBS/curl.real"
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

# --- an archive with nothing to do exits 3 ---
write_fake_archive <<'EOF'
{"version":"cassini.viewer.catalog.v1","meetings":[
  {"id":"ALREADY","title":"Done","dateLabel":"2026-04-01 08:00","audioPath":"./meetings/ALREADY.opus",
   "roomId":"existing-token","roomName":"Existing"}]}
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
check "it says the files carry no room" "$WORK/stdout" "their published files carry none"

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
