#!/usr/bin/env bash
# Pin annotations/meetings/<id> through the real AppAPI proxy, and prove that
# the real Nextcloud refuses a stale If-Match on the recordings tree (D-737).
#
# Runs against an ALREADY INSTALLED Cassini ExApp, the way
# validate-installed-exapp-private-talk.sh does. It starts nothing, tears
# nothing down, and is not wired into CI.
#
# What it covers:
#   1. The route is declared and a POST body survives the proxy. Proved by a
#      body the operator can only refuse if it received it: an unknown
#      actorKind comes back 400 with the operator's own reason. A route the
#      proxy does not know, or a body it dropped, cannot produce that answer.
#   2. A well-formed id that is in nobody's archive answers 404, on GET and
#      on POST.
#   3. With --meeting-id (a meeting ADMIN_USER can read): a mark commits and
#      is attributed to the caller, not to the actor id the body claims; GET
#      reads it back; marking it again adds nothing. OUTSIDER_USER, who must
#      not be able to read that meeting, gets byte-for-byte the 404 an absent
#      id gets. The run then undoes its own operation, so the meeting ends
#      with no marks. Its annotations revision stays bumped, because the file
#      was rewritten.
#   4. If-Match on the recordings tree. As the service account, with the
#      AppAPI act-as-user credentials the operator itself uses, a scratch file
#      in the archive root is written twice. A PUT carrying the first ETag
#      must get 412 and leave the second version in place. A PUT carrying
#      the current ETag must land. The scratch file is deleted on exit.
#
# What it does NOT cover:
#   - the operator's own 412 retry loop. Racing two real writers is not
#     deterministic, so that is unit-tested against a fake Nextcloud
#     (cassini-operator/internal/operator/annotations_meetings_test.go);
#   - the projection (annotations.sqlite3), annotations/tags, tag narrowing;
#   - carrying marks through a republish;
#   - If-Match against a real recording. If Nextcloud ignored the header, that
#     probe would overwrite the recording, so it runs on a scratch file in
#     the same tree instead.

set -euo pipefail

NEXTCLOUD_HOST="${CASSINI_HARNESS_HOST:-127.0.0.1}"
MEETING_ID=""
CONTAINER="${CASSINI_EXAPP_CONTAINER:-nc_app_gocassini}"
LOG_DIR="${LOG_DIR:-/tmp/cassini-annotations-route-$(date -u +%Y%m%dT%H%M%S)-$$}"
ADMIN_USER="${ADMIN_USER:-admin}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin}"
# The standard viewer user the harness creates (harness_create_standard_viewer_user).
OUTSIDER_USER="${OUTSIDER_USER:-alice}"
OUTSIDER_PASSWORD="${OUTSIDER_PASSWORD:-Tn8mY3qVrJ2x!E2e}"
# ncRecordingsOwner in cassini-operator/internal/operator/webdav_upload.go.
SERVICE_ACCOUNT="cassini"
# Well-formed (the operator accepts its shape) and in nobody's catalog.
ABSENT_ID="HARNESS-D737-NO-SUCH-MEETING"

log() { printf '\n\033[1;34m==>\033[0m \033[1m%s\033[0m\n' "$*" >&2; }
success() { printf '\033[1;32m%s\033[0m\n' "$*" >&2; }
fail() { printf '\033[1;31m[ERROR] %s\033[0m\n' "$*" >&2; exit 1; }

