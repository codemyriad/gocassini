#!/usr/bin/env bash
# Full Talk-driven recording roundtrip: trigger via Talk's recording-backend
# HTTP protocol → gocassini joins the call → captures audio → transcribes →
# publishes → Levenshtein-check the transcript against the scenario's
# expected text.
#
# This is the e2e that exercises the WHOLE merged stack:
#   - PR #22 (Talk recording-backend HMAC adapter)
#   - portable transcript production and extraction
#   - PR #30 (ExApp install handshake)
#   - PR #32 (bundled v3 model image)
# The ci-e2e-v3-transcript-verify.sh smoke verifies cassini build + model
# loading; this script additionally exercises the Talk recording-backend
# trigger path that PR #22 introduces.
#
# Phases:
#   1. Bring up Nextcloud + Talk + signaling (compose full profile)
#   2. Install + enable Talk (spreed app)
#   3. Run cassini-exapp container with TALK_RECORDING_SECRET set
#   4. Configure Talk's recording_servers to point at gocassini-operator
#      (verify via /api/v1/welcome that Talk can reach the backend)
#   5. Create a Talk call; spawn audio bot(s) playing a known scenario
#   6. Trigger Talk's start-recording via OCS; wait for the lifecycle to
#      complete (Talk → HMAC POST /api/v1/room/{token} → operator's
#      record job → recorder joins call → captures → stop → build →
#      publish; Talk receives status callbacks only, never an upload)
#   7. Wait for the published artifact in the gocassini container
#      (/srv/cassini-site/published/meetings/<id>.opus)
#   8. Assert Cassini uploaded NOTHING to Talk's recording store: the room
#      owner's <attachment folder>/Recording/<room token>/ is absent or
#      holds no files (D-551 — the .opus archive is the only path)
#   9. Decode the transcript back OUT of the published .opus (via
#      `cassini inspect --transcript`) and Levenshtein-check it against the
#      scenario's expected text — proving the .opus embeds the transcript
#
# This is the manual-install known-content quality roundtrip. All nine phases
# are implemented and gated; AppAPI is real, but the test starts the ExApp and
# injects its environment itself, so this does not exercise manifest gating.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$HARNESS_DIR/.." && pwd)"
COMPOSE_FILE="$HARNESS_DIR/compose.yml"
INFO_XML="$REPO_ROOT/appinfo/info.xml"
# shellcheck source=./lib/e2e-local.sh
source "$SCRIPT_DIR/lib/e2e-local.sh"
# shellcheck source=./lib-exapp-manifest.sh
source "$SCRIPT_DIR/lib-exapp-manifest.sh"
harness_e2e_local_stack_env full legacy none

# ---- Configuration --------------------------------------------------------

: "${IMAGE_REF:?IMAGE_REF must be set, e.g. ghcr.io/codemyriad/gocassini:latest}"

PROJECT_NAME="${PROJECT_NAME:-cassini-talk-rec-e2e-$$}"
APP_ID="${APP_ID:-$(exapp_app_id "$INFO_XML")}"
APP_VERSION="${APP_VERSION:-$(exapp_app_version "$INFO_XML")}"
APP_SECRET="${APP_SECRET:-$(head -c 24 /dev/urandom | base64 | tr -d '/+=' | head -c 32)}"
TALK_RECORDING_SECRET="${TALK_RECORDING_SECRET:-$(head -c 24 /dev/urandom | base64 | tr -d '/+=' | head -c 32)}"
SIGNALING_INTERNAL_SECRET="${SIGNALING_INTERNAL_SECRET:-6f4dca67263621ba7f9f9917e13de95a201f6f360be0d303e3008c2e6c8ad37d}"
SCENARIO_NAME="${SCENARIO_NAME:-showcase-lantern-festival-v1}"
SCENARIO_PATH="${SCENARIO_PATH:-$HARNESS_DIR/scenarios/showcase-lantern-festival.v1.json}"
SCENARIO_MEDIA_DIR="${SCENARIO_MEDIA_DIR:-$HARNESS_DIR/media/processed/showcase-lantern-festival-v1}"
RECORD_DURATION_SECONDS="${RECORD_DURATION_SECONDS:-40}"
MIN_LEVENSHTEIN="${MIN_LEVENSHTEIN:-0.40}"

# Host port for Nextcloud. Run-scoped by default (28100-28999, derived
# from the PID like PROJECT_NAME) so a leaked stack from an older,
# cancelled run can never block bringup with "port is already allocated"
# (gh issue #51). The stale-run sweep below removes such leaks anyway;
# the run-scoped port is belt-and-braces on top of it. The exported value
# flows into compose.yml's "${NEXTCLOUD_HOST_PORT:-28080}:80" mapping and
# into common.sh's NEXTCLOUD_URL default for callees (bootstrap.sh,
# stream-synthetic-meeting.sh). Other harness flows keep the stock 28080.
NEXTCLOUD_HOST_PORT="${NEXTCLOUD_HOST_PORT:-$(( 28100 + $$ % 900 ))}"
# janus binds ws_port 28188 on the host network (harness/config/janus/
# janus.transport.websockets.jcfg), which sits inside the derived range;
# step over it so PID % 900 == 88 cannot collide with our own stack.
if [ "$NEXTCLOUD_HOST_PORT" -eq 28188 ]; then
  NEXTCLOUD_HOST_PORT=28189
