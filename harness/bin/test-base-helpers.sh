#!/usr/bin/env bash
# Offline regression for shared harness helpers, including macOS Bash 3.2.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091 # SCRIPT_DIR is resolved dynamically above.
source "$SCRIPT_DIR/lib/base.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }

values=()
harness_add_unique values alpha
harness_add_unique values alpha
harness_add_unique values beta

[[ "${#values[@]}" -eq 2 ]] || fail "expected two unique values, got ${#values[@]}"
[[ "${values[0]}" == "alpha" ]] || fail "first value changed: ${values[0]}"
[[ "${values[1]}" == "beta" ]] || fail "second value changed: ${values[1]}"

# shellcheck disable=SC1091 # SCRIPT_DIR is resolved dynamically above.
source "$SCRIPT_DIR/lib/stack.sh"

# Generic local startup accepts a ready 200 or one narrow capture-only state:
# structured 503 with CUDA as the sole unhealthy dimension. Contract tests can
# require either state explicitly; DB/storage/access failures are never hidden.
healthy_status='{"ok":true,"stt":{"device":"cuda","device_usable":true,"detail":"cuda ready"},"db":{"ok":true},"storage":{"work_root":{"ok":true},"site_root":{"ok":true}},"recordings_access":{"ok":true}}'
gpu_deferred_status='{"ok":false,"stt":{"device":"cuda","device_usable":false,"detail":"GPU-only: no NVIDIA device"},"db":{"ok":true},"storage":{"work_root":{"ok":true},"site_root":{"ok":true}},"recordings_access":{"ok":true}}'
broken_storage_status='{"ok":false,"stt":{"device":"cuda","device_usable":false,"detail":"GPU-only: no NVIDIA device"},"db":{"ok":true},"storage":{"work_root":{"ok":false},"site_root":{"ok":true}},"recordings_access":{"ok":true}}'
unset CASSINI_HARNESS_EXPECT_GPU_UNAVAILABLE
harness_operator_status_matches 200 "$healthy_status" \
  || fail "auto readiness rejected healthy operator status"
harness_operator_status_matches 503 "$gpu_deferred_status" \
  || fail "auto readiness rejected capture-only operator status"
export CASSINI_HARNESS_EXPECT_GPU_UNAVAILABLE=0
harness_operator_status_matches 200 "$healthy_status" \
  || fail "strict GPU-ready mode rejected healthy operator status"
if harness_operator_status_matches 503 "$gpu_deferred_status"; then
  fail "strict GPU-ready mode accepted a GPU-less 503"
fi
export CASSINI_HARNESS_EXPECT_GPU_UNAVAILABLE=1
harness_operator_status_matches 503 "$gpu_deferred_status" \
  || fail "explicit capture-only readiness rejected the sole-STT 503"
if harness_operator_status_matches 200 "$healthy_status"; then
  fail "explicit capture-only readiness accepted an unexpectedly ready GPU"
fi
if harness_operator_status_matches 503 "$broken_storage_status"; then
  fail "capture-only readiness hid a storage failure"
fi
if harness_operator_status_matches 503 'not-json'; then
  fail "capture-only readiness accepted malformed JSON"
fi
unset CASSINI_HARNESS_EXPECT_GPU_UNAVAILABLE

# Core/appapi topology has no Compose profile arguments.
SPREED_PROFILE=default
CASSINI_HARNESS_PUBLIC_MODE=local-http
CASSINI_HARNESS_SERVICE_MODE=core
docker() { printf '%s\n' "$*"; }
compose_output="$(compose ps)"
[[ "$compose_output" == *"compose -p $PROJECT_NAME -f $COMPOSE_FILE ps"* ]] \
  || fail "compose did not handle an empty profile argument list: $compose_output"

# Most OCC calls have no optional OC_PASS/NC_PASS environment arguments.
compose() { printf '%s\n' "$*"; }
unset OC_PASS NC_PASS
occ_output="$(occ app:list)"
[[ "$occ_output" == "exec -T -u www-data nextcloud php occ app:list" ]] \
  || fail "occ did not handle an empty environment argument list: $occ_output"

# LAN media configuration is explicit, including the ExApp callback URL.
SPREED_PROFILE=full
CASSINI_HARNESS_PUBLIC_MODE=lan-http
CASSINI_HARNESS_PUBLIC_URL=http://127.0.0.1:28080
CASSINI_HARNESS_MEDIA_HOST=192.0.2.10
CASSINI_HARNESS_SIGNALING_PUBLIC_URL=http://192.0.2.10:28082
# shellcheck disable=SC2034 # fixture: read by harness_stack_env_validate
CASSINI_HARNESS_RECORDING_BACKEND=installed-exapp
CASSINI_TALK_BACKEND_URL=http://reverse-proxy
harness_stack_env_validate || fail "valid LAN media configuration was rejected"
if (unset CASSINI_TALK_BACKEND_URL; harness_stack_env_validate >/dev/null 2>&1); then
  fail "LAN installed-ExApp configuration accepted a missing callback URL"
fi

# The media preflight must accept the configured signaling endpoint.
SIGNALING_URL="$CASSINI_HARNESS_SIGNALING_PUBLIC_URL"
curl() { printf '{"nextcloud-spreed-signaling":"Welcome"}\n'; }
preflight_output="$(harness_verify_lan_signaling_reachability)"
[[ "$preflight_output" == *"Host can reach Talk signaling at $SIGNALING_URL"* ]] \
  || fail "host signaling preflight rejected a reachable endpoint: $preflight_output"

echo "PASS: core harness helpers handle macOS-compatible nounset and signaling paths (Bash ${BASH_VERSION})"
