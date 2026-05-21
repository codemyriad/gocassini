#!/usr/bin/env bash
#
# Cassini App Store Dogfood Testbed
#
# Stands up a pristine Nextcloud instance, a local Docker registry, and a
# mock Nextcloud App Store catalog server. The mock catalog advertises
# Cassini exactly as apps.nextcloud.com would after submission, pointing
# AppAPI at a locally-built gocassini image hosted in the local registry.
#
# After this script finishes, the admin can log into Nextcloud, open the
# AppAPI "External Apps" page, and click the real "Deploy and enable"
# button. AppAPI will pull the image, spawn the ExApp container on the
# host docker socket, and wire up the proxy routes.
#
# This is the same flow that will run against the production App Store
# once Cassini is submitted; only the catalog endpoint changes.
#
# Usage:
#   ./harness/bin/manual-test-setup.sh                # use existing image
#   ./harness/bin/manual-test-setup.sh --build        # force rebuild
#

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
PROJECT_ROOT="$(cd "$HARNESS_DIR/.." && pwd)"

export PROJECT_NAME="cassini-exapp-test"
export SPREED_PROFILE="default"

COMPOSE=(docker compose -p "$PROJECT_NAME" -f "$HARNESS_DIR/compose.yml")

log() {
  printf '\n\033[1;34m==>\033[0m \033[1m%s\033[0m\n' "$*"
}

success() {
  printf '\033[1;32m%s\033[0m\n' "$*"
}

error() {
  printf '\033[1;31m[ERROR] %s\033[0m\n' "$*" >&2
}

FORCE_BUILD=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --build|--force-build) FORCE_BUILD=true; shift ;;
    -h|--help)
      echo "Usage: $0 [--build]"
      echo "  --build    Force rebuilding the Cassini ExApp Docker image from source."
      exit 0
      ;;
    *) error "Unknown option: $1"; exit 1 ;;
  esac
done

log "1. Wiping previous state..."
docker rm -f cassini-exapp >/dev/null 2>&1 || true
docker volume rm cassini-exapp-state cassini-exapp-site >/dev/null 2>&1 || true
"${COMPOSE[@]}" down --volumes --remove-orphans

log "1b. Baking the dev release tarball (info.xml pointing at local registry)..."
RELEASES_DIR="$HARNESS_DIR/mockstore/releases"
mkdir -p "$RELEASES_DIR"
STAGE_DIR="$(mktemp -d)"
trap 'rm -rf "$STAGE_DIR"' EXIT
mkdir -p "$STAGE_DIR/gocassini/appinfo" "$STAGE_DIR/gocassini/img"
cp "$PROJECT_ROOT/appinfo/info.xml" "$STAGE_DIR/gocassini/appinfo/info.xml"
cp "$PROJECT_ROOT/appinfo/app.php"  "$STAGE_DIR/gocassini/appinfo/app.php"
cp "$PROJECT_ROOT/img/app.svg"      "$STAGE_DIR/gocassini/img/app.svg"
# Swap ghcr.io for the local dev registry so the deploy daemon pulls from
# 127.0.0.1:5001 (where step 5 below will have pushed the image).
sed -i 's|<registry>ghcr.io</registry>|<registry>127.0.0.1:5001</registry>|' \
  "$STAGE_DIR/gocassini/appinfo/info.xml"
tar -C "$STAGE_DIR" -czf "$RELEASES_DIR/gocassini-0.1.0.tar.gz" gocassini

IMAGE_LOCAL="cassini-exapp:e2e-v3-cpu-gpu"
IMAGE_REGISTRY="127.0.0.1:5001/codemyriad/gocassini:latest"

if [[ "$FORCE_BUILD" == "true" ]] || ! docker image inspect "$IMAGE_LOCAL" >/dev/null 2>&1; then
  log "2. Building Cassini ExApp image..."
  docker build -f "$PROJECT_ROOT/deployment/Dockerfile.exapp" -t "$IMAGE_LOCAL" "$PROJECT_ROOT"
else
  log "2. Reusing existing $IMAGE_LOCAL image. (pass --build to force rebuild)"