fi
export NEXTCLOUD_HOST_PORT
CONTAINER_NAME="${CONTAINER_NAME:-cassini-talk-rec-e2e-app}"
LOG_DIR="${LOG_DIR:-/tmp/cassini-talk-rec-e2e-$$}"
mkdir -p "$LOG_DIR"

# The standalone signaling server only accepts sessions for backend URLs
# in its allowlist; the stock config pins port 28080. Render a per-run
# copy with our run-scoped port; compose.yml mounts it via
# ${SIGNALING_CONF:-./config/signaling.conf}.
SIGNALING_CONF="$LOG_DIR/signaling.conf"
sed "s|:28080\$|:${NEXTCLOUD_HOST_PORT}|" \
  "$HARNESS_DIR/config/signaling.conf" >"$SIGNALING_CONF"
export SIGNALING_CONF

log()  { printf '[talk-rec-e2e] %s\n' "$*"; }
fail() { log "FAIL: $*"; exit 1; }
phase() { log "----- phase $1: $2 -----"; }
log "manual-install Talk roundtrip identity: $APP_ID@$APP_VERSION image=$IMAGE_REF"

compose() {
  local profile_args=()
  if [[ "${SPREED_PROFILE:-full}" == "full" ]]; then
    profile_args+=(--profile full)
  fi
  docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE" "${profile_args[@]}" "$@"
}
occ()     { compose exec -T -u www-data nextcloud php occ "$@"; }

# Bot streamer needs Nextcloud reachable from the host shell.
NC_URL_HOST="http://127.0.0.1:${NEXTCLOUD_HOST_PORT}"
# Operator runs with --network host so it sees the same loopback as
# Nextcloud's published host port and signaling. NC_URL_INTERNAL is the
# URL the operator (host-network) uses to reach Nextcloud.
NC_URL_INTERNAL="${NC_URL_INTERNAL:-http://127.0.0.1:${NEXTCLOUD_HOST_PORT}}"
# Operator listens on host port 28083 (avoids conflict with NC 28080,
# signaling 28082). Talk inside the compose network reaches it via the
# Docker bridge gateway.
OPERATOR_HOST_PORT="${OPERATOR_HOST_PORT:-28083}"
# Talk dials the recording-backend URL configured below; Talk is in the
# compose network and reaches the host-network operator via the bridge
# gateway. Note: Talk appends /api/v1/... paths itself; the configured
# URL is the base only.
TALK_BACKEND_URL_INTERNAL=""

cleanup() {
  local rc=$?
  log "cleanup (rc=$rc)"
  # Stop the bot streamer first so it doesn't keep dialing a dying stack.
  if [[ -n "${BOT_PID:-}" ]]; then
    kill "$BOT_PID" >/dev/null 2>&1 || true
  fi
  docker logs "$CONTAINER_NAME" >"$LOG_DIR/cassini-exapp.log" 2>&1 || true
  compose logs nextcloud >"$LOG_DIR/nextcloud.log" 2>&1 || true
  compose logs signaling >"$LOG_DIR/signaling.log" 2>&1 || true
  if [[ $rc -ne 0 ]]; then
    log "logs in $LOG_DIR"
    for f in cassini-exapp nextcloud signaling; do
      if [[ -s "$LOG_DIR/$f.log" ]]; then
        log "--- last 60 lines of $f ---"
        tail -n 60 "$LOG_DIR/$f.log" | sed 's/^/    /' || true
      fi
    done
    if [[ "${KEEP_ON_FAIL:-0}" == "1" ]]; then
      log "KEEP_ON_FAIL=1 — leaving stack up for debugging"
      log "  manual teardown: docker rm -f $CONTAINER_NAME; docker compose -p $PROJECT_NAME -f $COMPOSE_FILE down --volumes"
      return 0
    fi
  fi
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  compose down --volumes >/dev/null 2>&1 || true
}
trap cleanup EXIT
# A cancelled CI run delivers SIGINT (then SIGTERM) to the job's process
# group. Without handlers bash dies on the signal WITHOUT running the EXIT
# trap, and the restart: unless-stopped compose stack leaks (gh issue #51).
# `exit` inside a signal trap fires the EXIT trap exactly once; the
# stale-run sweep below is the backstop for the case where the runner
# SIGKILLs us before cleanup finishes.
trap 'exit 130' INT
trap 'exit 143' TERM

