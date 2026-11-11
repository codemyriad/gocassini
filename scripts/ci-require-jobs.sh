#!/usr/bin/env bash
# Reads a workflow's `toJSON(needs)` on stdin and fails unless every job's
# result is `success` or `skipped`. Used by the gate jobs branch protection
# requires (D-906), so that the jobs behind a gate can change freely.
#
# Fails closed: input that is not a JSON object of jobs with a result fails.
set -euo pipefail

needs="$(cat)"
if ! jq -e 'type == "object" and length > 0 and all(.[]; .result | type == "string")' \
    >/dev/null 2>&1 <<<"$needs"; then
  echo "::error::cannot read the needed jobs' results" >&2
  exit 1
fi

failed="$(jq -r 'to_entries[] | select(.value.result != "success" and .value.result != "skipped")
  | "\(.key): \(.value.result)"' <<<"$needs")"
jq -r 'to_entries[] | "\(.key): \(.value.result)"' <<<"$needs"
if [[ -n "$failed" ]]; then
  while IFS= read -r line; do
    echo "::error::${line}" >&2
  done <<<"$failed"
  exit 1
fi
