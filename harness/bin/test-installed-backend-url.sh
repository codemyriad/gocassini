#!/usr/bin/env bash
# Offline regression for installed-ExApp callback reachability in local HTTP.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091 # SCRIPT_DIR is resolved dynamically above.
source "$SCRIPT_DIR/lib/stack.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

unset CASSINI_TALK_BACKEND_URL
export CASSINI_HARNESS_CASSINI_MODE=installed-exapp
export CASSINI_HARNESS_PUBLIC_MODE=local-http
export CASSINI_HARNESS_HOST=10.0.2.15
harness_default_installed_exapp_backend_url
[[ "$CASSINI_TALK_BACKEND_URL" == "http://reverse-proxy" ]] \
  || fail "local-http VM topology did not select reverse-proxy callback URL"

export CASSINI_TALK_BACKEND_URL="https://explicit.example.test"
harness_default_installed_exapp_backend_url
[[ "$CASSINI_TALK_BACKEND_URL" == "https://explicit.example.test" ]] \
  || fail "explicit callback URL was overwritten"

unset CASSINI_TALK_BACKEND_URL
export CASSINI_HARNESS_PUBLIC_MODE=remote-https
harness_default_installed_exapp_backend_url
[[ -z "${CASSINI_TALK_BACKEND_URL:-}" ]] \
  || fail "remote HTTPS topology received a local reverse-proxy override"

unset CASSINI_TALK_BACKEND_URL
export CASSINI_HARNESS_PUBLIC_MODE=local-http
export CASSINI_HARNESS_CASSINI_MODE=none
harness_default_installed_exapp_backend_url
[[ -z "${CASSINI_TALK_BACKEND_URL:-}" ]] \
  || fail "non-installed topology received an ExApp callback override"

# Local installed mode must also render signaling with dev-only allowall so
# Nextcloud callbacks authenticated from the Docker-internal reverse-proxy
# origin are accepted. Remote HTTPS keeps explicit backend allowlisting.
export RUNTIME_DIR="$TMP_DIR/runtime"
export SPREED_PROFILE=full
export SIGNALING_SHARED_SECRET=test-signaling-shared
export SIGNALING_INTERNAL_SECRET=test-signaling-internal
export TURN_SHARED_SECRET=test-turn-shared
export TURN_SERVER=127.0.0.1:13479
export CASSINI_HARNESS_CASSINI_MODE=installed-exapp
export CASSINI_HARNESS_SERVICE_MODE=full
export CASSINI_HARNESS_PUBLIC_MODE=local-http
export CASSINI_HARNESS_PUBLIC_URL=""
export CASSINI_HARNESS_PUBLIC_HOST=""
export CASSINI_HARNESS_MEDIA_HOST=127.0.0.1
harness_render_stack_configs
grep -q '^allowall = true$' "$SIGNALING_CONF" \
  || fail "local installed signaling config does not allow authenticated internal origins"
grep -q '^secret = test-signaling-shared$' "$SIGNALING_CONF" \
  || fail "local installed allowall config lacks the shared secret"

export CASSINI_HARNESS_PUBLIC_MODE=remote-https
export CASSINI_HARNESS_PUBLIC_URL=https://cassini.example.test
export CASSINI_HARNESS_PUBLIC_HOST=cassini.example.test
harness_render_stack_configs
grep -q '^allowall = false$' "$SIGNALING_CONF" \
  || fail "remote signaling config should retain explicit backend allowlisting"

echo "PASS: local installed ExApp callbacks use Compose DNS and authenticated internal signaling"
