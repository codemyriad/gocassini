#!/usr/bin/env bash
# Faithful source-capture server leg: exact image -> AppAPI/HaRP install ->
# cassini_capture companion on a real Talk page -> upload through the AppAPI
# proxy -> bytes on the ExApp's disk.
#
# The browser e2e (cassini-app/e2e) proves the payload against a stub server,
# and the Go unit tests prove the handler against a synthetic request. Neither
# runs the seam between them. This leg does, without a browser, by standing in
# for the payload at the one point where its behaviour is fully specified: the
# multipart POST it makes after a call (cassini-app/src/capture/payload.ts,
# uploadCapture). Everything from that request onwards is real:
#
#   - Nextcloud's own session and AppAPI's proxy (ExAppProxyController): the
#     USER access level, the AUTHORIZATION-APP-API header the operator trusts
#     for the owner, and the multipart re-encoding the proxy performs.
#   - HaRP's tunnel into the installed ExApp container.
#   - The operator's Go handler: the CASSINI_SOURCE_CAPTURE gate, ownership
#     from the authenticated caller, the Talk membership check (acting as the
#     caller against a real Talk), staging and promotion to the capture root.
#   - The other direction of the bridge: the operator's enabled-edge sync of
#     `source_capture_enabled` into AppAPI's ExApp config, and the companion's
#     LoadAdditionalScriptsEvent listener reading it while a real Talk builds
#     a real call page.
#
# What it deliberately does NOT prove, so a green run is not mistaken for more:
# a browser joining a call, the encoded transform, OPFS, or the payload's own
# decision to upload. Those stay with cassini-app/e2e.
#
# Phases:
#   1. Tag the exact image; extract its built payload; package the companion
#      from the repo's PHP sources around that payload.
#   2. Bring up Nextcloud + AppAPI/HaRP + installed ExApp with
#      CASSINI_SOURCE_CAPTURE=1 (no media services: nothing here joins a call).
#   3. Install and enable cassini_capture in Nextcloud; create users and rooms.
#   4. Injection: the call page and Talk's index carry the payload script and
#      an enabled initial state for a logged-in participant; Files does not;
#      Talk's guest page does not; flipping the ExApp config flips the state.
#   5. Assets and permission poll through the proxy, as a user and anonymously.
#   6. Upload: a participant's synthetic capture lands byte-for-byte under the
#      capture root, owned by the authenticated user; a re-upload replaces it;
#      a non-participant, an anonymous caller, a malformed sidecar and a GET
#      are refused with the status the payload expects; a two-segment upload
#      (a device switch mid-call) lands whole.
#
# Evidence lands in $LOG_DIR (summary.json, every response body, container
# and compose logs). Teardown runs on every exit.

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
# shellcheck disable=SC1091 # SCRIPT_DIR is resolved dynamically above.
source "$SCRIPT_DIR/lib-exapp-manifest.sh"

: "${IMAGE_REF:?IMAGE_REF must name the exact pre-built CPU image under test}"
PROJECT_NAME="${PROJECT_NAME:-spreedtest}"
LOG_DIR="${LOG_DIR:-/tmp/cassini-installed-capture-$(date -u +%Y%m%dT%H%M%S)-$$}"
MANIFEST_PATH="$REPO_ROOT/appinfo/info.xml"
NEXTCLOUD_HOST_PORT="${NEXTCLOUD_HOST_PORT:-28080}"
NEXTCLOUD_URL="http://127.0.0.1:${NEXTCLOUD_HOST_PORT}"
PROXY_URL="$NEXTCLOUD_URL/index.php/apps/app_api/proxy/gocassini"
# The AppAPI proxy base the companion injects, relative to the webroot. The
# harness Nextcloud is served from the root, so the webroot is empty.
EXPECTED_PROXY_BASE="/index.php/apps/app_api/proxy/gocassini"
CANONICAL_LOCAL_IMAGE="cassini-exapp:e2e-v3-cpu-gpu"
EXAPP_CONTAINER="nc_app_gocassini"
EXAPP_ID="gocassini"
COMPANION_ID="cassini_capture"
# Where the ExApp image keeps cassini-app's build, including the capture
# bundles (deployment/Dockerfile.exapp CASSINI_VIEWER_DIST; capture_assets.go).
IMAGE_PAYLOAD_PATH="/opt/cassini/cassini-app/dist/capture/capture-payload.js"
# The standard viewer user the stack creates (lib/stack.sh
# harness_create_standard_viewer_user). DEV-ONLY committed test value.
ALICE="alice"
ALICE_PASSWORD='Tn8mY3qVrJ2x!E2e'
# A second account that is deliberately NOT a participant of the upload room.
BOB="bob"
BOB_PASSWORD='Rk7pW2sLq9Zx!E2e'
ADMIN_AUTH="admin:admin"
# Markers the companion leaves on a page (LoadTalkCaptureScriptListener):
# Nextcloud renders provideInitialState as a hidden input and addScript as a
# script tag under the app's js/ directory.
STATE_INPUT_ID="initial-state-${COMPANION_ID}-capture"
SCRIPT_MARK="${COMPANION_ID}/js/capture-payload.js"
# Must match captureSourceFormat (capture_upload.go) / SOURCE_CAPTURE_FORMAT
# (cassini-app/src/capture/protocol.ts).
CAPTURE_FORMAT="org.cassini.source-capture/1"
APP_VERSION="$(exapp_app_version "$MANIFEST_PATH")"
PRODUCTION_IMAGE="$(exapp_image_ref "$MANIFEST_PATH")"

