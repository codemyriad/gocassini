#!/bin/sh
set -eu

releases_url="${FFMPEG_RELEASES_URL:-https://ffmpeg.org/releases/}"

if [ -n "${FFMPEG_RELEASES_INDEX_FILE:-}" ]; then
  index_file=$FFMPEG_RELEASES_INDEX_FILE
  test -r "$index_file" || {
    echo "FFMPEG_RELEASES_INDEX_FILE is not readable: $index_file" >&2
    exit 1
  }
  index=$(sed -n '1,20000p' "$index_file")
else
  index=$(curl --fail --silent --show-error --location "$releases_url")
fi

version=$(printf '%s\n' "$index" \
  | grep -oE 'ffmpeg-[0-9]+\.[0-9]+(\.[0-9]+)?\.tar\.xz' \
  | sed -E 's/^ffmpeg-//; s/\.tar\.xz$//' \
  | LC_ALL=C sort -Vu \
  | tail -n 1)

case "$version" in
  ''|*[!0-9.]*|.*|*..*|*.)
    echo "could not resolve a stable FFmpeg release from $releases_url" >&2
    exit 1
    ;;
esac

printf '%s\n' "$version"
