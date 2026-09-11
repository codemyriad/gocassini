#!/usr/bin/env bash
# Installed AppAPI routes + HPB probe + real CPU recordings before/after restart.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# The legacy stack orchestrator uses global container/volume names. Refuse
# BEFORE it can reset or clean up somebody else's local installation.
if docker ps -a --format '{{.Names}}' | grep -Eq '^(appapi-harp|nc_app_gocassini|cassini-exapp)$'; then
  printf '%s\n' 'Recording-readiness e2e needs a dedicated Docker environment; an existing Cassini/HaRP container owns its fixed names.' >&2
  exit 1
fi
export CASSINI_VALIDATE_READINESS=1
export CASSINI_EXPECT_GPU_UNAVAILABLE=1
exec "$SCRIPT_DIR/ci-e2e-installed-exapp-talk.sh" "$@"