RESULT="failed"
CLEANUP_RESULT="not-run"
STACK_STARTED=0
SOURCE_IMAGE_ID=""
CAPTURE_ROOT=""
ROOM_TOKEN=""
PUBLIC_ROOM_TOKEN=""
CHECKS_PASSED=()

STACK_TOPOLOGY=(
  --public-mode local-http
  --services appapi
  --cassini installed-exapp
  --recording-backend none
)

mkdir -p "$LOG_DIR"
log() { printf '[capture-e2e] %s\n' "$*" | tee -a "$LOG_DIR/orchestrator.log"; }
fail() { log "FAIL: $*"; exit 1; }
pass() { CHECKS_PASSED+=("$1"); log "ok: $1"; }
# A command that fails outside an explicit assertion still names itself, so an
# abort reads as a failure of a step rather than a silent exit.
trap 'log "FAIL: unhandled error at line $LINENO: $BASH_COMMAND"' ERR

compose() { docker compose -p "$PROJECT_NAME" -f "$REPO_ROOT/harness/compose.yml" "$@"; }
occ() { compose exec -T -u www-data nextcloud php occ "$@"; }
# Snapshots the ExApp container log to a file and prints the path. Callers
# grep the file: under pipefail a `docker logs | grep -q` fails whenever grep
# exits before docker has finished writing, i.e. exactly when the line IS there.
exapp_logs() {
  local snapshot
  snapshot="$LOG_DIR/exapp-log.$(date +%s%N).txt"
  docker logs "$EXAPP_CONTAINER" >"$snapshot" 2>&1 || true
  printf '%s\n' "$snapshot"
}

collect_diagnostics() {
  set +e
  docker ps -a --no-trunc >"$LOG_DIR/docker-ps.txt" 2>&1
  docker inspect "$EXAPP_CONTAINER" >"$LOG_DIR/installed-container.json" 2>&1
  docker logs "$EXAPP_CONTAINER" >"$LOG_DIR/installed-container.log" 2>&1
  compose ps -a >"$LOG_DIR/compose-ps.txt" 2>&1
  compose logs --no-color >"$LOG_DIR/compose.log" 2>&1
  occ app_api:app:config:list "$EXAPP_ID" >"$LOG_DIR/exapp-config.txt" 2>&1
  occ app:list >"$LOG_DIR/nextcloud-apps.txt" 2>&1
  curl -sS -u "$ADMIN_AUTH" "$PROXY_URL/operator/status" \
    >"$LOG_DIR/operator-status.json" 2>"$LOG_DIR/operator-status.err"
  set -e
}

verify_cleanup() {
  local leaked=0
  if docker ps -a --format '{{.Names}}' | grep -Eq "^(${EXAPP_CONTAINER}|appapi-harp|${PROJECT_NAME}-)"; then
    docker ps -a --format '{{.Names}} {{.Status}}' \
      | grep -E "^(${EXAPP_CONTAINER}|appapi-harp|${PROJECT_NAME}-)" \
      >"$LOG_DIR/leaked-containers.txt" || true
    leaked=1
  fi
  if docker network inspect "${PROJECT_NAME}_default" >/dev/null 2>&1; then
    leaked=1
  fi
  if docker volume ls --format '{{.Name}}' \
    | grep -Eq "^(${EXAPP_CONTAINER}_data|${PROJECT_NAME}_(db_data|nextcloud_data|appapi_harp_certs))$"; then
    docker volume ls --format '{{.Name}}' \
      | grep -E "^(${EXAPP_CONTAINER}_data|${PROJECT_NAME}_(db_data|nextcloud_data|appapi_harp_certs))$" \
      >"$LOG_DIR/leaked-volumes.txt" || true
    leaked=1
  fi
  (( leaked == 0 ))
}

write_summary() {
  local rc="$1"
  jq -n \
    --arg result "$RESULT" \
    --argjson exit_code "$rc" \
    --arg image_ref "$IMAGE_REF" \
    --arg source_image_id "$SOURCE_IMAGE_ID" \
    --arg app_version "$APP_VERSION" \
    --arg cleanup "$CLEANUP_RESULT" \
    --arg capture_root "$CAPTURE_ROOT" \
    --arg room_token "$ROOM_TOKEN" \
    --argjson checks "$(printf '%s\n' "${CHECKS_PASSED[@]:-}" | sed '/^$/d' | jq -R . | jq -s .)" \
    '{result:$result,exit_code:$exit_code,image_ref:$image_ref,source_image_id:$source_image_id,app_version:$app_version,cleanup:$cleanup,capture_root:$capture_root,room_token:$room_token,checks_passed:$checks}' \
    >"$LOG_DIR/summary.json" || true
}

