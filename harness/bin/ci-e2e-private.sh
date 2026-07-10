#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Exercise the same recorder/publisher path as ci-e2e.sh in a real Talk
# one-to-one conversation. bootstrap.sh creates the invited CI user, the
# publisher authenticates as that user, and the recorder joins through the
# HPB-internal recording path rather than guest access.
export ROOM_TYPE=1
export ROOM_INVITE="${ROOM_INVITE:-${BOT_USER:-ci-botuser}}"
export PUB_USERS=1
export AUTH_USERS="${AUTH_USERS:-$ROOM_INVITE}"
export AUTH_PASSWORDS="${AUTH_PASSWORDS:-${BOT_PASSWORD:-ci-bot-password}}"
export CALL_NAME="${CALL_NAME:-CI Gocassini private call}"

exec "$SCRIPT_DIR/ci-e2e.sh"
