#!/usr/bin/env bash
# ci-apt-update.sh — `apt-get update` that a third-party vendor repo cannot break.
#
# GitHub's hosted runner images ship apt sources for vendors we never install
# from: Google Chrome, Microsoft, and whatever the image adds next. Those repos
# are outside our control, and when one of them serves a bad index every job
# that runs `apt-get update` under `set -e` dies on the spot — exit 100, on a
# step whose package list is entirely stock Ubuntu.
#
# That is not hypothetical and it is not once. On 2026-09-09 the whole of `main`
# went red because dl.google.com/linux/chrome-stable published a Release naming
# one Packages.gz and served another:
#
#   E: Failed to fetch .../chrome-stable/deb/dists/stable/main/binary-amd64/Packages.gz
#      Hash Sum mismatch
#   E: Some index files failed to download.
#   ##[error]Process completed with exit code 100.
#
# Four workflows, nine jobs, nothing to do with the commit that happened to be
# on main. Earlier the same thing arrived from packages.microsoft.com, and the
# remedy then was a one-line `rm` naming that host, at three of the nine call
# sites. A deny-list of hosts we have already been burned by only ever buys time
# until the next vendor, so this inverts it.
#
# THE RULE. Every apt package this repo installs in CI — jq, ffmpeg, shellcheck,
# xsltproc, libxml2-utils, ripgrep, python3-numpy — comes from the Ubuntu
# archive. So a source that is not the Ubuntu archive can only cost us; it is
# dropped before the update, and the update then talks to Ubuntu alone.
#
# WHAT IT DOES NOT DO. Dropping a source list does not uninstall anything. The
# runner image's preinstalled Chrome, Firefox, Node and gh stay exactly where
# they are — this only changes which indexes apt fetches. A future job that
# genuinely needs a vendor package will fail loudly on the install, pointing
# here, which is the right way round: a visible failure in the job that wants
# the package beats an invisible one in every job that does not.
#
# CAREFUL: on Ubuntu 24.04 the MAIN ARCHIVE LIVES IN THIS DIRECTORY TOO, as the
# deb822 file /etc/apt/sources.list.d/ubuntu.sources. Emptying the directory
# breaks apt completely. Hence the predicate below keeps anything pointing at
# ubuntu.com and drops only the rest.
#
# Usage:
#   sudo scripts/ci-apt-update.sh                  # prune, then apt-get update
#   APT_SOURCES_DIR=/tmp/x scripts/ci-apt-update.sh --prune-only   # for tests

set -euo pipefail

# Overridable so the test suite can exercise the predicate against a fixture
# directory without root and without touching the host's apt configuration.
APT_SOURCES_DIR="${APT_SOURCES_DIR:-/etc/apt/sources.list.d}"
PRUNE_ONLY=0

usage() {
  cat >&2 <<'EOF'
Usage: ci-apt-update.sh [--prune-only]

Drops apt sources that are not the Ubuntu archive, then runs `apt-get update`.
Needs root for both, unless --prune-only is pointed at a writable
APT_SOURCES_DIR.

  --prune-only   drop the sources and stop; do not run apt-get update
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prune-only) PRUNE_ONLY=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage; echo "ci-apt-update.sh: unknown option: $1" >&2; exit 2 ;;
  esac
done

# apt_source_uris lists the repository URIs a source file declares, in either
# format the runner image uses: the one-line `deb [opts] URI suite components`
# and deb822's `URIs: URI`. Both are covered by pulling out every http(s) URL,
# which is what makes one predicate enough for both.
apt_source_uris() {
  grep -oE 'https?://[^[:space:]]+' "$1" 2>/dev/null || true
}

# apt_source_is_ubuntu answers whether a source file may stay.
#
# A file stays when it declares no URI at all (a comment-only leftover has
# nothing to break) or when every URI it declares is an Ubuntu archive host —
# archive.ubuntu.com, security.ubuntu.com, ports.ubuntu.com, and the runner's
# regional mirrors such as azure.archive.ubuntu.com, all of which end in
# ubuntu.com. Anything else is a vendor we do not install from.
apt_source_is_ubuntu() {
  local uris
  uris="$(apt_source_uris "$1")"
  [[ -n "$uris" ]] || return 0
  ! grep -qvE '^https?://([^/]*\.)?ubuntu\.com(/|$)' <<<"$uris"
}

prune_third_party_sources() {
  local dir="$1"
  local file dropped=0 kept=0

  [[ -d "$dir" ]] || { echo "[apt] no $dir; nothing to prune" >&2; return 0; }

  local files=()
  while IFS= read -r file; do
    files+=("$file")
  done < <(find "$dir" -maxdepth 1 -type f \( -name '*.list' -o -name '*.sources' \) | sort)

  for file in ${files[@]+"${files[@]}"}; do
    if apt_source_is_ubuntu "$file"; then
      kept=$((kept + 1))
      continue
    fi
    echo "[apt] dropping third-party source: ${file##*/}" >&2
    rm -f "$file"
    dropped=$((dropped + 1))
  done

  echo "[apt] kept $kept Ubuntu source(s), dropped $dropped third-party source(s)" >&2
}

prune_third_party_sources "$APT_SOURCES_DIR"

if (( PRUNE_ONLY )); then
  exit 0
fi

# Retries cover the ordinary transient mirror blip, which is a different failure
# from the one above and is worth absorbing rather than reporting.
apt-get update -o Acquire::Retries=3
