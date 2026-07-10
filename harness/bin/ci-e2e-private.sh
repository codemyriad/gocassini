#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

# Exercise the same recorder/publisher path as ci-e2e.sh in a real Talk
# one-to-one conversation. bootstrap.sh creates the invited CI user, the
# publisher authenticates as that user, and the recorder joins through the
# HPB-internal recording path rather than guest access.
export BOT_USER BOT_PASSWORD
export ROOM_TYPE=1
export ROOM_INVITE="$BOT_USER"
export PUB_USERS=1
export AUTH_USERS="$BOT_USER"
export AUTH_PASSWORDS="$BOT_PASSWORD"
export CALL_NAME="${CALL_NAME:-CI Gocassini private call}"

exec "$SCRIPT_DIR/ci-e2e.sh"
