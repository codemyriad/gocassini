#!/usr/bin/env bash
# seed-operator-volume.sh — copy an AppAPI Cassini volume seed into the
# installed ExApp's persistent volume without ever mounting the seed writable.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

PACK_DIR="${CASSINI_HARNESS_SEED_OPERATOR_DIR:-}"
CONTAINER_NAME="${CASSINI_HARNESS_EXAPP_CONTAINER:-}"
SEED_MOUNT="/cassini-operator-seed"
VOLUME_MOUNT="/nc_app_gocassini_data"

usage() {
  cat <<'EOF'
Usage: harness/bin/seed-operator-volume.sh --pack DIR

Copy an AppAPI Cassini persistent-volume root into the installed ExApp's
fresh persistent volume. DIR must contain operator/jobs/. The source is bind
mounted read-only into a short-lived copier container.
EOF
}

die() { echo "[operator-seed] error: $*" >&2; exit 1; }
seed_log() { printf '[operator-seed] %s\n' "$*"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --pack) PACK_DIR="$2"; shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) usage >&2; die "unknown argument: $1" ;;
  esac
done

[[ -n "$PACK_DIR" ]] || { usage >&2; die "--pack is required"; }
[[ -d "$PACK_DIR" ]] || die "$PACK_DIR is not a directory"
[[ -d "$PACK_DIR/operator/jobs" ]] || die "$PACK_DIR is not an operator-volume seed: expected operator/jobs/"
[[ -n "$(find "$PACK_DIR" -mindepth 1 -maxdepth 1 -print -quit)" ]] || die "$PACK_DIR is empty"

if [[ -z "$CONTAINER_NAME" ]]; then
  # nc_app_gocassini is AppAPI's current Docker name. Keep the earlier
  # harness name as a fallback so a retained local AppAPI installation does
  # not turn a seed into a mysterious missing-container error.
  for candidate in nc_app_gocassini cassini-exapp; do
    if docker inspect "$candidate" >/dev/null 2>&1; then
      CONTAINER_NAME="$candidate"
      break
    fi
  done
fi

docker inspect "$CONTAINER_NAME" >/dev/null 2>&1 \
  || die "installed ExApp container $CONTAINER_NAME is not running; AppAPI must deploy Cassini before operator seeding"

image="$(docker inspect --format '{{.Config.Image}}' "$CONTAINER_NAME")"
[[ -n "$image" ]] || die "could not resolve the installed ExApp image from $CONTAINER_NAME"

# Never copy a SQLite-backed volume while the operator has it open. Stopping
# and starting this just-deployed container keeps the AppAPI registration and
# its named volume intact, while making the seed a coherent initial state.
seed_log "stopping $CONTAINER_NAME while its persistent volume is restored"
docker stop "$CONTAINER_NAME" >/dev/null
restart_exapp() { docker start "$CONTAINER_NAME" >/dev/null || true; }
trap restart_exapp EXIT

# --volumes-from uses the exact named volume AppAPI mounted into the ExApp,
# rather than assuming its implementation-specific name. The bind mount is
# deliberately separate and read-only; cp writes only the volume destination.
seed_log "copying $PACK_DIR into the operator persistent volume via $CONTAINER_NAME"
docker run --rm \
  --volumes-from "$CONTAINER_NAME" \
  --mount "type=bind,src=$PACK_DIR,dst=$SEED_MOUNT,readonly" \
  --entrypoint /bin/sh "$image" -c "set -eu; cp -a $SEED_MOUNT/. $VOLUME_MOUNT/"

trap - EXIT
docker start "$CONTAINER_NAME" >/dev/null
seed_log "copied operator volume seed; source remains unchanged at $PACK_DIR"
