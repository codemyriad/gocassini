#!/usr/bin/env bash
# seed-nc-files.sh — load a seed pack into the harness's Nextcloud recordings tree.
#
# A seed pack is what `cassini dev meetings pull --out <dir>` writes: a published
# site root holding catalog.json and meetings/<id>.opus. This puts one into the
# Cassini Team folder of a running harness stack, so the viewer, the insights
# surfaces and the agent read a real archive instead of a synthetic fixture.
#
#   harness/bin/seed-nc-files.sh --pack harness/runtime/seed/prod
#   harness/bin/seed-nc-files.sh --pack harness/runtime/seed/prod --dry-run
#   harness/bin/seed-nc-files.sh --pack harness/runtime/seed/prod --replace
#
# HOW IT LOADS THEM, AND WHY NOT OVER WEBDAV. The recordings tree is a Team
# folder, which is ordinary files under <datadir>/__groupfolders/<id>/files. So
# the pack is copied in and Nextcloud is told to rescan, rather than uploaded.
# The alternative is the operator's `backfill-nc-files`, which is the right tool
# for a production migration and the wrong one here: it spends three HTTP round
# trips per meeting through PHP, deliberately, so that no leaf is ever visible
# mid-upload. In a throwaway local stack where every seeded meeting is readable
# anyway, that caution buys nothing and costs the whole runtime. Measured on a
# 2 GB, 128-meeting archive: three seconds to copy, one to scan.
#
# WHAT THE SEEDED MEETINGS ARE VISIBLE TO. Every account on the stack. A leaf
# carrying no ACL rules of its own inherits the Team folder's `everyone: read`,
# and this script writes no per-meeting rules — production's do not survive the
# trip, because they name production accounts that do not exist here and are not
# readable to a non-admin caller in the first place. So the archive is real and
# its access control is not. Anything testing who may read what must not use a
# seeded meeting as evidence.
#
# ORDERING. This needs the Cassini Team folder to exist, which the ExApp creates
# when it is installed and provisioned. Run it after `dev stack up` has finished,
# never before: nothing is laid down on disk ahead of provisioning, so
# provisioning always creates its tree on empty disk exactly as it does on a
# stack that is never seeded.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

# The Team folder's mount point, and the recordings root inside it. Both are
# hard-coded in the operator (nc_provision.go, webdav_upload.go); this mirrors
# them and fails loudly rather than guessing if the mount point moves.
SEED_MOUNT_POINT="Cassini"
SEED_RECORDINGS_SUBPATH="Recordings"
# Where compose.seed.yml binds a pack inside the Nextcloud container.
SEED_MOUNT_PATH="/cassini-seed"

PACK_DIR="${CASSINI_HARNESS_SEED_DIR:-}"
DRY_RUN=0
REPLACE=0

usage() {
  cat >&2 <<'EOF'
Usage:
  harness/bin/seed-nc-files.sh --pack <dir> [--replace] [--dry-run]

Load a seed pack — catalog.json plus meetings/<id>.opus, as written by
`cassini dev meetings pull` — into a running harness stack's Cassini Team
folder.

  --pack DIR   the seed pack (default: $CASSINI_HARNESS_SEED_DIR)
  --replace    clear the existing recordings tree first, so the stack holds
               the pack and nothing else
  --dry-run    validate the pack and report the plan, change nothing

Seeded meetings are readable by EVERY account on the stack. Production's
per-meeting permissions are not reproduced and cannot be.
EOF
}

die() { echo "[seed] error: $*" >&2; exit 1; }
seed_log() { printf '[seed] %s\n' "$*"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --pack)
      [[ $# -ge 2 ]] || die "--pack needs a value"
      PACK_DIR="$2"; shift 2 ;;
    --pack=*) PACK_DIR="${1#--pack=}"; shift ;;
    --replace) REPLACE=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage; die "unknown option: $1" ;;
  esac
done

[[ -n "$PACK_DIR" ]] || { usage; die "--pack is required (or set CASSINI_HARNESS_SEED_DIR)"; }
[[ -d "$PACK_DIR" ]] || die "$PACK_DIR is not a directory"
command -v jq >/dev/null 2>&1 || die "jq is required to read the pack's catalog"

PACK_DIR="$(cd "$PACK_DIR" && pwd)"

# --- validate the pack -------------------------------------------------------
#
# Every asset the catalog names must be on disk before anything is copied. A
# catalog pointing at a file that is not there produces an archive whose index
# advertises meetings that 404, which is worse than an empty stack because it
# looks like a product bug.

