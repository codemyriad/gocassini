#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

echo "warning: cassini-lab/bin/ci-e2e.sh is deprecated; use ./bin/cassini dev ci-e2e ..." >&2
exec "$REPO_ROOT/bin/cassini" dev ci-e2e "$@"
