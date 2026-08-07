#!/usr/bin/env bash
# Faithful D-453 product vertical: exact image -> AppAPI/HaRP install -> Talk -> viewer artifact.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
# shellcheck disable=SC1091 # SCRIPT_DIR is resolved dynamically above.
source "$SCRIPT_DIR/lib-exapp-manifest.sh"

: "${IMAGE_REF:?IMAGE_REF must name the exact pre-built CPU image under test}"
PROJECT_NAME="${PROJECT_NAME:-spreedtest}"
DURATION="${DURATION:-30}"
JOB_TIMEOUT="${JOB_TIMEOUT:-1200}"
POLL_INTERVAL="${POLL_INTERVAL:-5}"
LOG_DIR="${LOG_DIR:-/tmp/cassini-installed-e2e-$(date -u +%Y%m%dT%H%M%S)-$$}"
MANIFEST_PATH="${D453_MANIFEST_PATH:-$REPO_ROOT/appinfo/info.xml}"
MEDIA_PREFIX="${MEDIA_PREFIX:-$REPO_ROOT/harness/media/processed/showcase-lantern-festival-v1/mira}"
EXPECT_CONFIG_FAILURE="${D453_EXPECT_CONFIG_FAILURE:-0}"
FAIL_AT="${D453_FAIL_AT:-}"
CANONICAL_LOCAL_IMAGE="cassini-exapp:e2e-v3-cpu-gpu"
# The AppAPI proxy base for the installed ExApp. Hoisted here (it is a constant,
# with no post-stack dependency) so the D-403 sensitivity control can probe
# /operator/status at the stack-up failure boundary, where the container is
# still live but the positive assertions below have not been reached.
PROXY_URL="http://127.0.0.1:28080/index.php/apps/app_api/proxy/gocassini"
PRODUCTION_IMAGE="$(exapp_image_ref "$MANIFEST_PATH")"
APP_VERSION="$(exapp_app_version "$MANIFEST_PATH")"
RESULT="failed"
# Set to 1 only once the run commits to driving a real recording (just before the
# validator invocation), so write_summary can report recording_performed as a
# FACT about the run rather than a branch-hardcoded constant. The D-403 control
# path exits before this is ever set, so its summary honestly reports false.
RECORDING_ATTEMPTED=0
# Names the negative-control branch that fired, surfaced in summary.json. Empty
# on positive runs.
CONTROL_BRANCH=""
CLEANUP_RESULT="not-run"
SOURCE_IMAGE_ID=""
INSTALLED_IMAGE_ID=""
STACK_STARTED=0

STACK_TOPOLOGY=(
  --public-mode local-http
  --services full
  --cassini installed-exapp
  --recording-backend installed-exapp
)

mkdir -p "$LOG_DIR"
log() { printf '[installed-e2e] %s\n' "$*" | tee -a "$LOG_DIR/orchestrator.log"; }
fail() { log "FAIL: $*"; exit 1; }

# The D-453 hooks make a green run mean something weaker than "recorded a real
# Talk call", so a stray value inherited from a job env must fail this required
# gate loudly. Deliberate negative runs (the D-403 sensitivity control, manual
# testing) opt in with D453_ALLOW_TEST_HOOKS=1.
if [[ ( "$EXPECT_CONFIG_FAILURE" != 0 || -n "$FAIL_AT" ) && "${D453_ALLOW_TEST_HOOKS:-0}" != 1 ]]; then
  fail "D453_EXPECT_CONFIG_FAILURE/D453_FAIL_AT are set but D453_ALLOW_TEST_HOOKS=1 is not; refusing to weaken the gate"
fi

compose() { docker compose -p "$PROJECT_NAME" -f "$REPO_ROOT/harness/compose.yml" "$@"; }
occ() { compose exec -T -u www-data nextcloud php occ "$@"; }

