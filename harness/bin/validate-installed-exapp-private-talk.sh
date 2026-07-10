#!/usr/bin/env bash
# Validate recordings against an already AppAPI/HaRP-installed Cassini ExApp.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
VALIDATOR="$SCRIPT_DIR/installed-validation.py"
ARTIFACT_VALIDATOR="$SCRIPT_DIR/validate-installed-artifact.sh"

NEXTCLOUD_HOST="${CASSINI_HARNESS_HOST:-127.0.0.1}"
DURATION=60
JOB_TIMEOUT=1200
POLL_INTERVAL=5
RUN_COUNT=2
CONVERSATION="admin"
PROJECT_NAME="${PROJECT_NAME:-cassini-exapp-test}"
LOG_DIR="${LOG_DIR:-/tmp/cassini-installed-validation-$(date -u +%Y%m%dT%H%M%S)-$$}"
ADMIN_USER="${ADMIN_USER:-admin}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin}"

log() { printf '\n\033[1;34m==>\033[0m \033[1m%s\033[0m\n' "$*" >&2; }
success() { printf '\033[1;32m%s\033[0m\n' "$*" >&2; }
fail() { printf '\033[1;31m[ERROR] %s\033[0m\n' "$*" >&2; exit 1; }

usage() {
  cat <<EOF
Usage: $0 [options]

Runs one or more private Talk recordings against an already installed ExApp,
then strictly validates the new job, viewer catalog entry, downloaded product
artifact, and transcript content. Two runs remain the manual default so archive
preservation is checked; faithful CI uses --run-count 1.

Options:
  --nextcloud-host <host-or-url>  Default: CASSINI_HARNESS_HOST, then 127.0.0.1
  --duration <seconds>            Playback duration per run. Default: 60
  --job-timeout <seconds>         Job/catalog timeout. Default: 1200
  --poll-interval <seconds>       Poll interval. Default: 5
  --run-count <count>             Recording attempts. Default: 2
  --conversation <name>           play-private conversation. Default: admin
  --project-name <name>           Harness Compose project. Default: cassini-exapp-test
  --log-dir <path>                Retained evidence directory
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --nextcloud-host) [[ $# -ge 2 ]] || fail "$1 requires a value"; NEXTCLOUD_HOST="$2"; shift 2 ;;
    --duration) [[ $# -ge 2 ]] || fail "$1 requires a value"; DURATION="$2"; shift 2 ;;
    --job-timeout) [[ $# -ge 2 ]] || fail "$1 requires a value"; JOB_TIMEOUT="$2"; shift 2 ;;
    --poll-interval) [[ $# -ge 2 ]] || fail "$1 requires a value"; POLL_INTERVAL="$2"; shift 2 ;;
    --run-count) [[ $# -ge 2 ]] || fail "$1 requires a value"; RUN_COUNT="$2"; shift 2 ;;
    --conversation) [[ $# -ge 2 ]] || fail "$1 requires a value"; CONVERSATION="$2"; shift 2 ;;
    --project-name) [[ $# -ge 2 ]] || fail "$1 requires a value"; PROJECT_NAME="$2"; shift 2 ;;
    --log-dir) [[ $# -ge 2 ]] || fail "$1 requires a value"; LOG_DIR="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done

for pair in "duration:$DURATION" "job-timeout:$JOB_TIMEOUT" "poll-interval:$POLL_INTERVAL" "run-count:$RUN_COUNT"; do
  name="${pair%%:*}" value="${pair#*:}"
  [[ "$value" =~ ^[0-9]+$ ]] || fail "--$name must be an integer"
done
(( DURATION > 0 && JOB_TIMEOUT > 0 && POLL_INTERVAL > 0 && RUN_COUNT > 0 )) \
  || fail "duration, timeout, poll interval, and run count must be positive"
for tool in curl jq python3; do command -v "$tool" >/dev/null 2>&1 || fail "$tool is required"; done
[[ -x "$VALIDATOR" ]] || fail "installed validator is not executable: $VALIDATOR"
[[ -x "$ARTIFACT_VALIDATOR" ]] || fail "artifact validator is not executable: $ARTIFACT_VALIDATOR"

normalize_base_url() {
  local input="$1"
  if [[ "$input" =~ ^https?:// ]]; then printf '%s\n' "${input%/}"; else printf 'http://%s:28080\n' "${input%/}"; fi
}

BASE_URL="$(normalize_base_url "$NEXTCLOUD_HOST")"
PROXY_URL="$BASE_URL/index.php/apps/app_api/proxy/gocassini"
CATALOG_URL="$PROXY_URL/published/catalog.json"
AUTH=(-u "$ADMIN_USER:$ADMIN_PASSWORD")
AUTH_VALUE="$ADMIN_USER:$ADMIN_PASSWORD"
COMPOSE=(docker compose -p "$PROJECT_NAME" -f "$REPO_ROOT/harness/compose.yml")
mkdir -p "$LOG_DIR"
SUMMARY_NDJSON="$LOG_DIR/runs.ndjson"
: >"$SUMMARY_NDJSON"
RUN_JOB_ID=""
RUN_ARTIFACT_SUMMARY=""
VALIDATION_RESULT="failed"
SUMMARY_WRITTEN=0

write_summary() {
  local rc="$1" image_id="unknown"
  (( SUMMARY_WRITTEN == 0 )) || return 0
  if command -v docker >/dev/null 2>&1; then
    image_id="$(docker inspect nc_app_gocassini --format '{{.Image}}' 2>/dev/null || echo unknown)"
  fi
  jq -s \
    --arg nextcloud "$BASE_URL" \
    --arg catalog "$CATALOG_URL" \
    --arg image_id "$image_id" \
    --arg cleanup "not-owned" \
    --arg result "$VALIDATION_RESULT" \
    --arg last_job_id "$RUN_JOB_ID" \
    --argjson exit_code "$rc" \
    '{result:$result,exit_code:$exit_code,nextcloud:$nextcloud,catalog:$catalog,image_id:$image_id,cleanup:$cleanup,last_job_id:$last_job_id,runs:.}' \
    "$SUMMARY_NDJSON" >"$LOG_DIR/summary.json" || true
  SUMMARY_WRITTEN=1
}

on_exit() {
  local rc=$?
  write_summary "$rc"
  if (( rc != 0 )); then
    printf '[validate] failure evidence retained at %s (summary: %s)\n' "$LOG_DIR" "$LOG_DIR/summary.json" >&2
  fi
}
trap on_exit EXIT

curl_body_or_status() {
  local url="$1" out body code
  out="$(curl -sS "${AUTH[@]}" -w '\n%{http_code}' "$url")" || return 1
  body="${out%$'\n'*}" code="${out##*$'\n'}"
  printf '%s\n%s\n' "$code" "$body"
}

fetch_json() {
  local url="$1" dest="$2" allow_missing="${3:-false}" response code body
  response="$(curl_body_or_status "$url")" || return 1
  code="$(printf '%s\n' "$response" | sed -n '1p')"
  body="$(printf '%s\n' "$response" | sed '1d')"
  case "$code" in
    200) printf '%s\n' "$body" >"$dest" ;;
    404) [[ "$allow_missing" == true ]] || return 2; printf '{"meetings":[]}\n' >"$dest" ;;
    *) printf '%s\n' "$body" >&2; return 3 ;;
  esac
  python3 -m json.tool "$dest" >/dev/null 2>&1 || return 4
}

catalog_ids() {
  python3 - "$1" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as source:
    catalog = json.load(source)
for meeting in catalog.get("meetings") or []:
    value = str(meeting.get("id") or "").strip()
    if value:
        print(value)
PY
}

base_url_host() {
  python3 - "$BASE_URL" <<'PY'
import sys
from urllib.parse import urlparse
print(urlparse(sys.argv[1]).hostname or "")
PY
}

ensure_nextcloud_host_trusted() {
  local host="$1"
  [[ -n "$host" && "$host" != localhost && "$host" != 127.* ]] || return 0
  "${COMPOSE[@]}" ps nextcloud >/dev/null 2>&1 || return 0
  log "Ensuring Nextcloud trusts validation host $host"
  "${COMPOSE[@]}" exec -T -u www-data nextcloud \
    php occ config:system:set trusted_domains 20 --value="$host" >/dev/null
}

wait_for_new_job_success() {
  local label="$1" before_jobs="$2"
  local deadline=$((SECONDS + JOB_TIMEOUT)) jobs="$LOG_DIR/jobs-${label}.json" result rc
  while (( SECONDS < deadline )); do
    if fetch_json "$PROXY_URL/operator/jobs" "$jobs" false; then
      set +e
      result="$($VALIDATOR select-job --before "$before_jobs" --current "$jobs" 2>"$LOG_DIR/jobs-${label}.err")"
      rc=$?
      set -e
      case "$rc" in
        0)
          RUN_JOB_ID="$(jq -r '.job_id' <<<"$result")"
          success "✓ $label job succeeded: $RUN_JOB_ID"
          return 0
          ;;
        10) printf '[validate] waiting for %s job: %s\n' "$label" "$result" >&2 ;;
        3) cat "$LOG_DIR/jobs-${label}.err" >&2 || true; fail "$label job failed: $result" ;;
        *) cat "$LOG_DIR/jobs-${label}.err" >&2 || true; fail "$label job selection was invalid" ;;
      esac
    fi
    sleep "$POLL_INTERVAL"
  done
  fail "timed out waiting for exactly one new $label job"
}

wait_for_catalog_artifact() {
  local label="$1" job_id="$2"
  local deadline=$((SECONDS + JOB_TIMEOUT)) catalog="$LOG_DIR/catalog-${label}.json" entry rc
  while (( SECONDS < deadline )); do
    if fetch_json "$CATALOG_URL" "$catalog" true; then
      set +e
      entry="$($VALIDATOR catalog-entry --catalog "$catalog" --job-id "$job_id" --proxy-url "$PROXY_URL" 2>"$LOG_DIR/catalog-${label}.err")"
      rc=$?
      set -e
      case "$rc" in
        0)
          RUN_ARTIFACT_SUMMARY="$(
            "$ARTIFACT_VALIDATOR" \
              --catalog "$catalog" \
              --job-id "$job_id" \
              --proxy-url "$PROXY_URL" \
              --auth "$AUTH_VALUE" \
              --output-dir "$LOG_DIR" \
              --label "$label"
          )" || fail "$label artifact failed download/decoding validation"
          success "✓ $label viewer artifact validated: $RUN_ARTIFACT_SUMMARY"
          return 0
          ;;
        10) printf '[validate] waiting for %s catalog entry: %s\n' "$label" "$entry" >&2 ;;
        *) cat "$LOG_DIR/catalog-${label}.err" >&2 || true; fail "$label catalog entry is invalid" ;;
      esac
    fi
    sleep "$POLL_INTERVAL"
  done
  fail "timed out waiting for $label catalog artifact for $job_id"
}

