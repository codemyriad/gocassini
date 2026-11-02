# shellcheck shell=bash  # sourced by the harness scripts; it has no shebang of its own
# Phases 1-7 of the manual-install Talk recording roundtrip, shared by
# ci-e2e-talk-record-roundtrip.sh and every scenario that needs the same
# recorded, published meeting before asserting something else about it
# (ci-e2e-summary-template.sh). Extracted verbatim from the roundtrip so the
# scenarios cannot drift apart; the roundtrip's comment block describes the
# phases.
#
# The sourcing script sets SCRIPT_DIR, HARNESS_DIR, REPO_ROOT, COMPOSE_FILE
# and INFO_XML, sources lib/e2e-local.sh and lib-exapp-manifest.sh, and calls
# harness_e2e_local_stack_env first. Sourcing this file resolves the run's
# configuration; nothing is started until the caller runs talk_rt_begin and
# then the phases in order:
#
#   talk_rt_begin                    EXIT/INT/TERM traps + stale-run sweep
#   talk_rt_phase1_stack             compose full profile + bootstrap
#   talk_rt_phase2_appapi            AppAPI + manual-install daemon
#   talk_rt_phase3_exapp             operator container, ExApp registration,
#                                    model preparation and activation
#   talk_rt_phase4_recording_servers Talk -> operator welcome handshake
#   talk_rt_phase5_room_and_bots     room + scenario audio bots in the call
#   talk_rt_phase6_record            Talk start, record, Talk stop
#   talk_rt_phase7_await_publish     the published .opus (PUBLISHED_OPUS)
#
# Knobs a scenario sets before sourcing, defaulting to the roundtrip's own:
#   TALK_RT_PREFIX            container/project/log-dir/room name prefix, and
#                             what the stale-run sweep matches
#   TALK_RT_LOG_TAG           the [tag] every log line carries
#   TALK_RT_COMPOSE_PROFILES  extra compose profiles (space separated) every
#                             compose call carries, so logs and teardown
#                             cover a scenario's own opt-in services
# and, optionally, a function talk_rt_cleanup_hook, run by the EXIT trap
# before the stack is torn down.

TALK_RT_PREFIX="${TALK_RT_PREFIX:-cassini-talk-rec-e2e}"
TALK_RT_LOG_TAG="${TALK_RT_LOG_TAG:-talk-rec-e2e}"
TALK_RT_COMPOSE_PROFILES="${TALK_RT_COMPOSE_PROFILES:-}"

# ---- Configuration --------------------------------------------------------

: "${IMAGE_REF:?IMAGE_REF must be set, e.g. ghcr.io/codemyriad/gocassini:latest}"

PROJECT_NAME="${PROJECT_NAME:-${TALK_RT_PREFIX}-$$}"
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
CONTAINER_NAME="${CONTAINER_NAME:-${TALK_RT_PREFIX}-app}"
LOG_DIR="${LOG_DIR:-/tmp/${TALK_RT_PREFIX}-$$}"
mkdir -p "$LOG_DIR"

# The standalone signaling server only accepts sessions for backend URLs
# in its allowlist; the stock config pins port 28080. Render a per-run
# copy with our run-scoped port; compose.yml mounts it via
# ${SIGNALING_CONF:-./config/signaling.conf}.
SIGNALING_CONF="$LOG_DIR/signaling.conf"
sed "s|:28080\$|:${NEXTCLOUD_HOST_PORT}|" \
  "$HARNESS_DIR/config/signaling.conf" >"$SIGNALING_CONF"
export SIGNALING_CONF

log()  { printf '[%s] %s\n' "$TALK_RT_LOG_TAG" "$*"; }
fail() { log "FAIL: $*"; exit 1; }
phase() { log "----- phase $1: $2 -----"; }
log "manual-install Talk roundtrip identity: $APP_ID@$APP_VERSION image=$IMAGE_REF"

