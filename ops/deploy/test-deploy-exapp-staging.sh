#!/usr/bin/env bash
#
# Offline regression tests for deploy-exapp.sh manifest staging.
#
# ssh/scp/docker/curl are stubs: no network, daemon or registry is used. The
# full deploy driver still runs, which pins the production properties that a
# helper-only test could miss: host/container paths are distinct per run,
# unrelated remote stdout cannot corrupt their handoff, the rendered manifest
# reaches the container, settled exits clean both files, and a timed-out
# detached register retains them.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY="$SCRIPT_DIR/deploy-exapp.sh"
TEST_ROOT="$(mktemp -d)"
STUB_BIN="$TEST_ROOT/bin"
STATE="$TEST_ROOT/state"
mkdir -p "$STUB_BIN" "$STATE/files" "$STATE/captured"
trap 'rm -rf "$TEST_ROOT"' EXIT

FAILURES=0
ok()   { printf 'ok   %s\n' "$1"; }
fail() { printf 'FAIL %s\n' "$1"; FAILURES=$((FAILURES + 1)); }

expect_eq() {
  local desc="$1" want="$2" got="$3"
  if [[ "$want" == "$got" ]]; then
    ok "$desc"
  else
    fail "$desc (want '$want', got '$got')"
  fi
}

expect_file() {
  local desc="$1" file="$2"
  if [[ -f "$file" ]]; then ok "$desc"; else fail "$desc (missing $file)"; fi
}

expect_no_file() {
  local desc="$1" file="$2"
  if [[ ! -e "$file" ]]; then ok "$desc"; else fail "$desc (still exists: $file)"; fi
}

expect_contains() {
  local desc="$1" pattern="$2" file="$3"
  if grep -Fq -- "$pattern" "$file"; then
    ok "$desc"
  else
    fail "$desc (missing '$pattern' in $file)"
  fi
}

cat > "$STUB_BIN/docker" <<'STUB'
#!/usr/bin/env bash
set -uo pipefail
if [[ "${1:-}" == buildx && "${2:-}" == imagetools && "${3:-}" == inspect ]]; then
  printf '{}\n'
  exit 0
fi
echo "unexpected local docker call" >&2
exit 98
STUB

cat > "$STUB_BIN/curl" <<'STUB'
#!/usr/bin/env bash
printf '{"version":1}\n'
STUB

cat > "$STUB_BIN/scp" <<'STUB'
#!/usr/bin/env bash
set -uo pipefail
if [[ -n "${TEST_SCP_EXIT:-}" ]]; then
  exit "$TEST_SCP_EXIT"
fi
src="${@: -2:1}"
dest="${@: -1}"
remote_path="${dest#*:}"
base="${remote_path##*/}"
cp "$src" "$TEST_STATE/files/host.$base"
cp "$src" "$TEST_STATE/captured/$base.xml"
printf '%s|%s\n' "$remote_path" "$TEST_STATE/captured/$base.xml" \
  >> "$TEST_STATE/scp.log"
STUB

cat > "$STUB_BIN/ssh" <<'STUB'
#!/usr/bin/env bash
set -uo pipefail

remote_command="${*: -1}"
body="$(cat)"

env_value() {
  local name="$1" rest
  rest="${remote_command#*${name}=\'}"
  [[ "$rest" != "$remote_command" ]] || return 1
  printf '%s\n' "${rest%%\'*}"
}

# Allocate one file in each remote namespace. The counter only models mktemp;
# the production script performs the actual atomic allocation on its target.
if [[ "$body" == *'host_manifest="$(mktemp /tmp/cassini-deploy-manifest.host.XXXXXXXX)"'* ]]; then
  n=0
  [[ ! -f "$TEST_STATE/counter" ]] || n="$(<"$TEST_STATE/counter")"
  n=$((n + 1))
  printf '%s\n' "$n" > "$TEST_STATE/counter"
  printf -v suffix '%04d' "$n"
  host="/tmp/cassini-deploy-manifest.host.HOST$suffix"
  container="/tmp/cassini-deploy-manifest.container.CONT$suffix"
  : > "$TEST_STATE/files/host.${host##*/}"
  : > "$TEST_STATE/files/container.${container##*/}"
  printf '%s|%s\n' "$host" "$container" >> "$TEST_STATE/allocations.log"
  [[ -z "${TEST_STAGE_CHATTER:-}" ]] || printf '%s\n' "$TEST_STAGE_CHATTER"
  printf 'CASSINI_DEPLOY_STAGE=%s|%s\n' "$host" "$container"
  exit 0