# ---- Stale-run sweep (gh issue #51) ----------------------------------------
# PROJECT_NAME is PID-scoped, the compose services are restart:
# unless-stopped, and a hard-cancelled run never finishes its cleanup trap.
# The leaked project outlives the run and — because the next run gets a
# fresh PID — no later `compose down` ever reaps it; one such leak held
# host port 28080 on the GPU runner for 11 days. Sweep every container
# matching our naming prefix or compose-project label, plus the matching
# compose networks/volumes, BEFORE bringing anything up.
#
# Safety: this cannot kill a concurrently-running sibling job. A GitHub
# runner process executes one job at a time (ubuntu-latest VMs are
# ephemeral and single-job; the self-hosted GPU box runs a single runner
# process, and GPU work is additionally serialized via the george-gpu
# concurrency group), and the sweep runs before this run creates anything
# — so every match is by definition debris from a previous, dead run. If
# you run two roundtrips concurrently on a workstation that assumption
# breaks: skip the sweep with CASSINI_E2E_SKIP_SWEEP=1.
sweep_stale_runs() {
  local stale
  stale=$(
    {
      # By container name: compose services (cassini-talk-rec-e2e-<pid>-*)
      # and the fixed-name exapp container (cassini-talk-rec-e2e-app).
      docker ps -aq --filter "name=cassini-talk-rec-e2e-" 2>/dev/null
      # By compose project label: catches services with a fixed
      # container_name that doesn't carry the project prefix (appapi-harp).
      docker ps -a --format '{{.ID}}\t{{.Label "com.docker.compose.project"}}' 2>/dev/null \
        | awk -F'\t' '$2 ~ /^cassini-talk-rec-e2e-/ {print $1}'
    } | sort -u
  ) || true
  if [[ -n "$stale" ]]; then
    log "sweeping stale e2e containers from previous runs: $(tr '\n' ' ' <<<"$stale")"
    xargs -r docker rm -f <<<"$stale" >/dev/null 2>&1 || true
  fi
  docker network ls --format '{{.Name}}' 2>/dev/null \
    | grep -E '^cassini-talk-rec-e2e-' \
    | xargs -r docker network rm >/dev/null 2>&1 || true
  docker volume ls -q 2>/dev/null \
    | grep -E '^cassini-talk-rec-e2e-' \
    | xargs -r docker volume rm >/dev/null 2>&1 || true
}
if [[ "${CASSINI_E2E_SKIP_SWEEP:-0}" != "1" ]]; then
  sweep_stale_runs
fi

# ============================================================================
# Phase 1: bring up Nextcloud + Talk signaling stack (compose full profile)
# ============================================================================
phase 1 "Bring up Nextcloud + signaling/janus/coturn (compose full profile)"

# Explicit stack topology: local HTTP, full media services, no installed
# ExApp, legacy recording backend (phase 5 re-points recording_servers at
# this script's own operator container). The run-scoped PROJECT_NAME,
# NEXTCLOUD_HOST_PORT, and SIGNALING_CONF exports above flow through
# `cassini dev stack up` into compose/bootstrap; plain `up` (no --reset)
# because the PID-scoped project is fresh by construction and the stale-run
# sweep above already reaped debris.
export PROJECT_NAME
"$REPO_ROOT/bin/cassini" dev stack up \
  --public-mode local-http \
  --services full \
  --cassini none \
  --recording-backend legacy \
  >"$LOG_DIR/stack-up.log" 2>&1 \
  || { tail -n 40 "$LOG_DIR/stack-up.log"; fail "cassini dev stack up failed"; }
log "OK stack up + bootstrap (Talk + signaling configured) at $NC_URL_HOST"

# ============================================================================
# Phase 2: install + enable AppAPI (needed for ExApp install)
# ============================================================================
phase 2 "Install + enable AppAPI"

occ app:install app_api >>"$LOG_DIR/occ.log" 2>&1 || true
occ app:enable  app_api >>"$LOG_DIR/occ.log" 2>&1
log "OK app_api enabled"

# Register a manual-install daemon pointing at our container.
occ app_api:daemon:register \
    manual_install \
    "Local manual install" \
    manual-install \
    http "$CONTAINER_NAME" "$NC_URL_INTERNAL" \
    >>"$LOG_DIR/occ.log" 2>&1 || log "WARN daemon may already be registered"

# ============================================================================
# Phase 3: run cassini-exapp container on the compose network
# ============================================================================
phase 3 "Start cassini-exapp ($IMAGE_REF) on the compose network"

docker pull "$IMAGE_REF" >>"$LOG_DIR/docker.log" 2>&1 || true

# Pre-create the bind-mount target subdirs so the operator's record doctor
# finds /var/lib/cassini-operator/tmp (the image ships it, but our bind
# mount replaces the image's dir). Same for /srv/cassini-site — without
# the bind mount, publish promote hits overlayfs EXDEV on dir rename.
mkdir -p "$LOG_DIR/operator-work/tmp" "$LOG_DIR/operator-work/jobs"
mkdir -p "$LOG_DIR/site"

# AppAPI lifecycle env (so the operator's middleware works) plus the
# Talk-recording-backend shared secret (so the HMAC adapter accepts
# Talk's POSTs).
# Override the default entrypoint (exapp-start.sh) which expects HaRP env
# vars (HP_FRP_ADDRESS etc) that we don't have outside a real AppAPI deploy.
# Run cassini-operator directly — same pattern as ci-e2e-install-exapp.sh.
# Detect CUDA variant from the image tag so the harness works against both
# CPU and GPU exapp images without a separate runbook. The CUDA image tags
# end in -cuda (per .github/workflows/publish-exapp-image.yml) and the
# CUDA-built sherpa-onnx requires `--gpus all` plus CASSINI_STT_DEVICE=cuda
# to actually exercise the GPU code path.
GPU_ARGS=()
STT_DEVICE_ENV=()
if [[ "$IMAGE_REF" == *cuda* || "${TALK_E2E_USE_GPU:-0}" == "1" ]]; then
  GPU_ARGS=(--gpus all)
  STT_DEVICE_ENV=(-e "CASSINI_STT_DEVICE=cuda")
  log "GPU mode: passing --gpus all and CASSINI_STT_DEVICE=cuda"
