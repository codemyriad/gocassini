#!/usr/bin/env bash
# Offline test for scripts/ci-require-jobs.sh (D-906).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

pass=0
expect() {
  local want="$1" input="$2" got=0
  ./scripts/ci-require-jobs.sh <<<"$input" >/dev/null 2>&1 || got=1
  if [[ "$want" == pass && "$got" != 0 ]] || [[ "$want" == fail && "$got" == 0 ]]; then
    echo "FAIL: expected $want for: $input" >&2
    exit 1
  fi
  pass=$((pass + 1))
}

expect pass '{"a":{"result":"success","outputs":{}},"b":{"result":"skipped","outputs":{}}}'
expect pass '{"a":{"result":"success"}}'
for bad in failure cancelled timed_out; do
  expect fail "{\"a\":{\"result\":\"success\"},\"b\":{\"result\":\"$bad\"}}"
done
# Fail closed on anything that is not a readable set of results.
expect fail ''
expect fail '{}'
expect fail 'null'
expect fail '[]'
expect fail 'not json'
expect fail '{"a":{"outputs":{}}}'

echo "ci-require-jobs: $pass cases passed"
