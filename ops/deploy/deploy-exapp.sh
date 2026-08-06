#!/usr/bin/env bash
#
# deploy-exapp.sh — register (or re-register) the Cassini ExApp on a Nextcloud
# instance described by a committed inventory file.
#
# This is the production counterpart of sandbox/deploy.sh. Both call the same
# register logic (ops/deploy/lib/exapp-register.sh); the differences are the
# daemon parameters and that this script never brings a stack up — it registers
# into a Nextcloud that already exists.
#
#   Dry run (default — reads live state, changes nothing):
#     ops/deploy/deploy-exapp.sh --inventory ops/deploy/inventory/example-cpu-local.env \
#                                --tag 0.2.0-alpha.5
#
#   Apply:
#     … --apply
#
# CPU and GPU deploys differ by ONE inventory value, CASSINI_COMPUTE_DEVICE,
# which is a property of the deploy daemon. AppAPI derives the image variant
# from the daemon (`<image-tag>-cuda`), so there is no GPU code path here and
# there must never be one.
#
# Rollback is the same command with the previous tag. The script prints it.
#
# What this script does NOT do, on purpose:
#   * never passes --rm-data (the AppAPI volume is the recording archive)
#   * never registers or re-registers a deploy daemon (see --print-daemon-cmd);
#     re-registering a daemon a live app depends on is a separate, riskier
#     operation and belongs in the admin UI
#   * never edits Talk config; it asserts it and reports drift
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# shellcheck source=ops/deploy/lib/exapp-register.sh
source "$SCRIPT_DIR/lib/exapp-register.sh"

INVENTORY=""
TAG=""
SRC_REF=""
APPLY=0
ALLOW_UNRELEASED=0
FORCE_UNREGISTER=0
PRINT_DAEMON_CMD=0
REMOTE_TIMEOUT="${CASSINI_DEPLOY_TIMEOUT:-900}"

usage() {
  cat <<'EOF'
Usage: ops/deploy/deploy-exapp.sh --inventory FILE --tag X.Y.Z[-pre] [options]

  --inventory FILE     Deployment inventory (see ops/deploy/inventory/)
  --tag TAG            Released image tag to deploy. Moving tags are refused.
  --src-ref REF        Git ref to take appinfo/info.xml from (default: v<TAG>)
  --apply              Actually deploy. Without it, this is a dry run.
  --allow-unreleased   Permit a sha-<shortsha> pin instead of a release tag
  --force-unregister   Recovery path for a Nextcloud/container desync: use
                       `unregister --force` before re-registering. Data is
                       still preserved.
  --print-daemon-cmd   Print the occ command that reproduces this inventory's
                       deploy daemon, then exit. Does not run it.
  -h, --help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --inventory) INVENTORY="$2"; shift 2 ;;
    --tag) TAG="$2"; shift 2 ;;
    --src-ref) SRC_REF="$2"; shift 2 ;;
    --apply) APPLY=1; shift ;;
    --allow-unreleased) ALLOW_UNRELEASED=1; shift ;;
    --force-unregister) FORCE_UNREGISTER=1; shift ;;
    --print-daemon-cmd) PRINT_DAEMON_CMD=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[[ -n "$INVENTORY" ]] || { echo "--inventory is required" >&2; usage >&2; exit 2; }
[[ -f "$INVENTORY" ]] || { echo "inventory not found: $INVENTORY" >&2; exit 2; }

set -a
# shellcheck disable=SC1090  # the inventory path is the whole point of --inventory
source "$INVENTORY"
set +a

: "${CASSINI_DEPLOY_NAME:?inventory must set CASSINI_DEPLOY_NAME}"
: "${CASSINI_NC_SSH:?inventory must set CASSINI_NC_SSH}"
: "${CASSINI_NC_CONTAINER:?inventory must set CASSINI_NC_CONTAINER}"
: "${CASSINI_NC_URL:?inventory must set CASSINI_NC_URL}"
: "${CASSINI_APP_ID:=gocassini}"
: "${CASSINI_DAEMON:?inventory must set CASSINI_DAEMON}"
: "${CASSINI_COMPUTE_DEVICE:?inventory must set CASSINI_COMPUTE_DEVICE (cpu|cuda|rocm)}"
: "${CASSINI_IMAGE_REPO:=ghcr.io/codemyriad/gocassini}"

