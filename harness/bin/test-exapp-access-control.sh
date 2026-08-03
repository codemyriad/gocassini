#!/usr/bin/env bash
# Fast/offline contract test for harness -> AppAPI access-control wiring.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
STACK_ENV="$SCRIPT_DIR/lib/stack-env.sh"
STACK="$SCRIPT_DIR/lib/stack.sh"
BOOTSTRAP="$SCRIPT_DIR/bootstrap.sh"
MANIFEST="$ROOT/appinfo/info.xml"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

manifest_count="$(python3 - "$MANIFEST" <<'PY'
import sys
from xml.etree import ElementTree as ET
root = ET.parse(sys.argv[1]).getroot()
names = [
    (node.findtext("name") or "").strip()
    for node in root.findall("./external-app/environment-variables/variable")
]
print(names.count("CASSINI_NC_ACCESS_CONTROL"))
PY
)"
[[ "$manifest_count" == "1" ]] \
  || fail "appinfo/info.xml must declare CASSINI_NC_ACCESS_CONTROL exactly once"

resolved_default="$(
  env -u CASSINI_NC_ACCESS_CONTROL bash -c '
    set -euo pipefail
    source "$1"
    harness_stack_env_resolve
    printf "%s" "$CASSINI_NC_ACCESS_CONTROL"
  ' _ "$STACK_ENV"
)"
[[ "$resolved_default" == "true" ]] \
  || fail "harness default is '$resolved_default', want true"

resolved_false="$(
  CASSINI_NC_ACCESS_CONTROL=false bash -c '
    set -euo pipefail
    source "$1"
    harness_stack_env_resolve
    printf "%s" "$CASSINI_NC_ACCESS_CONTROL"
  ' _ "$STACK_ENV"
)"
[[ "$resolved_false" == "false" ]] \
  || fail "harness override is '$resolved_false', want false"

# AppAPI drops undeclared or unpassed values. Guard both sides of that boundary.
grep -qF -- '--env "CASSINI_NC_ACCESS_CONTROL=$CASSINI_NC_ACCESS_CONTROL"' "$STACK" \
  || fail "harness registration does not pass CASSINI_NC_ACCESS_CONTROL to AppAPI"

# The installed-app harness must provide both native prerequisites before the
# ExApp enabled edge tries to create the recordings substrate.
for app in groupfolders group_everyone; do
  grep -qF "app:install $app" "$BOOTSTRAP" \
    || fail "bootstrap does not install required Nextcloud app $app"
  grep -qF "app:enable $app" "$BOOTSTRAP" \
    || fail "bootstrap does not enable required Nextcloud app $app"
done

bash -n "$STACK_ENV" || fail "bash -n failed for stack-env.sh"
bash -n "$STACK" || fail "bash -n failed for stack.sh"
bash -n "$BOOTSTRAP" || fail "bash -n failed for bootstrap.sh"

echo "PASS: access control is passed to AppAPI with Team folders + Everyone Group prerequisites"