finish() {
  local rc=$?
  trap - EXIT INT TERM
  collect_diagnostics
  if (( STACK_STARTED == 1 )); then
    log "tearing down through cassini dev stack"
    PROJECT_NAME="$PROJECT_NAME" \
      "$REPO_ROOT/bin/cassini" dev stack down --volumes "${STACK_TOPOLOGY[@]}" \
      >"$LOG_DIR/stack-down.log" 2>&1 || rc=1
  fi
  if verify_cleanup; then CLEANUP_RESULT="passed"; else CLEANUP_RESULT="failed"; rc=1; fi
  [[ "$rc" -ne 0 || "$RESULT" != "running" ]] || RESULT="passed"
  write_summary "$rc"
  log "result=$RESULT cleanup=$CLEANUP_RESULT evidence=$LOG_DIR/summary.json"
  exit "$rc"
}
trap finish EXIT INT TERM

# --- HTTP helpers -------------------------------------------------------------

# http_code AUTH METHOD URL OUT [curl args...] -> prints the status code, body
# to OUT. AUTH is user:password or empty for an anonymous request.
http_code() {
  local auth="$1" method="$2" url="$3" out="$4"
  shift 4
  local -a args=(-sS -o "$out" -w '%{http_code}' -X "$method")
  [[ -z "$auth" ]] || args+=(-u "$auth")
  curl "${args[@]}" "$@" "$url"
}

ocs() {
  local auth="$1" method="$2" url="$3" out="$4"
  shift 4
  http_code "$auth" "$method" "$url" "$out" -H 'OCS-APIRequest: true' -H 'Accept: application/json' "$@"
}

# page_state FILE -> the decoded companion initial state, or a non-zero exit
# when the page carries none.
page_state() {
  local value
  value="$(grep -o "<input[^>]*${STATE_INPUT_ID}[^>]*>" "$1" | sed -n 's/.*value="\([^"]*\)".*/\1/p' | head -n1)"
  [[ -n "$value" ]] || return 1
  printf '%s' "$value" | base64 -d
}

page_has_script() { grep -qF "$SCRIPT_MARK" "$1"; }

# assert_instrumented LABEL FILE EXPECTED_ENABLED: the page carries the script
# and an initial state naming the proxy base with the expected switch value.
assert_instrumented() {
  local label="$1" file="$2" expected="$3" state
  page_has_script "$file" || fail "$label: page carries no $SCRIPT_MARK script"
  state="$(page_state "$file")" || fail "$label: page carries no $STATE_INPUT_ID initial state"
  printf '%s\n' "$state" >"$LOG_DIR/state-$label.json"
  jq -e --argjson enabled "$expected" --arg base "$EXPECTED_PROXY_BASE" \
    '.enabled == $enabled and .proxyBase == $base' <<<"$state" >/dev/null \
    || fail "$label: unexpected initial state (want enabled=$expected proxyBase=$EXPECTED_PROXY_BASE): $state"
}

assert_not_instrumented() {
  local label="$1" file="$2"
  if page_has_script "$file"; then fail "$label: page unexpectedly carries $SCRIPT_MARK"; fi
  if page_state "$file" >/dev/null 2>&1; then fail "$label: page unexpectedly carries $STATE_INPUT_ID"; fi
}

# --- synthetic capture ----------------------------------------------------------

# make_segment FILE SECONDS: a real Opus-in-WebM file, the container and codec
# Chromium's MediaRecorder produces, so a later placement stage can read it.
make_segment() {
  ffmpeg -nostdin -loglevel error -y \
    -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=$2" \
    -c:a libopus -b:a 32k -f webm "$1"
}

# make_sidecar ROOM PARTICIPANT CALL_START_MS NAME... -> sidecar JSON on stdout,
# one segment per NAME, each 3 s long and back to back, with anchors that obey
# validateSidecar (capture_upload.go): monotonic frames and wall clock inside
# the segment window, RTP timestamps advancing on a 48 kHz clock.
make_sidecar() {
  local room="$1" participant="$2" start="$3"
  shift 3
  local -a names=("$@")
  local segments="[]" index=0 name seg_start seg_stop
  for name in "${names[@]}"; do
    seg_start=$((start + index * 3000))
    seg_stop=$((seg_start + 3000))
    segments="$(jq -c \
      --argjson index "$index" --arg name "$name" \
      --argjson start "$seg_start" --argjson stop "$seg_stop" \
      '. + [{
        index: $index, audioName: $name, mimeType: "audio/webm;codecs=opus",
        startWallMs: $start, stopWallMs: $stop, sampleRate: 48000, channelCount: 1,
        anchors: [
          {frameIndex: 0,   rtpTimestamp: 4000000,        ssrc: 42, wallMs: ($start + 20)},
          {frameIndex: 50,  rtpTimestamp: (4000000 + 48000), ssrc: 42, wallMs: ($start + 1020)},
          {frameIndex: 100, rtpTimestamp: (4000000 + 96000), ssrc: 42, wallMs: ($start + 2020)}
        ],
        muteIntervals: [[($start + 1200), ($start + 1700)]]
      }]' <<<"$segments")"
    index=$((index + 1))
  done
  jq -n \
    --arg format "$CAPTURE_FORMAT" --arg room "$room" --arg participant "$participant" \
    --argjson start "$start" --argjson end "$((start + index * 3000))" \
    --argjson segments "$segments" \
    '{format: $format, roomToken: $room, participantId: $participant,
      callStartWallMs: $start, callEndWallMs: $end,
      userAgent: "harness/ci-e2e-installed-exapp-capture", segments: $segments}'
}

