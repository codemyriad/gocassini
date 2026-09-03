#!/usr/bin/env bash
# Missing source-capture vertical: exact installed ExApp + native companion +
# real Chromium participant + real Talk/HPB call -> browser multipart -> bytes
# under the ExApp capture root.
#
# The stub-browser leg already owns capture mechanics under simulated loss, and
# ci-e2e-installed-exapp-capture.sh already owns injection, proxy/refusal, and
# synthetic multipart coverage. This leg deliberately repeats none of those
# matrices. It proves only the seam between them, including the regression that
# matters most: Talk replaces Alice's microphone track and the browser upload
# arrives with multiple source-audio segments.
#
# Capture follows Talk's official recording, so both participants are subjects
# and both are asserted: Alice and Bob must each connect to the SFU, each buffer
# their own OPFS capture, each upload it, and each land under their OWN
# authenticated owner directory with every stored byte accounted for by the
# response their own browser saw. The differential control is the administrator,
# who never joined and must own nothing.
#
# Evidence lands in $LOG_DIR (summary.json, browser API response bodies and
# diagnostics, container/compose logs). Teardown runs on every exit.

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
# shellcheck disable=SC1091 # SCRIPT_DIR is resolved dynamically above.
source "$SCRIPT_DIR/lib-exapp-manifest.sh"

: "${IMAGE_REF:?IMAGE_REF must name the exact pre-built CPU image under test}"
PROJECT_NAME="${PROJECT_NAME:-cassini-browser-capture-$$}"
LOG_DIR="${LOG_DIR:-/tmp/cassini-browser-call-capture-$(date -u +%Y%m%dT%H%M%S)-$$}"
NEXTCLOUD_HOST_PORT="${NEXTCLOUD_HOST_PORT:-28080}"
NEXTCLOUD_URL="http://127.0.0.1:${NEXTCLOUD_HOST_PORT}"
PROXY_URL="$NEXTCLOUD_URL/index.php/apps/app_api/proxy/gocassini"
MANIFEST_PATH="$REPO_ROOT/appinfo/info.xml"
CANONICAL_LOCAL_IMAGE="cassini-exapp:e2e-v3-cpu-gpu"
PRODUCTION_IMAGE="$(exapp_image_ref "$MANIFEST_PATH")"
APP_VERSION="$(exapp_app_version "$MANIFEST_PATH")"
EXAPP_CONTAINER="nc_app_gocassini"
COMPANION_ID="cassini_capture"
IMAGE_PAYLOAD_PATH="/opt/cassini/cassini-app/dist/capture/capture-payload.js"
CAPTURE_FORMAT="org.cassini.source-capture/1"
ALICE="alice"
ALICE_PASSWORD='Tn8mY3qVrJ2x!E2e'
BOB="bob"
BOB_PASSWORD='Rk7pW2sLq9Zx!E2e'
BROWSER_PROCESS_TIMEOUT="${BROWSER_PROCESS_TIMEOUT:-340}"

RESULT="failed"
CLEANUP_RESULT="not-run"
STACK_STARTED=0
SOURCE_IMAGE_ID=""
INSTALLED_IMAGE_ID=""
CAPTURE_ROOT=""
CAPTURE_ROOT_BEFORE_CALL="unknown"
ROOM_TOKEN=""
ALICE_CALL_START_MS=""
ALICE_SEGMENT_COUNT=0
ALICE_BYTES=0
BOB_CALL_START_MS=""
BOB_SEGMENT_COUNT=0
BOB_BYTES=0
CAPTURE_ROOT_EMPTY_BEFORE_CALL=false
ADMIN_NO_CAPTURE=false
# Set by verify_owner_capture, which cannot return them: `fail` has to exit the
# script, and a command substitution would swallow that exit in a subshell.
OWNER_CALL_START_MS=""
OWNER_SEGMENT_COUNT=0
OWNER_BYTES=0
CHECKS_PASSED=()

STACK_TOPOLOGY=(
  --public-mode local-http
  --services full
  --cassini installed-exapp
  --recording-backend installed-exapp
)

mkdir -p "$LOG_DIR"
log() { printf '[browser-capture-e2e] %s\n' "$*" | tee -a "$LOG_DIR/orchestrator.log"; }
fail() { log "FAIL: $*"; exit 1; }
pass() { CHECKS_PASSED+=("$1"); log "ok: $1"; }
trap 'log "FAIL: unhandled error at line $LINENO: $BASH_COMMAND"' ERR

compose() { docker compose -p "$PROJECT_NAME" -f "$REPO_ROOT/harness/compose.yml" "$@"; }
occ() { compose exec -T -u www-data nextcloud php occ "$@"; }

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
  http_code "$auth" "$method" "$url" "$out" \
    -H 'OCS-APIRequest: true' -H 'Accept: application/json' "$@"
}

exapp_logs() {
  local snapshot
  snapshot="$LOG_DIR/exapp-log.$(date +%s%N).txt"
  docker logs "$EXAPP_CONTAINER" >"$snapshot" 2>&1 || true
  printf '%s\n' "$snapshot"
}

