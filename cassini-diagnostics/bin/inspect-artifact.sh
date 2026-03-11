#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

echo "warning: cassini-diagnostics/bin/inspect-artifact.sh is deprecated; use ./bin/cassini inspect ..." >&2
exec "$REPO_ROOT/bin/cassini" inspect "$@"