usage() {
  cat <<EOF
Usage: $0 [options]

Pins annotations/meetings/<id> through the AppAPI proxy of an already
installed Cassini ExApp, and proves the real Nextcloud refuses a stale
If-Match on the recordings tree. Starts and stops nothing.

Options:
  --nextcloud-host <host-or-url>  Default: CASSINI_HARNESS_HOST, then 127.0.0.1
  --meeting-id <id>               A published meeting ADMIN_USER can read and
                                  OUTSIDER_USER cannot. Without it the
                                  commit/read-back/outsider half is skipped.
  --container <name>              The ExApp container. Default: nc_app_gocassini
  --log-dir <path>                Retained evidence directory

Environment: ADMIN_USER, ADMIN_PASSWORD, OUTSIDER_USER, OUTSIDER_PASSWORD.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --nextcloud-host) [[ $# -ge 2 ]] || fail "$1 requires a value"; NEXTCLOUD_HOST="$2"; shift 2 ;;
    --meeting-id) [[ $# -ge 2 ]] || fail "$1 requires a value"; MEETING_ID="$2"; shift 2 ;;
    --container) [[ $# -ge 2 ]] || fail "$1 requires a value"; CONTAINER="$2"; shift 2 ;;
    --log-dir) [[ $# -ge 2 ]] || fail "$1 requires a value"; LOG_DIR="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done

for tool in curl jq docker base64; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is required"
done

normalize_base_url() {
  local input="$1"
  if [[ "$input" =~ ^https?:// ]]; then printf '%s\n' "${input%/}"; else printf 'http://%s:28080\n' "${input%/}"; fi
}

BASE_URL="$(normalize_base_url "$NEXTCLOUD_HOST")"
PROXY_URL="$BASE_URL/index.php/apps/app_api/proxy/gocassini"
ANNOTATIONS_URL="$PROXY_URL/annotations/meetings"
SERVICE_DAV_URL="$BASE_URL/remote.php/dav/files/$SERVICE_ACCOUNT"
AUTH=(-u "$ADMIN_USER:$ADMIN_PASSWORD")
OUTSIDER_AUTH=(-u "$OUTSIDER_USER:$OUTSIDER_PASSWORD")
SERVICE_AUTH=()
PROBE_URL=""
MARK_OP_ID=""
MARK_UNDONE=0
mkdir -p "$LOG_DIR"

# request <out> <curl args...> prints the HTTP status and keeps the body in <out>.
request() {
  local out="$1"
  shift
  curl -sS -o "$out" -w '%{http_code}' "$@"
}

post_json() {
  local out="$1" url="$2" body="$3"
  shift 3
  request "$out" "$@" -X POST -H 'Content-Type: application/json' --data "$body" "$url"
}

undo_mark() {
  local out="$LOG_DIR/undo.json" code
  code="$(post_json "$out" "$ANNOTATIONS_URL/$MEETING_ID" \
    "{\"ops\":[{\"op\":\"undo-operation\",\"operationId\":\"$MARK_OP_ID\"}]}" "${AUTH[@]}")" || return 1
  [[ "$code" == 200 ]] || return 1
  MARK_UNDONE=1
}

cleanup() {
  local rc=$?
  # Leave the recording and the archive root as they were found, pass or fail.
  if [[ -n "$MARK_OP_ID" && "$MARK_UNDONE" != 1 ]]; then
    undo_mark || printf '[annotations-route] could not undo operation %s on %s; remove it by hand\n' "$MARK_OP_ID" "$MEETING_ID" >&2
  fi
  if [[ -n "$PROBE_URL" ]]; then
    curl -sS -o /dev/null "${SERVICE_AUTH[@]}" -X DELETE "$PROBE_URL" || true
  fi
  if (( rc != 0 )); then
    printf '[annotations-route] failure evidence retained at %s\n' "$LOG_DIR" >&2
  fi
}
trap cleanup EXIT

# --- 1. the route and its body survive the proxy -----------------------------
log "POST annotations/meetings/<id> through the AppAPI proxy"
code="$(post_json "$LOG_DIR/pin.json" "$ANNOTATIONS_URL/$ABSENT_ID" \
  '{"ops":[{"op":"unmark","itemId":"mk_harness"}],"actorKind":"robot"}' "${AUTH[@]}")" \
  || fail "cannot reach annotations/meetings through the proxy"
[[ "$code" == 400 ]] \
  || fail "a body with an unknown actorKind returned HTTP $code, expected the operator's 400; the route may be undeclared, or the body did not reach the app"
jq -e '.error | test("actorKind")' "$LOG_DIR/pin.json" >/dev/null \
  || fail "the 400 did not carry the operator's reason: $(cat "$LOG_DIR/pin.json")"
success "✓ the POST body reached the operator (unknown actorKind refused with its reason)"

# --- 2. absent is 404 on both verbs ------------------------------------------
log "An id in nobody's archive"
code="$(post_json "$LOG_DIR/absent-post.body" "$ANNOTATIONS_URL/$ABSENT_ID" \
  '{"ops":[{"op":"unmark","itemId":"mk_harness"}]}' "${AUTH[@]}")" || fail "cannot POST"
[[ "$code" == 404 ]] || fail "POST to an absent meeting returned HTTP $code, expected 404"
code="$(request "$LOG_DIR/absent-get.body" "${AUTH[@]}" "$ANNOTATIONS_URL/$ABSENT_ID")" || fail "cannot GET"
[[ "$code" == 404 ]] || fail "GET of an absent meeting returned HTTP $code, expected 404"
success "✓ an absent meeting is 404 on GET and POST"

# --- 3. commit, read back, idempotence, the outsider -------------------------
if [[ -n "$MEETING_ID" ]]; then
  log "Mark $MEETING_ID as $ADMIN_USER"
  mark='{"ops":[{"op":"mark","tag":{"label":"harness-d737"},"target":{"kind":"meeting"}}],"actorKind":"agent","actorId":"mallory"}'
  code="$(post_json "$LOG_DIR/mark.json" "$ANNOTATIONS_URL/$MEETING_ID" "$mark" "${AUTH[@]}")" || fail "cannot POST a mark"
  [[ "$code" == 200 ]] || fail "marking $MEETING_ID returned HTTP $code: $(cat "$LOG_DIR/mark.json")"
  MARK_OP_ID="$(jq -er '.operationId' "$LOG_DIR/mark.json")" || fail "the commit named no operationId"
  added="$(jq -er '.added[0]' "$LOG_DIR/mark.json")" || fail "the commit added no mark"
  revision="$(jq -er '.revision' "$LOG_DIR/mark.json")" || fail "the commit reported no revision"
  jq -e --arg id "$added" --arg me "$ADMIN_USER" \
    '.annotations.items[] | select(.id == $id) | .actor == {"kind":"agent","id":$me}' "$LOG_DIR/mark.json" >/dev/null \
    || fail "the mark is not attributed to $ADMIN_USER as an agent: $(jq -c '.annotations.items' "$LOG_DIR/mark.json")"
  ! grep -q mallory "$LOG_DIR/mark.json" || fail "an actor id from the request body reached the recording"
  jq -e 'has("containerSha256") or has("audioOpusSha256") | not' "$LOG_DIR/mark.json" >/dev/null \
    || fail "a digest reached the wire"
  success "✓ the mark committed at revision $revision, attributed to $ADMIN_USER"

  code="$(request "$LOG_DIR/read.json" "${AUTH[@]}" "$ANNOTATIONS_URL/$MEETING_ID")" || fail "cannot GET"
  [[ "$code" == 200 ]] || fail "reading $MEETING_ID back returned HTTP $code"
  jq -e --argjson rev "$revision" --arg id "$added" \
    '.revision == $rev and ([.annotations.items[].id] | index($id) != null)' "$LOG_DIR/read.json" >/dev/null \
    || fail "GET does not show the committed mark: $(cat "$LOG_DIR/read.json")"
  success "✓ GET reads the mark back"

  code="$(post_json "$LOG_DIR/remark.json" "$ANNOTATIONS_URL/$MEETING_ID" "$mark" "${AUTH[@]}")" || fail "cannot POST"
  [[ "$code" == 200 ]] || fail "re-marking returned HTTP $code"
  jq -e '.added | length == 0' "$LOG_DIR/remark.json" >/dev/null || fail "re-marking the same target added a mark"
  success "✓ re-marking the same target is a no-op"

  log "The outsider, $OUTSIDER_USER"
  code="$(request "$LOG_DIR/outsider-get.body" "${OUTSIDER_AUTH[@]}" "$ANNOTATIONS_URL/$MEETING_ID")" || fail "cannot GET as the outsider"
  [[ "$code" == 404 ]] || fail "the outsider's GET returned HTTP $code, expected 404"
  cmp -s "$LOG_DIR/outsider-get.body" "$LOG_DIR/absent-get.body" \
    || fail "the outsider's 404 differs from an absent meeting's, which says the meeting exists"
  code="$(post_json "$LOG_DIR/outsider-post.body" "$ANNOTATIONS_URL/$MEETING_ID" "$mark" "${OUTSIDER_AUTH[@]}")" || fail "cannot POST as the outsider"
  [[ "$code" == 404 ]] || fail "the outsider's POST returned HTTP $code, expected 404"
  cmp -s "$LOG_DIR/outsider-post.body" "$LOG_DIR/absent-post.body" \
    || fail "the outsider's POST 404 differs from an absent meeting's"
  success "✓ a meeting the outsider cannot read answers exactly as an absent one"

  undo_mark || fail "could not undo operation $MARK_OP_ID"
  success "✓ the run's own marks are undone"
else
  log "No --meeting-id: skipping the commit, read-back and outsider checks"
fi

# --- 4. a stale If-Match is refused by the real Nextcloud --------------------
log "If-Match on the recordings tree, as the service account"
app_secret="$(docker exec "$CONTAINER" printenv APP_SECRET)" || fail "cannot read APP_SECRET from $CONTAINER"
app_id="$(docker exec "$CONTAINER" printenv APP_ID)" || fail "cannot read APP_ID from $CONTAINER"
app_version="$(docker exec "$CONTAINER" printenv APP_VERSION)" || fail "cannot read APP_VERSION from $CONTAINER"
aa_version="$(docker exec "$CONTAINER" printenv AA_VERSION 2>/dev/null || true)"
# setAppAPIDAVHeadersForUser in webdav_upload.go, verbatim.
SERVICE_AUTH=(
  -H "AUTHORIZATION-APP-API: $(printf '%s:%s' "$SERVICE_ACCOUNT" "$app_secret" | base64 -w0)"
  -H "EX-APP-ID: $app_id"
  -H "EX-APP-VERSION: $app_version"
)
[[ -z "$aa_version" ]] || SERVICE_AUTH+=(-H "AA-VERSION: $aa_version")

# The Team folder when there is one: that is the access-controlled model's
# mount, and the one the design names as the risk. The default model's private
# root otherwise.
root="" code=""
for candidate in Cassini/Recordings CassiniNoACL/Recordings; do
  code="$(curl -sS -o /dev/null -w '%{http_code}' "${SERVICE_AUTH[@]}" -X PROPFIND -H 'Depth: 0' "$SERVICE_DAV_URL/$candidate")" || true
  if [[ "$code" == 207 ]]; then root="$candidate"; break; fi
done
[[ -n "$root" ]] \
  || fail "the service account reaches neither archive root (last HTTP $code); check the AppAPI credentials are accepted from this host"
PROBE_URL="$SERVICE_DAV_URL/$root/.cassini-ifmatch-probe-$$.txt"

# put_probe <content> [if-match] prints "<status> <etag>".
put_probe() {
  local content="$1" if_match="${2:-}" headers="$LOG_DIR/probe-put.headers" code etag
  local -a args=(curl -sS "${SERVICE_AUTH[@]}" -X PUT -D "$headers" -o /dev/null -w '%{http_code}' --data-binary "$content")
  [[ -z "$if_match" ]] || args+=(-H "If-Match: $if_match")
  code="$("${args[@]}" "$PROBE_URL")" || return 1
  etag="$(grep -i '^etag:' "$headers" | tail -n 1 | sed -E 's/^[^:]*:[[:space:]]*//' | tr -d '\r' || true)"
  printf '%s %s\n' "$code" "$etag"
}

probe_content() {
  curl -sS "${SERVICE_AUTH[@]}" "$PROBE_URL"
}

read -r code _ <<<"$(put_probe v0 '"cassini-no-such-etag"')" || fail "cannot PUT the probe"
[[ "$code" == 412 ]] || fail "If-Match on a file that does not exist returned HTTP $code, expected 412"

read -r code etag_one <<<"$(put_probe v1)" || fail "cannot PUT the probe"
[[ "$code" == 201 || "$code" == 204 ]] || fail "creating the probe returned HTTP $code"
[[ -n "$etag_one" ]] || fail "Nextcloud answered the PUT without an ETag"

read -r code etag_two <<<"$(put_probe v2)" || fail "cannot PUT the probe"
[[ "$code" == 201 || "$code" == 204 ]] || fail "rewriting the probe returned HTTP $code"
[[ -n "$etag_two" && "$etag_two" != "$etag_one" ]] || fail "a rewrite did not change the ETag ($etag_one -> $etag_two)"

read -r code _ <<<"$(put_probe v3 "$etag_one")" || fail "cannot PUT the probe"
[[ "$code" == 412 ]] || fail "a stale If-Match returned HTTP $code, expected 412 -- Nextcloud accepted a lost update"
[[ "$(probe_content)" == v2 ]] || fail "a refused write changed the file"
success "✓ a stale If-Match is refused with 412 and changes nothing ($root)"

read -r code _ <<<"$(put_probe v3 "$etag_two")" || fail "cannot PUT the probe"
[[ "$code" == 201 || "$code" == 204 ]] || fail "the current If-Match returned HTTP $code, expected success"
[[ "$(probe_content)" == v3 ]] || fail "a write carrying the current ETag did not land"
success "✓ the current If-Match lands"

success "annotations route pinned through the AppAPI proxy; If-Match holds on the recordings tree"