run_private_job() {
  local label="$1" started_at before_jobs
  before_jobs="$LOG_DIR/jobs-before-${label}.json"
  fetch_json "$PROXY_URL/operator/jobs" "$before_jobs" false || fail "cannot fetch jobs before $label"
  started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  log "Running private Talk playback for $label (single recording attempt)"
  (cd "$REPO_ROOT" && ./bin/cassini dev play-private \
    --conversation "$CONVERSATION" \
    --nextcloud-host "$NEXTCLOUD_HOST" \
    --duration "$DURATION") \
    > >(tee "$LOG_DIR/playback-${label}.log") \
    2> >(tee "$LOG_DIR/playback-${label}.err" >&2)

  wait_for_new_job_success "$label" "$before_jobs"
  wait_for_catalog_artifact "$label" "$RUN_JOB_ID"
  jq -nc \
    --arg label "$label" \
    --arg started_at "$started_at" \
    --argjson artifact "$RUN_ARTIFACT_SUMMARY" \
    '{label:$label,started_at:$started_at,job_id:$artifact.job_id,artifact:$artifact}' \
    >>"$SUMMARY_NDJSON"
}

log "Validating installed Cassini ExApp at $PROXY_URL"
log "Evidence directory: $LOG_DIR"
ensure_nextcloud_host_trusted "$(base_url_host)"
curl -fsS "$PROXY_URL/api/v1/welcome" | grep -q '"version":1' || fail "welcome route did not return version=1"
curl -fsS "${AUTH[@]}" "$PROXY_URL/operator/status" >"$LOG_DIR/operator-status.json"
python3 - "$LOG_DIR/operator-status.json" <<'PY' || fail "operator status lacks required Talk config"
import json, sys
with open(sys.argv[1], encoding="utf-8") as source:
    status = json.load(source)