# upload AUTH SIDECAR_FILE OUT SEGMENT_FILE... -> status code. The multipart
# shape is the payload's (uploadCapture in payload.ts): a "sidecar" part named
# capture.json and one "segments" part per audio file, named by audioName.
upload() {
  local auth="$1" sidecar="$2" out="$3"
  shift 3
  local -a parts=(-F "sidecar=@$sidecar;type=application/json;filename=capture.json")
  local segment index=0
  for segment in "$@"; do
    # One distinct field name per segment, mirroring the payload. A repeated
    # field name does not survive the proxy: it rebuilds the body through PHP,
    # which keeps only the last file under a repeated name.
    parts+=(-F "segment_$index=@$segment;type=audio/webm;filename=$(basename "$segment")")
    index=$((index + 1))
  done
  http_code "$auth" POST "$PROXY_URL/operator/capture/upload" "$out" "${parts[@]}"
}

exapp_file_sha256() { docker exec "$EXAPP_CONTAINER" sha256sum "$1" | cut -d' ' -f1; }

# --- preflight -------------------------------------------------------------------

for tool in docker curl jq ffmpeg sha256sum base64; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is required"
done
[[ -f "$MANIFEST_PATH" ]] || fail "manifest not found: $MANIFEST_PATH"

# --- 1. exact image + companion package -----------------------------------------

SOURCE_IMAGE_ID="$(docker image inspect "$IMAGE_REF" --format '{{.Id}}')" \
  || fail "image does not exist locally: $IMAGE_REF"
docker tag "$IMAGE_REF" "$CANONICAL_LOCAL_IMAGE"
log "exact image prepared: $IMAGE_REF -> $SOURCE_IMAGE_ID"

# The payload the companion delivers is the one this image serves as
# /ui/capture-payload.js: same source, same build. Taking it from the image
# rather than rebuilding it here keeps the leg free of a Node toolchain and
# makes the served-asset check below a byte-for-byte identity.
docker run --rm --entrypoint /bin/cat "$IMAGE_REF" "$IMAGE_PAYLOAD_PATH" >"$LOG_DIR/capture-payload.js" \
  || fail "image does not carry the built payload at $IMAGE_PAYLOAD_PATH"
[[ -s "$LOG_DIR/capture-payload.js" ]] || fail "extracted payload is empty"
PAYLOAD_SHA="$(sha256sum "$LOG_DIR/capture-payload.js" | cut -d' ' -f1)"
log "payload extracted from image: sha256=$PAYLOAD_SHA"

"$REPO_ROOT/scripts/build-capture-companion.sh" \
  --payload "$LOG_DIR/capture-payload.js" \
  --staging "$LOG_DIR/companion" \
  --output "$LOG_DIR/${COMPANION_ID}.tar.gz" >"$LOG_DIR/build-companion.log" 2>&1 \
  || { cat "$LOG_DIR/build-companion.log" >&2; fail "companion package build failed"; }
[[ -f "$LOG_DIR/companion/$COMPANION_ID/js/capture-payload.js" ]] || fail "companion staging tree lacks the payload"

# --- 2. stack --------------------------------------------------------------------

# The administrator switch, passed to AppAPI at registration exactly as a
# production install would (docs/source-audio-capture.md, "Trying it").
export CASSINI_SOURCE_CAPTURE=1
STACK_STARTED=1
if ! PROJECT_NAME="$PROJECT_NAME" NEXTCLOUD_HOST_PORT="$NEXTCLOUD_HOST_PORT" \
  "$REPO_ROOT/bin/cassini" dev stack up \
    "${STACK_TOPOLOGY[@]}" \
    --exapp-image-mode reuse-local \
    --reset \
    > >(tee "$LOG_DIR/stack-up.log") \
    2> >(tee "$LOG_DIR/stack-up.err" >&2); then
  fail "cassini dev stack up failed"
fi

docker inspect "$EXAPP_CONTAINER" >/dev/null 2>&1 || fail "AppAPI/HaRP did not create $EXAPP_CONTAINER"
installed_image_id="$(docker inspect "$EXAPP_CONTAINER" --format '{{.Image}}')"
[[ "$installed_image_id" == "$SOURCE_IMAGE_ID" ]] \
  || fail "installed ExApp image $installed_image_id differs from tested image $SOURCE_IMAGE_ID"
