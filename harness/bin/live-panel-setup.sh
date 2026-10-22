#!/usr/bin/env bash
# Install the cassini-live captions ExApp (with the transcript panel) into a
# running harness stack, built from the cassini-live submodule.
#
# Prerequisites: `./bin/cassini dev stack up --services full ...` is up, and
# the cassini-live submodule is checked out (git submodule update --init).
#
# What it does:
#   1. builds ghcr.io/codemyriad/cassini-live:<version> from the submodule
#   2. registers a manual_install AppAPI daemon pointing at the container name
#   3. runs the container on the compose network with enforced cgroup limits
#      (without them the engine refuses to load) and the harness HPB secret
#   4. registers the ExApp with the routes from its appinfo/info.xml
#   5. pins overwrite.cli.url to http://localhost:28080
#
# On the last point: the HPB keys rooms by Nextcloud backend URL. cassini-live
# derives its backend URL from overwrite.cli.url, while a browser session's
# comes from the address bar. If they differ (127.0.0.1 vs localhost), the two
# join DIFFERENT room instances and captions never arrive. Open Nextcloud at
# http://localhost:28080/ — not 127.0.0.1 — when testing captions.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
# shellcheck source=lib-exapp-manifest.sh
source "$SCRIPT_DIR/lib-exapp-manifest.sh"

PROJECT_NAME="${PROJECT_NAME:-spreedtest}"
COMPOSE_FILE="${COMPOSE_FILE:-$REPO_ROOT/harness/compose.yml}"
LIVE_DIR="${LIVE_DIR:-$REPO_ROOT/cassini-live}"
INFO_XML="$LIVE_DIR/appinfo/info.xml"
CONTAINER_NAME="${CONTAINER_NAME:-nc_app_live_transcription}"
APP_ID=live_transcription
APP_PORT=8080
DAEMON_NAME="${DAEMON_NAME:-manual_install}"
SIGNALING_CONF="${SIGNALING_CONF:-$REPO_ROOT/harness/config/signaling.conf}"
NC_LOCAL_URL="${NC_LOCAL_URL:-http://localhost:28080}"

log()  { printf '[live-panel] %s\n' "$*"; }
fail() { log "FAIL: $*"; exit 1; }
compose() { docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE" "$@"; }
occ()     { compose exec -T -u www-data nextcloud php occ "$@"; }

[[ -f "$INFO_XML" ]] || fail "cassini-live submodule missing ($INFO_XML); run: git submodule update --init"
[[ -f "$SIGNALING_CONF" ]] || fail "signaling config not found at $SIGNALING_CONF"

APP_VERSION="$(exapp_app_version "$INFO_XML")"
IMAGE_REF="$(exapp_image_ref "$INFO_XML")"
SIG_SECRET="$(awk '$1 == "internalsecret" { print $3 }' "$SIGNALING_CONF")"
[[ -n "$SIG_SECRET" ]] || fail "could not read internalsecret from $SIGNALING_CONF"
APP_SECRET="${APP_SECRET:-$(head -c 24 /dev/urandom | base64 | tr -d '/+=' | head -c 32)}"

log "building $IMAGE_REF from $LIVE_DIR"
docker build -t "$IMAGE_REF" "$LIVE_DIR"

log "ensuring app_api and the $DAEMON_NAME daemon"
occ app:install app_api >/dev/null 2>&1 || true
occ app:enable app_api >/dev/null
occ app_api:daemon:unregister "$DAEMON_NAME" >/dev/null 2>&1 || true
occ app_api:daemon:register "$DAEMON_NAME" "Manual install" \
  manual-install http "$CONTAINER_NAME" http://nextcloud >/dev/null

log "pinning overwrite.cli.url to $NC_LOCAL_URL (see header: HPB rooms are keyed by backend URL)"
occ config:system:set overwrite.cli.url --value "$NC_LOCAL_URL" >/dev/null

log "starting $CONTAINER_NAME with enforced limits"
docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
docker run -d --name "$CONTAINER_NAME" --network "${PROJECT_NAME}_default" \
  --cpus=4 --memory=2g --memory-swap=2g --pids-limit=128 \
  -e APP_HOST=0.0.0.0 -e APP_PORT=$APP_PORT -e APP_ID="$APP_ID" \
  -e APP_VERSION="$APP_VERSION" -e APP_SECRET="$APP_SECRET" -e AA_VERSION=5.0.0 \
  -e NEXTCLOUD_URL=http://nextcloud \
  -e CASSINI_TALK_SIGNALING_INTERNAL_SECRET="$SIG_SECRET" \
  "$IMAGE_REF" >/dev/null

log "waiting for the heartbeat"
hb=000
for _ in $(seq 1 40); do
  hb="$(compose exec -T nextcloud sh -c \
    "curl -s -o /dev/null -w '%{http_code}' http://$CONTAINER_NAME:$APP_PORT/heartbeat" || echo 000)"
  [[ "$hb" == "200" ]] && break
  sleep 2
done
[[ "$hb" == "200" ]] || fail "heartbeat never reached 200"

log "registering the ExApp"
ROUTES_JSON="$(exapp_routes_json "$INFO_XML")"
APP_JSON="$(jq -nc \
  --arg secret "$APP_SECRET" --arg version "$APP_VERSION" --arg daemon "$DAEMON_NAME" \
  --argjson routes "$ROUTES_JSON" \
  '{appid:"live_transcription",name:"Cassini Live",daemon_config_name:$daemon,
    version:$version,secret:$secret,port:8080,protocol:"http",system_app:0,routes:$routes}')"
occ app_api:app:unregister "$APP_ID" --force >/dev/null 2>&1 || true
occ app_api:app:register "$APP_ID" "$DAEMON_NAME" --json-info "$APP_JSON" \
  --force-scopes --wait-finish >/dev/null \
  || fail "AppAPI registration failed"

status="$(curl -s -u admin:admin -o /dev/null -w '%{http_code}' \
  "$NC_LOCAL_URL/index.php/apps/app_api/proxy/$APP_ID/panel/")"
[[ "$status" == "200" ]] || fail "panel install page answered $status through the proxy"

log "done. Captions and the transcript panel are live."
log "  open Nextcloud at:     $NC_LOCAL_URL/ (admin/admin) — use THIS host, not 127.0.0.1"
log "  bookmarklet pickup:    $NC_LOCAL_URL/index.php/apps/app_api/proxy/$APP_ID/panel/"
log "  create a test room:    ./bin/cassini dev room create --name 'Live panel test'"
log "  then: join the call, enable closed captions (CC in the call menu), click the bookmarklet"