compose() {
  local profile_args=()
  if [[ "${SPREED_PROFILE:-full}" == "full" ]]; then
    profile_args+=(--profile full)
  fi
  local extra
  for extra in $TALK_RT_COMPOSE_PROFILES; do
    profile_args+=(--profile "$extra")
  done
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

talk_rt_cleanup() {
  local rc=$?
  log "cleanup (rc=$rc)"
  # Stop the bot streamer first so it doesn't keep dialing a dying stack.
  if [[ -n "${BOT_PID:-}" ]]; then
    kill "$BOT_PID" >/dev/null 2>&1 || true
  fi
  if declare -F talk_rt_cleanup_hook >/dev/null; then
    talk_rt_cleanup_hook "$rc" || true
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

talk_rt_begin() {
  trap talk_rt_cleanup EXIT
  # A cancelled CI run delivers SIGINT (then SIGTERM) to the job's process
  # group. Without handlers bash dies on the signal WITHOUT running the EXIT
  # trap, and the restart: unless-stopped compose stack leaks (gh issue #51).
  # `exit` inside a signal trap fires the EXIT trap exactly once; the
  # stale-run sweep below is the backstop for the case where the runner
  # SIGKILLs us before cleanup finishes.
  trap 'exit 130' INT
  trap 'exit 143' TERM

  if [[ "${CASSINI_E2E_SKIP_SWEEP:-0}" != "1" ]]; then
    sweep_stale_runs
  fi
}

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
      # By container name: compose services (<prefix>-<pid>-*)
      # and the fixed-name exapp container (<prefix>-app).
      docker ps -aq --filter "name=${TALK_RT_PREFIX}-" 2>/dev/null
      # By compose project label: catches services with a fixed
      # container_name that doesn't carry the project prefix (appapi-harp).
      docker ps -a --format '{{.ID}}\t{{.Label "com.docker.compose.project"}}' 2>/dev/null \
        | awk -F'\t' -v prefix="^${TALK_RT_PREFIX}-" '$2 ~ prefix {print $1}'
    } | sort -u
  ) || true
  if [[ -n "$stale" ]]; then
    log "sweeping stale e2e containers from previous runs: $(tr '\n' ' ' <<<"$stale")"
    xargs -r docker rm -f <<<"$stale" >/dev/null 2>&1 || true
  fi
  docker network ls --format '{{.Name}}' 2>/dev/null \
    | grep -E "^${TALK_RT_PREFIX}-" \
    | xargs -r docker network rm >/dev/null 2>&1 || true
  docker volume ls -q 2>/dev/null \
    | grep -E "^${TALK_RT_PREFIX}-" \
    | xargs -r docker volume rm >/dev/null 2>&1 || true
}