fi

# CASSINI_PUBLISH_SINK=local below: this harness registers only an AppAPI
# *daemon*, never the app, so the self-generated APP_SECRET is unknown to
# Nextcloud and every act-as-user WebDAV call 401s. It is a Talk-protocol
# test rather than a deployment, so it says so explicitly instead of letting
# the installed-app default aim it at Nextcloud Files (D-549).
docker run -d \
  --name "$CONTAINER_NAME" \
  --network host \
  "${GPU_ARGS[@]}" \
  -v "$LOG_DIR/operator-work:/var/lib/cassini-operator" \
  -v "$LOG_DIR/site:/srv/cassini-site" \
  -e "APP_HOST=0.0.0.0" \
  -e "APP_PORT=${OPERATOR_HOST_PORT}" \
  -e "APP_ID=$APP_ID" \
  -e "APP_VERSION=$APP_VERSION" \
  -e "APP_SECRET=$APP_SECRET" \
  -e "AA_VERSION=5.0.0" \
  -e "NEXTCLOUD_URL=$NC_URL_INTERNAL" \
  -e "TALK_RECORDING_SECRET=$TALK_RECORDING_SECRET" \
  -e "CASSINI_TALK_RECORDING_SECRET=$TALK_RECORDING_SECRET" \
  -e "CASSINI_TALK_SIGNALING_INTERNAL_SECRET=$SIGNALING_INTERNAL_SECRET" \
  -e "CASSINI_OPERATOR_BIND_ADDR=0.0.0.0:${OPERATOR_HOST_PORT}" \
  -e "CASSINI_OPERATOR_BASE_PATH=/operator" \
  -e "CASSINI_APPAPI_REQUIRED=true" \
  -e "CASSINI_PUBLISH_SINK=local" \
  "${STT_DEVICE_ENV[@]}" \
  --entrypoint /usr/local/bin/cassini-operator \
  "$IMAGE_REF" \
  >>"$LOG_DIR/docker.log" 2>&1

# Wait for /heartbeat to answer. Operator is on host network, listens on
# OPERATOR_HOST_PORT directly on the host's loopback.
log "waiting for cassini-exapp /heartbeat"
DEADLINE=$(( SECONDS + 60 ))
HB_OK=0
while (( SECONDS < DEADLINE )); do
  status=$(curl -s -o /dev/null -w '%{http_code}' \
    "http://127.0.0.1:${OPERATOR_HOST_PORT}/heartbeat" 2>/dev/null || echo 000)
  if [[ "$status" == "200" ]]; then HB_OK=1; break; fi
  sleep 2
done
[[ $HB_OK -eq 1 ]] || fail "cassini-exapp /heartbeat never reached 200"
log "OK cassini-exapp heartbeat 200"