collect_diagnostics() {
  set +e
  docker ps -a --no-trunc >"$LOG_DIR/docker-ps.txt" 2>&1
  docker image inspect "$IMAGE_REF" >"$LOG_DIR/source-image.json" 2>&1
  docker image inspect "$PRODUCTION_IMAGE" >"$LOG_DIR/production-image.json" 2>&1
  docker inspect nc_app_gocassini >"$LOG_DIR/installed-container.json" 2>&1
  docker logs nc_app_gocassini >"$LOG_DIR/installed-container.log" 2>&1
  compose ps -a >"$LOG_DIR/compose-ps.txt" 2>&1
  compose logs --no-color >"$LOG_DIR/compose.log" 2>&1
  occ app_api:daemon:list --output=json >"$LOG_DIR/daemon.json" 2>"$LOG_DIR/daemon.err"
  curl -sS -u admin:admin \
    "http://127.0.0.1:28080/index.php/apps/app_api/proxy/gocassini/operator/status" \
    >"$LOG_DIR/operator-status.json" 2>"$LOG_DIR/operator-status.err"
  curl -sS -u admin:admin \
    "http://127.0.0.1:28080/index.php/apps/app_api/proxy/gocassini/operator/jobs" \
    >"$LOG_DIR/jobs.json" 2>"$LOG_DIR/jobs.err"
  curl -sS -u admin:admin \
    "http://127.0.0.1:28080/index.php/apps/app_api/proxy/gocassini/published/catalog.json" \
    >"$LOG_DIR/catalog.json" 2>"$LOG_DIR/catalog.err"
  set -e
}

verify_cleanup() {
  local leaked=0
  if docker ps -a --format '{{.Names}}' | grep -Eq "^(nc_app_gocassini|appapi-harp|${PROJECT_NAME}-)"; then
    docker ps -a --format '{{.Names}} {{.Status}}' \
      | grep -E "^(nc_app_gocassini|appapi-harp|${PROJECT_NAME}-)" \
      >"$LOG_DIR/leaked-containers.txt" || true
    leaked=1
  fi
  if docker network inspect "${PROJECT_NAME}_default" >/dev/null 2>&1; then
    docker network inspect "${PROJECT_NAME}_default" >"$LOG_DIR/leaked-network.json" 2>&1 || true
    leaked=1
  fi
  if docker volume ls --format '{{.Name}}' \
    | grep -Eq "^(nc_app_gocassini_data|${PROJECT_NAME}_(db_data|nextcloud_data|appapi_harp_certs))$"; then
    docker volume ls --format '{{.Name}}' \
      | grep -E "^(nc_app_gocassini_data|${PROJECT_NAME}_(db_data|nextcloud_data|appapi_harp_certs))$" \
      >"$LOG_DIR/leaked-volumes.txt" || true
    leaked=1
  fi
  (( leaked == 0 ))
}

write_summary() {
  local rc="$1" validator_summary="$LOG_DIR/validator/summary.json"
  [[ -f "$validator_summary" ]] || printf '{}\n' >"$LOG_DIR/validator-summary.empty.json"
  [[ -f "$validator_summary" ]] || validator_summary="$LOG_DIR/validator-summary.empty.json"
  # The D-403 control probe writes the installed status body it asserted on to
  # control-status.json; fall back to a JSON null so the control object is always
  # well-formed (positive runs never write it).
  local control_status_file="$LOG_DIR/control-status.json"
  [[ -f "$control_status_file" ]] || printf 'null\n' >"$LOG_DIR/control-status.empty.json"
  [[ -f "$control_status_file" ]] || control_status_file="$LOG_DIR/control-status.empty.json"
  jq -n \
    --arg result "$RESULT" \
    --argjson exit_code "$rc" \
    --arg image_ref "$IMAGE_REF" \
    --arg source_image_id "$SOURCE_IMAGE_ID" \
    --arg installed_image_id "$INSTALLED_IMAGE_ID" \
    --arg production_image "$PRODUCTION_IMAGE" \
    --arg app_version "$APP_VERSION" \
    --arg manifest "$MANIFEST_PATH" \
    --arg cleanup "$CLEANUP_RESULT" \
    --arg expect_config_failure "$EXPECT_CONFIG_FAILURE" \
    --argjson recording_attempted "$RECORDING_ATTEMPTED" \
    --arg control_branch "$CONTROL_BRANCH" \
    --slurpfile validation "$validator_summary" \
    --slurpfile control_status "$control_status_file" \
    '{result:$result,exit_code:$exit_code,image_ref:$image_ref,source_image_id:$source_image_id,installed_image_id:$installed_image_id,production_image:$production_image,app_version:$app_version,manifest:$manifest,cleanup:$cleanup,control:{expected:($expect_config_failure == "1"),signaling_internal_secret_configured:($control_status[0].talk.signaling_internal_secret_configured),recording_performed:($recording_attempted == 1),branch:(if $control_branch == "" then null else $control_branch end)},validation:($validation[0] // {})}' \
    >"$LOG_DIR/summary.json" || true
}

