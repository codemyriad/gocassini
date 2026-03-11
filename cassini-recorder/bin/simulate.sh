#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

echo "warning: cassini-recorder/bin/simulate.sh is deprecated; use ./bin/cassini record --simulate ..." >&2
cd "$REPO_ROOT/cassini-go-recorder"
exec go run ./cmd/gocassini --mode simulate "$@"
