#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

NEXTCLOUD_HOST="${CASSINI_HARNESS_HOST:-127.0.0.1}"
DURATION=60
JOB_TIMEOUT=1200
POLL_INTERVAL=5
ADMIN_USER="${ADMIN_USER:-admin}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin}"

log() {
  printf '\n\033[1;34m==>\033[0m \033[1m%s\033[0m\n' "$*" >&2
}

success() {
  printf '\033[1;32m%s\033[0m\n' "$*" >&2
}

fail() {
  printf '\033[1;31m[ERROR] %s\033[0m\n' "$*" >&2
  exit 1
}

usage() {
  cat <<EOF
Usage: $0 [--nextcloud-host <host-or-url>] [--duration <seconds>] [--job-timeout <seconds>]

Validates the installed AppAPI/HaRP Cassini ExApp Talk path by running two
private admin + Erlich Bachman one-to-one recording jobs and asserting both
remain in the published viewer catalog.

Options:
  --nextcloud-host  Bare host/IP or full Nextcloud base URL. Default: CASSINI_HARNESS_HOST, then 127.0.0.1
  --duration        Playback duration for each private call in seconds. Default: 60
  --job-timeout     Seconds to wait for each job to publish. Default: 1200
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --nextcloud-host)
      [[ $# -ge 2 ]] || fail "--nextcloud-host requires a value"
      NEXTCLOUD_HOST="$2"
      shift 2
      ;;
    --duration)
      [[ $# -ge 2 ]] || fail "--duration requires a value"
      DURATION="$2"
      shift 2
      ;;
    --job-timeout)
      [[ $# -ge 2 ]] || fail "--job-timeout requires a value"
      JOB_TIMEOUT="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
done

[[ "$DURATION" =~ ^[0-9]+$ ]] || fail "--duration must be an integer"
[[ "$JOB_TIMEOUT" =~ ^[0-9]+$ ]] || fail "--job-timeout must be an integer"
command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v python3 >/dev/null 2>&1 || fail "python3 is required"

normalize_base_url() {
  local input="$1"
  if [[ "$input" =~ ^https?:// ]]; then
    printf '%s\n' "${input%/}"
  else
    printf 'http://%s:28080\n' "${input%/}"
  fi
}

BASE_URL="$(normalize_base_url "$NEXTCLOUD_HOST")"
PROXY_URL="$BASE_URL/index.php/apps/app_api/proxy/gocassini"
CATALOG_URL="$PROXY_URL/published/catalog.json"
AUTH=(-u "$ADMIN_USER:$ADMIN_PASSWORD")
COMPOSE=(docker compose -p cassini-exapp-test -f "$REPO_ROOT/harness/compose.yml")
WORK_DIR="$(mktemp -d)"
WAIT_FOR_JOB_ID=""
RUN_PRIVATE_JOB_ID=""
cleanup() {
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

curl_body_or_status() {
  local url="$1"
  local out body code
  out="$(curl -sS "${AUTH[@]}" -w '\n%{http_code}' "$url")" || return 1
  body="${out%$'\n'*}"
  code="${out##*$'\n'}"
  printf '%s\n%s\n' "$code" "$body"
}

fetch_json() {
  local url="$1"
  local dest="$2"
  local allow_missing="${3:-false}"
  local response code body
  response="$(curl_body_or_status "$url")" || return 1
  code="$(printf '%s\n' "$response" | sed -n '1p')"
  body="$(printf '%s\n' "$response" | sed '1d')"
  case "$code" in
    200)
      printf '%s\n' "$body" >"$dest"
      ;;
    404)
      if [[ "$allow_missing" == "true" ]]; then
        printf '{"meetings":[]}\n' >"$dest"
      else
        return 2
      fi
      ;;
    *)
      printf '%s\n' "$body" >&2
      return 3
      ;;
  esac
}

catalog_ids() {
  local catalog_path="$1"
  python3 - "$catalog_path" <<'PY'
import json, sys
with open(sys.argv[1], encoding='utf-8') as f:
    data = json.load(f)
for meeting in data.get('meetings') or []:
    mid = str(meeting.get('id') or '').strip()
    if mid:
        print(mid)
PY
}

jobs_ids() {
  local jobs_path="$1"
  python3 - "$jobs_path" <<'PY'
import json, sys
with open(sys.argv[1], encoding='utf-8') as f:
    data = json.load(f)
for job in data if isinstance(data, list) else []:
    jid = str(job.get('id') or '').strip()
    if jid:
        print(jid)
PY
}

new_job_status() {
  local before_jobs="$1"
  local current_jobs="$2"
  python3 - "$before_jobs" "$current_jobs" <<'PY'
import json, sys
before_path, current_path = sys.argv[1:3]
with open(before_path, encoding='utf-8') as f:
    before = json.load(f)
with open(current_path, encoding='utf-8') as f:
    current = json.load(f)
before_ids = {str(job.get('id') or '') for job in before if isinstance(job, dict)}
new_jobs = [job for job in current if isinstance(job, dict) and str(job.get('id') or '') not in before_ids]
if not new_jobs:
    print('pending no-new-job')
    sys.exit(2)
new_jobs.sort(key=lambda job: str(job.get('created_at') or job.get('updated_at') or ''))
job = new_jobs[-1]
job_id = str(job.get('id') or '')
state = str(job.get('state') or '')
stage = str(job.get('stage') or '')
err = job.get('error') or ''
print(f'{job_id} {stage}/{state} {err}'.strip())
if state == 'succeeded':
    print(job_id)
    sys.exit(0)
if state in {'failed', 'interrupted'}:
    sys.exit(3)
sys.exit(2)
PY
}

catalog_entry_probe() {
  local catalog_path="$1"
  local job_id="$2"
  local proxy_url="$3"
  python3 - "$catalog_path" "$job_id" "$proxy_url" <<'PY'
import json, sys
from urllib.parse import urljoin
catalog_path, job_id, proxy_url = sys.argv[1:4]
with open(catalog_path, encoding='utf-8') as f:
    data = json.load(f)
base = proxy_url.rstrip('/') + '/published/'
for meeting in data.get('meetings') or []:
    if str(meeting.get('id') or '').strip() != job_id:
        continue
    artifact = str(meeting.get('artifactPath') or '').strip()
    if artifact:
        if not artifact.endswith('/'):
            artifact += '/'
        print('transcript_url\t' + urljoin(base, artifact + 'transcript.display.v1.json'))
        sys.exit(0)
    audio_path = str(meeting.get('audioPath') or '').strip()
    if audio_path:
        segment_count = int(meeting.get('segmentCount') or 0)
        print(f'portable\tsegmentCount={segment_count}\taudioPath={audio_path}')
        sys.exit(0)
    print(f'catalog entry {job_id} has no artifactPath or audioPath', file=sys.stderr)
    sys.exit(3)
print(f'missing catalog entry for {job_id}', file=sys.stderr)
sys.exit(2)
PY
}

validate_transcript_json() {
  local transcript_path="$1"
  python3 - "$transcript_path" <<'PY'
import json, sys
with open(sys.argv[1], encoding='utf-8') as f:
    data = json.load(f)
blocks = data.get('blocks')
if not isinstance(blocks, list) or not blocks:
    print('display transcript has no blocks', file=sys.stderr)
    sys.exit(1)
texts = []
word_count = 0
for block in blocks:
    if not isinstance(block, dict):
        continue
    text = str(block.get('text') or '').strip()
    if text:
        texts.append(text)
    for key in ('words', 'sourceWords'):
        words = block.get(key)
        if isinstance(words, list):
            word_count += len(words)
if not texts and word_count == 0:
    print('display transcript has no text or words', file=sys.stderr)
    sys.exit(1)
print(f'blocks={len(blocks)} text_blocks={len(texts)} words={word_count}')
PY
}

wait_for_new_job_success() {
  local label="$1"
  local before_jobs="$2"
  local deadline=$((SECONDS + JOB_TIMEOUT))
  local jobs_path="$WORK_DIR/jobs-${label}.json"
  local status_file="$WORK_DIR/status-${label}.txt"
  while (( SECONDS < deadline )); do
    if fetch_json "$PROXY_URL/operator/jobs" "$jobs_path" false; then
      set +e
      new_job_status "$before_jobs" "$jobs_path" >"$status_file"
      rc=$?
      set -e
      status_line="$(sed -n '1p' "$status_file")"
      case "$rc" in
        0)
          job_id="$(tail -n 1 "$status_file")"
          WAIT_FOR_JOB_ID="$job_id"
          success "✓ $label job succeeded: $job_id"
          return 0
          ;;
        2)
          printf '[validate] waiting for %s: %s\n' "$label" "$status_line" >&2
          ;;
        3)
          cat "$status_file" >&2 || true
          fail "$label job failed"
          ;;
        *)
          cat "$status_file" >&2 || true
          fail "$label job status parse failed"
          ;;
      esac
    fi
    sleep "$POLL_INTERVAL"
  done
  fail "timed out waiting for $label job to succeed"
}

