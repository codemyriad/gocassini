#!/usr/bin/env bash
# Hermetic negative/positive fixtures for installed job/catalog/artifact truth.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PY="$SCRIPT_DIR/installed-validation.py"
ARTIFACT="$SCRIPT_DIR/validate-installed-artifact.sh"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
expect_pass() { local label="$1"; shift; "$@" >/dev/null || fail "$label should pass"; }
expect_fail() { local label="$1"; shift; if "$@" >/dev/null 2>&1; then fail "$label should fail"; fi; }
expect_rc() {
  local expected="$1" label="$2"
  shift 2
  set +e
  "$@" >/dev/null 2>&1
  local actual=$?
  set -e
  [[ "$actual" -eq "$expected" ]] || fail "$label returned $actual, expected $expected"
}

cat >"$TMP_DIR/jobs-before.json" <<'JSON'
[{"id":"old","state":"succeeded"}]
JSON
cat >"$TMP_DIR/jobs-none.json" <<'JSON'
[{"id":"old","state":"succeeded"}]
JSON
cat >"$TMP_DIR/jobs-success.json" <<'JSON'
[{"id":"old","state":"succeeded"},{"id":"new","state":"succeeded","stage":"publish"}]
JSON
cat >"$TMP_DIR/jobs-failed.json" <<'JSON'
[{"id":"old","state":"succeeded"},{"id":"new","state":"failed","stage":"build","error":"boom"}]
JSON
cat >"$TMP_DIR/jobs-ambiguous.json" <<'JSON'
[{"id":"old","state":"succeeded"},{"id":"new-a","state":"succeeded"},{"id":"new-b","state":"running"}]
JSON
cat >"$TMP_DIR/jobs-build-deferred.json" <<'JSON'
[{"id":"old","state":"succeeded"},{"id":"new","state":"queued","stage":"build","record_exit_code":0,"record_finished_at":"2026-08-28T10:00:00Z","artifact_run_path":"/jobs/new/runs/attempt-001","artifact_meeting_path":null,"build_queued_at":"2026-08-28T10:00:00Z","build_retry_not_before":"2099-08-28T10:00:15Z","build_deferral_count":1,"build_started_at":null,"build_finished_at":null,"completed_at":null,"error":"resource governor: GPU memory unavailable: free VRAM is below floor"}]
JSON
cat >"$TMP_DIR/jobs-build-pending.json" <<'JSON'
[{"id":"old","state":"succeeded"},{"id":"new","state":"queued","stage":"build","record_exit_code":0,"record_finished_at":"2026-08-28T10:00:00Z","artifact_run_path":"/jobs/new/runs/attempt-001","artifact_meeting_path":null,"build_queued_at":"2026-08-28T10:00:00Z","build_retry_not_before":null,"build_deferral_count":0,"build_started_at":null,"build_finished_at":null,"completed_at":null,"error":null}]
JSON
cat >"$TMP_DIR/jobs-build-stale.json" <<'JSON'
[{"id":"old","state":"succeeded"},{"id":"new","state":"queued","stage":"build","record_exit_code":0,"record_finished_at":"2026-08-28T10:00:00Z","artifact_run_path":"/jobs/new/runs/attempt-001","artifact_meeting_path":null,"build_queued_at":"2026-08-28T10:00:00Z","build_retry_not_before":"2000-01-01T00:00:00Z","build_deferral_count":2,"build_started_at":null,"build_finished_at":null,"completed_at":null,"error":"resource governor: host memory unavailable: below floor"}]
JSON
cat >"$TMP_DIR/jobs-build-invalid-retry.json" <<'JSON'
[{"id":"old","state":"succeeded"},{"id":"new","state":"queued","stage":"build","record_exit_code":0,"record_finished_at":"2026-08-28T10:00:00Z","artifact_run_path":"/jobs/new/runs/attempt-001","artifact_meeting_path":null,"build_queued_at":"2026-08-28T10:00:00Z","build_retry_not_before":"eventually","build_deferral_count":1,"build_started_at":null,"build_finished_at":null,"completed_at":null,"error":"resource governor: GPU memory unavailable: below floor"}]
JSON
cat >"$TMP_DIR/jobs-build-blocked.json" <<'JSON'
[{"id":"old","state":"succeeded"},{"id":"new","state":"blocked","stage":"build","record_exit_code":0,"record_finished_at":"2026-08-28T10:00:00Z","artifact_run_path":"/jobs/new/runs/attempt-001","artifact_meeting_path":null,"build_queued_at":"2026-08-28T10:00:00Z","build_retry_not_before":null,"build_deferral_count":0,"build_started_at":null,"build_finished_at":null,"completed_at":null,"error":"resource governor: CUDA runtime unavailable: the installed portable image does not bundle the CUDA inference runtime; install the matching -cuda image on the GPU deploy daemon because operator speech recognition is GPU-only"}]
JSON
cat >"$TMP_DIR/jobs-build-blocked-retry.json" <<'JSON'
[{"id":"old","state":"succeeded"},{"id":"new","state":"blocked","stage":"build","record_exit_code":0,"record_finished_at":"2026-08-28T10:00:00Z","artifact_run_path":"/jobs/new/runs/attempt-001","artifact_meeting_path":null,"build_queued_at":"2026-08-28T10:00:00Z","build_retry_not_before":"2099-08-28T10:00:15Z","build_deferral_count":0,"build_started_at":null,"build_finished_at":null,"completed_at":null,"error":"resource governor: CUDA runtime unavailable: the installed portable image does not bundle the CUDA inference runtime; install the matching -cuda image on the GPU deploy daemon because operator speech recognition is GPU-only"}]
JSON
cat >"$TMP_DIR/jobs-build-blocked-vague.json" <<'JSON'
[{"id":"old","state":"succeeded"},{"id":"new","state":"blocked","stage":"build","record_exit_code":0,"record_finished_at":"2026-08-28T10:00:00Z","artifact_run_path":"/jobs/new/runs/attempt-001","artifact_meeting_path":null,"build_queued_at":"2026-08-28T10:00:00Z","build_retry_not_before":null,"build_deferral_count":0,"build_started_at":null,"build_finished_at":null,"completed_at":null,"error":"resource governor: CUDA runtime unavailable"}]
JSON
cat >"$TMP_DIR/jobs-build-blocked-after-retry.json" <<'JSON'
[{"id":"old","state":"succeeded"},{"id":"new","state":"blocked","stage":"build","record_exit_code":0,"record_finished_at":"2026-08-28T10:00:00Z","artifact_run_path":"/jobs/new/runs/attempt-001","artifact_meeting_path":null,"build_queued_at":"2026-08-28T10:00:00Z","build_retry_not_before":null,"build_deferral_count":1,"build_started_at":null,"build_finished_at":null,"completed_at":null,"error":"resource governor: CUDA runtime unavailable: the installed portable image does not bundle the CUDA inference runtime; install the matching -cuda image on the GPU deploy daemon because operator speech recognition is GPU-only"}]
JSON
expect_rc 10 "no new job" "$PY" select-job --before "$TMP_DIR/jobs-before.json" --current "$TMP_DIR/jobs-none.json"
expect_pass "one succeeded new job" "$PY" select-job --before "$TMP_DIR/jobs-before.json" --current "$TMP_DIR/jobs-success.json"
expect_rc 3 "failed new job" "$PY" select-job --before "$TMP_DIR/jobs-before.json" --current "$TMP_DIR/jobs-failed.json"
expect_fail "ambiguous new jobs" "$PY" select-job --before "$TMP_DIR/jobs-before.json" --current "$TMP_DIR/jobs-ambiguous.json"
expect_pass "transient VRAM pressure is durably deferred" "$PY" select-job --expect build-deferred --before "$TMP_DIR/jobs-before.json" --current "$TMP_DIR/jobs-build-deferred.json"
expect_rc 10 "build without a resource backoff is still pending" "$PY" select-job --expect build-deferred --before "$TMP_DIR/jobs-before.json" --current "$TMP_DIR/jobs-build-pending.json"
expect_rc 10 "elapsed resource backoff is still pending" "$PY" select-job --expect build-deferred --before "$TMP_DIR/jobs-before.json" --current "$TMP_DIR/jobs-build-stale.json"
expect_fail "malformed resource backoff is invalid" "$PY" select-job --expect build-deferred --before "$TMP_DIR/jobs-before.json" --current "$TMP_DIR/jobs-build-invalid-retry.json"
expect_fail "CPU fallback success violates GPU-only mode" "$PY" select-job --expect build-deferred --before "$TMP_DIR/jobs-before.json" --current "$TMP_DIR/jobs-success.json"
expect_pass "portable image build blocks immediately and actionably" "$PY" select-job --expect build-blocked --before "$TMP_DIR/jobs-before.json" --current "$TMP_DIR/jobs-build-blocked.json"
expect_rc 10 "portable build still in flight is pending" "$PY" select-job --expect build-blocked --before "$TMP_DIR/jobs-before.json" --current "$TMP_DIR/jobs-build-pending.json"
expect_fail "permanent block cannot retain a retry timestamp" "$PY" select-job --expect build-blocked --before "$TMP_DIR/jobs-before.json" --current "$TMP_DIR/jobs-build-blocked-retry.json"
expect_fail "permanent block must tell the admin how to recover" "$PY" select-job --expect build-blocked --before "$TMP_DIR/jobs-before.json" --current "$TMP_DIR/jobs-build-blocked-vague.json"
expect_fail "portable runtime must block without transient deferrals" "$PY" select-job --expect build-blocked --before "$TMP_DIR/jobs-before.json" --current "$TMP_DIR/jobs-build-blocked-after-retry.json"
expect_fail "CPU fallback success violates blocked GPU-only mode" "$PY" select-job --expect build-blocked --before "$TMP_DIR/jobs-before.json" --current "$TMP_DIR/jobs-success.json"