# Resolve the docker bridge gateway IP for the compose network — that's
# the address Talk (inside the compose network) uses to reach the
# host-network operator.
BRIDGE_GATEWAY=$(docker network inspect "${PROJECT_NAME}_default" \
  -f '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null)
[[ -n "$BRIDGE_GATEWAY" ]] || fail "could not resolve compose-network gateway"
TALK_BACKEND_URL_INTERNAL="http://${BRIDGE_GATEWAY}:${OPERATOR_HOST_PORT}"
log "operator reachable from compose network at: $TALK_BACKEND_URL_INTERNAL"

# ============================================================================
# Phase 4: configure Talk's recording_servers + verify /api/v1/welcome
# ============================================================================
phase 4 "Configure Talk recording_servers + verify welcome handshake"

# Talk reads recording_servers as JSON from app config.
RECORDING_CFG=$(printf '{"servers":[{"server":"%s","verify":false}],"secret":"%s"}' \
  "$TALK_BACKEND_URL_INTERNAL" "$TALK_RECORDING_SECRET")
occ config:app:set spreed recording_servers --value "$RECORDING_CFG" \
  >>"$LOG_DIR/occ.log" 2>&1

# Talk welcome: GET /api/v1/welcome should answer {"version":1} from inside
# the Nextcloud container (Talk's reachable target).
WELCOME=$(compose exec -T nextcloud sh -c \
  "curl -sf $TALK_BACKEND_URL_INTERNAL/api/v1/welcome" 2>/dev/null || true)
if [[ "$WELCOME" != *'"version":1'* ]]; then
  fail "Talk welcome from Nextcloud → gocassini failed (got: ${WELCOME:-<empty>})"
fi
log "OK Talk reaches gocassini /api/v1/welcome"

# ============================================================================
# Phase 5: create Talk room + start scenario audio bots
# ============================================================================
phase 5 "Create Talk room + start scenario audio bots"

if [[ ! -d "$SCENARIO_MEDIA_DIR" ]]; then
  fail "scenario media not found: $SCENARIO_MEDIA_DIR
  Run: harness/bin/prepare-synthetic-meeting.sh \\
       --scenario $SCENARIO_PATH \\
       --output-dir $SCENARIO_MEDIA_DIR"
fi

# Create a Talk public room as admin via OCS (roomType=3 = public).
ROOM_NAME="cassini-talk-rec-e2e-$(date +%s)"
ROOM_RESP=$(curl -sf -u admin:admin \
  -H "OCS-APIRequest: true" \
  -H "Accept: application/json" \
  -H "Content-Type: application/json" \
  -d "{\"roomType\":3,\"roomName\":\"$ROOM_NAME\"}" \
  "$NC_URL_HOST/ocs/v2.php/apps/spreed/api/v4/room" \
  || fail "OCS create room failed")
ROOM_TOKEN=$(printf '%s' "$ROOM_RESP" | python3 -c \
  "import json,sys; print(json.load(sys.stdin)['ocs']['data']['token'])" \
  || fail "could not parse room token from: $ROOM_RESP")
CALL_URL="$NC_URL_HOST/call/$ROOM_TOKEN"
log "room token: $ROOM_TOKEN"
log "call URL:   $CALL_URL"

# Stream scenario audio into the call. The synthetic-meeting streamer
# launches go-talk-rotator bots (one per scenario participant) that
# join the call as guests and play their pre-rendered OGG track.
# Bots reach Talk's signaling via the host-network services on
# 127.0.0.1, same as ci-e2e-rejoin.sh's stream-video.sh.
BOT_LOG="$LOG_DIR/bots.log"
(
  # shellcheck disable=SC2097,SC2098 # the prefix exports the outer CALL_URL to the
  # subprocess; --call-url below expands that same outer value, not the prefix.
  CALL_URL="$CALL_URL" \
  SCENARIO="$SCENARIO_PATH" \
  OUTPUT_DIR="$SCENARIO_MEDIA_DIR" \
  PREPARE=0 \
  "$HARNESS_DIR/bin/stream-synthetic-meeting.sh" \
    --call-url "$CALL_URL"
) >"$BOT_LOG" 2>&1 &
BOT_PID=$!
log "bot streamer pid=$BOT_PID (log: $BOT_LOG)"

# Wait for at least one bot to actually be in the call before triggering
# recording — Talk rejects recording start with 400 when there's no active
# call. Poll Talk's "active call" state via OCS.
log "waiting for at least one participant in call (up to 90s)"
DEADLINE=$(( SECONDS + 90 ))
PARTICIPANTS=0
while (( SECONDS < DEADLINE )); do
  PEEK=$(curl -sf -u admin:admin \
    -H "OCS-APIRequest: true" \
    -H "Accept: application/json" \
    "$NC_URL_HOST/ocs/v2.php/apps/spreed/api/v4/room/$ROOM_TOKEN" \
    2>/dev/null || true)
  if [[ -n "$PEEK" ]]; then
    PARTICIPANTS=$(printf '%s' "$PEEK" | python3 -c \
      "import json,sys; d=json.load(sys.stdin)['ocs']['data']; print(1 if d.get('hasCall') else 0)" \
      2>/dev/null || echo 0)
  fi
  if [[ "$PARTICIPANTS" == "1" ]]; then break; fi
  sleep 3
done
if [[ "$PARTICIPANTS" != "1" ]]; then
  log "WARN: no active call detected after 90s; bot log tail:"
  tail -n 30 "$BOT_LOG" | sed 's/^/    /' || true
  # Continue anyway — Talk's reject behavior is informative
fi
log "OK active call detected (or proceeding anyway)"

# ============================================================================
# Phase 6: trigger Talk recording via OCS; Talk fires HMAC POST at gocassini
# ============================================================================
phase 6 "Trigger Talk recording via OCS POST /apps/spreed/api/v1/recording"

# Status: 1 = audio+video, 2 = audio only. Audio-only is enough for the
# transcript check and produces a smaller MKV.
TRIGGER_RESP=$(curl -sf -u admin:admin \
  -H "OCS-APIRequest: true" \
  -H "Accept: application/json" \
  -H "Content-Type: application/json" \
  -d '{"status":2}' \
  "$NC_URL_HOST/ocs/v2.php/apps/spreed/api/v1/recording/$ROOM_TOKEN" \
  || fail "OCS recording start failed; Talk side rejected the request")
log "OCS recording trigger response: ${TRIGGER_RESP:0:200}"

# Within a few seconds Talk should call /api/v1/room/$ROOM_TOKEN on
# gocassini with the HMAC headers. Confirm by tailing the operator log.
log "waiting for operator to log Talk-started callback (up to 30s)"
DEADLINE=$(( SECONDS + 30 ))
until docker logs "$CONTAINER_NAME" 2>&1 | grep -qE "record started id="; do
  if (( SECONDS > DEADLINE )); then
    log "operator log tail:"
    docker logs "$CONTAINER_NAME" 2>&1 | tail -n 40 | sed 's/^/    /'
    fail "operator never accepted the Talk-triggered record job"
  fi
  sleep 2
done
JOB_ID=$(docker logs "$CONTAINER_NAME" 2>&1 \
  | grep -oE 'record started id=[A-Z0-9]+' | head -n1 | sed 's/.*=//')
log "OK operator accepted Talk-triggered job: $JOB_ID"

# Let the recorder run for RECORD_DURATION_SECONDS, then trigger Talk's
# stop (status=0) so the operator transitions record → upload → build →
# publish. The Talk recording-backend protocol carries no duration: the
# recorder runs until Talk sends stop OR the room becomes empty.
log "recording for ${RECORD_DURATION_SECONDS}s before triggering Talk stop"
sleep "$RECORD_DURATION_SECONDS"

log "triggering Talk recording stop via OCS DELETE"
STOP_RESP=$(curl -sf -u admin:admin -X DELETE \
  -H "OCS-APIRequest: true" \
  -H "Accept: application/json" \
  "$NC_URL_HOST/ocs/v2.php/apps/spreed/api/v1/recording/$ROOM_TOKEN" \
  || true)
log "OCS recording stop response: ${STOP_RESP:0:200}"

# ============================================================================
# Phase 7: wait for the recording lifecycle to complete; pull transcript
# ============================================================================
phase 7 "Wait for record → build → publish; await published .opus"

# Total budget: bot audio plays for $RECORD_DURATION_SECONDS seconds, then
# recorder stops on empty room (default 8s grace), then build (transcribe
# v3 int8 on 30s of audio takes ~10-20s on CPU), then publish. Generous
# timeout:
PUBLISH_DEADLINE=$(( SECONDS + RECORD_DURATION_SECONDS + 180 ))
log "waiting for /srv/cassini-site/published/meetings/<id>.opus (up to $((PUBLISH_DEADLINE - SECONDS))s)"
PUBLISHED_OPUS=""
while (( SECONDS < PUBLISH_DEADLINE )); do
  CANDIDATE=$(docker exec "$CONTAINER_NAME" \
    sh -c 'ls /srv/cassini-site/published/meetings/*.opus 2>/dev/null | head -n1' \
    || true)
  if [[ -n "$CANDIDATE" ]] && \
     docker exec "$CONTAINER_NAME" test -s "$CANDIDATE"; then
    PUBLISHED_OPUS="$CANDIDATE"
    break
  fi
  sleep 4
done
[[ -n "$PUBLISHED_OPUS" ]] || fail "published .opus never appeared"
log "OK published meeting: $PUBLISHED_OPUS"

# Bot streamer should have exited by now (or we kill it).
wait "$BOT_PID" 2>/dev/null || true

# ============================================================================
# Phase 7b: assert the published catalog carries the room it was recorded in
# ============================================================================
phase 7b "Assert the published catalog carries the room"

# Every hop from Talk to catalog.json is unit-tested and nothing proves the
# chain. It has broken here before: the exporter re-derives a catalog entry's
# room from the file on every republish, so a room that reached the catalog
# once could be silently dropped by the next re-seal (D-640).
#
# The .opus lands before catalog.json — the local sink writes the catalog last
# — so the poll above is not enough to have it on disk yet.
CATALOG_HOST_PATH="$LOG_DIR/published-catalog.json"
CATALOG_DEADLINE=$(( SECONDS + 90 ))
CATALOG_READY=0
while (( SECONDS < CATALOG_DEADLINE )); do
  if docker exec "$CONTAINER_NAME" cat /srv/cassini-site/published/catalog.json \
       >"$CATALOG_HOST_PATH" 2>/dev/null &&
     CATALOG_PATH="$CATALOG_HOST_PATH" python3 -c \
       'import json,os,sys; sys.exit(0 if json.load(open(os.environ["CATALOG_PATH"])).get("meetings") else 1)' \
       2>/dev/null; then
    CATALOG_READY=1
    break
  fi
  sleep 3
done
(( CATALOG_READY == 1 )) || fail "published catalog.json never carried a meeting"

# roomId is a one-way derivation of the Talk token, never the token itself
# (D-622). Recomputing it here independently is the point: it proves the whole
# token -> id -> catalog path rather than that some string round-tripped. This
# harness leaves CASSINI_ROOM_ID_PEPPER unset, so the HMAC key is empty.
CATALOG_PATH="$CATALOG_HOST_PATH" \
EXPECT_ROOM_NAME="$ROOM_NAME" \
EXPECT_ROOM_TOKEN="$ROOM_TOKEN" \
python3 - <<'PY' || fail "published catalog does not carry the recorded room"
import hashlib
import hmac
import json
import os
import sys

with open(os.environ["CATALOG_PATH"], encoding="utf-8") as handle:
    catalog = json.load(handle)
meetings = catalog.get("meetings") or []
if len(meetings) != 1:
    print(f"expected exactly one published meeting, got {len(meetings)}", file=sys.stderr)
    sys.exit(1)

entry = meetings[0]
mac = hmac.new(b"", b"cassini.room.token.v1\x00" + os.environ["EXPECT_ROOM_TOKEN"].encode(), hashlib.sha256)
expected = {
    "roomName": os.environ["EXPECT_ROOM_NAME"],
    "roomId": "rm_" + mac.hexdigest()[:16],
}

ok = True
for field, want in expected.items():
    got = entry.get(field)
    if got != want:
        print(f"catalog {field} = {got!r}, want {want!r}", file=sys.stderr)
        ok = False
if not ok:
    print(f"catalog entry: {json.dumps(entry, sort_keys=True)}", file=sys.stderr)
sys.exit(0 if ok else 1)
PY
log "OK catalog carries room: name=$ROOM_NAME"

# ============================================================================
# Phase 8: assert Cassini uploaded NOTHING to Talk's recording store
# ============================================================================
phase 8 "Assert Cassini uploaded nothing to Talk's recording store"

# Cassini never uses Talk's OCS recording-store endpoint, for any file
# (D-551). A meeting reaches Nextcloud exactly once, as the published .opus
# under the canonical recordings root — the only tree covered by the per-file
# ACL model the read proxy enforces (D-521). A raw .mkv filed into the room
# owner's Talk attachment folder would be an unmanaged parallel copy of the
# same meeting, outside that model entirely.
#
# spreed would file such an upload at <attachment folder>/Recording/<token>/,
# defaulting to /Talk/Recording/<token>/ (spreed lib/Config.php
# getRecordingFolder() + lib/Service/RecordingService.php). Phase 7 already
# waited for publish, which is sequenced after the point where the upload
# used to happen, so there is nothing to poll for: either the folder is
# absent, or it exists and holds no files.
RECORDING_OWNER="${RECORDING_OWNER:-admin}"
RECORDING_OWNER_PASSWORD="${RECORDING_OWNER_PASSWORD:-admin}"
DAV_RECORDING_DIR="${DAV_RECORDING_DIR:-$NC_URL_HOST/remote.php/dav/files/$RECORDING_OWNER/Talk/Recording/$ROOM_TOKEN}"

DAV_PROPFIND="$LOG_DIR/dav-recording-propfind.xml"
DAV_STATUS=$(curl -s -o "$DAV_PROPFIND" -w '%{http_code}' \
  -u "$RECORDING_OWNER:$RECORDING_OWNER_PASSWORD" \
  -X PROPFIND -H "Depth: 1" -H "Content-Type: application/xml" \
  --data '<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:getcontentlength/><d:resourcetype/></d:prop></d:propfind>' \
  "$DAV_RECORDING_DIR/" || true)

case "$DAV_STATUS" in
  404|405)
    log "OK no Talk recording folder for the room ($DAV_STATUS) — nothing was uploaded to Talk"
    ;;
  207)
    # The folder exists (a previous run, an unrelated Talk recording, or a
    # pre-created tree). It must contain no files — only the collection itself.
    python3 - "$DAV_PROPFIND" <<'PY' || fail "Cassini uploaded a file to Talk's recording store (PROPFIND response: $DAV_PROPFIND)"