if (( PRINT_DAEMON_CMD )); then
  cat <<EOF
# Deploy daemon for inventory '$CASSINI_DEPLOY_NAME'. Run this by hand only when
# creating the daemon for the first time; re-registering a daemon that a live
# app points at retargets that app.
occ app_api:daemon:register \\
  ${CASSINI_DAEMON} \\
  "${CASSINI_DAEMON_DISPLAY:-$CASSINI_DAEMON}" \\
  ${CASSINI_DAEMON_DEPLOY_ID:-docker-install} \\
  ${CASSINI_DAEMON_PROTOCOL:-http} \\
  ${CASSINI_DAEMON_HOST:?inventory must set CASSINI_DAEMON_HOST} \\
  ${CASSINI_NC_URL} \\
  --net=${CASSINI_DAEMON_NET:-nextcloud-aio} \\
  --compute_device=${CASSINI_COMPUTE_DEVICE}${CASSINI_DAEMON_HARP:+ \\
  --harp \\
  --harp_frp_address ${CASSINI_DAEMON_FRP_ADDRESS} \\
  --harp_docker_socket_port ${CASSINI_DAEMON_DOCKER_SOCKET_PORT} \\
  --harp_shared_key '<HP_SHARED_KEY from the HaRP host, never committed>'}
EOF
  exit 0
fi

[[ -n "$TAG" ]] || { echo "--tag is required" >&2; usage >&2; exit 2; }
SRC_REF="${SRC_REF:-v$TAG}"
EXAPP_ALLOW_UNRELEASED_TAG="$ALLOW_UNRELEASED"
export EXAPP_ALLOW_UNRELEASED_TAG

log() { printf '\n\033[1;34m==>\033[0m \033[1m%s\033[0m\n' "$*"; }
exapp_log() { log "$@"; }

# ---------------------------------------------------------------------------
# Transport: occ over ssh, detached
# ---------------------------------------------------------------------------
#
# Lifecycle commands run detached on the target host and this side polls for
# completion. A client-side timeout in the middle of app_api:app:register
# desyncs Nextcloud from the container, after which `enable` hangs forever
# instead of erroring; running detached means a dropped ssh cannot cause that.
#
# Secrets are never sent from here. An argument of the form @@SECRET:NAME@@ is
# resolved on the target host by the inventory's CASSINI_SECRET_CMD_<NAME>
# command, so the values exist only there.

READONLY_OCC_COMMANDS="app_api:app:list app_api:daemon:list config:app:get config:system:get status app:list"

remote_occ() {
  local payload secrets_sh
  payload="$(printf '%s\0' "$@" | base64 | tr -d '\n')"

  secrets_sh=""
  local name var
  for name in $(compgen -A variable | grep '^CASSINI_SECRET_CMD_' || true); do
    var="${name#CASSINI_SECRET_CMD_}"
    secrets_sh+="    ${var}) ${!name} ;;"$'\n'
  done

  # Deliberately an UNQUOTED heredoc: the secret-resolver case arms are built
  # here and interpolated in. Everything that must evaluate on the target host
  # is escaped (\$). shellcheck cannot tell the two apart.
  # shellcheck disable=SC2087
  ssh -o BatchMode=yes "$CASSINI_NC_SSH" \
      "CASSINI_ARGV='$payload' CASSINI_NC_CONTAINER='$CASSINI_NC_CONTAINER' CASSINI_TIMEOUT='$REMOTE_TIMEOUT' bash -s" <<REMOTE
set -uo pipefail
work="\$(mktemp -d /tmp/cassini-deploy.XXXXXX)"

resolve_secret() {
  case "\$1" in
$secrets_sh
    *) echo "no resolver for secret \$1" >&2; return 1 ;;
  esac
}