[[ "$(docker image inspect "$PRODUCTION_IMAGE" --format '{{.Id}}')" == "$SOURCE_IMAGE_ID" ]] \
  || fail "manifest production tag does not identify exact IMAGE_REF"

# AppAPI must have delivered the declared variable into the container; the
# handler's gate reads it there, so nothing below is meaningful otherwise.
docker inspect "$EXAPP_CONTAINER" --format '{{range .Config.Env}}{{println .}}{{end}}' >"$LOG_DIR/exapp-env.txt"
grep -qx 'CASSINI_SOURCE_CAPTURE=1' "$LOG_DIR/exapp-env.txt" \
  || fail "CASSINI_SOURCE_CAPTURE=1 did not reach the ExApp container environment"
pass "appapi delivers CASSINI_SOURCE_CAPTURE into the ExApp container"

# The operator announces where it stores captures at startup (run.go).
CAPTURE_ROOT="$(sed -n 's/.*capture_root -> \(.*\)$/\1/p' "$(exapp_logs)" | tail -n1)"
[[ -n "$CAPTURE_ROOT" ]] || fail "operator log does not announce capture_root"
log "capture root inside the ExApp: $CAPTURE_ROOT"

# Enabled-edge sync (nc_provision.go enabledCallback): the operator writes the
# switch into AppAPI's ExApp config, which is what the companion reads.
grep -qF 'source capture: synchronized companion initial state enabled=true' "$(exapp_logs)" \
  || fail "operator did not report synchronizing the companion initial state"
occ app_api:app:config:get "$EXAPP_ID" source_capture_enabled >"$LOG_DIR/exapp-config-switch.txt" 2>&1 \
  || fail "AppAPI has no source_capture_enabled ExApp config for $EXAPP_ID"
pass "operator synchronized the companion switch into AppAPI ExApp config"

# --- 3. companion install, users, rooms ------------------------------------------

log "installing $COMPANION_ID into Nextcloud"
compose cp "$LOG_DIR/companion/$COMPANION_ID" nextcloud:/var/www/html/custom_apps/
compose exec -T -u root nextcloud chown -R www-data:www-data "/var/www/html/custom_apps/$COMPANION_ID"
occ app:enable "$COMPANION_ID" >"$LOG_DIR/companion-enable.log" 2>&1 \
  || { cat "$LOG_DIR/companion-enable.log" >&2; fail "occ app:enable $COMPANION_ID failed"; }
occ app:list >"$LOG_DIR/nextcloud-apps-after-install.txt"
sed -n '/^Enabled:/,/^Disabled:/p' "$LOG_DIR/nextcloud-apps-after-install.txt" | grep -q "  - ${COMPANION_ID}:" \
  || fail "$COMPANION_ID is not enabled after install"
pass "companion app installs and enables on a real Nextcloud"

log "creating outsider user $BOB"
export OC_PASS="$BOB_PASSWORD"
compose exec -T -e OC_PASS -u www-data nextcloud php occ user:add --password-from-env --display-name=Bob "$BOB" >/dev/null
unset OC_PASS

# A group conversation: alice is added by the admin, bob is not. The upload
# handler's membership check must see exactly that difference.
ocs "$ADMIN_AUTH" POST "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v4/room" "$LOG_DIR/room-create.json" \
  --data-urlencode roomType=2 --data-urlencode "roomName=Capture leg $(date -u +%H%M%S)" >/dev/null
ROOM_TOKEN="$(jq -r '.ocs.data.token // empty' "$LOG_DIR/room-create.json")"
[[ -n "$ROOM_TOKEN" ]] || fail "group room creation returned no token: $(<"$LOG_DIR/room-create.json")"
code="$(ocs "$ADMIN_AUTH" POST "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v4/room/$ROOM_TOKEN/participants" \
  "$LOG_DIR/room-add-alice.json" --data-urlencode "newParticipant=$ALICE" --data-urlencode source=users)"
[[ "$code" == "200" ]] || fail "adding $ALICE to room $ROOM_TOKEN -> HTTP $code"
log "group room $ROOM_TOKEN: participants admin + $ALICE"

# A public conversation, for Talk's guest page.
ocs "$ADMIN_AUTH" POST "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v4/room" "$LOG_DIR/room-create-public.json" \
  --data-urlencode roomType=3 --data-urlencode "roomName=Capture guest $(date -u +%H%M%S)" >/dev/null
PUBLIC_ROOM_TOKEN="$(jq -r '.ocs.data.token // empty' "$LOG_DIR/room-create-public.json")"
[[ -n "$PUBLIC_ROOM_TOKEN" ]] || fail "public room creation returned no token"

