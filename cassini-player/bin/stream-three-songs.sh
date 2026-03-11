#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

echo "warning: cassini-player/bin/stream-three-songs.sh is deprecated; use ./bin/cassini dev player three-songs ..." >&2
exec "$REPO_ROOT/bin/cassini" dev player three-songs "$@"