mapfile -d '' -t argv < <(printf '%s' "\$CASSINI_ARGV" | base64 -d)
for i in "\${!argv[@]}"; do
  case "\${argv[\$i]}" in
    *@@SECRET:*@@*)
      name="\${argv[\$i]#*@@SECRET:}"; name="\${name%%@@*}"
      value="\$(resolve_secret "\$name")" || exit 90
      [ -n "\$value" ] || { echo "secret \$name resolved empty" >&2; exit 91; }
      argv[\$i]="\${argv[\$i]//@@SECRET:\$name@@/\$value}"
      ;;
  esac
done
printf '%s\0' "\${argv[@]}" > "\$work/argv"

cat > "\$work/run.sh" <<'RUNNER'
#!/usr/bin/env bash
work="\$1"; container="\$2"
mapfile -d '' -t argv < "\$work/argv"
docker exec --user www-data "\$container" php occ "\${argv[@]}"
echo "\$?" > "\$work/rc"
RUNNER
chmod +x "\$work/run.sh"

setsid nohup "\$work/run.sh" "\$work" "\$CASSINI_NC_CONTAINER" \
  > "\$work/out" 2>&1 < /dev/null &

waited=0
while [ ! -f "\$work/rc" ] && [ "\$waited" -lt "\$CASSINI_TIMEOUT" ]; do
  sleep 1; waited=\$((waited + 1))
done

cat "\$work/out"
if [ ! -f "\$work/rc" ]; then
  echo "occ did not finish within \${CASSINI_TIMEOUT}s; it is STILL RUNNING detached." >&2
  echo "Log: \$work/out on \$(hostname). Do not re-run until it settles." >&2
  exit 124
fi
rc="\$(cat "\$work/rc")"
rm -rf "\$work"
exit "\$rc"
REMOTE
}

occ() {
  local cmd="$1"
  if [[ " $READONLY_OCC_COMMANDS " == *" $cmd "* ]] || (( APPLY )); then
    remote_occ "$@"
    return
  fi
  printf '[dry-run] occ' >&2
  printf ' %q' "$@" >&2
  printf '\n' >&2
}

# ---------------------------------------------------------------------------
# Plan
# ---------------------------------------------------------------------------

EFFECTIVE_TAG="$(exapp_image_tag_for_device "$TAG" "$CASSINI_COMPUTE_DEVICE")"

log "Deploy plan: $CASSINI_DEPLOY_NAME"
cat <<EOF
  nextcloud        $CASSINI_NC_URL (ssh $CASSINI_NC_SSH, container $CASSINI_NC_CONTAINER)
  app              $CASSINI_APP_ID
  deploy daemon    $CASSINI_DAEMON
  compute device   $CASSINI_COMPUTE_DEVICE
  manifest source  $SRC_REF:appinfo/info.xml
  <image-tag>      $TAG              (what the manifest pins)
  image AppAPI pulls
                   $CASSINI_IMAGE_REPO:$EFFECTIVE_TAG
                   (derived from the daemon's compute device — not a parameter
                    of this script, and never written into the manifest)
  mode             $( ((APPLY)) && echo APPLY || echo "DRY RUN (pass --apply to deploy)")
EOF

log "Refusing moving tags"
exapp_assert_immutable_tag "$TAG"
echo "  $TAG is immutable."

# ---------------------------------------------------------------------------
# Live state — the rollback target
# ---------------------------------------------------------------------------

log "Reading live state"
CURRENT_LIST="$(occ app_api:app:list 2>/dev/null || true)"
echo "  app_api:app:list → ${CURRENT_LIST//$'\n'/ }"

CURRENT_IMAGE=""
if [[ -n "${CASSINI_IMAGE_INSPECT_CMD:-}" ]]; then
  CURRENT_IMAGE="$(eval "$CASSINI_IMAGE_INSPECT_CMD" 2>/dev/null | tr -d '\r' || true)"
fi
if [[ -n "$CURRENT_IMAGE" ]]; then
  echo "  running image → $CURRENT_IMAGE   ← ROLLBACK TARGET"
  case "${CURRENT_IMAGE##*:}" in
    latest|latest-*|cuda|rocm)
      cat >&2 <<'WARN'

  !! The running deployment is pinned to a MOVING tag. Whatever it is running
  !! now is not reproducible and there is no tag to roll back to. Record the
  !! digest before you replace it:
  !!   docker inspect <container> --format '{{index .RepoDigests 0}}'
WARN
      ;;
  esac
