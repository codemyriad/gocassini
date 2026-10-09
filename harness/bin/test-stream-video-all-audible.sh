#!/usr/bin/env bash
# Hermetic check that stream-video.sh hands the all-audible choice to the Go
# rotator: off by default, on with ROTATOR_ALL_AUDIBLE=1 or --all-audible, and
# a typo in the env var is refused instead of silently rotating. A fake `go`
# records the rotator arguments, so no stack, media or Go toolchain is needed.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STREAM_VIDEO="$SCRIPT_DIR/stream-video.sh"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

mkdir -p "$TMP_DIR/bin" "$TMP_DIR/rotator" "$TMP_DIR/media"
touch "$TMP_DIR/rotator/main.go" "$TMP_DIR/media/bot.ivf" "$TMP_DIR/media/bot.ogg"
cat >"$TMP_DIR/bin/go" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$@" >"$FAKE_GO_ARGS"
SH
chmod +x "$TMP_DIR/bin/go"

# run_streamer <args-file> [stream-video.sh args...]
run_streamer() {
  local args_file="$1"
  shift
  PATH="$TMP_DIR/bin:$PATH" FAKE_GO_ARGS="$args_file" GO_ROTATOR_DIR="$TMP_DIR/rotator" \
    "$STREAM_VIDEO" --call-url "https://cloud.example.test/call/token" \
    --skip-prepare --media-prefix "$TMP_DIR/media/bot" "$@" >/dev/null 2>&1
}

(unset ROTATOR_ALL_AUDIBLE; run_streamer "$TMP_DIR/default.args") \
  || fail "default run failed"
grep -qx -- '--all-audible' "$TMP_DIR/default.args" \
  && fail "default run must keep the rotation (no --all-audible)"
grep -qx -- '--rotate-seconds' "$TMP_DIR/default.args" \
  || fail "default run lost --rotate-seconds"

ROTATOR_ALL_AUDIBLE=1 run_streamer "$TMP_DIR/env.args" \
  || fail "ROTATOR_ALL_AUDIBLE=1 run failed"
grep -qx -- '--all-audible' "$TMP_DIR/env.args" \
  || fail "ROTATOR_ALL_AUDIBLE=1 did not reach the rotator"

(unset ROTATOR_ALL_AUDIBLE; run_streamer "$TMP_DIR/flag.args" --all-audible) \
  || fail "--all-audible run failed"
grep -qx -- '--all-audible' "$TMP_DIR/flag.args" \
  || fail "--all-audible did not reach the rotator"

if ROTATOR_ALL_AUDIBLE=yes run_streamer "$TMP_DIR/typo.args"; then
  fail "ROTATOR_ALL_AUDIBLE=yes must be refused"
fi

echo "PASS: stream-video.sh passes the all-audible choice to the rotator only when asked"