fi

if [[ "$body" == *'docker cp "$CASSINI_STAGE_HOST"'* ]]; then
  host="$(env_value CASSINI_STAGE_HOST)"
  container="$(env_value CASSINI_STAGE_CONTAINER)"
  cp "$TEST_STATE/files/host.${host##*/}" \
    "$TEST_STATE/files/container.${container##*/}"
  printf '%s|%s\n' "$host" "$container" >> "$TEST_STATE/copies.log"
  exit 0
fi

if [[ "$body" == *'cleanup_rc=0'* ]]; then
  host="$(env_value CASSINI_STAGE_HOST)"
  container="$(env_value CASSINI_STAGE_CONTAINER)"
  rm -f "$TEST_STATE/files/host.${host##*/}" \
    "$TEST_STATE/files/container.${container##*/}"
  printf '%s|%s\n' "$host" "$container" >> "$TEST_STATE/cleanups.log"
  exit "${TEST_CLEANUP_EXIT:-0}"
fi

# All remaining calls are remote_occ. Decode only enough of its NUL-delimited
# argv to choose a deterministic response; never log the --env arguments.
payload="${remote_command#*CASSINI_ARGV=\'}"
payload="${payload%%\'*}"
mapfile -t occ_argv < <(printf '%s' "$payload" | base64 -d | tr '\0' '\n')
cmd="${occ_argv[0]:-}"

case "$cmd" in
  app_api:app:list)
    printf 'gocassini (Cassini): 0.2.0-beta.3 [enabled]\n'
    ;;
  config:app:get)
    if [[ "${occ_argv[2]:-}" == recording_servers ]]; then
      printf '{"server":"https://nextcloud.invalid/index.php/apps/app_api/proxy/gocassini"}\n'
    fi
    ;;
  app_api:app:register)
    info_xml=""
    for ((i = 0; i < ${#occ_argv[@]}; i++)); do
      if [[ "${occ_argv[$i]}" == --info-xml ]]; then
        info_xml="${occ_argv[$((i + 1))]:-}"
      fi
    done
    printf '%s\n' "$info_xml" >> "$TEST_STATE/register-paths.log"
    case "${TEST_REGISTER_MODE:-success}" in
      success) ;;
      fail) exit 42 ;;
      timeout)
        echo 'occ did not finish within 1s; it is STILL RUNNING detached.' >&2
        exit 124
        ;;
      *) exit 97 ;;
    esac
    ;;
esac
exit 0
STUB

chmod +x "$STUB_BIN/docker" "$STUB_BIN/curl" "$STUB_BIN/scp" "$STUB_BIN/ssh"

cat > "$TEST_ROOT/inventory.env" <<'INVENTORY'
CASSINI_DEPLOY_NAME=offline-staging-test
CASSINI_NC_SSH=fake-host
CASSINI_NC_CONTAINER=fake-nextcloud
CASSINI_NC_URL=https://nextcloud.invalid
CASSINI_APP_ID=gocassini
CASSINI_DAEMON=fake-daemon
CASSINI_COMPUTE_DEVICE=cpu
CASSINI_IMAGE_REPO=example.invalid/gocassini
CASSINI_SECRET_CMD_RECORDING="printf '%s' test-recording-secret"
CASSINI_SECRET_CMD_SIGNALING="printf '%s' test-signaling-secret"
INVENTORY

run_deploy() {
  local name="$1" mode="$2" scp_exit="${3:-}" stage_chatter="${4:-}" rc
  local output="$TEST_ROOT/$name.out"
  TEST_STATE="$STATE" TEST_REGISTER_MODE="$mode" TEST_SCP_EXIT="$scp_exit" \
    TEST_STAGE_CHATTER="$stage_chatter" \
    PATH="$STUB_BIN:$PATH" \
    "$DEPLOY" --inventory "$TEST_ROOT/inventory.env" \
      --tag 0.2.0-beta.4 --src-ref v0.2.0-beta.4 --apply \
      >"$output" 2>&1
  rc=$?
  printf '%s\n' "$rc"
}

# Two complete runs prove there is no process-global staging name and exercise
# normal success cleanup.
rc="$(run_deploy success-one success)"
expect_eq "first deploy succeeds" 0 "$rc"
rc="$(run_deploy success-two success)"
expect_eq "second deploy succeeds" 0 "$rc"

