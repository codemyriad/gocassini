#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

compose ps
echo
curl -fsS "$NEXTCLOUD_STATUS_URL" || true
echo

if [[ -f "$RUNTIME_DIR/last_call_url" ]]; then
  echo "last_call_url=$(cat "$RUNTIME_DIR/last_call_url")"
fi