import sys
import xml.etree.ElementTree as ET

ns = {"d": "DAV:"}
files = []
for resp in ET.parse(sys.argv[1]).getroot().findall("d:response", ns):
    href = (resp.findtext("d:href", "", ns) or "").strip()
    # A collection carries <d:collection/> inside <d:resourcetype>; anything
    # without it is a file.
    is_collection = any(
        el.find("{DAV:}collection") is not None for el in resp.iter("{DAV:}resourcetype")
    )
    if is_collection:
        continue
    sizes = [
        int(el.text)
        for el in resp.iter("{DAV:}getcontentlength")
        if (el.text or "").strip().isdigit()
    ]
    files.append((href, sizes[0] if sizes else -1))

if files:
    print(
        "[talk-rec-e2e] FAIL Talk's recording folder is not empty — Cassini "
        f"must never upload through the recording store (D-551): {files}",
        file=sys.stderr,
    )
    sys.exit(1)
PY
    log "OK Talk recording folder exists but holds no files — nothing was uploaded to Talk"
    ;;
  *)
    sed 's/^/    /' "$DAV_PROPFIND" 2>/dev/null | head -n 20 || true
    fail "PROPFIND $DAV_RECORDING_DIR/ returned unexpected HTTP $DAV_STATUS"
    ;;