wait_for_catalog_transcript() {
  local label="$1"
  local job_id="$2"
  local deadline=$((SECONDS + JOB_TIMEOUT))
  local catalog_path="$WORK_DIR/catalog-${label}.json"
  local transcript_path="$WORK_DIR/transcript-${label}.json"
  local probe="" probe_kind="" probe_value=""
  while (( SECONDS < deadline )); do
    if fetch_json "$CATALOG_URL" "$catalog_path" true; then
      set +e
      probe="$(catalog_entry_probe "$catalog_path" "$job_id" "$PROXY_URL" 2>/dev/null)"
      rc=$?
      set -e
      if [[ "$rc" -eq 0 && -n "$probe" ]]; then
        IFS=$'\t' read -r probe_kind probe_value _ <<<"$probe"
        if [[ "$probe_kind" == "portable" ]]; then
          success "✓ $label portable catalog artifact visible for $job_id ($probe_value)"
          return 0
        fi
        if [[ "$probe_kind" == "transcript_url" ]]; then
          if fetch_json "$probe_value" "$transcript_path" false; then
            transcript_summary="$(validate_transcript_json "$transcript_path")" || fail "$label transcript is empty"
            success "✓ $label catalog/transcript visible for $job_id ($transcript_summary)"
            return 0
          fi
        fi
      fi
    fi
    printf '[validate] waiting for %s catalog/transcript: %s\n' "$label" "$job_id" >&2
    sleep "$POLL_INTERVAL"
  done
  fail "timed out waiting for $label catalog/transcript for $job_id"
}