collect_diagnostics() {
  set +e
  docker ps -a --no-trunc >"$LOG_DIR/docker-ps.txt" 2>&1
  docker image inspect "$IMAGE_REF" >"$LOG_DIR/source-image.json" 2>&1
  docker inspect "$EXAPP_CONTAINER" >"$LOG_DIR/installed-container.json" 2>&1
  docker logs "$EXAPP_CONTAINER" >"$LOG_DIR/installed-container.log" 2>&1
  compose ps -a >"$LOG_DIR/compose-ps.txt" 2>&1
  compose logs --no-color >"$LOG_DIR/compose.log" 2>&1
  occ app:list >"$LOG_DIR/nextcloud-apps.txt" 2>&1
  occ app_api:app:config:list gocassini >"$LOG_DIR/exapp-config.txt" 2>&1
  curl -sS --max-time 10 -u admin:admin "$PROXY_URL/operator/status" \
    >"$LOG_DIR/operator-status.json" 2>"$LOG_DIR/operator-status.err"
  set -e
}

verify_cleanup() {
  local leaked=0
  if docker ps -a --format '{{.Names}}' \
    | grep -Eq "^(${EXAPP_CONTAINER}|cassini-exapp|appapi-harp|${PROJECT_NAME}[-_])"; then
    docker ps -a --format '{{.Names}} {{.Status}}' \
      | grep -E "^(${EXAPP_CONTAINER}|cassini-exapp|appapi-harp|${PROJECT_NAME}[-_])" \
      >"$LOG_DIR/leaked-containers.txt" || true
    leaked=1
  fi
  if docker network inspect "${PROJECT_NAME}_default" >/dev/null 2>&1; then
    docker network inspect "${PROJECT_NAME}_default" >"$LOG_DIR/leaked-network.json" 2>&1 || true
    leaked=1
  fi
  if docker volume ls --format '{{.Name}}' \
    | grep -Eq "^(cassini-exapp-state|cassini-exapp-site|${EXAPP_CONTAINER}_data|${PROJECT_NAME}_(db_data|nextcloud_data|appapi_harp_certs))$"; then
    docker volume ls --format '{{.Name}}' \
      | grep -E "^(cassini-exapp-state|cassini-exapp-site|${EXAPP_CONTAINER}_data|${PROJECT_NAME}_(db_data|nextcloud_data|appapi_harp_certs))$" \
      >"$LOG_DIR/leaked-volumes.txt" || true
    leaked=1
  fi
  (( leaked == 0 ))
}

assert_fresh_stack_targets() {
  local resources="$LOG_DIR/preflight-existing-resources.txt"
  {
    docker ps -a --filter "label=com.docker.compose.project=$PROJECT_NAME" \
      --format 'container:{{.Names}}'
    docker volume ls --filter "label=com.docker.compose.project=$PROJECT_NAME" \
      --format 'volume:{{.Name}}'
    docker network ls --filter "label=com.docker.compose.project=$PROJECT_NAME" \
      --format 'network:{{.Name}}'
    docker ps -a --format '{{.Names}}' \
      | grep -E '^(appapi-harp|cassini-exapp|nc_app_gocassini)$' \
      | sed 's/^/global-container:/' || true
    docker volume ls --format '{{.Name}}' \
      | grep -E '^(cassini-exapp-state|cassini-exapp-site|nc_app_gocassini_data)$' \
      | sed 's/^/global-volume:/' || true
  } | sort -u >"$resources"
  [[ ! -s "$resources" ]] \
    || fail "refusing --reset because harness target names already exist: $(tr '\n' ' ' <"$resources")"

  local listeners="$LOG_DIR/preflight-host-listeners.txt" port
  {
    ss -H -ltn
    ss -H -lun
  } >"$listeners"
  for port in "$NEXTCLOUD_HOST_PORT" 13479 14222 17088 28082 28088 28188; do
    if awk -v wanted="$port" '
      { local_address = $4; sub(/^.*:/, "", local_address) }
      local_address == wanted { found = 1 }
      END { exit !found }
    ' "$listeners"; then
      fail "full-stack host port $port is already in use; refusing to disturb its owner"
    fi
  done
}

# readable_json echoes a path holding parseable JSON, substituting a `null`
# document when the real one is missing or truncated, so a failed run still
# writes a summary instead of losing every other field with it.
readable_json() {
  local candidate="$1" fallback="$2"
  if jq -e . "$candidate" >/dev/null 2>&1; then
    printf '%s\n' "$candidate"
    return
  fi
  printf 'null\n' >"$fallback"
  printf '%s\n' "$fallback"
}