talk_rt_phase1_stack() {
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

  # Nextcloud and Talk run inside the compose network while the GPU-enabled
  # operator uses the host network. Both AppAPI and Talk reach it through this
  # bridge address and the run's operator port.
  BRIDGE_GATEWAY=$(docker network inspect "${PROJECT_NAME}_default" \
    -f '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null)
  [[ -n "$BRIDGE_GATEWAY" ]] || fail "could not resolve compose-network gateway"
}

talk_rt_phase2_appapi() {
  # ============================================================================
  # Phase 2: install + enable AppAPI (needed for ExApp install)
  # ============================================================================
  phase 2 "Install + enable AppAPI"

  occ app:install app_api >>"$LOG_DIR/occ.log" 2>&1 || true
  occ app:enable  app_api >>"$LOG_DIR/occ.log" 2>&1
  log "OK app_api enabled"

  # Register a manual-install daemon pointing at the host-network container.
  # The app's dynamic host port is supplied when the running ExApp is registered
  # below.
  occ app_api:daemon:register \
      manual_install \
      "Local manual install" \
      manual-install \
      http "$BRIDGE_GATEWAY" "$NC_URL_INTERNAL" \
      >>"$LOG_DIR/occ.log" 2>&1 || log "WARN daemon may already be registered"
}

talk_rt_phase3_exapp() {
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
  # Run cassini-operator directly with AppAPI-shaped environment variables.
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

  # CASSINI_PUBLISH_SINK=local below: this harness tests Talk capture and the
  # local publishing path. The installed-ExApp job separately owns Nextcloud
  # Files delivery and access control, so declare the sink instead of letting
  # the AppAPI environment select nextcloud-files (D-549).
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

  # Register the already-running container as an ExApp. Merely registering its
  # daemon does not tell Nextcloud the APP_SECRET: without this matching app row,
  # Cassini's act-as-owner Talk OCS lookup is rejected and roomName can never
  # reach the catalog even though roomId (derived locally from the token) does.
  # Keep the routes manifest-derived so AppAPI validates the same application
  # identity shipped by this image.
  ROUTES_JSON="$(exapp_routes_json "$INFO_XML")"
  APP_JSON=$(jq -nc \
    --arg secret "$APP_SECRET" \
    --arg appid "$APP_ID" \
    --arg version "$APP_VERSION" \
    --argjson routes "$ROUTES_JSON" \
    --argjson port "$OPERATOR_HOST_PORT" \
    '{
       appid:               $appid,
       name:                "Cassini",
       daemon_config_name:  "manual_install",
       version:             $version,
       secret:              $secret,
       port:                $port,
       protocol:            "http",
       system_app:          0,
       routes:              $routes
     }')
  occ app_api:app:unregister "$APP_ID" --force >/dev/null 2>&1 || true
  occ app_api:app:register "$APP_ID" manual_install \
    --json-info "$APP_JSON" \
    --force-scopes \
    --wait-finish \
    >"$LOG_DIR/register.log" 2>&1 \
    || { tail -n 40 "$LOG_DIR/register.log"; fail "AppAPI ExApp registration failed"; }
  grep -q 'heartbeat check failed' "$LOG_DIR/register.log" \
    && fail "AppAPI registration reported heartbeat failure"
  log "OK AppAPI registered the running ExApp identity"

  test_device=cpu
  if (( ${#GPU_ARGS[@]} > 0 )); then test_device=cuda; fi
  OPERATOR_URL="$NC_URL_HOST/index.php/apps/app_api/proxy/$APP_ID/operator" \
    DEVICE="$test_device" LOG_DIR="$LOG_DIR/model-setup" \
    bash "$SCRIPT_DIR/configure-test-transcription.sh"

  TALK_BACKEND_URL_INTERNAL="http://${BRIDGE_GATEWAY}:${OPERATOR_HOST_PORT}"
  log "operator reachable from compose network at: $TALK_BACKEND_URL_INTERNAL"
}

talk_rt_phase4_recording_servers() {
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
}

talk_rt_phase5_room_and_bots() {
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
  ROOM_NAME="${TALK_RT_PREFIX}-$(date +%s)"
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

  # Prove that the identity registered in phase 3 can make the same act-as-owner
  # lookup the operator performs asynchronously at recording start. OCS can wrap
  # an authentication error in HTTP 200, so inspect both its status and payload;
  # otherwise this only fails much later as a catalog entry with no roomName.
  APPAPI_AUTH=$(printf 'admin:%s' "$APP_SECRET" | base64 | tr -d '\n')
  ROOM_LOOKUP=$(curl -sf \
    -H "AUTHORIZATION-APP-API: $APPAPI_AUTH" \
    -H "EX-APP-ID: $APP_ID" \
    -H "EX-APP-VERSION: $APP_VERSION" \
    -H "OCS-APIRequest: true" \
    -H "Accept: application/json" \
    "$NC_URL_HOST/ocs/v2.php/apps/spreed/api/v4/room/$ROOM_TOKEN" \
    || fail "registered ExApp could not request the Talk room")
  jq -e --arg name "$ROOM_NAME" \
    '.ocs.meta.status == "ok" and ((.ocs.data.displayName // .ocs.data.name // "") == $name)' \
    <<<"$ROOM_LOOKUP" >/dev/null \
    || fail "registered ExApp could not resolve the Talk room name"
  log "OK registered ExApp resolves the Talk room as its owner"

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
}

talk_rt_phase6_record() {
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
}

talk_rt_phase7_await_publish() {
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
}