else
  cat <<'NOTE'
  running image → unknown.
  AppAPI exposes no daemon-agnostic "which image is this app running" query, and
  for a remote daemon the container is not on the Nextcloud host. Set
  CASSINI_IMAGE_INSPECT_CMD in the inventory so the rollback target is always
  read from the live container rather than assumed.
NOTE
fi

# ---------------------------------------------------------------------------
# Manifest
# ---------------------------------------------------------------------------

log "Preparing manifest from $SRC_REF"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
git -C "$REPO_ROOT" show "$SRC_REF:appinfo/info.xml" > "$WORK/info.src.xml"
exapp_render_manifest "$WORK/info.src.xml" "$WORK/gocassini-info.xml" "$TAG"
exapp_assert_manifest_declares "$WORK/gocassini-info.xml" \
  CASSINI_TALK_RECORDING_SECRET CASSINI_TALK_SIGNALING_INTERNAL_SECRET

MANIFEST_VERSION="$(exapp_manifest_get "$WORK/gocassini-info.xml" version)"
echo "  <version>   $MANIFEST_VERSION"
echo "  <image-tag> $TAG"
if [[ "$MANIFEST_VERSION" != "$TAG" ]]; then
  cat >&2 <<EOF

  !! <version> ($MANIFEST_VERSION) and the deployed tag ($TAG) disagree.
  !! app_api:app:list will report $MANIFEST_VERSION for an image built from
  !! something else. Deploy a released tag with --src-ref v<version>, or accept
  !! the mismatch knowingly.
EOF
  if (( ! ALLOW_UNRELEASED )); then
    echo "Refusing to deploy a manifest whose version does not match the tag." >&2
    exit 1
  fi
fi

# ---------------------------------------------------------------------------
# Preflight: the image AppAPI will pull must be pullable, children included
# ---------------------------------------------------------------------------

log "Verifying $CASSINI_IMAGE_REPO:$EFFECTIVE_TAG is pullable"
if ! exapp_assert_pullable "$CASSINI_IMAGE_REPO:$EFFECTIVE_TAG"; then
  cat >&2 <<EOF

Aborting BEFORE any change. Nothing on $CASSINI_NC_SSH was touched.

The tag AppAPI would pull for a '$CASSINI_COMPUTE_DEVICE' daemon is not
pullable. For a CUDA daemon AppAPI falls back to the plain tag when the -cuda
variant fails, which silently downgrades the deploy to CPU transcription with
no error anywhere. Fix the tag (rebuild/republish) before deploying.
EOF
  exit 1
fi
echo "  $CASSINI_IMAGE_REPO:$EFFECTIVE_TAG resolves, every child manifest present."

if [[ "$EFFECTIVE_TAG" != "$TAG" ]]; then
  log "Verifying the CPU fallback tag $CASSINI_IMAGE_REPO:$TAG"
  exapp_assert_pullable "$CASSINI_IMAGE_REPO:$TAG" \
    && echo "  fallback present." \
    || echo "  fallback MISSING (not fatal, but a -cuda failure would have nowhere to fall back to)." >&2
fi

# ---------------------------------------------------------------------------
# Talk-side state (assert, never change)
# ---------------------------------------------------------------------------

log "Checking Talk-side configuration"
REC_SERVERS="$(occ config:app:get spreed recording_servers 2>/dev/null || true)"
if [[ -z "$REC_SERVERS" ]]; then
  echo "  spreed.recording_servers is UNSET — the Record button cannot reach Cassini." >&2