write_summary() {
  local rc="$1" browser_result stored_alice stored_bob
  browser_result="$(readable_json "$LOG_DIR/browser/result.json" "$LOG_DIR/browser-result.empty.json")"
  stored_alice="$(readable_json "$LOG_DIR/stored-sidecar-$ALICE.json" "$LOG_DIR/stored-sidecar-$ALICE.empty.json")"
  stored_bob="$(readable_json "$LOG_DIR/stored-sidecar-$BOB.json" "$LOG_DIR/stored-sidecar-$BOB.empty.json")"
  jq -n \
    --arg result "$RESULT" \
    --argjson exit_code "$rc" \
    --arg image_ref "$IMAGE_REF" \
    --arg source_image_id "$SOURCE_IMAGE_ID" \
    --arg installed_image_id "$INSTALLED_IMAGE_ID" \
    --arg app_version "$APP_VERSION" \
    --arg cleanup "$CLEANUP_RESULT" \
    --arg capture_root "$CAPTURE_ROOT" \
    --arg capture_root_before_call "$CAPTURE_ROOT_BEFORE_CALL" \
    --arg room_token "$ROOM_TOKEN" \
    --arg alice_call_start_ms "$ALICE_CALL_START_MS" \
    --argjson alice_segment_count "$ALICE_SEGMENT_COUNT" \
    --argjson alice_bytes "$ALICE_BYTES" \
    --arg bob_call_start_ms "$BOB_CALL_START_MS" \
    --argjson bob_segment_count "$BOB_SEGMENT_COUNT" \
    --argjson bob_bytes "$BOB_BYTES" \
    --argjson capture_root_empty "$CAPTURE_ROOT_EMPTY_BEFORE_CALL" \
    --argjson admin_no_capture "$ADMIN_NO_CAPTURE" \
    --argjson checks "$(printf '%s\n' "${CHECKS_PASSED[@]:-}" | sed '/^$/d' | jq -R . | jq -s .)" \
    --slurpfile browser "$browser_result" \
    --slurpfile alice_sidecar "$stored_alice" \
    --slurpfile bob_sidecar "$stored_bob" \
    '{result:$result,exit_code:$exit_code,image_ref:$image_ref,source_image_id:$source_image_id,
      installed_image_id:$installed_image_id,app_version:$app_version,cleanup:$cleanup,
      capture_root:$capture_root,room_token:$room_token,
      stored:{alice:{call_start_ms:$alice_call_start_ms,segment_count:$alice_segment_count,
          bytes:$alice_bytes,sidecar:$alice_sidecar[0]},
        bob:{call_start_ms:$bob_call_start_ms,segment_count:$bob_segment_count,
          bytes:$bob_bytes,sidecar:$bob_sidecar[0]}},
      controls:{capture_root_empty_before_call:$capture_root_empty,
        capture_root_state_before_call:$capture_root_before_call,uninvolved_user:"admin",
        uninvolved_user_stored_nothing:$admin_no_capture},
      browser:$browser[0],checks_passed:$checks}' \
    >"$LOG_DIR/summary.json" || true
}

