#!/usr/bin/env bash
# test-ci-apt-update.sh — offline unit test for scripts/ci-apt-update.sh.
#
# Exercises the predicate that decides which apt sources survive, against
# fixture directories in a temp dir. No root, no apt, no network: this belongs
# in the fast every-PR lint gate, so it must not touch the host's apt state.
#
# The load-bearing case is the LAST one. Ubuntu 24.04 keeps the main archive in
# /etc/apt/sources.list.d/ubuntu.sources — the same directory the vendor repos
# live in — so a pruner that empties the directory breaks apt entirely. That is
# the mistake this file exists to catch.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SUT="$SCRIPT_DIR/ci-apt-update.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

failures=0

fail() {
  echo "[test] FAIL: $*" >&2
  failures=$((failures + 1))
}

# prune runs the script against a fresh fixture directory and echoes its path.
prune() {
  local dir="$1"
  APT_SOURCES_DIR="$dir" "$SUT" --prune-only >/dev/null 2>&1
}

assert_present() {
  [[ -f "$1" ]] || fail "$2: expected $(basename "$1") to survive, it was dropped"
}

assert_absent() {
  [[ ! -f "$1" ]] || fail "$2: expected $(basename "$1") to be dropped, it survived"
}

# --- 1. the two vendors that have actually broken main ----------------------

case1="$WORK/case1"; mkdir -p "$case1"
echo 'deb [arch=amd64] https://dl.google.com/linux/chrome-stable/deb/ stable main' \
  > "$case1/google-chrome.list"
echo 'deb [arch=amd64] https://packages.microsoft.com/ubuntu/24.04/prod noble main' \
  > "$case1/microsoft-prod.list"
echo 'deb http://azure.archive.ubuntu.com/ubuntu noble main universe' \
  > "$case1/ubuntu-mirror.list"
prune "$case1"
assert_absent "$case1/google-chrome.list" "case1"
assert_absent "$case1/microsoft-prod.list" "case1"
assert_present "$case1/ubuntu-mirror.list" "case1"

# --- 2. every Ubuntu archive host the runners use ----------------------------

case2="$WORK/case2"; mkdir -p "$case2"
for host in archive.ubuntu.com security.ubuntu.com ports.ubuntu.com \
            azure.archive.ubuntu.com eastus.azure.archive.ubuntu.com; do
  echo "deb http://$host/ubuntu noble main" > "$case2/${host}.list"
done
prune "$case2"
for host in archive.ubuntu.com security.ubuntu.com ports.ubuntu.com \
            azure.archive.ubuntu.com eastus.azure.archive.ubuntu.com; do
  assert_present "$case2/${host}.list" "case2"
done

# --- 3. a host that merely looks like Ubuntu is still a vendor ---------------
#
# The predicate anchors on the host, not on a substring, so neither a prefix nor
# a suffix dressed up as the archive gets to stay.

case3="$WORK/case3"; mkdir -p "$case3"
echo 'deb http://notubuntu.com/ubuntu noble main' > "$case3/lookalike-prefix.list"
echo 'deb http://archive.ubuntu.com.vendor.net/ubuntu noble main' > "$case3/lookalike-suffix.list"
prune "$case3"
assert_absent "$case3/lookalike-prefix.list" "case3"
assert_absent "$case3/lookalike-suffix.list" "case3"

# --- 4. a mixed file is a vendor file ----------------------------------------
#
# One non-Ubuntu URI is enough to condemn the file: apt would still fetch that
# index, which is the whole failure being prevented.

case4="$WORK/case4"; mkdir -p "$case4"
{
  echo 'deb http://archive.ubuntu.com/ubuntu noble main'
  echo 'deb https://dl.google.com/linux/chrome-stable/deb/ stable main'
} > "$case4/mixed.list"
prune "$case4"
assert_absent "$case4/mixed.list" "case4"

# --- 5. comment-only and empty files are left alone --------------------------

case5="$WORK/case5"; mkdir -p "$case5"
printf '# nothing to see here\n' > "$case5/comments.list"
: > "$case5/empty.sources"
prune "$case5"
assert_present "$case5/comments.list" "case5"
assert_present "$case5/empty.sources" "case5"

# --- 6. deb822, which is how Ubuntu 24.04 ships the MAIN ARCHIVE -------------
#
# The regression this whole file is here for: ubuntu.sources lives in
# sources.list.d alongside the vendor lists, and dropping it would leave the
# runner with no archive at all — every apt-get install after the update would
# fail to find its package.

case6="$WORK/case6"; mkdir -p "$case6"
cat > "$case6/ubuntu.sources" <<'EOF'
Types: deb
URIs: http://azure.archive.ubuntu.com/ubuntu/
Suites: noble noble-updates noble-backports
Components: main universe restricted multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg

Types: deb
URIs: http://azure.archive.ubuntu.com/ubuntu/
Suites: noble-security
Components: main universe restricted multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
EOF
cat > "$case6/vendor.sources" <<'EOF'
Types: deb
URIs: https://dl.google.com/linux/chrome-stable/deb/
Suites: stable
Components: main
EOF
prune "$case6"
assert_present "$case6/ubuntu.sources" "case6"
assert_absent "$case6/vendor.sources" "case6"

# --- 7. files apt does not read are not touched ------------------------------

case7="$WORK/case7"; mkdir -p "$case7"
echo 'deb https://dl.google.com/linux/chrome-stable/deb/ stable main' > "$case7/google-chrome.list.save"
echo 'deb https://dl.google.com/linux/chrome-stable/deb/ stable main' > "$case7/README"
prune "$case7"
assert_present "$case7/google-chrome.list.save" "case7"
assert_present "$case7/README" "case7"

# --- 8. a missing directory is not an error ----------------------------------

APT_SOURCES_DIR="$WORK/does-not-exist" "$SUT" --prune-only >/dev/null 2>&1 \
  || fail "case8: a missing sources directory should be tolerated"

if (( failures > 0 )); then
  echo "[test] ci-apt-update.sh: $failures assertion(s) failed" >&2
  exit 1
fi

echo "[test] ci-apt-update.sh source pruning OK"
