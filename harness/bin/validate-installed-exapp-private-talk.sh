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
MEDIA_PREFIX=""
EXPECT_BUILD_BLOCKED=0
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
then strictly validates the new job, direct Nextcloud Files delivery, the
Files-backed viewer catalog/artifact responses, and transcript content. Two
runs remain the manual default so archive preservation is checked; faithful CI
uses --run-count 1.

Options:
  --nextcloud-host <host-or-url>  Default: CASSINI_HARNESS_HOST, then 127.0.0.1
  --duration <seconds>            Playback duration per run. Default: 60
  --job-timeout <seconds>         Job/catalog timeout. Default: 1200
  --poll-interval <seconds>       Poll interval. Default: 5
  --run-count <count>             Recording attempts. Default: 2
  --conversation <name>           play-private conversation. Default: admin
  --media-prefix <path>            Existing IVF/OGG prefix used for all participants
  --expect-build-blocked          Require retained capture plus immediate build/blocked
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
    --media-prefix) [[ $# -ge 2 ]] || fail "$1 requires a value"; MEDIA_PREFIX="$2"; shift 2 ;;
    --expect-build-blocked) EXPECT_BUILD_BLOCKED=1; shift ;;
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
# Both modes inspect the installed container. Positive artifact validation also
# runs the host CLI, whose transcript extraction needs host ffprobe and ffmpeg;
# the blocked portable-image path probes media metadata inside the container and
# deliberately performs no speech decode.
for tool in docker curl jq python3; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is required"
done
if (( ! EXPECT_BUILD_BLOCKED )); then
  for tool in ffprobe ffmpeg; do
    command -v "$tool" >/dev/null 2>&1 || fail "$tool is required"
  done
fi
[[ -x "$VALIDATOR" ]] || fail "installed validator is not executable: $VALIDATOR"
[[ -x "$ARTIFACT_VALIDATOR" ]] || fail "artifact validator is not executable: $ARTIFACT_VALIDATOR"

normalize_base_url() {
  local input="$1"
  if [[ "$input" =~ ^https?:// ]]; then printf '%s\n' "${input%/}"; else printf 'http://%s:28080\n' "${input%/}"; fi
}

BASE_URL="$(normalize_base_url "$NEXTCLOUD_HOST")"
PROXY_URL="$BASE_URL/index.php/apps/app_api/proxy/gocassini"
CATALOG_URL="$PROXY_URL/published/catalog.json"
MEETINGS_LIST_URL="$PROXY_URL/published/meetings-list"
FILES_ROOT_URL="$BASE_URL/remote.php/dav/files/$ADMIN_USER/Cassini/Recordings"
AUTH=(-u "$ADMIN_USER:$ADMIN_PASSWORD")
# The standard viewer user the harness creates (harness_create_standard_viewer_user).
# In the virtual everyone group, so the group folder mounts for her, but never
# in the room -- exactly the caller access control exists to stop.
OUTSIDER_USER="${OUTSIDER_USER:-alice}"
OUTSIDER_PASSWORD="${OUTSIDER_PASSWORD:-Tn8mY3qVrJ2x!E2e}"
AUTH_VALUE="$ADMIN_USER:$ADMIN_PASSWORD"
COMPOSE=(docker compose -p "$PROJECT_NAME" -f "$REPO_ROOT/harness/compose.yml")
mkdir -p "$LOG_DIR"
SUMMARY_NDJSON="$LOG_DIR/runs.ndjson"
: >"$SUMMARY_NDJSON"
RUN_JOB_ID=""
RUN_ARTIFACT_SUMMARY=""
RUN_BLOCKED_SUMMARY=""
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

# join_url resolves a catalog audioPath ("./meetings/<id>.opus") against a base.
join_url() {
  python3 -c 'import sys; from urllib.parse import urljoin; print(urljoin(sys.argv[1], sys.argv[2]))' "$1" "$2"
}

assert_files_source() {
  local url="$1" label="$2" range="${3:-}" code
  # Separate 'local': $label expands before the assignments above take effect
  # (SC2318), which pointed headers at the caller's label and failed CI.
  local headers="$LOG_DIR/${label}-source.headers"
  local -a request=(curl -sS "${AUTH[@]}" -D "$headers" -o /dev/null -w '%{http_code}')
  [[ -z "$range" ]] || request+=(-H "Range: $range")
  code="$("${request[@]}" "$url")" || return 1
  [[ "$code" == 200 || "$code" == 206 ]] || return 1
  grep -Eiq "^X-Cassini-Meeting-Source:[[:space:]]*nextcloud-files[[:space:]]*\r?$" "$headers"
}

validate_files_archive_entry() {
  local label="$1" job_id="$2"
  local catalog="$LOG_DIR/catalog-${label}.json" audio_path audio_url code
  # The authoritative Cassini/Recordings/catalog.json is deliberately owner-only:
  # the operator reads it as the recordings owner and serves each caller a
  # filtered view, so nobody else -- the administrator included -- can read the
  # unfiltered archive index out of Files. This used to fetch it directly as
  # $ADMIN_USER, which worked only while `admin` was itself the recordings
  # owner; D-532 moved ownership to the `cassini` service account and D-554 made
  # the ACL unconditional, so that read is now a 404 by design.
  #
  # Assert what the caller is actually promised instead: the per-caller catalog
  # they were served names the meeting, and the recording itself is really in
  # Files, readable by this caller because they were in the room.
  audio_path="$(jq -er --arg id "$job_id" '.meetings[] | select(.id == $id) | .audioPath' "$catalog")" || return 1
  audio_url="$(join_url "$FILES_ROOT_URL/" "$audio_path")" || return 1
  code="$(curl -sS "${AUTH[@]}" -H 'Range: bytes=0-3' -o "$LOG_DIR/files-${label}.opus-prefix" -w '%{http_code}' "$audio_url")" || return 1
  [[ "$code" == 206 ]] || return 1
  [[ "$(wc -c <"$LOG_DIR/files-${label}.opus-prefix" | tr -d ' ')" == 4 ]] || return 1
  # ...and that the authoritative index stays unreadable to a caller who is not
  # the owner. If this ever starts returning a body, the archive index is
  # leaking to everyone the group folder mounts for.
  code="$(curl -sS "${AUTH[@]}" -o /dev/null -w '%{http_code}' "$FILES_ROOT_URL/catalog.json")" || return 1
  [[ "$code" == 404 || "$code" == 403 ]] || return 1
  assert_files_source "$CATALOG_URL" "${label}-catalog" || return 1
  grep -Eiq '^Cache-Control:.*no-store' "$LOG_DIR/${label}-catalog-source.headers" || return 1
  assert_files_source "$PROXY_URL/published/${audio_path#./}" "${label}-opus" 'bytes=0-3' || return 1
  validate_meetings_list_endpoint "$label" "$job_id" || return 1
}

# The meetings-list endpoint (D-701), asserted against the REAL AppAPI proxy.
#
# The load-bearing assertion is the filtered one. Nothing else Cassini serves
# through the proxy carries a query string, so that AppAPI forwards it was only
# ever established by reading its ExAppProxyController -- not by observing it on
# a supported Nextcloud. A stripped query would be invisible in every unit test
# and would silently turn every filtered request into an unfiltered one.
validate_meetings_list_endpoint() {
  local label="$1" job_id="$2"
  local listing="$LOG_DIR/meetings-list-${label}.json" code

  # Unfiltered: the caller who was in the room sees their meeting here, exactly
  # as they do in the catalog.
  fetch_json "$MEETINGS_LIST_URL" "$listing" false || return 1
  jq -e --arg id "$job_id" '[.meetings[] | select(.id == $id)] | length == 1' "$listing" >/dev/null || return 1
  jq -e '.version == "cassini.viewer.catalog.v1"' "$listing" >/dev/null || return 1
  # An unfiltered listing carries no filter echo, so a client cannot mistake one.
  jq -e 'has("filter") | not' "$listing" >/dev/null || return 1

  # A range that predates every recording must come back empty AND echo the
  # bounds the server parsed. Both halves matter: the echo proves the query
  # string arrived, and the empty list proves it was applied. A stripped query
  # would return the full listing with no echo, failing both.
  fetch_json "$MEETINGS_LIST_URL?from=1970-01-01&to=1970-01-02" "$listing" false || return 1
  jq -e '.meetings | length == 0' "$listing" >/dev/null || return 1
  jq -e '.filter.from == "1970-01-01 00:00:00"' "$listing" >/dev/null || return 1
  # A bare `to` date covers the whole day, rather than stopping at its midnight.
  jq -e '.filter.to == "1970-01-02 23:59:59"' "$listing" >/dev/null || return 1

  # A caller error is 400, not a 502 and not an empty 200 -- an agent has to be
  # able to tell "fix your query" from "retry later".
  code="$(curl -sS "${AUTH[@]}" -o /dev/null -w '%{http_code}' "$MEETINGS_LIST_URL?from=nonsense")" || return 1
  [[ "$code" == 400 ]] || return 1
}

# A user who was not in the room must see nothing and be able to fetch nothing.
# This is the half the install e2e cannot reach -- it never records -- and it is
# the whole point of D-521: being logged in is necessary, not sufficient.
validate_non_participant_denied() {
  local label="$1" job_id="$2"
  local catalog="$LOG_DIR/catalog-${label}-outsider.json" code
  local -a outsider_auth=(-u "$OUTSIDER_USER:$OUTSIDER_PASSWORD")

  code="$(curl -sS "${outsider_auth[@]}" -o "$catalog" -w '%{http_code}' "$CATALOG_URL")" || return 1
  # 200 with an empty list, never an error: the read path fails closed, and the
  # viewer must show "nothing here for you" rather than an outage.
  [[ "$code" == 200 ]] || return 1
  jq -e '.meetings | length == 0' "$catalog" >/dev/null 2>&1 || return 1

  # Playback is denied as a miss, so a recording a caller may not read never
  # even reveals that it exists.
  code="$(curl -sS "${outsider_auth[@]}" -o /dev/null -w '%{http_code}' \
    "$PROXY_URL/published/meetings/${job_id}.opus")" || return 1
  [[ "$code" == 404 ]] || return 1

  # And the same through Nextcloud's own WebDAV, which is what actually enforces
  # it -- the operator is not the thing saying no.
  code="$(curl -sS "${outsider_auth[@]}" -o /dev/null -w '%{http_code}' \
    "$BASE_URL/remote.php/dav/files/$OUTSIDER_USER/Cassini/Recordings/meetings/${job_id}.opus")" || return 1
  [[ "$code" == 404 || "$code" == 403 ]] || return 1

  # The list endpoint denies identically. It is louder than catalog.json about
  # SUBSTRATE failures, which makes it worth pinning that a genuine denial is
  # still a plain empty 200 here -- if this ever became a 4xx/5xx, the endpoint
  # would be telling an outsider that something exists to be refused.
  local listing="$LOG_DIR/meetings-list-${label}-outsider.json"
  code="$(curl -sS "${outsider_auth[@]}" -o "$listing" -w '%{http_code}' "$MEETINGS_LIST_URL")" || return 1
  [[ "$code" == 200 ]] || return 1
  jq -e '.meetings | length == 0' "$listing" >/dev/null 2>&1 || return 1
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

wait_for_new_job_blocked() {
  local label="$1" before_jobs="$2"
  local deadline=$((SECONDS + JOB_TIMEOUT)) jobs="$LOG_DIR/jobs-${label}.json" result rc
  while (( SECONDS < deadline )); do
    if fetch_json "$PROXY_URL/operator/jobs" "$jobs" false; then
      set +e
      result="$($VALIDATOR select-job --expect build-blocked --before "$before_jobs" --current "$jobs" 2>"$LOG_DIR/jobs-${label}.err")"
      rc=$?
      set -e
      case "$rc" in
        0)
          RUN_JOB_ID="$(jq -r '.job_id' <<<"$result")"
          RUN_BLOCKED_SUMMARY="$result"
          success "✓ $label recording preserved and portable-image build blocked: $RUN_JOB_ID"
          return 0
          ;;
        10) printf '[validate] waiting for %s build to block: %s\n' "$label" "$result" >&2 ;;
        3) cat "$LOG_DIR/jobs-${label}.err" >&2 || true; fail "$label job failed instead of blocking: $result" ;;
        *) cat "$LOG_DIR/jobs-${label}.err" >&2 || true; fail "$label blocked-build selection was invalid" ;;
      esac
    fi
    sleep "$POLL_INTERVAL"
  done
  fail "timed out waiting for exactly one new $label recording to reach build/blocked"
}