first_allocation="$(sed -n '1p' "$STATE/allocations.log")"
second_allocation="$(sed -n '2p' "$STATE/allocations.log")"
first_host="${first_allocation%%|*}"
first_container="${first_allocation#*|}"
second_host="${second_allocation%%|*}"
second_container="${second_allocation#*|}"

if [[ "$first_host" != "$first_container" ]]; then
  ok "host and container staging paths are distinct"
else
  fail "host and container staging paths collided"
fi
if [[ "$first_host" != "$second_host" && "$first_container" != "$second_container" ]]; then
  ok "successive deploys receive unique staging paths"
else
  fail "successive deploys reused staging paths"
fi

first_capture="$STATE/captured/${first_host##*/}.xml"
expect_file "rendered manifest was uploaded" "$first_capture"
expect_contains "uploaded manifest carries the release image tag" \
  '<image-tag>0.2.0-beta.4</image-tag>' "$first_capture"
expect_eq "registration uses the first container path" "$first_container" \
  "$(sed -n '1p' "$STATE/register-paths.log")"
expect_no_file "success removes the first host staging file" \
  "$STATE/files/host.${first_host##*/}"
expect_no_file "success removes the first container staging file" \
  "$STATE/files/container.${first_container##*/}"

# A settled remote failure cleans up too.
rc="$(run_deploy register-failure fail)"
expect_eq "ordinary register failure is returned" 1 "$rc"
failure_allocation="$(sed -n '3p' "$STATE/allocations.log")"
failure_host="${failure_allocation%%|*}"
failure_container="${failure_allocation#*|}"
expect_no_file "ordinary failure removes the host staging file" \
  "$STATE/files/host.${failure_host##*/}"
expect_no_file "ordinary failure removes the container staging file" \
  "$STATE/files/container.${failure_container##*/}"

# Cleanup itself must never replace the failure status that caused the exit.
TEST_CLEANUP_EXIT=88
export TEST_CLEANUP_EXIT
rc="$(run_deploy scp-failure success 37)"
unset TEST_CLEANUP_EXIT
expect_eq "cleanup preserves the originating exit status" 37 "$rc"

# A 124 means the detached register may still open the manifest. Preserve both
# copies and the diagnostic status so an operator does not race that process.
rc="$(run_deploy register-timeout timeout)"
expect_eq "detached register timeout remains exit 124" 124 "$rc"
timeout_allocation="$(sed -n '5p' "$STATE/allocations.log")"
timeout_host="${timeout_allocation%%|*}"
timeout_container="${timeout_allocation#*|}"
expect_file "timeout retains the host staging file" \
  "$STATE/files/host.${timeout_host##*/}"
expect_file "timeout retains the container staging file" \
  "$STATE/files/container.${timeout_container##*/}"
expect_contains "timeout explains why staging was retained" \
  'manifest staging files were intentionally retained' "$TEST_ROOT/register-timeout.out"

# A target shell may print a banner or other text before running bash -s. The
# marked allocation record remains machine-readable, and normal EXIT cleanup
# must still remove both files.
rc="$(run_deploy chatty-stage success "" 'remote shell banner')"
expect_eq "unrelated remote stdout does not reject staging paths" 0 "$rc"
chatty_allocation="$(tail -n 1 "$STATE/allocations.log")"
chatty_host="${chatty_allocation%%|*}"
chatty_container="${chatty_allocation#*|}"
expect_eq "chatty allocation reaches registration" "$chatty_container" \
  "$(tail -n 1 "$STATE/register-paths.log")"
expect_no_file "chatty deploy removes the host staging file" \
  "$STATE/files/host.${chatty_host##*/}"
expect_no_file "chatty deploy removes the container staging file" \
  "$STATE/files/container.${chatty_container##*/}"

# Secret resolver commands are embedded only in the remote shell input. No
# diagnostic, path log or captured command may reveal their resolved values.
if grep -R -F -e test-recording-secret -e test-signaling-secret \
  "$STATE" "$TEST_ROOT"/*.out >/dev/null 2>&1; then
  fail "deploy output or test state exposed a resolved secret"
else
  ok "deploy output and state do not expose resolved secrets"
fi

echo
if (( FAILURES )); then
  echo "$FAILURES failure(s)"
  exit 1
fi
echo "all checks passed"
