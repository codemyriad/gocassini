#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/releases.html" <<'EOF'
<a href="ffmpeg-1.9.9.tar.xz">ffmpeg-1.9.9.tar.xz</a>
<a href="ffmpeg-2.0.tar.xz">ffmpeg-2.0.tar.xz</a>
<a href="ffmpeg-2.0.1.tar.xz.asc">ffmpeg-2.0.1.tar.xz.asc</a>
<a href="ffmpeg-2.0.1.tar.xz">ffmpeg-2.0.1.tar.xz</a>
<a href="ffmpeg-snapshot.tar.xz">ffmpeg-snapshot.tar.xz</a>
<a href="ffmpeg-3.0-beta1.tar.xz">ffmpeg-3.0-beta1.tar.xz</a>
EOF

got=$(FFMPEG_RELEASES_INDEX_FILE="$tmp/releases.html" "$root/resolve-latest.sh")
if [[ "$got" != 2.0.1 ]]; then
  echo "resolve-latest returned $got, want 2.0.1" >&2
  exit 1
fi

: >"$tmp/empty.html"
if FFMPEG_RELEASES_INDEX_FILE="$tmp/empty.html" "$root/resolve-latest.sh" >/dev/null 2>&1; then
  echo "resolve-latest accepted an index with no stable releases" >&2
  exit 1
fi

echo "ffmpeg release resolver tests passed"