talk = status.get("talk") if isinstance(status, dict) else None
if not isinstance(talk, dict):
    raise SystemExit("status has no talk object")
for key in ("secret_configured", "signaling_internal_secret_configured"):
    if talk.get(key) is not True:
        raise SystemExit(f"talk.{key} is not true")
PY
success "✓ installed ExApp status has both manifest-gated Talk secrets"

before_catalog="$LOG_DIR/catalog-before.json"
fetch_json "$CATALOG_URL" "$before_catalog" true || fail "cannot fetch initial catalog"
mapfile -t previous_catalog_ids < <(catalog_ids "$before_catalog")
printf '[validate] existing catalog ids: %s\n' "${previous_catalog_ids[*]:-(none)}" >&2

log "Preparing private playback scaffold"
(cd "$REPO_ROOT" && ./bin/cassini dev play-private \
  --scaffold-only --nextcloud-host "$NEXTCLOUD_HOST") \
  > >(tee "$LOG_DIR/scaffold.log") \
  2> >(tee "$LOG_DIR/scaffold.err" >&2)

new_job_ids=()
for run in $(seq 1 "$RUN_COUNT"); do
  run_private_job "job${run}"
  new_job_ids+=("$RUN_JOB_ID")
done

final_catalog="$LOG_DIR/catalog-final.json"
fetch_json "$CATALOG_URL" "$final_catalog" false || fail "cannot fetch final catalog"
contains_args=(--catalog "$final_catalog")
for id in "${new_job_ids[@]}" "${previous_catalog_ids[@]}"; do
  [[ -n "$id" ]] && contains_args+=(--id "$id")
done
archive_summary="$($VALIDATOR catalog-contains "${contains_args[@]}")" \
  || fail "archive preservation check failed"
success "✓ archive preservation check passed ($archive_summary)"

VALIDATION_RESULT="passed"
write_summary 0

cat <<EOF

Installed ExApp private Talk validation passed.
  Nextcloud: $BASE_URL
  Runs:      $RUN_COUNT
  Jobs:      ${new_job_ids[*]}
  Catalog:   $CATALOG_URL
  Evidence:  $LOG_DIR
  Summary:   $LOG_DIR/summary.json
EOF
