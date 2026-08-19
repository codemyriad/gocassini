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

# ---------------------------------------------------------------------------
# the payload
# ---------------------------------------------------------------------------

STUBS="$WORK/stubs"
mkdir -p "$STUBS"

write_fake_catalog() { cat >"$WORK/catalog.json"; }

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
case "\$method" in
  GET) cat "$WORK/catalog.json" >"\$out"; echo 200 ;;
  PUT) cp "\$data" "$WORK/put-body.json"; echo 204 ;;
  PROPPATCH)
    cp "\$data" "$WORK/proppatch-body.xml"
    printf '<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:propstat><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>' >"\$out"
    echo 207 ;;
  *) echo 500 ;;
esac
EOF
chmod +x "$STUBS/curl"

run_payload() {
  rm -f "$WORK/put-body.json" "$WORK/proppatch-body.xml"
  env PATH="$STUBS:$PATH" \
    NEXTCLOUD_URL="https://cloud.test" APP_SECRET="s3cret" APP_ID="gocassini" APP_VERSION="0.2.0" \
    bash "$PAYLOAD" "$@" >"$WORK/stdout" 2>"$WORK/stderr"
}

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