base_url_host() {
  python3 - "$BASE_URL" <<'PY'
import sys
from urllib.parse import urlparse
parsed = urlparse(sys.argv[1])
print(parsed.hostname or '')
PY
}

ensure_nextcloud_host_trusted() {
  local host="$1"
  if [[ -z "$host" || "$host" == "localhost" || "$host" == 127.* ]]; then
    return 0
  fi
  if ! docker compose -p cassini-exapp-test -f "$REPO_ROOT/harness/compose.yml" ps nextcloud >/dev/null 2>&1; then
    return 0
  fi
  log "Ensuring Nextcloud trusts validation host $host"
  "${COMPOSE[@]}" exec -T -u www-data nextcloud php occ config:system:set trusted_domains 20 --value="$host" >/dev/null
}

assert_catalog_contains_all() {
  local catalog_path="$1"
  shift
  python3 - "$catalog_path" "$@" <<'PY'
import json, sys
catalog_path, expected = sys.argv[1], sys.argv[2:]
with open(catalog_path, encoding='utf-8') as f:
    data = json.load(f)
ids = {str(meeting.get('id') or '').strip() for meeting in data.get('meetings') or []}
missing = [item for item in expected if item and item not in ids]
if missing:
    print('missing catalog ids: ' + ', '.join(missing), file=sys.stderr)
    sys.exit(1)
print(f'catalog_entries={len(ids)}')
PY
}

run_private_job() {
  local label="$1"
  local before_jobs="$WORK_DIR/jobs-before-${label}.json"
  log "Capturing job baseline for $label"
  fetch_json "$PROXY_URL/operator/jobs" "$before_jobs" false || fail "cannot fetch operator jobs before $label"

  log "Running private admin + Erlich playback for $label"
  (cd "$REPO_ROOT" && ./bin/cassini dev play-private --conversation admin --nextcloud-host "$NEXTCLOUD_HOST" --duration "$DURATION" >&2)

  log "Waiting for installed ExApp job to publish for $label"
  wait_for_new_job_success "$label" "$before_jobs"
  job_id="$WAIT_FOR_JOB_ID"
  wait_for_catalog_transcript "$label" "$job_id"
  RUN_PRIVATE_JOB_ID="$job_id"
}

log "Validating installed Cassini ExApp at $PROXY_URL"
ensure_nextcloud_host_trusted "$(base_url_host)"
curl -fsS "$PROXY_URL/api/v1/welcome" | grep -q '"version":1' || fail "welcome route did not return version=1"
status_json="$(curl -fsS "${AUTH[@]}" "$PROXY_URL/operator/status")"
echo "$status_json" | grep -q '"secret_configured":true' || fail "operator status does not report Talk recording secret configured"
echo "$status_json" | grep -q '"signaling_internal_secret_configured":true' || fail "operator status does not report signaling internal secret configured"
success "✓ installed ExApp status has required Talk config"

before_catalog="$WORK_DIR/catalog-before.json"
fetch_json "$CATALOG_URL" "$before_catalog" true || fail "cannot fetch initial catalog"
mapfile -t previous_catalog_ids < <(catalog_ids "$before_catalog")
printf '[validate] existing catalog ids: %s\n' "${previous_catalog_ids[*]:-(none)}" >&2

log "Preparing private playback scaffold"
(cd "$REPO_ROOT" && ./bin/cassini dev play-private --scaffold-only --nextcloud-host "$NEXTCLOUD_HOST")

run_private_job job1
job1="$RUN_PRIVATE_JOB_ID"
run_private_job job2
job2="$RUN_PRIVATE_JOB_ID"

final_catalog="$WORK_DIR/catalog-final.json"
fetch_json "$CATALOG_URL" "$final_catalog" false || fail "cannot fetch final catalog"
expected_ids=("$job1" "$job2" "${previous_catalog_ids[@]}")
summary="$(assert_catalog_contains_all "$final_catalog" "${expected_ids[@]}")" || fail "archive preservation check failed"
success "✓ archive preservation check passed ($summary)"

cat <<EOF

Installed ExApp private Talk validation passed.
  Nextcloud: $BASE_URL
  Job 1:     $job1
  Job 2:     $job2
  Catalog:   $CATALOG_URL
EOF
