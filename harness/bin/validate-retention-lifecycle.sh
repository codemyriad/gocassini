#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"
fixture_dir="$(mktemp -d)"
trap 'rm -rf "$fixture_dir"' EXIT
if command -v g++ >/dev/null; then
  cpp_runtime="$(g++ -print-file-name=libstdc++.so.6)"
  if [[ "$cpp_runtime" = /* ]]; then
    LD_LIBRARY_PATH="$(dirname "$cpp_runtime")${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
    export LD_LIBRARY_PATH
  fi
fi
TRANSCRIPTION_OPUS_FIXTURE="$fixture_dir/source.opus" go -C cassini-go-recorder test ./internal/portable -run '^TestTranscriptionPreservation$' -count=1
go -C cassini-go-recorder build -o "$fixture_dir/cassini" ./cmd/cassini
CASSINI_RETENTION_HARNESS=1 CASSINI_RETENTION_FIXTURE="$fixture_dir/source.opus" CASSINI_TEST_BIN="$fixture_dir/cassini" \
  go -C cassini-operator test ./internal/operator -run '^TestInstalledRetentionLifecycle$' -count=1 -v
