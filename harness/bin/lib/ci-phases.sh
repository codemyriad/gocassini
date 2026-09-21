#!/usr/bin/env bash
# Observational timings for sequential phases. The caller owns EXIT cleanup and
# must close a failed phase before collecting diagnostics or tearing down.
# This library never wraps commands: their errexit behavior remains unchanged.
CI_PHASE_NAME=""
CI_PHASE_CLOCK=""
CI_PHASE_STARTED=""
CI_PHASE_REPORTER="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)/scripts/ci_phase_timings.py"

ci_phase_begin() {
  CI_PHASE_NAME="$1"
  if ! CI_PHASE_CLOCK="$(python3 -c 'import time; print(time.monotonic_ns())')"; then
    CI_PHASE_NAME=""
    echo '::warning::Could not start phase timing' >&2
    return 0
  fi
  CI_PHASE_STARTED="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  if [[ "${GITHUB_ACTIONS:-}" == true ]]; then
    printf '::group::%s\n' "$CI_PHASE_NAME"
  else
    printf '[phase] %s\n' "$CI_PHASE_NAME"
  fi
}

ci_phase_end() {
  [[ -n "$CI_PHASE_NAME" ]] || return 0
  python3 "$CI_PHASE_REPORTER" record "$LOG_DIR/phase-timings.jsonl" \
    --phase "$CI_PHASE_NAME" --clock "$CI_PHASE_CLOCK" \
    --started "$CI_PHASE_STARTED" --exit-code "${1:-0}" \
    || echo '::warning::Could not record phase timing' >&2
  if [[ "${GITHUB_ACTIONS:-}" == true ]]; then printf '::endgroup::\n'; fi
  CI_PHASE_NAME=""
}
