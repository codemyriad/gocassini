#!/usr/bin/env bash
# Configure downloads only: never cache package indexes or installed system files.
# Call before ci-apt-update.sh and the existing apt-get install commands.
set -euo pipefail

archive_dir="${1:?usage: ci-apt-package-cache.sh /absolute/archive/directory}"
if [[ "$archive_dir" != /* || "$archive_dir" == *[\"\\\;]* || "$archive_dir" == *$'\n'* ]]; then
  echo 'APT archive directory must be an absolute path without config metacharacters' >&2
  exit 2
fi
config_dir="${APT_CONFIG_DIR:-/etc/apt/apt.conf.d}"
mkdir -p "$archive_dir" "$config_dir"
chmod 755 "$archive_dir"
printf 'Dir::Cache::archives "%s";\nAPT::Keep-Downloaded-Packages "true";\nBinary::apt::APT::Keep-Downloaded-Packages "true";\n' \
  "$archive_dir" > "$config_dir/99-cassini-package-cache"
echo "[apt] package downloads cached in $archive_dir"
