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

# Each gate is the only required check for its workflow, so a job missing
# from its `needs` is enforced by nothing. Check every job is listed, apart
# from the named exceptions.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# jobs_of WORKFLOW: the top-level job ids.
jobs_of() {
  awk '/^jobs:/ { in_jobs = 1; next }
       in_jobs && /^  [A-Za-z0-9_-]+:[[:space:]]*$/ { sub(/^  /, ""); sub(/:.*/, ""); print }' "$1"
}

# needs_of WORKFLOW JOB: the job's `needs`, flow (`[a, b]`) or block list.
needs_of() {
  awk -v job="$2" '
    $0 ~ "^  " job ":[[:space:]]*$" { in_job = 1; next }
    in_job && /^  [A-Za-z0-9_-]+:/ { exit }
    in_job && /^    needs:/ {
      line = $0; sub(/^    needs:[[:space:]]*/, "", line)
      if (line ~ /^\[/) { gsub(/[][ ]/, "", line); n = split(line, a, ","); for (i = 1; i <= n; i++) print a[i]; exit }
      in_needs = 1; next
    }
    in_needs && /^[[:space:]]*#/ { next }
    in_needs && /^      - / { sub(/^      - /, ""); sub(/[[:space:]].*/, ""); print; next }
    in_needs { exit }' "$1"
}

# check_gate WORKFLOW GATE EXCEPTION...
check_gate() {
  local workflow="$ROOT/.github/workflows/$1" gate="$2" missing
  shift 2
  local -a jobs
  mapfile -t jobs < <(jobs_of "$workflow")
  (( ${#jobs[@]} > 0 )) || { echo "FAIL: no jobs parsed from $workflow" >&2; exit 1; }
  missing="$(comm -23 \
    <(printf '%s\n' "${jobs[@]}" | grep -vxF -e "$gate" "${@/#/-e}" | sort) \
    <(needs_of "$workflow" "$gate" | sort))"
  if [[ -n "$missing" ]]; then
    echo "FAIL: $gate in $(basename "$workflow") does not need: ${missing//$'\n'/ }" >&2
    echo "      Add them to its needs, or to the exceptions in $0." >&2
    exit 1
  fi
  echo "gate $gate needs every job in $(basename "$workflow")"
}

check_gate ci.yml ci-passed
check_gate publish-exapp-image.yml faithful-installed-exapp-talk-cpu \
  e2e-summary-template-cuda d403-manifest-sensitivity-control release-evidence