finish() {
  local rc=$?
  trap - EXIT INT TERM
  collect_diagnostics
  if (( STACK_STARTED == 1 )); then
    log "tearing down through cassini dev stack"
    CASSINI_HARNESS_INFO_XML="$MANIFEST_PATH" PROJECT_NAME="$PROJECT_NAME" \
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

# Portable artifact validation runs the host CLI, whose transcript extraction
# probes and decodes the downloaded Opus file with ffprobe and ffmpeg.
for tool in docker curl jq python3 ffprobe ffmpeg; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is required"
done
[[ -f "$MANIFEST_PATH" ]] || fail "manifest not found: $MANIFEST_PATH"
[[ -s "$MEDIA_PREFIX.ivf" && -s "$MEDIA_PREFIX.ogg" ]] \
  || fail "materialize known media pair: $MEDIA_PREFIX.{ivf,ogg}"

SOURCE_IMAGE_ID="$(docker image inspect "$IMAGE_REF" --format '{{.Id}}')" \
  || fail "image does not exist locally: $IMAGE_REF"
docker tag "$IMAGE_REF" "$CANONICAL_LOCAL_IMAGE"
[[ "$(docker image inspect "$CANONICAL_LOCAL_IMAGE" --format '{{.Id}}')" == "$SOURCE_IMAGE_ID" ]] \
  || fail "canonical reuse-local tag does not identify IMAGE_REF"
log "exact image prepared: $IMAGE_REF -> $SOURCE_IMAGE_ID"

# Explicit flags mask ambient remote harness variables. D-493 owns every
# resource created below and the matching teardown in finish(). Mark ownership
# before invoking up so a partial setup failure is still torn down.
STACK_STARTED=1
if ! CASSINI_HARNESS_INFO_XML="$MANIFEST_PATH" PROJECT_NAME="$PROJECT_NAME" \
  "$REPO_ROOT/bin/cassini" dev stack up \
    "${STACK_TOPOLOGY[@]}" \
    --exapp-image-mode reuse-local \
    --reset \
    > >(tee "$LOG_DIR/stack-up.log") \
    2> >(tee "$LOG_DIR/stack-up.err" >&2); then
  if [[ "$EXPECT_CONFIG_FAILURE" == 1 ]]; then
    # D-403 sensitivity control. Stack up failed at route verification
    # (harness_verify_exapp_routes, lib/stack.sh) because the stripped manifest
    # never declared CASSINI_TALK_SIGNALING_INTERNAL_SECRET, so AppAPI dropped
    # the admin-supplied --env value and the operator reports the signaling
    # secret unconfigured. up.sh performs no teardown on failure, so the ExApp
    # container is still installed and reachable through the AppAPI proxy right
    # here — probe /operator/status directly and assert the PARSED value, not a
    # log string that lives in a shared library one reword from vacuous.
    #
    # The assertion is differential: the declared sibling (secret_configured)
    # must STILL be true, so the only difference the control observes is the
    # missing declaration — the exact D-403 boundary, not a wholesale env
    # injection failure or an empty .talk object.
    control_status="$(curl -fsS -u admin:admin "$PROXY_URL/operator/status")" \
      || fail "D-403 control: operator status unreachable after stack-up failure"
    printf '%s\n' "$control_status" >"$LOG_DIR/control-status.json"
    jq -e '.talk.signaling_internal_secret_configured == false and .talk.secret_configured == true' \
      <<<"$control_status" >/dev/null \
      || fail "D-403 control: stripped manifest did not suppress the signaling secret (status: $control_status)"
    RESULT="sensitivity-control-passed"
    CONTROL_BRANCH="stack-verification"
    log "D-403 sensitivity control passed: signaling secret reported unconfigured, recording never attempted"
    exit 0
  fi
  fail "cassini dev stack up failed"
fi

[[ "$FAIL_AT" != after-stack ]] || fail "forced failure after stack setup"

daemon_json="$(occ app_api:daemon:list --output=json)"
printf '%s\n' "$daemon_json" >"$LOG_DIR/daemon-asserted.json"
jq -e 'length == 1 and .[0].name == "harp_local" and .[0].deploy_id == "docker-install" and .[0].deploy_config.harp != null' \
  <<<"$daemon_json" >/dev/null || fail "AppAPI daemon is not the expected HaRP docker-install owner"

docker inspect nc_app_gocassini >/dev/null 2>&1 || fail "AppAPI/HaRP did not create nc_app_gocassini"
INSTALLED_IMAGE_ID="$(docker inspect nc_app_gocassini --format '{{.Image}}')"
production_id="$(docker image inspect "$PRODUCTION_IMAGE" --format '{{.Id}}')"
[[ "$production_id" == "$SOURCE_IMAGE_ID" ]] || fail "manifest production tag does not identify exact IMAGE_REF"
[[ "$INSTALLED_IMAGE_ID" == "$SOURCE_IMAGE_ID" ]] \
  || fail "installed ExApp image $INSTALLED_IMAGE_ID differs from tested image $SOURCE_IMAGE_ID"

# PROXY_URL is defined once near the top so the D-403 control can reuse it at
# the stack-up failure boundary above.
curl -fsS "$PROXY_URL/api/v1/welcome" | grep -q '"version":1' \
  || fail "welcome route did not return version=1"
status_json="$(curl -fsS -u admin:admin "$PROXY_URL/operator/status")"
printf '%s\n' "$status_json" >"$LOG_DIR/operator-status-asserted.json"
jq -e --arg version "$APP_VERSION" '.version == $version and .image_tag == $version' \
  <<<"$status_json" >/dev/null || fail "operator identity does not match manifest version $APP_VERSION"

# The D-403 negative control never reaches this point: a stripped manifest fails
# stack up at route verification, and the control asserts + exits at that failure
# boundary above. Reaching here therefore means EXPECT_CONFIG_FAILURE=0, so the
# positive Talk-configuration gate below is the only assertion this status feeds.
jq -e '.talk.secret_configured == true and .talk.signaling_internal_secret_configured == true and .talk.backend_url_override_configured == true' \
  <<<"$status_json" >/dev/null || fail "manifest-gated Talk configuration is incomplete"

RESULT="running"
# The run now commits to driving a real recording; record that as a fact so
# summary.json's control.recording_performed is true here and false on the D-403
# control path (which exited before this line).
RECORDING_ATTEMPTED=1
VALIDATOR_LOG_DIR="$LOG_DIR/validator"
LOG_DIR="$VALIDATOR_LOG_DIR" \
  "$SCRIPT_DIR/validate-installed-exapp-private-talk.sh" \
    --run-count 1 \
    --conversation admin \
    --media-prefix "$MEDIA_PREFIX" \
    --duration "$DURATION" \
    --job-timeout "$JOB_TIMEOUT" \
    --poll-interval "$POLL_INTERVAL" \
    --project-name "$PROJECT_NAME"

validator_summary="$VALIDATOR_LOG_DIR/summary.json"
jq -e '.result == "passed" and (.runs | length) == 1 and .runs[0].artifact.segment_count > 0 and .runs[0].artifact.word_count > 0' \
  "$validator_summary" >/dev/null || fail "validator summary lacks one positive segment/word result"
log "faithful product vertical passed with exact image, positive segments, and decoded words"
