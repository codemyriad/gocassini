# shellcheck shell=bash
#
# Canonical AppAPI daemon/app registration logic.
#
# One implementation, three callers (D-458 / D-565):
#
#   * sandbox/deploy.sh          — HaRP daemon, brings the compose stack up
#   * harness/bin/lib/stack.sh   — HaRP daemon, install phase of the e2e stack
#   * ops/deploy/deploy-exapp.sh — production, registers into an existing
#                                  Nextcloud, never brings a stack up
#
# The only real differences between them are the daemon parameters and whether
# a stack is started; the register dance itself is identical, and every
# operational lesson below was learned the hard way on one of the three.
#
# ---------------------------------------------------------------------------
# Contract
# ---------------------------------------------------------------------------
#
# Sourcing this file defines functions and nothing else. The caller must
# provide one function before calling anything here:
#
#   occ <args...>   Run `occ` against the target Nextcloud and pass its stdout,
#                   stderr and exit status straight through.
#
# `occ` is the transport seam. The sandbox runs `docker compose exec`, the
# harness runs its own wrapper, production runs a detached `docker exec` over
# ssh. Detachment lives in the caller's `occ` on purpose: whether a lifecycle
# command survives a dropped client is a property of how you reach the host,
# not of the register logic.
#
# `exapp_log` may be overridden; a plain default is provided.
#
# ---------------------------------------------------------------------------
# Operational rules encoded here (do not "simplify" these away)
# ---------------------------------------------------------------------------
#
# * Never pass `--rm-data`. The AppAPI volume is the recording archive. This
#   library has no code path that can delete it and must not grow one.
#
# * Re-registering over a live registration fails at /init with a 401: AppAPI
#   still holds the old APP_SECRET, which the freshly created container does
#   not share. Unregister first (data preserved), then register.
#
# * `app_api:app:register|enable|disable` must survive a dropped client. A
#   client-side timeout mid-handshake desyncs Nextcloud from the container, and
#   from then on `enable` hangs instead of erroring. Recovery is
#   `unregister --force` (which still keeps data) followed by a re-register —
#   see exapp_unregister_app --force.
#
# * `app_api:app:register` sets the DB enabled flag but does not necessarily
#   PUT /enabled to the container, and `app_api:app:enable` short-circuits when
#   the flag is already set. Cassini registers its navigation entries only on
#   receiving `PUT /enabled?enabled=1`, so a missing nav icon on an app that
#   reports [enabled] means the callback never landed. disable → enable forces
#   it; see --enable-cycle.
#
# * The compute device is a property of the DAEMON, never of the app or the
#   image argument. AppAPI derives the image variant from the daemon
#   (`<image-tag>-cuda` for a CUDA daemon, falling back to the plain tag), so a
#   CPU deploy and a GPU deploy differ by one daemon parameter and nothing
#   else. Anything that forks the register path on device is a bug.

if [[ "${CASSINI_EXAPP_REGISTER_LIB_SOURCED:-0}" == "1" ]]; then
  return 0
fi
CASSINI_EXAPP_REGISTER_LIB_SOURCED=1

if ! declare -F exapp_log >/dev/null 2>&1; then
  exapp_log() { printf '\n\033[1;34m==>\033[0m \033[1m%s\033[0m\n' "$*" >&2; }
fi

exapp_die() { echo "error: $*" >&2; return 1; }

# ---------------------------------------------------------------------------
# Manifest helpers
# ---------------------------------------------------------------------------

# exapp_manifest_get FILE ELEMENT
# Read a single-line scalar element out of appinfo/info.xml.
exapp_manifest_get() {
  local file="$1" element="$2" value
  value="$(sed -n "s#.*<${element}>\\([^<]*\\)</${element}>.*#\\1#p" "$file" | head -n1)"
  if [[ -z "$value" ]]; then
    exapp_die "no <$element> found in $file"
    return 1
  fi
  printf '%s\n' "$value"
}