cat >"$TMP_DIR/catalog-missing.json" <<'JSON'
{"meetings":[{"id":"old","audioPath":"./old.opus","segmentCount":1}]}
JSON
cat >"$TMP_DIR/catalog-zero.json" <<'JSON'
{"meetings":[{"id":"new","audioPath":"./zero.opus","segmentCount":0}]}
JSON
cat >"$TMP_DIR/catalog-malformed.json" <<'JSON'
{"meetings":[{"id":"new","audioPath":"./valid.opus","segmentCount":"1"}]}
JSON
cat >"$TMP_DIR/catalog-valid.json" <<'JSON'
{"meetings":[{"id":"old","audioPath":"./old.opus","segmentCount":1},{"id":"new","audioPath":"./valid.opus","segmentCount":2}]}
JSON
cat >"$TMP_DIR/catalog-zero-words.json" <<'JSON'
{"meetings":[{"id":"new","audioPath":"./zero-words.opus","segmentCount":2}]}
JSON
cat >"$TMP_DIR/catalog-corrupt.json" <<'JSON'
{"meetings":[{"id":"new","audioPath":"./corrupt.opus","segmentCount":2}]}
JSON
cat >"$TMP_DIR/catalog-directory-valid.json" <<'JSON'
{"meetings":[{"id":"new","artifactPath":"./valid-dir"}]}
JSON
cat >"$TMP_DIR/catalog-directory-empty.json" <<'JSON'
{"meetings":[{"id":"new","artifactPath":"./empty-dir"}]}
JSON
expect_rc 10 "missing catalog entry" "$PY" catalog-entry --catalog "$TMP_DIR/catalog-missing.json" --job-id new --proxy-url https://nextcloud.test/proxy
expect_fail "zero segments" "$PY" catalog-entry --catalog "$TMP_DIR/catalog-zero.json" --job-id new --proxy-url https://nextcloud.test/proxy
expect_fail "malformed segments" "$PY" catalog-entry --catalog "$TMP_DIR/catalog-malformed.json" --job-id new --proxy-url https://nextcloud.test/proxy
expect_pass "valid portable catalog entry" "$PY" catalog-entry --catalog "$TMP_DIR/catalog-valid.json" --job-id new --proxy-url https://nextcloud.test/proxy
expect_pass "archive IDs preserved" "$PY" catalog-contains --catalog "$TMP_DIR/catalog-valid.json" --id old --id new
expect_fail "archive ID removed" "$PY" catalog-contains --catalog "$TMP_DIR/catalog-missing.json" --id old --id new