fi

log "3. Starting nextcloud, db, registry, mockstore, appapi-harp, reverse-proxy..."
"${COMPOSE[@]}" up -d nextcloud db registry mockstore appapi-harp reverse-proxy

log "4. Waiting for local registry..."
for attempt in $(seq 1 30); do
  if curl -fsS http://127.0.0.1:5001/v2/ >/dev/null 2>&1; then
    success "✓ Local registry up"
    break
  fi
  if [[ $attempt -eq 30 ]]; then error "Local registry did not come up on 127.0.0.1:5001"; exit 1; fi
  sleep 1
done

log "5. Pushing Cassini image to local registry..."
docker tag "$IMAGE_LOCAL" "$IMAGE_REGISTRY"
docker push "$IMAGE_REGISTRY"

log "6. Waiting for mockstore..."
for attempt in $(seq 1 30); do
  if "${COMPOSE[@]}" exec -T nextcloud curl -fsS http://mockstore/api/v1/appapi_apps.json >/dev/null 2>&1; then
    success "✓ Mock App Store reachable at http://mockstore/api/v1/appapi_apps.json"
    break
  fi
  if [[ $attempt -eq 30 ]]; then error "Mock App Store did not come up"; exit 1; fi
  sleep 1
done

log "7. Granting Nextcloud access to host docker socket..."
SOCKET_GID=$("${COMPOSE[@]}" exec -T nextcloud stat -c '%g' /var/run/docker.sock)
"${COMPOSE[@]}" exec -T -u root nextcloud sh -c "
  EXISTING_GROUP=\$(getent group $SOCKET_GID | cut -d: -f1)
  if [ -z \"\$EXISTING_GROUP\" ]; then
    groupadd -g $SOCKET_GID docker-host
    GROUP_NAME=docker-host
  else
    GROUP_NAME=\$EXISTING_GROUP
  fi
  usermod -aG \"\$GROUP_NAME\" www-data
"
"${COMPOSE[@]}" restart nextcloud

log "8. Bootstrapping Nextcloud (trusted domains, Talk, admin)..."
"$SCRIPT_DIR/bootstrap.sh"

occ() {
  "${COMPOSE[@]}" exec -T -u www-data nextcloud php occ "$@"
}

log "9. Installing AppAPI..."
occ app:install app_api || true
occ app:enable app_api

log "10. Patching AppAPI: CSP + dogfood signature bypass..."
"${COMPOSE[@]}" exec -T nextcloud php < "$SCRIPT_DIR/patch-csp.php"
"${COMPOSE[@]}" exec -T nextcloud php < "$SCRIPT_DIR/patch-archive-fetcher.php"
"${COMPOSE[@]}" restart nextcloud

# bootstrap.sh already waits for NC the first time; do it again post-restart.
log "11. Waiting for Nextcloud after CSP-patch restart..."
for attempt in $(seq 1 60); do
  if "${COMPOSE[@]}" exec -T -u www-data nextcloud php occ status 2>&1 | grep -q "installed: true"; then
    success "✓ Nextcloud back up"
    break
  fi
  if [[ $attempt -eq 60 ]]; then error "Nextcloud did not come back up after restart"; exit 1; fi
  sleep 1
done

log "12. Registering HaRP deploy daemon..."
occ app_api:daemon:unregister docker_local >/dev/null 2>&1 || true
occ app_api:daemon:unregister harp_local   >/dev/null 2>&1 || true
# HP_SHARED_KEY must match the value in compose.yml's appapi-harp service.
# nextcloud_url is BOTH the URL AppAPI uses internally for heartbeat
# (${nc_url}/exapps/<appId>/heartbeat — must route to HaRP) AND the value
# injected into the ExApp container as NEXTCLOUD_URL (callbacks to /index.
# php/apps/app_api/... must reach NC). The reverse-proxy splits both paths,
# so pointing the daemon at it satisfies both directions.
occ app_api:daemon:register \
    harp_local \
    "HaRP (local)" \
    docker-install \
    http \
    "appapi-harp:8780" \
    "http://reverse-proxy" \
    --net="${PROJECT_NAME}_default" \
    --harp \
    --harp_frp_address "appapi-harp:8782" \
    --harp_shared_key "dogfood-shared-key-not-secret" \
    --set-default \
    --compute_device=cpu

log "13. Pointing AppAPI at the mock App Store catalog..."
occ config:system:set appstoreurl --value http://mockstore/api/v1
occ config:system:set appstoreenabled --type boolean --value true
occ config:system:set has_internet_connection --type boolean --value true
# Nextcloud's HTTP client refuses requests to RFC1918/loopback hosts by
# default (SSRF guard). Our mockstore is one of those, so allow it.
occ config:system:set allow_local_remote_servers --type boolean --value true
# Invalidate any cached catalog and the failure backoff.
"${COMPOSE[@]}" exec -T -u www-data nextcloud find /var/www/html/data -name 'appapi_apps.json' -delete >/dev/null 2>&1 || true
occ config:app:delete app_api appstore-appapi-fetcher-lastFailure >/dev/null 2>&1 || true

log "14. Warming the catalog (server-side fetch)..."
"${COMPOSE[@]}" exec -T -u www-data nextcloud php -r '
require_once "/var/www/html/lib/base.php";
\OC::$server->get(\OCA\AppAPI\Fetcher\ExAppFetcher::class)->get(true);
echo "ok\n";
' 2>&1 | tail -5 || true

log "15. Creating standard user 'alice' for viewer testing..."
export OC_PASS="Tn8mY3qVrJ2x!E2e"
"${COMPOSE[@]}" exec -T -e OC_PASS -u www-data nextcloud php occ user:add --password-from-env --display-name=Alice alice >/dev/null 2>&1 || true
unset OC_PASS

cat <<EOF

$(printf '\033[1;32m======================================================================\033[0m')
$(printf '\033[1;32m   Cassini App Store dogfood testbed ready                            \033[0m')
$(printf '\033[1;32m======================================================================\033[0m')

Admin install path:
  The "Deploy and enable" button you might expect from older AppAPI versions
  does not exist in current Nextcloud (32 or 33) — AppAPI's admin page only
  shows daemon configuration. The catalog/install UI was not in either the
  33.0.0 or v33.0.3 cuts of the AppAPI code. The install path is CLI:

      docker compose -p cassini-exapp-test exec -u www-data nextcloud \\
          php occ app_api:app:register gocassini harp_local --wait-finish

  Same code path the (missing) UI button would invoke. The mock catalog,
  reverse proxy, HaRP daemon, and image registry stand up the production
  topology; this command is the install trigger.

  After it prints "ExApp gocassini deployed successfully":
  1. Open  http://127.0.0.1:28080/
  2. Log in as  admin / admin
  3. Navigate to the post-deploy URLs below to verify the proxy routes.

  AppAPI daemon config is visible at:
     http://127.0.0.1:28080/index.php/settings/admin/app_api

  AppAPI will pull 127.0.0.1:5001/codemyriad/gocassini:latest, spawn the
  ExApp container on the host Docker engine, exchange keys, and wire up
  the proxy routes. Heartbeat and /init progress appear in the same UI.

  Do NOT use the standard Apps page "Enable" button — that path does not
  go through AppAPI and will not deploy the container.

Standard user (for /viewer access after deploy):
  alice / Tn8mY3qVrJ2x!E2e

Post-deploy URLs:
  Admin control panel: http://127.0.0.1:28080/index.php/apps/app_api/proxy/gocassini/control-panel/
  User viewer:         http://127.0.0.1:28080/index.php/apps/app_api/proxy/gocassini/viewer/

Tear down later:
  docker compose -p $PROJECT_NAME down --volumes

EOF

if command -v xdg-open >/dev/null 2>&1; then
  xdg-open "http://127.0.0.1:28080/" >/dev/null 2>&1 &
elif command -v open >/dev/null 2>&1; then
  open "http://127.0.0.1:28080/" >/dev/null 2>&1 &
fi
