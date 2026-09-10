#!/usr/bin/env bash
# Install the matching native companion into an already-running local harness.
# Repeat after rebuilding/reinstalling the ExApp to refresh its browser payload.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=harness/bin/lib/base.sh
source "$SCRIPT_DIR/lib/base.sh"

if [[ $# -gt 0 ]]; then
  echo "Usage: PROJECT_NAME=spreedtest $0" >&2
  exit 2
fi

compose() { docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE" "$@"; }
occ() { compose exec -T -u www-data nextcloud php occ "$@"; }

# Avoid installing into a different project's Nextcloud: AppAPI uses a global
# container name, so merely finding nc_app_gocassini is insufficient.
docker inspect nc_app_gocassini --format '{{json .NetworkSettings.Networks}}' \
  | jq -e --arg network "${PROJECT_NAME}_default" 'has($network)' >/dev/null

running_version="$(docker exec nc_app_gocassini printenv APP_VERSION)"
source_version="$(sed -n 's|.*<version>\(.*\)</version>.*|\1|p' "$REPO_ROOT/appinfo/info.xml" | head -n1)"
if [[ "$running_version" != "$source_version" ]]; then
  echo "Running gocassini is $running_version but this checkout is $source_version; rebuild/reinstall the ExApp first." >&2
  exit 1
fi

enabled="$(occ app_api:app:config:get gocassini source_capture_enabled | tr -d '[:space:]')"
if [[ "$enabled" != true && "$enabled" != 1 ]]; then
  echo "gocassini source_capture_enabled is not true; install/enable the ExApp with CASSINI_SOURCE_CAPTURE=1 first." >&2
  exit 1
fi

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT
docker cp nc_app_gocassini:/opt/cassini/cassini-app/dist/capture/capture-payload.js "$work_dir/payload.js"
(cd "$REPO_ROOT" && ./scripts/build-capture-companion.sh \
  --payload "$work_dir/payload.js" \
  --staging "$work_dir/companion" --output "$work_dir/cassini_capture.tar.gz")

compose exec -T -u root nextcloud rm -rf /var/www/html/custom_apps/cassini_capture
compose cp "$work_dir/companion/cassini_capture" nextcloud:/var/www/html/custom_apps/
compose exec -T -u root nextcloud chown -R www-data:www-data /var/www/html/custom_apps/cassini_capture
occ app:enable cassini_capture
occ app:list --output=json | jq -e '.enabled.cassini_capture' >/dev/null
compose exec -T nextcloud cat /var/www/html/custom_apps/cassini_capture/js/capture-payload.js \
  > "$work_dir/installed.js"
cmp "$work_dir/payload.js" "$work_dir/installed.js"
echo "cassini_capture enabled with the running ExApp's exact payload. Reload Talk pages to load it."