printf 'VALID\n' >"$TMP_DIR/valid.opus"
printf 'ZERO\n' >"$TMP_DIR/zero-words.opus"
printf 'CORRUPT\n' >"$TMP_DIR/corrupt.opus"
cat >"$TMP_DIR/valid-display.json" <<'JSON'
{"blocks":[{"text":"hello viewer","words":[{"text":"hello"}]}]}
JSON
cat >"$TMP_DIR/empty-display.json" <<'JSON'
{"blocks":[]}
JSON

cat >"$TMP_DIR/curl-stub" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
source_dir="${FIXTURE_DIR:?}"
auth="" url="" output=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -f|-s|-S|-fsS) shift ;;
    -u) auth="$2"; shift 2 ;;
    -o) output="$2"; shift 2 ;;
    http://*|https://*) url="$1"; shift ;;
    *) echo "curl-stub: unexpected argument: $1" >&2; exit 2 ;;
  esac
done
[[ "$auth" == "admin:admin" ]] || { echo "curl-stub: authenticated proxy request required" >&2; exit 2; }
case "$url" in
  */valid.opus) cp "$source_dir/valid.opus" "$output" ;;
  */zero-words.opus) cp "$source_dir/zero-words.opus" "$output" ;;
  */corrupt.opus) cp "$source_dir/corrupt.opus" "$output" ;;
  */valid-dir/transcript.display.v1.json) cp "$source_dir/valid-display.json" "$output" ;;
  */empty-dir/transcript.display.v1.json) cp "$source_dir/empty-display.json" "$output" ;;
  *) echo "curl-stub: unknown URL: $url" >&2; exit 22 ;;