esac

# ============================================================================
# Phase 9: Levenshtein-check transcript vs scenario expected text
# ============================================================================
phase 9 "Levenshtein-check transcript vs scenario expected text"

# Recover the transcript by decoding it back OUT of the published .opus, not
# from the build bundle. This proves the published artifact actually EMBEDS the
# expected transcript (D-429): `cassini inspect --transcript` reads the default
# words transcript from the portable .opus's CASSINI_TX_<id>_PAYLOAD_* tags and
# emits transcript.words.v1.json — the exact inverse of the transcript
# packer. The Levenshtein check below then runs against THAT .opus-derived
# transcript.
TRANSCRIPT_HOST="$LOG_DIR/transcript.words.v1.json"
if ! docker exec "$CONTAINER_NAME" \
  cassini inspect --transcript "$PUBLISHED_OPUS" >"$TRANSCRIPT_HOST" 2>"$LOG_DIR/inspect-transcript.err"; then
  sed 's/^/    /' "$LOG_DIR/inspect-transcript.err" 2>/dev/null | head -n 20 || true
  fail "cassini inspect --transcript failed to decode transcript from published .opus: $PUBLISHED_OPUS"
fi
if [[ ! -s "$TRANSCRIPT_HOST" ]]; then
  fail "decoded transcript from $PUBLISHED_OPUS is empty"