# The premise the handler's membership check rests on: Talk lets a participant
# read the participant list and refuses everyone else. Assert it against this
# Talk rather than believe it.
code="$(ocs "$ALICE:$ALICE_PASSWORD" GET "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v4/room/$ROOM_TOKEN/participants" "$LOG_DIR/participants-alice.json")"
[[ "$code" == "200" ]] || fail "Talk refused the participant list to participant $ALICE (HTTP $code)"
code="$(ocs "$BOB:$BOB_PASSWORD" GET "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v4/room/$ROOM_TOKEN/participants" "$LOG_DIR/participants-bob.json")"
[[ "$code" != 2* ]] || fail "Talk served the participant list to non-participant $BOB (HTTP $code); the membership check has no basis"
pass "talk exposes room membership exactly as the upload handler assumes"

# --- 4. injection on real Talk pages ----------------------------------------------

RESULT="running"

code="$(http_code "$ALICE:$ALICE_PASSWORD" GET "$NEXTCLOUD_URL/index.php/call/$ROOM_TOKEN" "$LOG_DIR/page-call-alice.html")"
[[ "$code" == "200" ]] || fail "call page as $ALICE -> HTTP $code"
assert_instrumented call-page "$LOG_DIR/page-call-alice.html" true
pass "talk call page carries the companion script and an enabled initial state for a logged-in participant"

code="$(http_code "$ALICE:$ALICE_PASSWORD" GET "$NEXTCLOUD_URL/index.php/apps/spreed/" "$LOG_DIR/page-index-alice.html")"
[[ "$code" == "200" ]] || fail "Talk index as $ALICE -> HTTP $code"
assert_instrumented talk-index "$LOG_DIR/page-index-alice.html" true
pass "talk index page is instrumented too"

code="$(http_code "$ALICE:$ALICE_PASSWORD" GET "$NEXTCLOUD_URL/index.php/apps/files/" "$LOG_DIR/page-files-alice.html")"
[[ "$code" == "200" ]] || fail "Files page as $ALICE -> HTTP $code"
assert_not_instrumented files-page "$LOG_DIR/page-files-alice.html"
pass "files page is left alone"

code="$(http_code "" GET "$NEXTCLOUD_URL/index.php/call/$PUBLIC_ROOM_TOKEN" "$LOG_DIR/page-call-guest.html")"
[[ "$code" == "200" ]] || fail "guest call page -> HTTP $code (expected Talk's guest page)"
[[ -s "$LOG_DIR/page-call-guest.html" ]] || fail "guest call page is empty"
assert_not_instrumented guest-page "$LOG_DIR/page-call-guest.html"
pass "talk guest page is not instrumented"