validate_blocked_run_bundle() {
  local label="$1" run_path manifest recording_path recording_bytes audio_packets detail build_log_path build_log_bytes container_log
  run_path="$(jq -er '.artifact_run_path' <<<"$RUN_BLOCKED_SUMMARY")" \
    || fail "$label blocked job has no run path"
  manifest="$LOG_DIR/run-${label}.cassini.json"
  docker exec nc_app_gocassini cat "$run_path/cassini.json" >"$manifest" \
    || fail "$label blocked run manifest is not readable"
  jq -e '
    .kind == "run"
    and .version == "cassini.run.v1"
    and .state == "ready"
    and .stage == "ready"
    and .source_mode == "talk"
    and (.recording.path | type == "string" and length > 0)
  ' "$manifest" >/dev/null || fail "$label blocked run manifest is not ready Talk capture"
  recording_path="$(jq -er '.recording.path' "$manifest")"
  [[ "$recording_path" != /* && "$recording_path" != *..* ]] \
    || fail "$label blocked run manifest has unsafe recording path: $recording_path"
  docker exec nc_app_gocassini test -s "$run_path/$recording_path" \
    || fail "$label blocked recording is missing or empty"
  recording_bytes="$(docker exec nc_app_gocassini stat -c %s "$run_path/$recording_path")"
  [[ "$recording_bytes" =~ ^[1-9][0-9]*$ ]] \
    || fail "$label blocked recording size is invalid: $recording_bytes"
  docker exec nc_app_gocassini ffprobe -v error -select_streams a:0 \
    -count_packets -show_entries stream=codec_type,nb_read_packets -of json \
    "$run_path/$recording_path" >"$LOG_DIR/run-${label}.audio-streams.json" \
    || fail "$label blocked recording failed audio-stream metadata probe"
  audio_packets="$(jq -er '[.streams[] | select(.codec_type == "audio") | ((.nb_read_packets | tonumber?) // 0)] | max // 0' \
    "$LOG_DIR/run-${label}.audio-streams.json")" \
    || fail "$label blocked recording has invalid audio packet metadata"
  if [[ ! "$audio_packets" =~ ^[0-9]+$ ]] || (( audio_packets < 10 )); then
    fail "$label blocked recording has too few audio packets: $audio_packets"
  fi

  detail="$LOG_DIR/job-detail-${label}.json"
  fetch_json "$PROXY_URL/operator/jobs/$RUN_JOB_ID" "$detail" false \
    || fail "$label blocked job detail is unavailable"
  build_log_path="$(jq -er --argjson attempt "$(jq -r '.job.current_attempt_number' "$detail")" \
    '.attempts[] | select(.attempt_number == $attempt) | .build_log_path' "$detail")" \
    || fail "$label blocked attempt has no build log path"
  docker exec nc_app_gocassini test -f "$build_log_path" \
    || fail "$label blocked build log does not exist"
  build_log_bytes="$(docker exec nc_app_gocassini stat -c %s "$build_log_path")"
  [[ "$build_log_bytes" == "0" ]] \
    || fail "$label blocked build invoked the transcription CLI (build log is ${build_log_bytes} bytes)"
  container_log="$LOG_DIR/container-${label}.log"
  docker logs nc_app_gocassini >"$container_log" 2>&1 \
    || fail "$label cannot read installed ExApp log"
  grep -F "build blocked id=$RUN_JOB_ID" "$container_log" \
    | grep -F "resource governor: CUDA runtime unavailable" >/dev/null \
    || fail "$label container log does not confirm the permanent portable-image CUDA block"
  RUN_BLOCKED_SUMMARY="$(jq -c --argjson recording_bytes "$recording_bytes" --argjson audio_packets "$audio_packets" \
    '. + {recording_bytes:$recording_bytes,audio_packets:$audio_packets}' <<<"$RUN_BLOCKED_SUMMARY")"
  success "✓ $label raw Talk recording is durable (${recording_bytes} bytes, ${audio_packets} audio packets, ASR CLI not invoked)"
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
          validate_files_archive_entry "$label" "$job_id" \
            || fail "$label was not delivered to Nextcloud Files or the viewer did not identify Files as its source"
          validate_non_participant_denied "$label" "$job_id" \
            || fail "$label was visible or fetchable by $OUTSIDER_USER, who was not in the room"
          success "✓ $label viewer artifact validated from Nextcloud Files: $RUN_ARTIFACT_SUMMARY"
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
  local -a play_args
  before_jobs="$LOG_DIR/jobs-before-${label}.json"
  fetch_json "$PROXY_URL/operator/jobs" "$before_jobs" false || fail "cannot fetch jobs before $label"
  started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  log "Running private Talk playback for $label (single recording attempt)"
  play_args=(
    --conversation "$CONVERSATION"
    --nextcloud-host "$NEXTCLOUD_HOST"
    --duration "$DURATION"
  )
  [[ -z "$MEDIA_PREFIX" ]] || play_args+=(--media-prefix "$MEDIA_PREFIX")
  (cd "$REPO_ROOT" && ./bin/cassini dev play-private "${play_args[@]}") \
    > >(tee "$LOG_DIR/playback-${label}.log") \
    2> >(tee "$LOG_DIR/playback-${label}.err" >&2)

  if (( EXPECT_BUILD_BLOCKED )); then
    wait_for_new_job_blocked "$label" "$before_jobs"
    validate_blocked_run_bundle "$label"
    jq -nc \
      --arg label "$label" \
      --arg started_at "$started_at" \
      --argjson blocked "$RUN_BLOCKED_SUMMARY" \
      '{label:$label,started_at:$started_at,job_id:$blocked.job_id,blocked:$blocked}' \
      >>"$SUMMARY_NDJSON"
  else
    wait_for_new_job_success "$label" "$before_jobs"
    wait_for_catalog_artifact "$label" "$RUN_JOB_ID"
    jq -nc \
      --arg label "$label" \
      --arg started_at "$started_at" \
      --argjson artifact "$RUN_ARTIFACT_SUMMARY" \
      '{label:$label,started_at:$started_at,job_id:$artifact.job_id,artifact:$artifact}' \
      >>"$SUMMARY_NDJSON"
  fi
}

log "Validating installed Cassini ExApp at $PROXY_URL"
log "Evidence directory: $LOG_DIR"
ensure_nextcloud_host_trusted "$(base_url_host)"
curl -fsS "$PROXY_URL/api/v1/welcome" | grep -q '"version":1' || fail "welcome route did not return version=1"
status_code="$(curl -sS "${AUTH[@]}" -o "$LOG_DIR/operator-status.json" -w '%{http_code}' "$PROXY_URL/operator/status")"
if (( EXPECT_BUILD_BLOCKED )); then
  [[ "$status_code" == "503" ]] || fail "GPU-less operator status returned HTTP $status_code, expected 503"
else
  [[ "$status_code" == "200" ]] || fail "ready operator status returned HTTP $status_code, expected 200"
fi
python3 - "$LOG_DIR/operator-status.json" "$EXPECT_BUILD_BLOCKED" <<'PY' || fail "operator status lacks required Talk/GPU contract"
import json, sys
with open(sys.argv[1], encoding="utf-8") as source:
    status = json.load(source)
expect_blocked = sys.argv[2] == "1"
talk = status.get("talk") if isinstance(status, dict) else None
if not isinstance(talk, dict):
    raise SystemExit("status has no talk object")
for key in ("secret_configured", "signaling_internal_secret_configured"):
    if talk.get(key) is not True:
        raise SystemExit(f"talk.{key} is not true")
if expect_blocked:
    stt = status.get("stt") or {}
    storage = status.get("storage") or {}
    if not (
        status.get("ok") is False
        and stt.get("device") == "cuda"
        and stt.get("device_usable") is False
        and str(stt.get("detail") or "").strip()
        and (status.get("db") or {}).get("ok") is True
        and (storage.get("work_root") or {}).get("ok") is True
        and (storage.get("site_root") or {}).get("ok") is True
        and (status.get("recordings_access") or {}).get("ok") is True
    ):
        raise SystemExit("503 is not the expected sole-STT GPU unavailability")
elif status.get("ok") is not True:
    raise SystemExit("operator reported HTTP 200 without ok=true")
PY
success "✓ installed ExApp status has Talk secrets and the expected GPU readiness state"

before_catalog="$LOG_DIR/catalog-before.json"
fetch_json "$CATALOG_URL" "$before_catalog" true || fail "cannot fetch initial catalog"
jq -e '.meetings | type == "array"' "$before_catalog" >/dev/null \
  || fail "initial catalog does not contain a meetings array"
mapfile -t previous_catalog_ids < <(catalog_ids "$before_catalog")
printf '[validate] existing catalog ids: %s\n' "${previous_catalog_ids[*]:-(none)}" >&2

log "Preparing private playback scaffold"
scaffold_args=(--scaffold-only --nextcloud-host "$NEXTCLOUD_HOST")
[[ -z "$MEDIA_PREFIX" ]] || scaffold_args+=(--media-prefix "$MEDIA_PREFIX")
(cd "$REPO_ROOT" && ./bin/cassini dev play-private "${scaffold_args[@]}") \
  > >(tee "$LOG_DIR/scaffold.log") \
  2> >(tee "$LOG_DIR/scaffold.err" >&2)

new_job_ids=()
for run in $(seq 1 "$RUN_COUNT"); do
  run_private_job "job${run}"
  new_job_ids+=("$RUN_JOB_ID")
done

final_catalog="$LOG_DIR/catalog-final.json"
if (( EXPECT_BUILD_BLOCKED )); then
  fetch_json "$CATALOG_URL" "$final_catalog" true || fail "cannot fetch final catalog"
  jq -e '.meetings | type == "array"' "$final_catalog" >/dev/null \
    || fail "final catalog does not contain a meetings array"
  for id in "${new_job_ids[@]}"; do
    if jq -e --arg id "$id" '.meetings | any(.id == $id)' "$final_catalog" >/dev/null; then
      fail "GPU-blocked job $id was published despite having no transcript"
    fi
  done
  for id in "${previous_catalog_ids[@]}"; do
    if [[ -n "$id" ]] && ! jq -e --arg id "$id" '.meetings | any(.id == $id)' "$final_catalog" >/dev/null; then
      fail "previously published meeting $id disappeared while a GPU build was blocked"
    fi
  done
  success "✓ GPU-blocked recordings were not published as completed meetings"
  VALIDATION_RESULT="passed"
  write_summary 0
  cat <<EOF

Installed ExApp portable-image capture/block validation passed.
  Nextcloud: $BASE_URL
  Runs:      $RUN_COUNT
  Jobs:      ${new_job_ids[*]}
  Evidence:  $LOG_DIR
  Summary:   $LOG_DIR/summary.json
EOF
  exit 0
fi
fetch_json "$CATALOG_URL" "$final_catalog" false || fail "cannot fetch final catalog"
contains_args=(--catalog "$final_catalog")
for id in "${new_job_ids[@]}" "${previous_catalog_ids[@]}"; do
  [[ -n "$id" ]] && contains_args+=(--id "$id")
done
archive_summary="$($VALIDATOR catalog-contains "${contains_args[@]}")" \
  || fail "archive preservation check failed"
# Archive preservation in Files itself. This used to read
# Cassini/Recordings/catalog.json over WebDAV as $ADMIN_USER, which only worked
# while `admin` was the recordings owner: the authoritative index is owner-only
# on purpose (D-532 moved ownership to `cassini`, D-554 made the ACL
# unconditional), so that read is a 404 now and asserting on it would be
# asserting the archive index leaks.
#
# Assert on the recordings instead, which is the actual claim: every meeting
# this run produced is still in Files and still readable by a participant. The
# viewer-facing half of preservation is already covered by the per-caller
# catalog check above.
for id in "${new_job_ids[@]}"; do
  [[ -n "$id" ]] || continue
  code="$(curl -sS "${AUTH[@]}" -H 'Range: bytes=0-3' -o /dev/null -w '%{http_code}' \
    "$FILES_ROOT_URL/meetings/${id}.opus")" \
    || fail "Nextcloud Files archive request failed for $id"
  [[ "$code" == 206 || "$code" == 200 ]] \
    || fail "Nextcloud Files archive does not hold a readable recording for $id (HTTP $code)"
done
success "✓ viewer and Nextcloud Files archive preservation checks passed ($archive_summary)"

VALIDATION_RESULT="passed"
write_summary 0

cat <<EOF

Installed ExApp private Talk validation passed.
  Nextcloud: $BASE_URL
  Runs:      $RUN_COUNT
  Jobs:      ${new_job_ids[*]}
  Catalog:   $CATALOG_URL
  List:      $MEETINGS_LIST_URL
  Evidence:  $LOG_DIR
  Summary:   $LOG_DIR/summary.json
EOF