finish() {
  local rc=$?
  trap - EXIT INT TERM
  collect_diagnostics
  if (( STACK_STARTED == 1 )); then
    log "tearing down through cassini dev stack"
    PROJECT_NAME="$PROJECT_NAME" "$REPO_ROOT/bin/cassini" dev stack down --volumes \
      "${STACK_TOPOLOGY[@]}" >"$LOG_DIR/stack-down.log" 2>&1 || rc=1
  fi
  if verify_cleanup; then CLEANUP_RESULT="passed"; else CLEANUP_RESULT="failed"; rc=1; fi
  if (( rc == 0 )) && [[ "$RESULT" == "running" ]]; then
    RESULT="passed"
  elif (( rc != 0 )); then
    RESULT="failed"
  fi
  write_summary "$rc"
  log "result=$RESULT cleanup=$CLEANUP_RESULT evidence=$LOG_DIR/summary.json"
  exit "$rc"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

for tool in docker curl jq node sha256sum base64 timeout go ss; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is required"
done
[[ -f "$MANIFEST_PATH" ]] || fail "manifest not found: $MANIFEST_PATH"
[[ -f "$SCRIPT_DIR/browser-call-capture.mjs" ]] || fail "Playwright driver is missing"
[[ "$PROJECT_NAME" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || fail "unsafe Compose project name: $PROJECT_NAME"
if [[ ! "$NEXTCLOUD_HOST_PORT" =~ ^[0-9]+$ ]] \
  || (( NEXTCLOUD_HOST_PORT <= 0 || NEXTCLOUD_HOST_PORT > 65535 )); then
  fail "NEXTCLOUD_HOST_PORT must be an integer from 1 through 65535"
fi
node -e 'import("@playwright/test")' >"$LOG_DIR/playwright-import.log" 2>&1 \
  || fail "@playwright/test is unavailable; run npm ci at the repository root"
assert_fresh_stack_targets

# Keep every Go process this leg invokes inside the machine's resource budget.
export GOMEMLIMIT="${GOMEMLIMIT:-2GiB}"
export GOFLAGS="-p=1"
export CASSINI_HARNESS_EXPECT_GPU_UNAVAILABLE="${CASSINI_HARNESS_EXPECT_GPU_UNAVAILABLE:-1}"
# Pinned rather than left to the default — which is also on — so the assertion
# below that it reached the container says what this leg needs.
export CASSINI_SOURCE_CAPTURE=1

SOURCE_IMAGE_ID="$(docker image inspect "$IMAGE_REF" --format '{{.Id}}')" \
  || fail "image does not exist locally: $IMAGE_REF"
docker tag "$IMAGE_REF" "$CANONICAL_LOCAL_IMAGE"
log "exact image prepared: $IMAGE_REF -> $SOURCE_IMAGE_ID"

docker run --rm --entrypoint /bin/cat "$IMAGE_REF" "$IMAGE_PAYLOAD_PATH" >"$LOG_DIR/capture-payload.js" \
  || fail "image does not carry $IMAGE_PAYLOAD_PATH"
[[ -s "$LOG_DIR/capture-payload.js" ]] || fail "capture payload extracted from the image is empty"
"$REPO_ROOT/scripts/build-capture-companion.sh" \
  --payload "$LOG_DIR/capture-payload.js" \
  --staging "$LOG_DIR/companion" \
  --output "$LOG_DIR/${COMPANION_ID}.tar.gz" \
  >"$LOG_DIR/build-companion.log" 2>&1 \
  || { sed -n '1,200p' "$LOG_DIR/build-companion.log" >&2; fail "companion package build failed"; }

STACK_STARTED=1
if ! PROJECT_NAME="$PROJECT_NAME" NEXTCLOUD_HOST_PORT="$NEXTCLOUD_HOST_PORT" \
  "$REPO_ROOT/bin/cassini" dev stack up "${STACK_TOPOLOGY[@]}" \
    --exapp-image-mode reuse-local --reset \
    > >(tee "$LOG_DIR/stack-up.log") 2> >(tee "$LOG_DIR/stack-up.err" >&2); then
  fail "cassini dev stack up failed"
fi

docker inspect "$EXAPP_CONTAINER" >/dev/null 2>&1 || fail "AppAPI/HaRP did not install $EXAPP_CONTAINER"
INSTALLED_IMAGE_ID="$(docker inspect "$EXAPP_CONTAINER" --format '{{.Image}}')"
[[ "$INSTALLED_IMAGE_ID" == "$SOURCE_IMAGE_ID" ]] \
  || fail "installed image $INSTALLED_IMAGE_ID differs from tested image $SOURCE_IMAGE_ID"
[[ "$(docker image inspect "$PRODUCTION_IMAGE" --format '{{.Id}}')" == "$SOURCE_IMAGE_ID" ]] \
  || fail "manifest production tag does not identify exact IMAGE_REF"
docker inspect "$EXAPP_CONTAINER" --format '{{range .Config.Env}}{{println .}}{{end}}' >"$LOG_DIR/exapp-env.txt"
grep -qx 'CASSINI_SOURCE_CAPTURE=1' "$LOG_DIR/exapp-env.txt" \
  || fail "CASSINI_SOURCE_CAPTURE=1 did not reach the installed ExApp"
pass "AppAPI/HaRP installed the exact image"

# Fresh harness users otherwise receive Nextcloud's first-run overlay on top of
# Talk. It is unrelated to the media path and can intercept the real join
# controls, so remove it before either browser logs in.
occ app:disable firstrunwizard >"$LOG_DIR/firstrunwizard-disable.log" 2>&1 \
  || fail "disabling the unrelated first-run wizard failed"

CAPTURE_ROOT="$(sed -n 's/.*capture_root -> \(.*\)$/\1/p' "$(exapp_logs)" | tail -n1)"
[[ -n "$CAPTURE_ROOT" ]] || fail "operator log does not announce capture_root"

log "installing the image's payload through the native companion"
compose cp "$LOG_DIR/companion/$COMPANION_ID" nextcloud:/var/www/html/custom_apps/
compose exec -T -u root nextcloud chown -R www-data:www-data "/var/www/html/custom_apps/$COMPANION_ID"
occ app:enable "$COMPANION_ID" >"$LOG_DIR/companion-enable.log" 2>&1 \
  || { sed -n '1,160p' "$LOG_DIR/companion-enable.log" >&2; fail "enabling $COMPANION_ID failed"; }

export OC_PASS="$BOB_PASSWORD"
compose exec -T -e OC_PASS -u www-data nextcloud php occ user:add \
  --password-from-env --display-name=Bob "$BOB" >"$LOG_DIR/user-add-bob.log" 2>&1
unset OC_PASS

code="$(ocs "$ALICE:$ALICE_PASSWORD" POST \
  "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v4/room" "$LOG_DIR/room-create.json" \
  --data-urlencode roomType=2 --data-urlencode "roomName=Browser capture $(date -u +%H%M%S)")"
[[ "$code" == "200" || "$code" == "201" ]] || fail "creating Alice-owned Talk room -> HTTP $code: $(<"$LOG_DIR/room-create.json")"
ROOM_TOKEN="$(jq -r '.ocs.data.token // empty' "$LOG_DIR/room-create.json")"
[[ -n "$ROOM_TOKEN" ]] || fail "Talk room creation returned no token"

code="$(ocs "$ALICE:$ALICE_PASSWORD" POST \
  "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v4/room/$ROOM_TOKEN/participants" \
  "$LOG_DIR/room-add-bob.json" --data-urlencode "newParticipant=$BOB" --data-urlencode source=users)"
[[ "$code" == "200" ]] || fail "adding Bob to $ROOM_TOKEN -> HTTP $code: $(<"$LOG_DIR/room-add-bob.json")"
code="$(ocs "$ALICE:$ALICE_PASSWORD" GET \
  "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v4/room/$ROOM_TOKEN/participants" \
  "$LOG_DIR/participants-before-browser.json")"
[[ "$code" == "200" ]] || fail "reading Talk participants -> HTTP $code"
pass "Alice owns a real Talk room containing Bob"

docker exec "$EXAPP_CONTAINER" test ! -e "$CAPTURE_ROOT/$ROOM_TOKEN" \
  || fail "negative control invalid: the new room already has a capture directory"
if docker exec "$EXAPP_CONTAINER" test -d "$CAPTURE_ROOT"; then
  CAPTURE_ROOT_BEFORE_CALL="present-without-room"
else
  CAPTURE_ROOT_BEFORE_CALL="absent-lazy"
fi
CAPTURE_ROOT_EMPTY_BEFORE_CALL=true
pass "negative control: capture root has no room directory before the call ($CAPTURE_ROOT_BEFORE_CALL)"

RESULT="running"
mkdir -p "$LOG_DIR/browser"
if ! NEXTCLOUD_URL="$NEXTCLOUD_URL" ROOM_TOKEN="$ROOM_TOKEN" \
  ALICE_PASSWORD="$ALICE_PASSWORD" BOB_PASSWORD="$BOB_PASSWORD" \
  BROWSER_LOG_DIR="$LOG_DIR/browser" \
  timeout --signal=TERM --kill-after=15s "${BROWSER_PROCESS_TIMEOUT}s" \
  node "$SCRIPT_DIR/browser-call-capture.mjs" \
    > >(tee "$LOG_DIR/browser/stdout.log") \
    2> >(tee "$LOG_DIR/browser/stderr.log" >&2); then
  fail "real-browser Talk call failed (see $LOG_DIR/browser/result.json)"
fi

# The contract the browser leg's result.json must satisfy. Kept between these
# markers because test-browser-capture-contract.sh extracts it verbatim and
# evaluates it offline against synthesized documents — an edit that weakens it
# has to fail there rather than pass unnoticed on a machine with no stack.
# BEGIN browser-result contract
BROWSER_RESULT_CONTRACT='
  .result == "passed"
  and .recording.callRecording == 2
  and (.alice.preRecordingOPFS | length) == 0
  and (.bob.preRecordingOPFS | length) == 0
  and (.alice.joinedBeforeRecordingOPFS | length) == 0
  and (.bob.joinedBeforeRecordingOPFS | length) == 0
  and (.alice.afterLeaveOPFS | length) == 0
  and (.bob.afterLeaveOPFS | length) == 0
  and (.alice.captureStorageKeys | length) == 0
  and (.bob.captureStorageKeys | length) == 0
  and .alice.mediaBeforeRecording.audioBytesSent > 2000
  and .bob.mediaBeforeRecording.audioBytesSent > 2000
  and .alice.mediaBeforeRecording.mediaDialogClicked == true
  and .bob.mediaBeforeRecording.mediaDialogClicked == true
  and any(.alice.mediaBeforeRecording.connections[]; .connectionState == "connected"
    and (.iceConnectionState == "connected" or .iceConnectionState == "completed")
    and .liveAudioSenders > 0 and .audioBytesSent > 2000)
  and any(.bob.mediaBeforeRecording.connections[]; .connectionState == "connected"
    and (.iceConnectionState == "connected" or .iceConnectionState == "completed")
    and .liveAudioSenders > 0 and .audioBytesSent > 2000)
  and .alice.mediaAfterSwitch.audioBytesSent >= (.alice.mediaImmediatelyAfterSwitch.audioBytesSent + 2000)
  and .alice.microphoneSwitch.mode == "distinct-device"
  and .alice.microphoneSwitch.before.deviceId != .alice.microphoneSwitch.after.deviceId
  and .alice.microphoneSwitch.before.trackId != .alice.microphoneSwitch.after.trackId
  and ([.alice.duringRecordingOPFS[].files[] | select(.name | test("^segment-[0-9]+\\.webm$"))] | length) >= 3
  and (.alice.reload.capturesBefore | length) == 1
  and (.alice.reload.capturesAfter | length) == 1
  and .alice.reload.capturesAfter[0] == .alice.reload.capturesBefore[0]
  and .alice.reload.segmentsAfter > .alice.reload.segmentsBefore
  and .alice.reload.preservedPreReloadBytes == true
  and .alice.mediaAfterReload.rejoined == true
  and .alice.mediaAfterReload.audioBytesSent > 2000
  and ([.bob.duringRecordingOPFS[].files[] | select(.name | test("^segment-[0-9]+\\.webm$"))] | length) >= 1
  and .alice.upload.status == 202
  and .bob.upload.status == 202
  and .alice.observedUploadRequestCount == 1
  and .bob.observedUploadRequestCount == 1
'
# END browser-result contract
jq -e "$BROWSER_RESULT_CONTRACT" "$LOG_DIR/browser/result.json" >/dev/null \
  || fail "browser result lacks connected media, a capture and accepted upload from each participant, or microphone rotation"
pass "both real browser participants sent SFU audio, captured themselves, and uploaded"
pass "neither browser stored anything of its own for capture"
pass "Talk's microphone selection replaced Alice's live sender and cut multiple browser segments"
pass "Alice reloaded mid-recording, rejoined, and the rejoined page resumed the same capture"

# verify_owner_capture asserts one participant's capture landed whole under
# their own owner directory and is reconciled byte-for-byte against the upload
# response their own browser saw. Results come back in OWNER_* rather than on
# stdout: `fail` must exit the script, and a command substitution would trap
# that exit inside a subshell and carry on.
verify_owner_capture() {
  local who="$1" owner="$2" min_segments="$3"
  local owner_root="$CAPTURE_ROOT/$ROOM_TOKEN/$owner"
  local sidecar="$LOG_DIR/stored-sidecar-$owner.json"
  docker exec "$EXAPP_CONTAINER" test -d "$owner_root" \
    || fail "accepted browser upload left no $owner owner directory under the capture root"
  OWNER_CALL_START_MS="$(docker exec "$EXAPP_CONTAINER" ls -1 "$owner_root")"
  [[ "$OWNER_CALL_START_MS" =~ ^[0-9]+$ ]] \
    || fail "expected exactly one numeric $owner call directory, got: $OWNER_CALL_START_MS"
  local final_dir="$owner_root/$OWNER_CALL_START_MS"
  docker exec "$EXAPP_CONTAINER" cat "$final_dir/capture.json" >"$sidecar" \
    || fail "stored $owner capture has no capture.json"

  jq -e --arg owner "$owner" --arg room "$ROOM_TOKEN" --arg format "$CAPTURE_FORMAT" \
    --arg start "$OWNER_CALL_START_MS" --argjson min "$min_segments" '
    (.segments | length) as $count
    | .format == $format
      and .roomToken == $room
      and .participantId == $owner
      and .ownerUserId == $owner
      and (.callStartWallMs | tostring) == $start
      and (.receivedAt | type == "string" and length > 0)
      and (.userAgent | test("Chrom(e|ium)"))
      and $count >= $min
      and ([.segments[].index] == [range(0; $count)])
      and all(.segments[]; .audioName == ("segment-" + (.index | tostring) + ".webm")
        and (.mimeType | startswith("audio/webm")))
  ' "$sidecar" >/dev/null \
    || fail "stored sidecar is not $owner's contiguous browser capture of this call"

  local -a segment_names
  mapfile -t segment_names < <(jq -r '.segments[].audioName' "$sidecar")
  OWNER_SEGMENT_COUNT="${#segment_names[@]}"
  (( OWNER_SEGMENT_COUNT >= min_segments )) \
    || fail "stored $owner capture has fewer than $min_segments segment(s)"
  OWNER_BYTES=0
  local name bytes
  printf 'name\tbytes\n' >"$LOG_DIR/stored-segments-$owner.tsv"
  for name in "${segment_names[@]}"; do
    [[ "$name" =~ ^segment-[0-9]+\.webm$ ]] || fail "unsafe or unexpected segment name: $name"
    bytes="$(docker exec "$EXAPP_CONTAINER" stat -c %s "$final_dir/$name")" \
      || fail "sidecar-declared segment is absent: $owner/$name"
    [[ "$bytes" =~ ^[0-9]+$ ]] || fail "$owner/$name has a non-numeric byte size: $bytes"
    (( bytes > 1000 )) || fail "$owner/$name is implausibly small ($bytes bytes)"
    OWNER_BYTES=$((OWNER_BYTES + bytes))
    printf '%s\t%s\n' "$name" "$bytes" >>"$LOG_DIR/stored-segments-$owner.tsv"
  done
  (( OWNER_BYTES > 10000 )) \
    || fail "$owner's browser capture is implausibly small in total ($OWNER_BYTES bytes)"

  jq -e --arg who "$who" --arg room "$ROOM_TOKEN" \
    --argjson segments "$OWNER_SEGMENT_COUNT" --argjson bytes "$OWNER_BYTES" '
    (.[$who].upload.body | fromjson) as $upload
    | $upload.status == "accepted"
      and $upload.room == $room
      and $upload.segments == $segments
      and $upload.bytes == $bytes
  ' "$LOG_DIR/browser/result.json" >/dev/null \
    || fail "$owner's browser-observed upload response does not account for every stored byte"

  grep -qF "capture upload: room=$ROOM_TOKEN owner=$owner segments=$OWNER_SEGMENT_COUNT bytes=$OWNER_BYTES" \
    "$(exapp_logs)" || fail "operator log does not account for $owner's browser upload"
}

verify_owner_capture alice "$ALICE" 3
ALICE_CALL_START_MS="$OWNER_CALL_START_MS"
ALICE_SEGMENT_COUNT="$OWNER_SEGMENT_COUNT"
ALICE_BYTES="$OWNER_BYTES"

verify_owner_capture bob "$BOB" 1
BOB_CALL_START_MS="$OWNER_CALL_START_MS"
BOB_SEGMENT_COUNT="$OWNER_SEGMENT_COUNT"
BOB_BYTES="$OWNER_BYTES"

[[ "$ALICE_CALL_START_MS" != "$BOB_CALL_START_MS" || "$ALICE_BYTES" != "$BOB_BYTES" ]] \
  || fail "Alice and Bob stored identical captures; one browser's audio was filed twice"

# The administrator never joined the call. An owner directory for them would
# mean the upload was attributed to the proxying identity rather than to the
# authenticated browser user.
docker exec "$EXAPP_CONTAINER" test ! -e "$CAPTURE_ROOT/$ROOM_TOKEN/admin" \
  || fail "capture was attributed to the room administrator instead of the browser user"
ADMIN_NO_CAPTURE=true
staging_left="$(docker exec "$EXAPP_CONTAINER" sh -c 'find "$1" -maxdepth 1 -type d -name "upload-*" -print' sh "$CAPTURE_ROOT" || true)"
[[ -z "$staging_left" ]] || fail "browser upload left staging directories behind: $staging_left"

pass "both browser-produced WebM captures landed byte-plausibly on the ExApp"
pass "disk paths and server-stamped sidecars attribute each capture to its own authenticated user"
log "real-browser Talk source-capture seam passed: alice $ALICE_SEGMENT_COUNT segments/$ALICE_BYTES bytes, bob $BOB_SEGMENT_COUNT segments/$BOB_BYTES bytes"

# ============================================================================
# Assert Talk recording lifecycle: operator job completion and source audio splice
# ============================================================================
log "waiting for operator job for room $ROOM_TOKEN to reach stage=done state=succeeded"

JOB_DEADLINE=$(( SECONDS + 300 ))
JOB_ID=""
JOB_STAGE=""
JOB_STATE=""
JOB_POLL_FILE="$LOG_DIR/jobs-poll.json"
JOB_DETAIL_FILE="$LOG_DIR/job-detail.json"

while (( SECONDS < JOB_DEADLINE )); do
  if curl -sS -u admin:admin "$PROXY_URL/operator/jobs" >"$JOB_POLL_FILE" 2>"$LOG_DIR/jobs-poll.err"; then
    JOB_INFO="$(jq -r --arg room "$ROOM_TOKEN" '
      [.[] | select((.request_json | test($room)) or ((.request_json | fromjson? | .roomToken // "") == $room))]
      | sort_by(.created_at) | last // empty
      | if . then "\(.id) \(.stage) \(.state)" else "" end
    ' "$JOB_POLL_FILE" 2>/dev/null || true)"

    if [[ -z "$JOB_INFO" ]]; then
      JOB_INFO="$(jq -r '
        if length == 1 then "\(.[0].id) \(.[0].stage) \(.[0].state)" else "" end
      ' "$JOB_POLL_FILE" 2>/dev/null || true)"
    fi

    if [[ -n "$JOB_INFO" ]]; then
      read -r j_id j_stage j_state <<<"$JOB_INFO"
      JOB_ID="$j_id"
      JOB_STAGE="$j_stage"
      JOB_STATE="$j_state"
      if [[ "$j_stage" == "done" && "$j_state" == "succeeded" ]]; then
        break
      fi
      if [[ "$j_state" == "failed" || "$j_state" == "interrupted" ]]; then
        fail "operator job $JOB_ID failed in stage=$j_stage: $(<"$JOB_POLL_FILE")"
      fi
    fi
  fi
  sleep 3
done

[[ -n "$JOB_ID" ]] || fail "timed out waiting for operator to register a job for room $ROOM_TOKEN"
[[ "$JOB_STAGE" == "done" && "$JOB_STATE" == "succeeded" ]] \
  || fail "operator job $JOB_ID did not reach stage=done state=succeeded within timeout (stage=$JOB_STAGE, state=$JOB_STATE)"
pass "operator job $JOB_ID reached stage=done state=succeeded"

# Fetch full job detail
curl -sS -u admin:admin "$PROXY_URL/operator/jobs/$JOB_ID" >"$JOB_DETAIL_FILE" 2>"$LOG_DIR/job-detail.err" \
  || fail "failed to fetch job detail for $JOB_ID"

CURRENT_ATTEMPT="$(jq -r '.job.current_attempt_number // 1' "$JOB_DETAIL_FILE")"
BUILD_LOG_PATH="$(jq -r --argjson att "$CURRENT_ATTEMPT" '
  .attempts[] | select(.attempt_number == $att) | .build_log_path // empty
' "$JOB_DETAIL_FILE" 2>/dev/null || true)"

if [[ -z "$BUILD_LOG_PATH" ]]; then
  BUILD_LOG_PATH="$(jq -r '.attempts[0].build_log_path // empty' "$JOB_DETAIL_FILE" 2>/dev/null || true)"
fi

if [[ -n "$BUILD_LOG_PATH" ]] && docker exec "$EXAPP_CONTAINER" test -f "$BUILD_LOG_PATH"; then
  docker exec "$EXAPP_CONTAINER" cat "$BUILD_LOG_PATH" >"$LOG_DIR/build.log"
else
  CANDIDATE_LOG="/nc_app_gocassini_data/operator/jobs/runs/${JOB_ID}--attempt-$(printf '%03d' "$CURRENT_ATTEMPT").logs/build.log"
  if docker exec "$EXAPP_CONTAINER" test -f "$CANDIDATE_LOG"; then
    docker exec "$EXAPP_CONTAINER" cat "$CANDIDATE_LOG" >"$LOG_DIR/build.log"
  else
    fail "build.log could not be located inside container for job $JOB_ID attempt $CURRENT_ATTEMPT"
  fi
fi

[[ -s "$LOG_DIR/build.log" ]] || fail "build.log is empty for job $JOB_ID"

# Assert build log for Alice
grep -Eiq "source audio: .*alice.* transcribing from participant capture spliced over [1-9][0-9]* ms of their recorded track \([1-9][0-9]*/[1-9][0-9]* segments, 0 skipped," \
  "$LOG_DIR/build.log" || {
    log "--- build.log excerpt ---"
    grep -E "source audio" "$LOG_DIR/build.log" || true
    fail "build log does not assert successful splice for Alice with 0 skipped"
  }
if grep -Eiq "source audio: .*alice.*keeping the recorded audio" "$LOG_DIR/build.log"; then
  fail "Alice has 'keeping the recorded audio' in build log"
fi

# Assert build log for Bob
grep -Eiq "source audio: .*bob.* transcribing from participant capture spliced over [1-9][0-9]* ms of their recorded track \([1-9][0-9]*/[1-9][0-9]* segments, 0 skipped," \
  "$LOG_DIR/build.log" || {
    log "--- build.log excerpt ---"
    grep -E "source audio" "$LOG_DIR/build.log" || true
    fail "build log does not assert successful splice for Bob with 0 skipped"
  }
if grep -Eiq "source audio: .*bob.*keeping the recorded audio" "$LOG_DIR/build.log"; then
  fail "Bob has 'keeping the recorded audio' in build log"
fi

pass "build log asserts participant capture splice for Alice and Bob with 0 skipped"

# Assert published meeting manifest records provenance.sourceAudio
CANONICAL_MEETING="$(jq -r '.job.artifact_meeting_path // empty' "$JOB_DETAIL_FILE" 2>/dev/null || true)"
ATTEMPT_MEETING="$(jq -r --argjson att "$CURRENT_ATTEMPT" '
  .attempts[] | select(.attempt_number == $att) | .artifact_meeting_path // empty
' "$JOB_DETAIL_FILE" 2>/dev/null || true)"

ARTIFACT_MEETING_PATH=""
for cand in "$CANONICAL_MEETING" "$ATTEMPT_MEETING" \
  "/nc_app_gocassini_data/operator/jobs/current/${JOB_ID}.meeting" \
  "/nc_app_gocassini_data/operator/jobs/runs/${JOB_ID}--attempt-$(printf '%03d' "$CURRENT_ATTEMPT").meeting"; do
  if [[ -n "$cand" ]] && docker exec "$EXAPP_CONTAINER" test -f "$cand/manifest.json"; then
    ARTIFACT_MEETING_PATH="$cand"
    break
  fi
done

[[ -n "$ARTIFACT_MEETING_PATH" ]] || fail "meeting manifest.json not found inside container for job $JOB_ID"
docker exec "$EXAPP_CONTAINER" cat "$ARTIFACT_MEETING_PATH/manifest.json" >"$LOG_DIR/meeting-manifest.json"

jq -e --arg alice "$ALICE" --arg bob "$BOB" '
  .provenance.sourceAudio as $sa
  | ($sa | type == "array" and length >= 2)
  and any($sa[]; (.owner == $alice or (.speaker_id | test($alice; "i"))) and .spliced_ms > 0 and .skipped == 0)
  and any($sa[]; (.owner == $bob or (.speaker_id | test($bob; "i"))) and .spliced_ms > 0 and .skipped == 0)
' "$LOG_DIR/meeting-manifest.json" >/dev/null || {
  log "--- meeting manifest.json provenance ---"
  jq '.provenance // {}' "$LOG_DIR/meeting-manifest.json" || true
  fail "meeting manifest provenance.sourceAudio does not record successful splice for both Alice and Bob"
}

pass "meeting manifest provenance.sourceAudio records successful splice for both Alice and Bob"

