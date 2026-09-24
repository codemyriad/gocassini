#!/usr/bin/env bash
# Import a static meetings pack into the installed ExApp's private Files root.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

PACK_DIR="${CASSINI_HARNESS_SEED_PUBLISHED_DIR:-}"
usage() {
  echo "Usage: harness/bin/seed-published.sh --pack DIR" >&2
}
die() { echo "[published-seed] error: $*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --pack) [[ $# -ge 2 ]] || die "--pack needs a directory"; PACK_DIR="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) usage; die "unknown argument: $1" ;;
  esac
done
[[ -d "$PACK_DIR" ]] || die "seed pack directory does not exist: $PACK_DIR"
PACK_DIR="$(cd "$PACK_DIR" && pwd)"

# Validate the complete pack before modifying Nextcloud. Only the catalog's
# recordings are copied; a static pack can also contain unrelated site assets.
names_file="$(mktemp)"
container_id=""
container_manifest=""
temporary=""
cleanup() {
  rm -f "$names_file"
  if [[ -n "$container_id" && -n "$temporary" ]]; then
    docker exec "$container_id" rm -f -- "$temporary" >/dev/null 2>&1 || true
  fi
  if [[ -n "$container_id" && -n "$container_manifest" ]]; then
    docker exec "$container_id" rm -f -- "$container_manifest" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT
python3 - "$PACK_DIR" > "$names_file" <<'PY' || die "seed pack is invalid"
import json
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
catalog = root / 'catalog.json'
if catalog.is_symlink() or not catalog.is_file():
    raise SystemExit('catalog.json is missing or is a symlink')
data = json.loads(catalog.read_text())
if data.get('version') != 'cassini.viewer.catalog.v1' or not isinstance(data.get('meetings'), list) or not data['meetings']:
    raise SystemExit('catalog must be cassini.viewer.catalog.v1 with at least one meeting')
seen = set()
for entry in data['meetings']:
    path = entry.get('audioPath') if isinstance(entry, dict) else None
    if not isinstance(path, str) or not path.startswith('./meetings/'):
        raise SystemExit(f'invalid meeting audioPath: {path!r}')
    name = path.split('/')[-1]
    if path != './meetings/' + name or name in ('', '.', '..') or not name.endswith('.opus') or any(ord(c) < 32 or ord(c) == 127 for c in name):
        raise SystemExit(f'invalid meeting audioPath: {path!r}')
    source = root / 'meetings' / name
    if name in seen or source.is_symlink() or not source.is_file() or source.stat().st_size == 0:
        raise SystemExit(f'duplicate, missing, linked, or empty recording: {name}')
    seen.add(name)
    print(name)
PY

harness_stack_init
occ user:info cassini >/dev/null || die "cassini recordings owner is missing"
occ user:info admin >/dev/null || die "admin is missing"
container_id="$(compose ps -q nextcloud)"
[[ -n "$container_id" ]] || die "Nextcloud container is not running"
docker exec "$container_id" test -r /cassini-published-seed/catalog.json \
  || die "seed pack is not mounted read-only in Nextcloud; start the stack with --seed-published"

data_dir="$(occ config:system:get datadirectory | tr -d '\r')"
[[ -n "$data_dir" ]] || die "Nextcloud has no data directory"
recordings_dir="$data_dir/cassini/files/CassiniRecordings/meetings"
docker exec "$container_id" mkdir -p -- "$recordings_dir"
docker exec "$container_id" chown www-data:www-data -- "$data_dir/cassini/files/CassiniRecordings" "$recordings_dir"

copied=0
while IFS= read -r name; do
  source="/cassini-published-seed/meetings/$name"
  target="$recordings_dir/$name"
  docker exec "$container_id" test -f "$source" || die "mounted seed is missing $name"
  if docker exec "$container_id" test -e "$target"; then
    docker exec "$container_id" cmp -s -- "$source" "$target" \
      || die "$name already exists with different bytes; existing recordings were left intact"
    continue
  fi
  temporary="$target.seed-$$"
  docker exec "$container_id" cp -- "$source" "$temporary"
  docker exec "$container_id" chown www-data:www-data -- "$temporary"
  docker exec "$container_id" mv -- "$temporary" "$target"
  temporary=""
  copied=$((copied + 1))
done < "$names_file"

# Direct filesystem copies need a Files scan before the share manager can find
# the nodes and before the installed app can resolve their file IDs.
occ files:scan --path="cassini/files/CassiniRecordings" >/dev/null
container_manifest="/tmp/cassini-published-seed-$$.txt"
docker exec -i "$container_id" sh -c 'cat > "$1"' seed "$container_manifest" < "$names_file"
docker exec "$container_id" chmod 644 "$container_manifest"
compose exec -T -u www-data nextcloud php /usr/local/bin/cassini-seed-published-shares.php "$container_manifest"

# The operator can have populated its owner-inventory cache while the AppAPI
# deployment was still starting on an empty archive. Restart after the import
# so its first inventory sees the files that have just been scanned and shared.
# This makes the seed visible immediately instead of waiting for the cache TTL.
exapp_container=""
for candidate in nc_app_gocassini cassini-exapp; do
  if docker inspect "$candidate" >/dev/null 2>&1; then
    exapp_container="$candidate"
    break
  fi
done
[[ -n "$exapp_container" ]] || die "installed ExApp container is not running after published seeding"
# Import descriptions through the operator, mapping names to this Nextcloud's
# file IDs. Neither a source database nor source shares can supply those IDs.
docker cp "$PACK_DIR/catalog.json" "$exapp_container:/tmp/cassini-seed-catalog.json"
docker exec "$exapp_container" /usr/local/bin/cassini-operator import-meeting-metadata --catalog /tmp/cassini-seed-catalog.json
docker exec "$exapp_container" rm -f /tmp/cassini-seed-catalog.json
docker exec "$exapp_container" /usr/local/bin/cassini-operator backfill-search --strict
docker exec "$exapp_container" /usr/local/bin/cassini-operator backfill-annotations --strict
log "Restarting $exapp_container so its recording inventory includes the imported archive"
docker restart "$exapp_container" >/dev/null

expected_count="$(wc -l < "$names_file" | tr -d ' ')"
proxy_url="http://127.0.0.1:${NEXTCLOUD_HOST_PORT:-28080}/index.php/apps/app_api/proxy/gocassini"
meetings_json="$(harness_http_body_with_retry "seeded meetings list" -u "$ADMIN_USER:$ADMIN_PASSWORD" "$proxy_url/published/meetings-list")"
actual_count="$(jq '.meetings | length' <<<"$meetings_json")"
[[ "$actual_count" == "$expected_count" ]] \
  || die "imported $expected_count recording(s), but admin sees $actual_count after the ExApp restart"
python3 - "$PACK_DIR/catalog.json" "$meetings_json" <<'PY'
import json, sys
expected = json.load(open(sys.argv[1]))['meetings']
actual = {e['audioPath']: e for e in json.loads(sys.argv[2])['meetings']}
for entry in expected:
    if actual.get(entry['audioPath']) != entry:
        raise SystemExit('seeded metadata differs from catalog: ' + entry['audioPath'])
PY

echo "[published-seed] imported $copied recording(s); admin can list all $actual_count seeded recording(s)"
