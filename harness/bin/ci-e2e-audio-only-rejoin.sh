#!/usr/bin/env bash
# Keep the existing opt-in rejoin leg and run the same recovery gate by default.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export RETAIN_VIDEO=false
exec "$SCRIPT_DIR/ci-e2e-rejoin.sh" "$@"
