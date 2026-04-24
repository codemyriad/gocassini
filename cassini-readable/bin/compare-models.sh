#!/usr/bin/env bash
set -euo pipefail

cat >&2 <<'EOF'
This wrapper is obsolete.

The old Python-based cassini-transcriber package was removed.
There is currently no standalone model-comparison CLI in this worktree.

If model comparison is still needed, it should be rebuilt on top of the native
Go readable-cleanup pipeline in cassini-go-recorder/internal/transcribe.
EOF

exit 1