esac
SH
cat >"$TMP_DIR/cassini-stub" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
[[ "$1" == inspect && "$2" == --transcript ]] || exit 2
case "$(cat "$3")" in
  VALID*) printf '%s\n' '{"version":"transcript.words.v1","wordCount":2,"segments":[{"words":[{"text":"hello"},{"text":"world"}]}]}' ;;
  ZERO*) printf '%s\n' '{"version":"transcript.words.v1","wordCount":0,"segments":[{"words":[]}]}' ;;
  *) echo "corrupt portable artifact" >&2; exit 1 ;;
esac
SH
chmod +x "$TMP_DIR/curl-stub" "$TMP_DIR/cassini-stub"

artifact_check() {
  local catalog="$1" label="$2"
  FIXTURE_DIR="$TMP_DIR" \
  CASSINI_VALIDATION_CURL_BIN="$TMP_DIR/curl-stub" \
  CASSINI_VALIDATION_CASSINI_BIN="$TMP_DIR/cassini-stub" \
    "$ARTIFACT" --catalog "$catalog" --job-id new \
      --proxy-url https://nextcloud.test/proxy --auth admin:admin \
      --output-dir "$TMP_DIR/output-$label" --label "$label"
}

expect_pass "downloaded portable with decoded words" artifact_check "$TMP_DIR/catalog-valid.json" valid
expect_fail "portable with zero decoded words" artifact_check "$TMP_DIR/catalog-zero-words.json" zero
expect_fail "corrupt portable artifact" artifact_check "$TMP_DIR/catalog-corrupt.json" corrupt
expect_pass "non-empty directory transcript" artifact_check "$TMP_DIR/catalog-directory-valid.json" directory
expect_fail "empty directory transcript" artifact_check "$TMP_DIR/catalog-directory-empty.json" empty-directory

echo "PASS: installed validation rejects stale, ambiguous, failed, empty, and corrupt results"