# exapp_render_manifest SRC DEST TAG
#
# Copy a manifest and pin <image-tag> to TAG. TAG is always the BASE tag
# (0.2.0-alpha.5), never the device variant: AppAPI appends `-cuda` itself
# based on the daemon. Writing `0.2.0-alpha.5-cuda` here produces a request for
# `0.2.0-alpha.5-cuda-cuda`.
exapp_render_manifest() {
  local src="$1" dest="$2" tag="$3"
  [[ -f "$src" ]] || { exapp_die "manifest not found: $src"; return 1; }
  [[ -n "$tag" ]] || { exapp_die "exapp_render_manifest needs a tag"; return 1; }
  case "$tag" in
    *-cuda|*-rocm)
      exapp_die "<image-tag> must be the base tag; AppAPI appends the device suffix (got $tag)"
      return 1
      ;;
  esac
  sed -E "s#<image-tag>[^<]*</image-tag>#<image-tag>${tag}</image-tag>#" "$src" > "$dest"
  grep -q "<image-tag>${tag}</image-tag>" "$dest" \
    || { exapp_die "failed to pin <image-tag> to $tag in $dest"; return 1; }
}

# exapp_assert_manifest_declares FILE VAR...
#
# AppAPI silently drops --env values for variables the manifest does not
# declare under <environment-variables>. A deploy that "succeeded" while
# dropping CASSINI_TALK_RECORDING_SECRET looks fine until the first recording.
exapp_assert_manifest_declares() {
  local file="$1"; shift
  local var missing=0
  for var in "$@"; do
    grep -q "<name>${var}</name>" "$file" || {
      echo "error: $file does not declare <environment-variables> entry $var" >&2
      missing=1
    }
  done
  return "$missing"
}

# ---------------------------------------------------------------------------
# Image tag rules
# ---------------------------------------------------------------------------

# exapp_assert_immutable_tag TAG
#
# Production must key on a released, immutable tag. `latest`, `latest-cuda`,
# `cuda`, `branch-*` and `dispatch-*` all move under a running deploy: the
# label in `app_api:app:list` then describes a version that has nothing to do
# with the bytes in the container, and there is no tag to roll back to.
#
# Set EXAPP_ALLOW_UNRELEASED_TAG=1 to also accept `sha-<shortsha>` pins. Those
# are immutable but unreleased — the app's <version> label will lag the code,
# and nothing corresponding exists on the App Store.
exapp_assert_immutable_tag() {
  local tag="$1"
  [[ -n "$tag" ]] || { exapp_die "no image tag given"; return 1; }
  case "$tag" in
    latest|latest-*|cuda|rocm|branch-*|dispatch-*|main|stable)
      echo "error: '$tag' is a moving tag and must never be deployed." >&2
      echo "       A moving tag is not a rollback target and not reproducible:" >&2
      echo "       the same registration resolves to different bytes tomorrow." >&2
      echo "       Deploy a released tag (X.Y.Z[-pre]) instead." >&2
      return 1
      ;;
    sha-*)
      if [[ "${EXAPP_ALLOW_UNRELEASED_TAG:-0}" != "1" ]]; then
        echo "error: '$tag' is a raw commit pin, not a released tag." >&2
        echo "       It is immutable, but the app <version> label will lag the" >&2
        echo "       code and no App Store release matches it. Cut a release, or" >&2
        echo "       pass --allow-unreleased if this is a deliberate hotfix." >&2
        return 1
      fi
      ;;
    *[!0-9.a-zA-Z_-]*)
      exapp_die "image tag contains characters a Docker tag cannot hold: $tag"
      return 1
      ;;
  esac
}

# exapp_image_tag_for_device TAG DEVICE
#
# The one place the compute device changes anything. Mirrors AppAPI's own
# derivation so preflight checks the tag AppAPI will actually pull.
exapp_image_tag_for_device() {
  local tag="$1" device="${2:-cpu}"
  case "$device" in
    cuda) printf '%s-cuda\n' "$tag" ;;
    rocm) printf '%s-rocm\n' "$tag" ;;
    cpu|"") printf '%s\n' "$tag" ;;
    *) exapp_die "unknown compute device: $device (expected cpu|cuda|rocm)"; return 1 ;;
  esac
}

