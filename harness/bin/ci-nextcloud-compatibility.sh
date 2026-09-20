#!/usr/bin/env bash
# One disposable, locked Nextcloud installation and inspectable evidence.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
: "${IMAGE_REF:?exact Cassini image required}"
: "${LOG_DIR:?isolated evidence directory required}"
mode="${COMPAT_MODE:-baseline}"
# Never let observations or a browser result from an earlier run qualify this
# attempt if collection fails. Each invocation owns a newly created directory.
mkdir -p "$(dirname "$LOG_DIR")"
mkdir "$LOG_DIR" || { echo 'Choose a new LOG_DIR for each compatibility attempt' >&2; exit 1; }
if [[ -n "${COMPAT_STACK:-}" ]]; then
  python3 "$ROOT/scripts/nextcloud_compatibility.py" prepare --stack "$COMPAT_STACK" --out "$LOG_DIR/lock"
else
  python3 "$ROOT/scripts/nextcloud_compatibility.py" prepare --baseline "${1:?baseline ID required}" --out "$LOG_DIR/lock"
fi
# Generated exclusively from validated inventory values, shell-quoted by Python.
# shellcheck disable=SC1091
source "$LOG_DIR/lock/env.sh"
export CASSINI_COMPAT_FIXTURE=1
export PROJECT_NAME="spreedtest"
started="$(date -u +%Y-%m-%dT%H:%M:%S+00:00)"
finish() {
  local rc=$?
  trap - EXIT INT TERM
  python3 "$ROOT/scripts/compatibility_evidence.py" collect \
    --log "$LOG_DIR" --stack "$LOG_DIR/lock/stack.json" --mode "$mode" \
    --started "$started" --exit-code "$rc" \
    --manifest "${D453_MANIFEST_PATH:-$ROOT/appinfo/info.xml}" \
    --out "$LOG_DIR/compatibility.json" || rc=1
  exit "$rc"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
# The existing orchestrator resets fixed names and volumes. Refuse any retained
# fixture before it can reset it; unrelated Docker workloads remain untouched.
if docker ps -a --format '{{.Names}}' | grep -Eq '^(appapi-harp|nc_app_gocassini|cassini-exapp|spreedtest[-_])' \
  || docker volume ls --format '{{.Name}}' | grep -Eq '^(spreedtest_|nc_app_gocassini_data$)' \
  || docker network ls --format '{{.Name}}' | grep -Fxq 'spreedtest_default'; then
  echo 'Compatibility run needs a dedicated empty Cassini Docker fixture' >&2
  exit 1
fi
docker compose -f "$ROOT/harness/compose.yml" --profile full pull
"$ROOT/harness/bin/ci-e2e-recording-readiness.sh"
