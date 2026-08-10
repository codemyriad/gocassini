#!/usr/bin/env bash
# test-backfill-nc-files.sh — offline regressions for scripts/backfill-nc-files.sh.
#
# The script's job is to reach the right container and translate the command's
# outcome into something an admin can act on. Both halves are worth pinning:
# a wrong container silently migrates nothing, and collapsing the guard's
# refusal into a generic failure would tell an admin their install is broken
# when it is simply already migrated.
#
# `docker` is stubbed through the DOCKER hook, so this needs no daemon and no
# Nextcloud.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET="$SCRIPT_DIR/backfill-nc-files.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

failures=0
fail() { echo "  FAIL: $*" >&2; failures=$((failures + 1)); }

# make_stub <exit-code-for-exec> [running]
# Writes a fake container CLI that records the exec argv it was given.
make_stub() {
  local exec_status="$1" running="${2:-true}"
  # Clear the previous case's record, so "the container was never reached" is
  # distinguishable from "it was reached last time".
  rm -f "$WORK/exec-argv"
  cat >"$WORK/docker" <<EOF
#!/usr/bin/env bash
case "\$1" in
  inspect)
    if [[ "\$2" == "-f" ]]; then echo "$running"; exit 0; fi
    # 'inspect <name>' — existence probe
    [[ "\$2" == "missing-container" ]] && exit 1
    exit 0
    ;;
  exec)
    shift
    printf '%s\n' "\$@" >"$WORK/exec-argv"
    exit $exec_status
    ;;
esac
exit 0
EOF
  chmod +x "$WORK/docker"
}

run_target() {
  DOCKER="$WORK/docker" "$TARGET" "$@" >"$WORK/stdout" 2>"$WORK/stderr"
}

echo "test-backfill-nc-files.sh"

# --- the command is invoked in the container, with the flags passed through ---
make_stub 0
if ! run_target --dry-run --public; then
  fail "a successful migration should exit 0"
fi
argv="$(cat "$WORK/exec-argv")"
for want in nc_app_gocassini cassini-operator backfill-nc-files --dry-run --public; do
  grep -qxF -e "$want" <<<"$argv" || fail "exec argv is missing '$want': $argv"
done
echo "  ok: flags reach the operator command inside the container"

# --- --site-root takes a value, in both spellings ---
make_stub 0
run_target --site-root /srv/other || fail "--site-root should be accepted"
grep -qxF -e "/srv/other" "$WORK/exec-argv" || fail "--site-root value not forwarded"
make_stub 0
run_target --site-root=/srv/eq || fail "--site-root=VALUE should be accepted"
grep -qxF -e "/srv/eq" "$WORK/exec-argv" || fail "--site-root=VALUE not forwarded"
echo "  ok: --site-root is forwarded in both spellings"

# --- the container is selectable, so a non-default app id still works ---
make_stub 0
run_target --container nc_app_custom || fail "--container should be accepted"
grep -qxF -e "nc_app_custom" "$WORK/exec-argv" || fail "--container not honoured"
make_stub 0
CASSINI_CONTAINER=nc_app_env DOCKER="$WORK/docker" "$TARGET" >/dev/null 2>&1 \
  || fail "CASSINI_CONTAINER should be honoured"
grep -qxF -e "nc_app_env" "$WORK/exec-argv" || fail "CASSINI_CONTAINER not honoured"
echo "  ok: the container is selectable by flag and by environment"

# --- a missing container is named, not a raw docker error ---
make_stub 0
if run_target --container missing-container; then
  fail "a missing container should fail"
fi
grep -q "not found" "$WORK/stderr" || fail "missing container not explained: $(cat "$WORK/stderr")"
grep -q "nc_app_" "$WORK/stderr" || fail "missing container error does not say how to find the right name"
echo "  ok: a missing container is explained"

# --- a stopped container is refused before exec, with the reason ---
make_stub 0 false
if run_target; then
  fail "a stopped container should fail"
fi
grep -q "not running" "$WORK/stderr" || fail "stopped container not explained: $(cat "$WORK/stderr")"
echo "  ok: a stopped container is refused"

# --- exit 3 is the guard's refusal: a legitimate answer, relayed as itself ---
make_stub 3
set +e
run_target
status=$?
set -e
[[ "$status" -eq 3 ]] || fail "guard refusal exit = $status, want 3"
grep -q "Nothing was changed" "$WORK/stderr" || fail "refusal not explained: $(cat "$WORK/stderr")"
grep -q "already stores its recordings" "$WORK/stderr" || fail "refusal does not say the install is already migrated"
echo "  ok: a refusal is relayed as 'already migrated', not as a breakage"

# --- a real failure warns that re-running is not the fix ---
make_stub 1
set +e
run_target
status=$?
set -e
[[ "$status" -eq 1 ]] || fail "failure exit = $status, want 1"
grep -q "Re-running is NOT the fix" "$WORK/stderr" || fail "partial-run guidance missing: $(cat "$WORK/stderr")"
echo "  ok: a partial run tells the admin what to do next"

# --- unknown options are rejected rather than silently forwarded ---
make_stub 0
if run_target --wipe-everything; then
  fail "an unknown option should be rejected"
fi
grep -q "unknown option" "$WORK/stderr" || fail "unknown option not reported"
[[ ! -f "$WORK/exec-argv" ]] || fail "an unknown option still reached the container"
echo "  ok: unknown options never reach the container"

if [[ "$failures" -ne 0 ]]; then
  echo "$failures check(s) failed" >&2
  exit 1
fi
echo "all checks passed"