# exapp_assert_pullable REF
#
# "Listed in the registry" is not "pullable". A tag can resolve to an index
# whose child manifests were pruned out from under it; `docker pull` then fails
# and AppAPI silently falls back to the plain (CPU) tag, running transcription
# without the GPU and reporting nothing. Resolve the tag AND every child.
exapp_assert_pullable() {
  local ref="$1" raw kids digest failed=0
  command -v docker >/dev/null 2>&1 \
    || { exapp_die "docker is required to verify $ref is pullable"; return 1; }

  if ! raw="$(docker buildx imagetools inspect --raw "$ref" 2>/dev/null)"; then
    echo "error: $ref does not resolve in the registry." >&2
    return 1
  fi
  kids="$(printf '%s' "$raw" | jq -r '.manifests[]?.digest' 2>/dev/null || true)"
  if [[ -z "$kids" ]]; then
    return 0   # single manifest, already proven by the successful inspect
  fi
  for digest in $kids; do
    if ! docker buildx imagetools inspect --raw "${ref%%:*}@${digest}" >/dev/null 2>&1; then
      echo "error: $ref resolves but child manifest ${digest} is missing (dangling tag)." >&2
      echo "       A dangling tag cannot be repaired by retagging: it needs a" >&2
      echo "       rebuild at that source ref." >&2
      failed=1
    fi
  done
  return "$failed"
}

# ---------------------------------------------------------------------------
# Daemon registration
# ---------------------------------------------------------------------------

# exapp_register_daemon --name N --display D --host H --nc-url U [options]
#
#   --deploy-id ID          default docker-install
#   --protocol P            default http
#   --net NAME              docker network the ExApp container joins
#   --harp                  register as a HaRP daemon
#   --frp-address ADDR      HaRP FRP endpoint (host:port)
#   --shared-key KEY        HaRP shared key
#   --docker-socket-port N  HaRP remote Docker socket port (remote engines)
#   --compute-device D      cpu|cuda|rocm — THE parameter (see header)
#   --set-default           make this the default daemon for new installs
#   --replace               unregister an existing daemon of the same name first
#
# --replace is destructive to the *daemon record*, not to data, but an app
# registered against that daemon points at it by name: re-registering a daemon
# an installed app depends on is a change to that app's deploy target. Callers
# that manage a live install should leave it off and use the admin UI.
exapp_register_daemon() {
  local name="" display="" host="" nc_url=""
  local deploy_id="docker-install" protocol="http"
  local net="" harp=0 frp_address="" shared_key="" socket_port=""
  local compute_device="cpu" set_default=0 replace=0

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --name) name="$2"; shift 2 ;;
      --display) display="$2"; shift 2 ;;
      --host) host="$2"; shift 2 ;;
      --nc-url) nc_url="$2"; shift 2 ;;
      --deploy-id) deploy_id="$2"; shift 2 ;;
      --protocol) protocol="$2"; shift 2 ;;
      --net) net="$2"; shift 2 ;;
      --harp) harp=1; shift ;;
      --frp-address) frp_address="$2"; shift 2 ;;
      --shared-key) shared_key="$2"; shift 2 ;;
      --docker-socket-port) socket_port="$2"; shift 2 ;;
      --compute-device) compute_device="$2"; shift 2 ;;
      --set-default) set_default=1; shift ;;
      --replace) replace=1; shift ;;
      *) exapp_die "exapp_register_daemon: unknown option $1"; return 2 ;;
    esac
  done

  [[ -n "$name" && -n "$host" && -n "$nc_url" ]] \
    || { exapp_die "exapp_register_daemon needs --name, --host and --nc-url"; return 2; }
  exapp_image_tag_for_device x "$compute_device" >/dev/null || return 1
  display="${display:-$name}"

  local -a args=(
    app_api:daemon:register
    "$name" "$display" "$deploy_id" "$protocol" "$host" "$nc_url"
    "--compute_device=$compute_device"
  )
  [[ -n "$net" ]] && args+=("--net=$net")
  if (( harp )); then
    args+=(--harp)
    [[ -n "$frp_address" ]] && args+=(--harp_frp_address "$frp_address")
    [[ -n "$shared_key" ]] && args+=(--harp_shared_key "$shared_key")
    [[ -n "$socket_port" ]] && args+=(--harp_docker_socket_port "$socket_port")
  fi
  (( set_default )) && args+=(--set-default)

  if (( replace )); then
    exapp_log "Removing any existing daemon '$name'"
    occ app_api:daemon:unregister "$name" >/dev/null 2>&1 || true
  fi
  exapp_log "Registering deploy daemon '$name' (compute device: $compute_device)"
  occ "${args[@]}"
}

