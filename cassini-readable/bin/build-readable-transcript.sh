#!/usr/bin/env bash
set -euo pipefail

cat >&2 <<'EOF'
This wrapper is obsolete.

The old Python-based cassini-transcriber package was removed.
Readable transcript generation now happens inside the native Go Cassini build
pipeline.

Use:
  ./bin/cassini build /path/to/meeting.mkv --out /tmp/meeting.meeting

If you need a standalone readable-only CLI, it must be reintroduced on top of
cassini-go-recorder/internal/transcribe.
EOF

exit 1