CATALOG="$PACK_DIR/catalog.json"
[[ -f "$CATALOG" ]] || die "$PACK_DIR holds no catalog.json, so it is not a seed pack"

catalog_version="$(jq -r '.version // ""' "$CATALOG")" \
  || die "$CATALOG is not valid JSON"
[[ "$catalog_version" == "cassini.viewer.catalog.v1" ]] \
  || die "$CATALOG declares version '$catalog_version'; this seeder reads cassini.viewer.catalog.v1"

mapfile -t PACK_ASSETS < <(jq -r '.meetings[]? | .audioPath // empty' "$CATALOG")
(( ${#PACK_ASSETS[@]} > 0 )) || die "$CATALOG lists no meetings, so there is nothing to seed"

for asset in "${PACK_ASSETS[@]}"; do
  case "$asset" in
    /*|*://*|..|../*|*/../*)
      # The same rule the pull applies when writing a pack. Re-checked here
      # because a pack can arrive from anywhere — a colleague, a USB stick — and
      # this step runs `cp` with the paths it names.
      die "catalog entry names '$asset', which is not a path inside the pack" ;;
  esac
  [[ -f "$PACK_DIR/$asset" ]] \
    || die "catalog names $asset, which is not in the pack; re-run the pull to complete it"
done

pack_bytes="$(du -sk "$PACK_DIR" | awk '{print $1 * 1024}')"

# --- locate the destination --------------------------------------------------

harness_stack_init

folders_json="$(occ groupfolders:list --output=json 2>/dev/null)" \
  || die "could not list Team folders; is the stack up? (cassini dev stack up)"

FOLDER_ID="$(jq -r --arg mp "$SEED_MOUNT_POINT" \
  'map(select(.mountPoint == $mp)) | first | .id // empty' <<<"$folders_json")"
if [[ -z "$FOLDER_ID" ]]; then
  die "this stack has no '$SEED_MOUNT_POINT' Team folder.
  It is created when the Cassini ExApp is installed and provisioned, so either
  the stack is not in installed-exapp mode (CASSINI_HARNESS_CASSINI_MODE), or
  'dev stack up' has not finished its ExApp install phase."
fi

DATA_DIR="$(occ config:system:get datadirectory 2>/dev/null | tr -d '\r')"
[[ -n "$DATA_DIR" ]] || die "could not read Nextcloud's datadirectory"

FOLDER_ROOT="$DATA_DIR/__groupfolders/$FOLDER_ID"
RECORDINGS_DIR="$FOLDER_ROOT/files/$SEED_RECORDINGS_SUBPATH"

seed_log "pack $PACK_DIR (${#PACK_ASSETS[@]} meeting(s), $((pack_bytes / 1024 / 1024)) MiB)"
seed_log "$SEED_MOUNT_POINT Team folder id=$FOLDER_ID -> $RECORDINGS_DIR"

if (( DRY_RUN )); then
  seed_log "dry run: would copy ${#PACK_ASSETS[@]} meeting(s) into the Team folder, merge them"
  seed_log "dry run: into its catalog, rescan, and protect catalog.json. Nothing was changed."
  exit 0
fi

# --- copy the pack in --------------------------------------------------------
#
# Two transports, one destination. A stack brought up with compose.seed.yml has
# the pack bind-mounted read-only inside the container, so the copy never leaves
# the container; a stack that was already running has not, so the bytes are
# streamed in as a tar. The steps after this point are identical either way.
#
# NOT `docker cp`. It is the obvious tool and it is not dependable here: Docker
# Desktop has been seen refusing every copy into a long-running container with
# an error about an unrelated file already existing, whatever the destination.
# `docker exec` is the transport everything else in this script already relies
# on, so the tar stream has one fewer way to fail.

NEXTCLOUD_CID="$(compose ps -q nextcloud)"
[[ -n "$NEXTCLOUD_CID" ]] || die "the nextcloud container is not running"

nc_root() { docker exec -i "$NEXTCLOUD_CID" "$@"; }

if (( REPLACE )); then
  seed_log "replacing the existing recordings tree"
  nc_root sh -c "rm -rf '$RECORDINGS_DIR'"
fi

nc_root sh -c "mkdir -p '$RECORDINGS_DIR/meetings'"

# The recordings, but NOT the catalog: the index is merged below rather than
# overwritten, so seeding a stack that already recorded meetings does not make
# those meetings vanish from the list.
if nc_root test -d "$SEED_MOUNT_PATH/meetings" 2>/dev/null; then
  seed_log "copying from the bind mount at $SEED_MOUNT_PATH"
  nc_root sh -c "cp -a '$SEED_MOUNT_PATH/meetings/.' '$RECORDINGS_DIR/meetings/'"
else
  seed_log "streaming the pack in (no $SEED_MOUNT_PATH mount on this stack)"
  # COPYFILE_DISABLE keeps macOS from adding AppleDouble members; the extract
  # side is always GNU tar, which otherwise warns about the xattr keywords bsdtar
  # writes and would make a clean run look like a failing one.
  COPYFILE_DISABLE=1 tar -C "$PACK_DIR/meetings" -cf - . \
    | nc_root sh -c "tar -C '$RECORDINGS_DIR/meetings' --warning=no-unknown-keyword -xf -"
fi
nc_root sh -c "chown -R www-data:www-data '$FOLDER_ROOT'"

# --- merge the index ---------------------------------------------------------
#
# Index last, objects first — the ordering the publish sink and the operator's
# backfill both use, so a run that dies part way leaves files nothing points at
# rather than an index pointing at files that are not there.
#
# Merged, and then filtered to what is actually on disk. Two things follow from
# that, both deliberate: a meeting this stack recorded itself survives a seed,
# and an entry naming a file that is not there is dropped rather than published
# as a meeting that 404s.

existing_catalog="$(nc_root sh -c "cat '$RECORDINGS_DIR/catalog.json' 2>/dev/null" || true)"
if [[ -z "$existing_catalog" ]] || ! jq -e . >/dev/null 2>&1 <<<"$existing_catalog"; then
  existing_catalog='{"version":"cassini.viewer.catalog.v1","meetings":[]}'
fi

present_json="$(nc_root sh -c "ls -1 '$RECORDINGS_DIR/meetings' 2>/dev/null" \
  | tr -d '\r' | jq -R . | jq -s .)"

merged_catalog="$(jq -n \
  --argjson existing "$existing_catalog" \
  --argjson incoming "$(cat "$CATALOG")" \
  --argjson present "$present_json" '
  def basename: sub("^.*/"; "");
  {
    version: "cassini.viewer.catalog.v1",
    # Incoming last so that a re-seed of the same meeting replaces the entry it
    # already had, rather than leaving a stale one beside the new file.
    meetings: (($existing.meetings // []) + ($incoming.meetings // []))
      | map(select((.audioPath // "") | basename | IN($present[])))
      | reduce .[] as $m ({}; .[$m.id] = $m)
      | to_entries | map(.value)
  }')"

nc_root sh -c "cat > '$RECORDINGS_DIR/catalog.json'" <<<"$merged_catalog"
nc_root sh -c "chown www-data:www-data '$RECORDINGS_DIR/catalog.json'"

merged_count="$(jq '.meetings | length' <<<"$merged_catalog")"
seed_log "catalog holds $merged_count meeting(s) after the merge"

# --- make Nextcloud see them -------------------------------------------------

seed_log "scanning the Team folder"
occ groupfolders:scan "$FOLDER_ID" >/dev/null

# Match production's floor: the authoritative catalog is owner-only, and the app
# filters it per caller. Without this the harness would be readable in a way
# production is not, exactly where the read proxy's whole job is that it is not.
if occ groupfolders:permissions "$FOLDER_ID" \
     "$SEED_RECORDINGS_SUBPATH/catalog.json" -g everyone -- -read >/dev/null 2>&1; then
  seed_log "catalog.json protected (everyone: deny)"
else
  seed_log "WARNING: could not deny 'everyone' on catalog.json."
  seed_log "WARNING: the meetings are seeded, but this stack lets any account read the"
  seed_log "WARNING: raw catalog, which production does not. Do not test the read proxy's"
  seed_log "WARNING: catalog filtering against it."
fi

held="$(nc_root sh -c "ls -1 '$RECORDINGS_DIR/meetings' 2>/dev/null | wc -l" | tr -d ' \r')"
seed_log "seeded ${#PACK_ASSETS[@]} meeting(s); the archive now holds $held"
seed_log "seeded meetings are readable by EVERY account on this stack — their production"
seed_log "permissions did not come with them, so do not test access control against them"
