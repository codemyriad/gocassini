#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

ROOM_NAME="Codex test room $(date -u +%Y%m%d-%H%M%S)"
ROOM_TYPE="${ROOM_TYPE:-3}"
ROOM_INVITE="${ROOM_INVITE:-}"
ROOM_CREATOR_USER="${ROOM_CREATOR_USER:-$ADMIN_USER}"
ROOM_CREATOR_PASSWORD="${ROOM_CREATOR_PASSWORD:-$ADMIN_PASSWORD}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name)
      ROOM_NAME="$2"
      shift 2
      ;;
    --room-type)
      ROOM_TYPE="$2"
      shift 2
      ;;
    --invite)
      ROOM_INVITE="$2"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ "$ROOM_TYPE" == "1" && -z "$ROOM_INVITE" ]]; then
  echo "room type 1 requires --invite or ROOM_INVITE" >&2
  exit 2
fi
if [[ "$ROOM_TYPE" != "1" && -n "$ROOM_INVITE" ]]; then
  # For group/public rooms Talk's 'invite' field means a group/circle ID, not
  # a user, and setting it would silently drop the room name. Refuse instead
  # of changing semantics behind the caller's back.
  echo "--invite/ROOM_INVITE is only supported for roomType=1 (one-to-one)" >&2
  exit 2
fi

wait_for_nextcloud 180

create_url="$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v4/room"
create_fields=(--data-urlencode "roomType=$ROOM_TYPE")
if [[ "$ROOM_TYPE" == "1" ]]; then
  # A one-to-one conversation is created by inviting its other participant;
  # roomName is only meaningful for named group/public conversations.
  create_fields+=(--data-urlencode "invite=$ROOM_INVITE")
else
  create_fields+=(--data-urlencode "roomName=$ROOM_NAME")
fi
response="$(
  curl -sS \
    -u "$ROOM_CREATOR_USER:$ROOM_CREATOR_PASSWORD" \
    -H 'OCS-APIRequest: true' \
    -H 'Accept: application/json' \
    -X POST "$create_url" \
    "${create_fields[@]}"
)"

room_token="$(
  python3 - "$response" <<'PY'
import json
import sys

raw = sys.argv[1]
data = json.loads(raw)
ocs = data.get("ocs", {})
meta = ocs.get("meta", {})
if meta.get("status") != "ok":
    raise SystemExit(f"room creation failed: {meta}")
payload = ocs.get("data")
token = ""
if isinstance(payload, str):
    token = payload
elif isinstance(payload, dict):
    for key in ("token", "roomToken", "id"):
        value = payload.get(key)
        if isinstance(value, str) and value:
            token = value
            break
if not token:
    raise SystemExit(f"room creation response had no token: {payload}")
print(token)
PY
)"

call_url="${NEXTCLOUD_PUBLIC_URL%/}/call/$room_token"
printf '%s\n' "$room_token" > "$RUNTIME_DIR/last_room_token"
printf '%s\n' "$call_url" > "$RUNTIME_DIR/last_call_url"

log "Created room token: $room_token"
log "Call URL: $call_url"

# Keep last line machine-friendly for command substitution.
echo "$call_url"