fi
log "OK transcript decoded from published .opus: $TRANSCRIPT_HOST ($(wc -c <"$TRANSCRIPT_HOST") bytes)"

EXPECTED_TEXT=$(python3 - "$SCENARIO_PATH" <<'PY'
import json, sys
with open(sys.argv[1]) as f:
    data = json.load(f)
print(" ".join(t["text"] for t in data.get("turns", [])))
PY
)
if [[ -z "$EXPECTED_TEXT" ]]; then
  fail "could not derive expected text from $SCENARIO_PATH"
fi

python3 - "$TRANSCRIPT_HOST" "$EXPECTED_TEXT" "$MIN_LEVENSHTEIN" "${MIN_CAPTURED_RUN_WORDS:-6}" <<'PY'
import json, re, sys, unicodedata

transcript_path = sys.argv[1]
expected = sys.argv[2]
min_ratio = float(sys.argv[3])
min_captured_run = int(sys.argv[4])

def normalise(text: str) -> str:
    text = unicodedata.normalize("NFKD", text)
    text = "".join(c for c in text if not unicodedata.combining(c))
    text = text.lower()
    text = re.sub(r"[^a-z0-9 ]+", " ", text)
    text = re.sub(r"\s+", " ", text).strip()
    return text

def lev(a: str, b: str) -> int:
    if len(a) < len(b):
        a, b = b, a
    if not b:
        return len(a)
    prev = list(range(len(b) + 1))
    for i, ca in enumerate(a, 1):
        curr = [i]
        for j, cb in enumerate(b, 1):
            curr.append(min(curr[j-1]+1, prev[j]+1, prev[j-1] + (0 if ca == cb else 1)))
        prev = curr
    return prev[-1]

def longest_word_run_in(got_words: list[str], want_words: list[str]) -> tuple[int, int]:
    # Longest run of consecutive got_words that appears verbatim inside
    # want_words. Returns (best_length, start_in_got). Captures the case
    # where the recorder only got one bot's slice of speech: that slice
    # should still be a substring of the scenario.
    if not got_words or not want_words:
        return 0, 0
    want_index: dict[str, list[int]] = {}
    for idx, w in enumerate(want_words):
        want_index.setdefault(w, []).append(idx)
    best = 0
    best_start = 0
    for got_start, gw in enumerate(got_words):
        for want_start in want_index.get(gw, ()):
            length = 0
            while (
                got_start + length < len(got_words)
                and want_start + length < len(want_words)
                and got_words[got_start + length] == want_words[want_start + length]
            ):
                length += 1
            if length > best:
                best = length
                best_start = got_start
    return best, best_start

with open(transcript_path) as f:
    data = json.load(f)

words = []
for segment in data.get("segments", []):
    for w in segment.get("words", []):
        if (w.get("text") or "").strip():
            words.append(w["text"])
got = normalise(" ".join(words))
want = normalise(expected)

if not got:
    print("[talk-rec-e2e] FAIL transcript empty after normalisation")
    sys.exit(1)

distance = lev(got, want)
ratio = 1.0 - distance / max(len(got), len(want))
print(f"[talk-rec-e2e] expected ({len(want)} chars): {want[:120]!r}...")
print(f"[talk-rec-e2e] got      ({len(got)} chars): {got[:120]!r}...")
print(f"[talk-rec-e2e] edit-distance={distance} ratio={ratio:.4f} levenshtein-threshold={min_ratio:.2f}")

if ratio >= min_ratio:
    print(f"[talk-rec-e2e] OK   Levenshtein ratio {ratio:.4f} >= threshold {min_ratio:.2f}")
    sys.exit(0)

# Partial-capture fallback: with multi-bot rotation the recorder typically
# captures one bot's contiguous slice clearly while the others come back
# as sparse-RTP gaps. If that slice is a verbatim substring of the
# scenario then transcription is working end-to-end even though the full
# Levenshtein ratio is dominated by missing content.
got_words = got.split()
want_words = want.split()
run_len, run_start = longest_word_run_in(got_words, want_words)
got_slice = " ".join(got_words[run_start : run_start + run_len])
print(f"[talk-rec-e2e] longest captured run inside expected text: {run_len} words")
if run_len >= min_captured_run:
    print(f"[talk-rec-e2e] OK   captured run of {run_len} words verbatim: {got_slice[:160]!r}")
    sys.exit(0)
print(f"[talk-rec-e2e] FAIL Levenshtein {ratio:.4f} < {min_ratio:.2f} and longest verbatim run {run_len} < {min_captured_run}")
sys.exit(1)
PY

log "FULL TALK-DRIVEN E2E PASSED — Talk record-button → gocassini → transcript"
