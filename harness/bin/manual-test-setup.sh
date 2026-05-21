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

IMAGE_LOCAL="cassini-exapp:e2e-v3-cpu-gpu"
# Tag the local image as if it had been pulled from ghcr.io. Combined with
# the daemon's `--registry-from=ghcr.io --registry-to=local` mapping below,
# this lets AppAPI use info.xml verbatim (same registry/image/tag the
# production App Store install would use) without us hosting a local
# registry or rewriting info.xml.
IMAGE_AS_PRODUCTION="ghcr.io/codemyriad/gocassini:latest"

if [[ "$FORCE_BUILD" == "true" ]] || ! docker image inspect "$IMAGE_LOCAL" >/dev/null 2>&1; then
  log "2. Building Cassini ExApp image..."
  docker build -f "$PROJECT_ROOT/deployment/Dockerfile.exapp" -t "$IMAGE_LOCAL" "$PROJECT_ROOT"
else
  log "2. Reusing existing $IMAGE_LOCAL image. (pass --build to force rebuild)"
fi
docker tag "$IMAGE_LOCAL" "$IMAGE_AS_PRODUCTION"

log "3. Starting nextcloud, db, appapi-harp, reverse-proxy..."
"${COMPOSE[@]}" up -d nextcloud db appapi-harp reverse-proxy

log "4. Granting Nextcloud access to host docker socket..."
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

log "5. Bootstrapping Nextcloud (trusted domains, Talk, admin)..."
"$SCRIPT_DIR/bootstrap.sh"

occ() {
  "${COMPOSE[@]}" exec -T -u www-data nextcloud php occ "$@"
}

log "6. Installing AppAPI..."
occ app:install app_api || true
occ app:enable app_api

log "7. Patching AppAPI CSP for ExApp proxy responses..."
"${COMPOSE[@]}" exec -T nextcloud php < "$SCRIPT_DIR/patch-csp.php"
"${COMPOSE[@]}" restart nextcloud

# bootstrap.sh already waits for NC the first time; do it again post-restart.
log "8. Waiting for Nextcloud after CSP-patch restart..."
for attempt in $(seq 1 60); do
  if "${COMPOSE[@]}" exec -T -u www-data nextcloud php occ status 2>&1 | grep -q "installed: true"; then
    success "✓ Nextcloud back up"
    break
  fi
  if [[ $attempt -eq 60 ]]; then error "Nextcloud did not come back up after restart"; exit 1; fi
  sleep 1
done

log "9. Registering HaRP deploy daemon..."
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

log "10. Mapping ghcr.io to local so the daemon uses our locally-built image..."
# AppAPI sees info.xml's <registry>ghcr.io</registry>, looks for the mapping,
# finds --registry-to=local, and skips the docker pull — the image must
# already exist locally (we tagged it in step 2). Lets the dev path use
# info.xml verbatim, same content the production App Store install reads.
occ app_api:daemon:registry:add harp_local \
    --registry-from=ghcr.io --registry-to=local

log "11. Copying info.xml into the Nextcloud container for app:register..."
"${COMPOSE[@]}" cp "$PROJECT_ROOT/appinfo/info.xml" nextcloud:/tmp/gocassini-info.xml
"${COMPOSE[@]}" exec -T -u root nextcloud chown www-data:www-data /tmp/gocassini-info.xml

log "12. Creating standard user 'alice' for viewer testing..."
export OC_PASS="Tn8mY3qVrJ2x!E2e"
"${COMPOSE[@]}" exec -T -e OC_PASS -u www-data nextcloud php occ user:add --password-from-env --display-name=Alice alice >/dev/null 2>&1 || true
unset OC_PASS

cat <<EOF

$(printf '\033[1;32m======================================================================\033[0m')
$(printf '\033[1;32m   Cassini App Store dogfood testbed ready                            \033[0m')
$(printf '\033[1;32m======================================================================\033[0m')

Admin install path:
  Per the official Nextcloud ExApp Development Guide, the canonical
  "install from the App Store as if it were published" pattern is:

      docker compose -p cassini-exapp-test exec -u www-data nextcloud \\
          php occ app_api:app:register gocassini harp_local \\
              --info-xml /tmp/gocassini-info.xml \\
              --test-deploy-mode --wait-finish

  --info-xml hands AppAPI the exact info.xml a published install would
  fetch from apps.nextcloud.com. --test-deploy-mode lets you re-register
  without unregistering first (the iteration sweet spot). HaRP daemon +
  reverse-proxy + registry-to-local mapping mirror the production
  topology; this command is the install trigger.

  After it prints "ExApp gocassini deployed successfully":
  1. Open  http://127.0.0.1:28080/
  2. Log in as  admin / admin
  3. Navigate to the post-deploy URLs below to verify the proxy routes.

  AppAPI daemon config is visible at:
     http://127.0.0.1:28080/index.php/settings/admin/app_api

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
