#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

echo "warning: cassini-lab/bin/status.sh is deprecated; use ./bin/cassini dev stack status ..." >&2
exec "$REPO_ROOT/bin/cassini" dev stack status "$@"
