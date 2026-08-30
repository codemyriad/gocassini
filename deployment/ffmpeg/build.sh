#!/bin/sh
set -eu

requested_version=${1:-latest}
prefix=${2:-/out/ffmpeg}
jobs=${FFMPEG_BUILD_JOBS:-2}
releases_url=${FFMPEG_RELEASES_URL:-https://ffmpeg.org/releases}
releases_url=${releases_url%/}
signing_key_url=${FFMPEG_SIGNING_KEY_URL:-https://ffmpeg.org/ffmpeg-devel.asc}
signing_fingerprint=${FFMPEG_SIGNING_KEY_FINGERPRINT:-FCF986EA15E6E293A5644F10B4322F04D67658D8}

script_dir=$(unset CDPATH; cd -- "$(dirname -- "$0")" && pwd)
if [ "$requested_version" = latest ]; then
  version=$("$script_dir/resolve-latest.sh")
else
  version=$requested_version
fi

case "$version" in
  ''|*[!0-9.]*|.*|*..*|*.)
    echo "invalid FFmpeg version: $version" >&2
    exit 1
    ;;
esac
case "$jobs" in
  ''|*[!0-9]*)
    echo "FFMPEG_BUILD_JOBS must be a positive integer, got: $jobs" >&2
    exit 1
    ;;
esac
test "$jobs" -gt 0 || {
  echo "FFMPEG_BUILD_JOBS must be positive, got: $jobs" >&2
  exit 1
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
archive="ffmpeg-$version.tar.xz"
source_url="$releases_url/$archive"

curl --fail --silent --show-error --location "$source_url" --output "$work/$archive"
curl --fail --silent --show-error --location "$source_url.asc" --output "$work/$archive.asc"
curl --fail --silent --show-error --location "$signing_key_url" --output "$work/ffmpeg-devel.asc"

actual_fingerprint=$(gpg --batch --show-keys --with-colons "$work/ffmpeg-devel.asc" \
  | awk -F: '$1 == "fpr" { print $10; exit }')
if [ "$actual_fingerprint" != "$signing_fingerprint" ]; then
  echo "FFmpeg signing-key fingerprint mismatch: got $actual_fingerprint, want $signing_fingerprint" >&2
  exit 1
fi

export GNUPGHOME="$work/gnupg"
mkdir -m 0700 "$GNUPGHOME"
gpg --batch --quiet --import "$work/ffmpeg-devel.asc"
gpg --batch --verify "$work/$archive.asc" "$work/$archive"

source_sha256=$(sha256sum "$work/$archive" | awk '{print $1}')
tar -xJf "$work/$archive" -C "$work"
source_dir="$work/ffmpeg-$version"
test -d "$source_dir" || {
  echo "FFmpeg archive did not contain $source_dir" >&2
  exit 1
}

cd "$source_dir"
./configure \
  --prefix="$prefix" \
  --disable-static \
  --enable-shared \
  --disable-autodetect \
  --disable-network \
  --disable-debug \
  --disable-doc \
  --disable-ffplay \
  --enable-libopus
make -j "$jobs"
make install

mkdir -p "$prefix/share/licenses/ffmpeg"
cp COPYING.LGPLv2.1 COPYING.LGPLv3 "$prefix/share/licenses/ffmpeg/"
cp LICENSE.md "$prefix/share/licenses/ffmpeg/" 2>/dev/null || true
rm -rf "$prefix/include" "$prefix/lib/pkgconfig" "$prefix/share/man"
find "$prefix" -type f -name '*.a' -delete

LD_LIBRARY_PATH="$prefix/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" \
  "$prefix/bin/ffmpeg" -version | head -n 1
LD_LIBRARY_PATH="$prefix/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" \
  "$prefix/bin/ffprobe" -version | head -n 1

cat >"$prefix/BUILDINFO" <<EOF
resolved_version=$version
source_url=$source_url
source_sha256=$source_sha256
signing_key_fingerprint=$signing_fingerprint
requested_version=$requested_version
EOF
LD_LIBRARY_PATH="$prefix/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" \
  "$prefix/bin/ffmpeg" -buildconf 2>/dev/null \
  | sed -n '/configuration:/,$p' >>"$prefix/BUILDINFO" || true
