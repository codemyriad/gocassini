#!/usr/bin/env bash
# Offline regression for installed-ExApp callback reachability in local HTTP.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091 # SCRIPT_DIR is resolved dynamically above.
source "$SCRIPT_DIR/lib/stack.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }

[[ -f "$SCRIPT_DIR/../config/signaling-extra-ca.pem" ]] \
  || fail "the default signaling trust mount must be a checked-in file"

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

# A full media stack in remote mode needs the internal HTTPS helper even if
# the caller spells the service mode "full" instead of "full-remote".
callback_services="$(harness_compose_services_for_mode)"
grep -Fxq signaling-public-proxy <<<"$callback_services" \
  || fail "remote full stack omitted its callback HTTPS helper"
[[ -s "$SIGNALING_PUBLIC_PROXY_CERT" ]] || fail "remote helper certificate was not generated"
# Reject accidental trust bypasses: signaling trusts this exact generated cert.
if grep -q '^skipverify = true$' "$SIGNALING_CONF"; then
  fail "remote backend certificate verification was disabled"
fi
cp "$SIGNALING_PUBLIC_PROXY_CERT" "$TMP_DIR/original-cert"
harness_render_stack_configs
cmp -s "$SIGNALING_PUBLIC_PROXY_CERT" "$TMP_DIR/original-cert" \
  || fail "rendering unchanged remote config rotated the cached TLS identity"
CASSINI_HARNESS_PUBLIC_HOST=other.example.test
CASSINI_HARNESS_PUBLIC_URL=https://other.example.test
harness_render_stack_configs
if cmp -s "$SIGNALING_PUBLIC_PROXY_CERT" "$TMP_DIR/original-cert"; then
  fail "changing the public hostname did not regenerate its certificate"
fi
harness_stack_env_resolve
[[ "$CASSINI_HARNESS_SIGNALING_HOST_ALIAS" != "$CASSINI_HARNESS_PUBLIC_HOST" ]] \
  || fail "remote host mapping bypasses the Docker DNS HTTPS helper"
CASSINI_HARNESS_PUBLIC_MODE=lan-http
harness_stack_env_resolve
[[ "$CASSINI_HARNESS_SIGNALING_HOST_ALIAS" == "$CASSINI_HARNESS_PUBLIC_HOST" ]] \
  || fail "LAN HTTP lost its explicit public host mapping"

# A failing certificate command must fail the render even when the caller
# handles failure explicitly (and Bash therefore disables implicit errexit).
# shellcheck disable=SC2317 # Dispatched through the renderer command array.
openssl() { return 1; }
if harness_render_stack_configs >/dev/null 2>&1; then
  fail "certificate generation failure was swallowed"
fi
unset -f openssl

echo "PASS: local and remote callbacks use their configured Compose routes"