else
  echo "  spreed.recording_servers.server → $(sed -E 's/.*"server":"([^"]*)".*/\1/' <<<"$REC_SERVERS")"
  grep -q "app_api/proxy/$CASSINI_APP_ID" <<<"$REC_SERVERS" \
    || echo "  !! it does not point at the $CASSINI_APP_ID AppAPI proxy" >&2
fi
CALL_RECORDING="$(occ config:app:get spreed call_recording 2>/dev/null || true)"
# Talk reads this with a default of 'yes' (spreed lib/Config.php:
# getAppValue('spreed','call_recording','yes') !== 'yes'), so unset means
# enabled. It is only ever set to turn recording OFF. Assert "not disabled".
if [[ "$CALL_RECORDING" == "no" ]]; then
  echo "  !! spreed.call_recording=no — call recording is DISABLED instance-wide." >&2
else
  echo "  spreed.call_recording → ${CALL_RECORDING:-<unset; Talk defaults to yes>} (recording enabled)"
fi

# ---------------------------------------------------------------------------
# Deploy
# ---------------------------------------------------------------------------

if (( ! APPLY )); then
  log "Dry run complete — nothing was changed"
  cat <<EOF
Re-run with --apply to deploy. On apply this script will:
  1. copy the pinned manifest to $CASSINI_NC_SSH
  2. occ app_api:app:unregister $CASSINI_APP_ID     (persistent volume preserved)
  3. occ app_api:app:register $CASSINI_APP_ID $CASSINI_DAEMON --info-xml … --wait-finish
     with the two secrets resolved on the target host
  4. occ app_api:app:disable/enable to force PUT /enabled (nav entries)
  5. verify the app is enabled and the welcome route answers

Downtime: the ExApp (recording + viewer) is down for the image pull and
container start. Talk calls keep working.
EOF
  exit 0
fi

log "Copying manifest to $CASSINI_NC_SSH"
scp -q "$WORK/gocassini-info.xml" "$CASSINI_NC_SSH:/tmp/gocassini-info.xml"
ssh -o BatchMode=yes "$CASSINI_NC_SSH" \
  "docker cp /tmp/gocassini-info.xml $CASSINI_NC_CONTAINER:/tmp/gocassini-info.xml \
   && docker exec --user root $CASSINI_NC_CONTAINER chown www-data:www-data /tmp/gocassini-info.xml"

declare -a register_args=(
  --app-id "$CASSINI_APP_ID"
  --daemon "$CASSINI_DAEMON"
  --info-xml /tmp/gocassini-info.xml
  --env "CASSINI_TALK_RECORDING_SECRET=@@SECRET:RECORDING@@"
  --env "CASSINI_TALK_SIGNALING_INTERNAL_SECRET=@@SECRET:SIGNALING@@"
  --replace
  --enable-cycle
)
(( FORCE_UNREGISTER )) && register_args+=(--force-unregister)

exapp_register_app "${register_args[@]}"

log "Verifying"
occ app_api:app:list | grep -i "$CASSINI_APP_ID" || {
  echo "$CASSINI_APP_ID missing from app_api:app:list" >&2; exit 1; }
# PUBLIC route, so plain curl is meaningful here. An UNAUTHENTICATED curl to
# /exapps/… is not: it returns 502 whether the edge route is right or wrong.
if curl -fsS "$CASSINI_NC_URL/index.php/apps/app_api/proxy/$CASSINI_APP_ID/api/v1/welcome" \
     | grep -q '"version":1'; then
  echo "  welcome route OK"
else
  echo "  welcome route did NOT answer — check nextcloud.log and the container log" >&2
  exit 1
fi

if [[ -n "${CASSINI_IMAGE_INSPECT_CMD:-}" ]]; then
  echo "  running image → $(eval "$CASSINI_IMAGE_INSPECT_CMD" 2>/dev/null || echo unknown)"
fi

log "Deployed $CASSINI_APP_ID $TAG to $CASSINI_DEPLOY_NAME"
if [[ -n "$CURRENT_IMAGE" ]]; then
  # The rollback argument is the BASE tag: strip the device suffix AppAPI added
  # when it pulled, or the next run would ask for `…-cuda-cuda`.
  rollback_tag="${CURRENT_IMAGE##*:}"
  rollback_tag="${rollback_tag%-cuda}"
  rollback_tag="${rollback_tag%-rocm}"
  case "$rollback_tag" in
    latest|cuda|rocm)
      echo "Previous deployment was on a moving tag ($CURRENT_IMAGE); there is no"
      echo "rollback tag. Pin the last known-good release instead."
      ;;
    *)
      cat <<EOF
Roll back with:
  ops/deploy/deploy-exapp.sh --inventory $INVENTORY \\
    --tag $rollback_tag --src-ref v$rollback_tag --apply
EOF
      ;;
  esac
fi