# ---------------------------------------------------------------------------
# App registration
# ---------------------------------------------------------------------------

# exapp_app_is_registered APP_ID
#
# `app_api:app:list` prints one line per ExApp: "gocassini (Cassini): 1.2.3
# [enabled]". Match the id at the start of a line so a substring of some other
# app's display name cannot produce a false positive.
exapp_app_is_registered() {
  occ app_api:app:list 2>/dev/null | grep -qE "^[[:space:]]*$1[[:space:](:]"
}

# exapp_unregister_app APP_ID [--force]
#
# Data is preserved either way — `--rm-data` is deliberately unreachable from
# this library. --force is the recovery path for a Nextcloud/container desync
# (see the header): it drops the registration without waiting on a container
# that will never answer.
exapp_unregister_app() {
  local app_id="$1"; shift
  local -a args=(app_api:app:unregister "$app_id")
  [[ "${1:-}" == "--force" ]] && args+=(--force)
  exapp_log "Unregistering $app_id (persistent volume preserved)"
  occ "${args[@]}"
}

# exapp_register_app --app-id ID --daemon D --info-xml PATH [options]
#
#   --env K=V           repeatable; only manifest-declared keys survive
#   --test-deploy-mode  local/dev only — relaxes AppAPI's deploy checks
#   --log FILE          capture register output here instead of the terminal
#   --enable-cycle      force PUT /enabled by disable → enable after register
#   --replace           unregister an existing registration first (keeps data)
#   --force-unregister  use --force on that unregister (desync recovery)
exapp_register_app() {
  local app_id="" daemon="" info_xml="" log_file=""
  local test_deploy_mode=0 enable_cycle=0 replace=0 force_unregister=0
  local -a envs=()

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --app-id) app_id="$2"; shift 2 ;;
      --daemon) daemon="$2"; shift 2 ;;
      --info-xml) info_xml="$2"; shift 2 ;;
      --env) envs+=("$2"); shift 2 ;;
      --test-deploy-mode) test_deploy_mode=1; shift ;;
      --log) log_file="$2"; shift 2 ;;
      --enable-cycle) enable_cycle=1; shift ;;
      --replace) replace=1; shift ;;
      --force-unregister) force_unregister=1; replace=1; shift ;;
      *) exapp_die "exapp_register_app: unknown option $1"; return 2 ;;
    esac
  done

  [[ -n "$app_id" && -n "$daemon" && -n "$info_xml" ]] \
    || { exapp_die "exapp_register_app needs --app-id, --daemon and --info-xml"; return 2; }

  if (( replace )) && exapp_app_is_registered "$app_id"; then
    if (( force_unregister )); then
      exapp_unregister_app "$app_id" --force || true
    else
      exapp_unregister_app "$app_id" || true
    fi
  fi

  local -a args=(app_api:app:register "$app_id" "$daemon" --info-xml "$info_xml")
  local kv
  for kv in "${envs[@]}"; do
    args+=(--env "$kv")
  done
  (( test_deploy_mode )) && args+=(--test-deploy-mode)
  args+=(--wait-finish)

  exapp_log "Registering $app_id on daemon '$daemon'"
  if [[ -n "$log_file" ]]; then
    if ! occ "${args[@]}" >"$log_file" 2>&1; then
      tail -200 "$log_file" >&2 || true
      exapp_die "app_api:app:register failed; see $log_file"
      return 1
    fi
    if grep -q 'heartbeat check failed' "$log_file"; then
      tail -200 "$log_file" >&2 || true
      exapp_die "app_api:app:register reported heartbeat failure; see $log_file"
      return 1
    fi
  else
    occ "${args[@]}" || return 1
  fi

  if (( enable_cycle )); then
    exapp_log "Cycling enable state to force PUT /enabled on the container"
    occ app_api:app:disable "$app_id" >/dev/null 2>&1 || true
    occ app_api:app:enable "$app_id" >/dev/null
  fi
}