# The companion's script URL resolves to the payload we packaged.
script_url="$(grep -o "[^\"']*${SCRIPT_MARK}[^\"']*" "$LOG_DIR/page-call-alice.html" | head -n1)"
[[ -n "$script_url" ]] || fail "could not extract the companion script URL from the call page"
case "$script_url" in
  http://*|https://*) ;;
  /*) script_url="$NEXTCLOUD_URL$script_url" ;;
  *) fail "unexpected companion script URL shape: $script_url" ;;
esac
code="$(http_code "$ALICE:$ALICE_PASSWORD" GET "$script_url" "$LOG_DIR/companion-script.js")"
[[ "$code" == "200" ]] || fail "companion script $script_url -> HTTP $code"
[[ "$(sha256sum "$LOG_DIR/companion-script.js" | cut -d' ' -f1)" == "$PAYLOAD_SHA" ]] \
  || fail "companion serves a payload that differs from the image's build"
pass "companion serves the image's own payload build"

# The switch, seen from the companion: flip AppAPI's ExApp config the way the
# operator's enabled-edge sync writes it, and the next page load must say no.
occ app_api:app:config:set --update-only --value false "$EXAPP_ID" source_capture_enabled >"$LOG_DIR/config-set-off.txt"
code="$(http_code "$ALICE:$ALICE_PASSWORD" GET "$NEXTCLOUD_URL/index.php/call/$ROOM_TOKEN" "$LOG_DIR/page-call-alice-off.html")"
[[ "$code" == "200" ]] || fail "call page with the switch off -> HTTP $code"
assert_instrumented call-page-off "$LOG_DIR/page-call-alice-off.html" false
occ app_api:app:config:set --update-only --value true "$EXAPP_ID" source_capture_enabled >"$LOG_DIR/config-set-on.txt"
code="$(http_code "$ALICE:$ALICE_PASSWORD" GET "$NEXTCLOUD_URL/index.php/call/$ROOM_TOKEN" "$LOG_DIR/page-call-alice-on.html")"
[[ "$code" == "200" ]] || fail "call page with the switch restored -> HTTP $code"
assert_instrumented call-page-on "$LOG_DIR/page-call-alice-on.html" true
pass "companion initial state follows the ExApp config switch without caching"

# --- 5. assets and permission poll through the proxy ------------------------------

code="$(http_code "$ALICE:$ALICE_PASSWORD" GET "$PROXY_URL/ui/capture-payload.js" "$LOG_DIR/proxy-payload.js")"
[[ "$code" == "200" ]] || fail "proxy ui/capture-payload.js as $ALICE -> HTTP $code"
[[ "$(sha256sum "$LOG_DIR/proxy-payload.js" | cut -d' ' -f1)" == "$PAYLOAD_SHA" ]] \
  || fail "proxy serves a payload that differs from the image's build"
code="$(http_code "$ALICE:$ALICE_PASSWORD" GET "$PROXY_URL/ui/capture-worker.js" "$LOG_DIR/proxy-worker.js")"
[[ "$code" == "200" && -s "$LOG_DIR/proxy-worker.js" ]] || fail "proxy ui/capture-worker.js as $ALICE -> HTTP $code"
code="$(http_code "" GET "$PROXY_URL/ui/capture-worker.js" "$LOG_DIR/proxy-worker-anon.txt")"
[[ "$code" != 2* ]] || fail "proxy served ui/capture-worker.js anonymously (HTTP $code)"
pass "capture assets are served through the proxy to a user and refused anonymously"

code="$(http_code "$ALICE:$ALICE_PASSWORD" GET "$PROXY_URL/operator/capture/enabled" "$LOG_DIR/proxy-enabled.json")"
[[ "$code" == "200" ]] || fail "proxy operator/capture/enabled as $ALICE -> HTTP $code"
jq -e '.enabled == true' "$LOG_DIR/proxy-enabled.json" >/dev/null \
  || fail "operator/capture/enabled did not answer enabled=true: $(<"$LOG_DIR/proxy-enabled.json")"
code="$(http_code "" GET "$PROXY_URL/operator/capture/enabled" "$LOG_DIR/proxy-enabled-anon.txt")"
[[ "$code" != 2* ]] || fail "proxy answered operator/capture/enabled anonymously (HTTP $code)"
pass "permission poll answers a user through the proxy and refuses anonymous callers"

# --- 6. upload -------------------------------------------------------------------

WORK="$LOG_DIR/upload"
mkdir -p "$WORK"
make_segment "$WORK/segment-0.webm" 3
SEGMENT_BYTES="$(stat -c%s "$WORK/segment-0.webm")"
SEGMENT_SHA="$(sha256sum "$WORK/segment-0.webm" | cut -d' ' -f1)"
(( SEGMENT_BYTES > 1000 )) || fail "synthetic segment is implausibly small ($SEGMENT_BYTES bytes)"
CALL_START_MS="$(( $(date +%s) * 1000 - 60000 ))"
make_sidecar "$ROOM_TOKEN" "$ALICE" "$CALL_START_MS" segment-0.webm >"$WORK/capture.json"

code="$(upload "$ALICE:$ALICE_PASSWORD" "$WORK/capture.json" "$LOG_DIR/upload-alice.json" "$WORK/segment-0.webm")"
[[ "$code" == "202" ]] || fail "upload as participant $ALICE -> HTTP $code: $(<"$LOG_DIR/upload-alice.json")"
jq -e --arg room "$ROOM_TOKEN" --argjson bytes "$SEGMENT_BYTES" \
  '.status == "accepted" and .room == $room and .segments == 1 and .bytes == $bytes' \
  "$LOG_DIR/upload-alice.json" >/dev/null \
  || fail "upload response does not account for the bytes sent: $(<"$LOG_DIR/upload-alice.json")"

final_dir="$CAPTURE_ROOT/$ROOM_TOKEN/$ALICE/$CALL_START_MS"
docker exec "$EXAPP_CONTAINER" test -f "$final_dir/capture.json" \
  || fail "no sidecar under $final_dir inside the ExApp"
docker exec "$EXAPP_CONTAINER" test -f "$final_dir/segment-0.webm" \
  || fail "no segment under $final_dir inside the ExApp"
[[ "$(exapp_file_sha256 "$final_dir/segment-0.webm")" == "$SEGMENT_SHA" ]] \
  || fail "segment on the ExApp's disk differs from what the participant sent"
docker exec "$EXAPP_CONTAINER" cat "$final_dir/capture.json" >"$LOG_DIR/stored-sidecar.json"
jq -e --arg owner "$ALICE" --arg room "$ROOM_TOKEN" --arg format "$CAPTURE_FORMAT" \
  '.ownerUserId == $owner and .roomToken == $room and .format == $format
   and (.receivedAt | type == "string" and length > 0)
   and (.segments | length == 1) and .segments[0].audioName == "segment-0.webm"
   and (.segments[0].anchors | length == 3) and (.segments[0].muteIntervals | length == 1)' \
  "$LOG_DIR/stored-sidecar.json" >/dev/null \
  || fail "stored sidecar is not the participant's, stamped with the authenticated owner: $(<"$LOG_DIR/stored-sidecar.json")"
grep -qF "capture upload: room=$ROOM_TOKEN owner=$ALICE segments=1 bytes=$SEGMENT_BYTES" "$(exapp_logs)" \
  || fail "operator log does not record the accepted upload"
pass "a participant's capture lands byte-for-byte under the capture root, owned by the authenticated user"

# A retry of the same call replaces rather than accumulates.
code="$(upload "$ALICE:$ALICE_PASSWORD" "$WORK/capture.json" "$LOG_DIR/upload-alice-again.json" "$WORK/segment-0.webm")"
[[ "$code" == "202" ]] || fail "re-upload as $ALICE -> HTTP $code"
entries="$(docker exec "$EXAPP_CONTAINER" ls -1 "$CAPTURE_ROOT/$ROOM_TOKEN/$ALICE")"
[[ "$entries" == "$CALL_START_MS" ]] || fail "re-upload left more than one directory: $entries"
docker exec "$EXAPP_CONTAINER" test -e "$final_dir.superseded" \
  && fail "re-upload left the superseded directory behind"
pass "a re-upload of the same call replaces the previous one"

# Refusals, with the status the payload acts on: 403 means discard, anything
# else non-2xx means keep the local copy and retry later (uploadCapture).
code="$(upload "$BOB:$BOB_PASSWORD" "$WORK/capture.json" "$LOG_DIR/upload-bob.txt" "$WORK/segment-0.webm")"
[[ "$code" == "403" ]] || fail "upload by non-participant $BOB -> HTTP $code (expected 403): $(<"$LOG_DIR/upload-bob.txt")"
docker exec "$EXAPP_CONTAINER" test -e "$CAPTURE_ROOT/$ROOM_TOKEN/$BOB" \
  && fail "a refused upload left files under $CAPTURE_ROOT/$ROOM_TOKEN/$BOB"
pass "a logged-in non-participant is refused with 403 and stores nothing"

code="$(upload "" "$WORK/capture.json" "$LOG_DIR/upload-anon.txt" "$WORK/segment-0.webm")"
[[ "$code" != 2* ]] || fail "anonymous upload was accepted (HTTP $code)"
pass "an anonymous upload is refused at the proxy (HTTP $code)"

jq '.format = "org.cassini.source-capture/0"' "$WORK/capture.json" >"$WORK/capture-bad-format.json"
code="$(upload "$ALICE:$ALICE_PASSWORD" "$WORK/capture-bad-format.json" "$LOG_DIR/upload-bad-format.txt" "$WORK/segment-0.webm")"
[[ "$code" == "400" ]] || fail "upload with an unknown format -> HTTP $code (expected 400)"
grep -q 'unsupported capture format' "$LOG_DIR/upload-bad-format.txt" \
  || fail "the handler's rejection reason did not survive the proxy: $(<"$LOG_DIR/upload-bad-format.txt")"
pass "the handler's 400 and its reason reach the client through the proxy"

code="$(http_code "$ALICE:$ALICE_PASSWORD" GET "$PROXY_URL/operator/capture/upload" "$LOG_DIR/upload-get.txt")"
[[ "$code" != 2* ]] || fail "GET on the upload route was accepted (HTTP $code)"
pass "the upload route admits only POST (GET -> HTTP $code)"

# Uploads that were not through staging must not linger: only promoted
# directories may exist under the root.
staging_left="$(docker exec "$EXAPP_CONTAINER" sh -c "ls -1d '$CAPTURE_ROOT'/upload-* 2>/dev/null" || true)"
[[ -z "$staging_left" ]] || fail "staging directories left behind: $staging_left"
pass "refused uploads leave no staging directory"

# A device switch mid-call yields two segments (capture_upload.go: "Segments
# are cut on track replacement"), and the sidecar names both, so both must land.
#
# THIS IS THE CHECK THAT FOUND THE DEFECT. Before the fix it answered
# 400 "missing segment": AppAPI's ExAppProxyController does not stream a
# multipart body, it rebuilds one from PHP's $_FILES, and PHP keeps only the
# LAST file sent under a repeated field name — so a two-segment capture reached
# the operator as one part. Every recording cut by a microphone change was
# silently lost. No tier below this one could see it: the browser fixture parses
# the multipart itself and the Go tests post straight to the handler.
#
# Segments are now identified by their file name on both sides, so the field
# names above exist only to stop the proxy collapsing the parts.
make_segment "$WORK/segment-1.webm" 3
MULTI_START_MS="$((CALL_START_MS + 120000))"
make_sidecar "$ROOM_TOKEN" "$ALICE" "$MULTI_START_MS" segment-0.webm segment-1.webm >"$WORK/capture-multi.json"
code="$(upload "$ALICE:$ALICE_PASSWORD" "$WORK/capture-multi.json" "$LOG_DIR/upload-multi.json" "$WORK/segment-0.webm" "$WORK/segment-1.webm")"
[[ "$code" == "202" ]] || fail "two-segment upload -> HTTP $code: $(<"$LOG_DIR/upload-multi.json")"
multi_dir="$CAPTURE_ROOT/$ROOM_TOKEN/$ALICE/$MULTI_START_MS"
for name in segment-0.webm segment-1.webm; do
  [[ "$(exapp_file_sha256 "$multi_dir/$name")" == "$(sha256sum "$WORK/$name" | cut -d' ' -f1)" ]] \
    || fail "two-segment upload: $name on disk differs from what was sent"
done
pass "a two-segment capture lands whole"

log "source-capture server leg passed: ${#CHECKS_PASSED[@]} checks against the exact image"
