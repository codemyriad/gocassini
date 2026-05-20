#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

OPERATOR_PORT="${OPERATOR_PORT:-14000}"
OPERATOR_BIND="${OPERATOR_BIND:-0.0.0.0:$OPERATOR_PORT}"
OPERATOR_URL="${OPERATOR_URL:-http://127.0.0.1:$OPERATOR_PORT}"
RECORDING_STATUS="${RECORDING_STATUS:-1}"
LIFECYCLE_TIMEOUT="${LIFECYCLE_TIMEOUT:-120}"
START_RECORDING_RETRIES="${START_RECORDING_RETRIES:-5}"
ROOM_NAME="${ROOM_NAME:-D-263 lifecycle $(date -u +%Y%m%d-%H%M%S)}"
KEEP_RUNTIME="${KEEP_RUNTIME:-0}"

RUN_ROOT="$(mktemp -d "$RUNTIME_DIR/d263-lifecycle.XXXXXX")"
FAKE_CASSINI="$RUN_ROOT/fake-cassini.sh"
OPERATOR_BIN="${CASSINI_OPERATOR_BIN:-$RUN_ROOT/cassini-operator}"
OPERATOR_LOG="$RUN_ROOT/operator.log"
FAKE_LOG="$RUN_ROOT/fake-cassini.log"
COOKIE_JAR="$RUN_ROOT/nextcloud.cookies"
GUEST_COOKIE_JAR="$RUN_ROOT/nextcloud-guest.cookies"
OPERATOR_PID=""
ROOM_TOKEN=""

cleanup() {
  local rc=$?
  if [[ -n "$OPERATOR_PID" ]] && kill -0 "$OPERATOR_PID" >/dev/null 2>&1; then
    kill "$OPERATOR_PID" >/dev/null 2>&1 || true
    wait "$OPERATOR_PID" >/dev/null 2>&1 || true
  fi
  if [[ "$KEEP_RUNTIME" != "1" && "$rc" -eq 0 ]]; then
    rm -rf "$RUN_ROOT"
  else
    log "D-263 lifecycle runtime kept at: $RUN_ROOT"
    log "Operator log: $OPERATOR_LOG"
    log "Fake cassini log: $FAKE_LOG"
  fi
  return "$rc"
}
trap cleanup EXIT

json_value() {
  local expr="$1"
  local raw="$2"
  python3 - "$expr" "$raw" <<'PY'
import json
import sys

expr = sys.argv[1]
raw = sys.argv[2]
data = json.loads(raw)
value = data
for part in expr.split("."):
    if part == "":
        continue
    if isinstance(value, list):
        value = value[int(part)]
    else:
        value = value.get(part)
if value is None:
    print("")
elif isinstance(value, (dict, list)):
    print(json.dumps(value, separators=(",", ":")))
else:
    print(value)
PY
}

assert_ocs_ok() {
  local label="$1"
  local raw="$2"
  python3 - "$label" "$raw" <<'PY'
import json
import sys

label = sys.argv[1]
raw = sys.argv[2]
try:
    data = json.loads(raw)
except Exception as exc:
    raise SystemExit(f"{label}: invalid JSON: {exc}\n{raw}")
meta = data.get("ocs", {}).get("meta", {})
if meta.get("status") != "ok":
    raise SystemExit(f"{label}: OCS failure: {meta}\n{raw}")
PY
}

ocs_form() {
  local method="$1"
  local url="$2"
  shift 2
  curl -sS \
    -u "$ADMIN_USER:$ADMIN_PASSWORD" \
    -b "$COOKIE_JAR" \
    -c "$COOKIE_JAR" \
    -H "OCS-APIRequest: true" \
    -H "Accept: application/json" \
    -X "$method" "$url" \
    "$@"
}

ocs_json() {
  local method="$1"
  local url="$2"
  local body="$3"
  curl -sS \
    -u "$ADMIN_USER:$ADMIN_PASSWORD" \
    -b "$COOKIE_JAR" \
    -c "$COOKIE_JAR" \
    -H "OCS-APIRequest: true" \
    -H "Accept: application/json" \
    -H "Content-Type: application/json" \
    -X "$method" "$url" \
    --data "$body"
}

guest_ocs_form() {
  local method="$1"
  local url="$2"
  shift 2
  curl -sS \
    -b "$GUEST_COOKIE_JAR" \
    -c "$GUEST_COOKIE_JAR" \
    -H "OCS-APIRequest: true" \
    -H "Accept: application/json" \
    -X "$method" "$url" \
    "$@"
}

guest_ocs_json() {
  local method="$1"
  local url="$2"
  local body="$3"
  curl -sS \
    -b "$GUEST_COOKIE_JAR" \
    -c "$GUEST_COOKIE_JAR" \
    -H "OCS-APIRequest: true" \
    -H "Accept: application/json" \
    -H "Content-Type: application/json" \
    -X "$method" "$url" \
    --data "$body"
}

write_fake_cassini() {
  cat > "$FAKE_CASSINI" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

LOG_FILE="${FAKE_CASSINI_LOG:-}"
if [[ -n "$LOG_FILE" ]]; then
  printf '%s\n' "$*" >> "$LOG_FILE"
fi

cmd="${1:-}"
if [[ "$#" -gt 0 ]]; then
  shift
fi

now_utc() {
  date -u +%Y-%m-%dT%H:%M:%SZ
}

write_recording() {
  local out="$1"
  mkdir -p "$out"
  if command -v ffmpeg >/dev/null 2>&1; then
    if ffmpeg -hide_banner -loglevel error -y \
      -f lavfi -i "color=c=black:s=16x16:r=1" \
      -f lavfi -i "anullsrc=channel_layout=mono:sample_rate=48000" \
      -t 0.5 \
      -map 0:v:0 -map 1:a:0 \
      -c:v ffv1 -c:a pcm_s16le \
      "$out/recording.mkv"; then
      :
    else
      printf 'fake recording\n' > "$out/recording.mkv"
    fi
  else
    printf 'fake recording\n' > "$out/recording.mkv"
  fi
  cat > "$out/cassini.json" <<EOF
{
  "kind": "run",
  "version": "cassini.run.v1",
  "created_at_utc": "$(now_utc)",
  "state": "ready",
  "stage": "ready",
  "source_mode": "talk",
  "recorder_name": "D263LifecycleFake",
  "recording": {
    "path": "recording.mkv",
    "format": "mkv"
  }
}
EOF
}

write_meeting() {
  local out="$1"
  mkdir -p "$out"
  printf 'D-263 lifecycle meeting\n' > "$out/transcript.txt"
  cat > "$out/cassini.json" <<EOF
{
  "kind": "meeting",
  "version": "cassini.meeting.v1",
  "created_at_utc": "$(now_utc)",
  "state": "ready",
  "stage": "ready",
  "source_kind": "run",
  "source_path": "fake",
  "files": {
    "transcript": "transcript.txt"
  }
}
EOF
}

write_site() {
  local out="$1"
  mkdir -p "$out"
  printf '<!doctype html><title>D-263 lifecycle</title><h1>D-263 lifecycle</h1>\n' > "$out/index.html"
  cat > "$out/cassini.json" <<EOF
{
  "kind": "site",
  "version": "cassini.site.v1",
  "created_at_utc": "$(now_utc)",
  "state": "ready",
  "stage": "ready",
  "source_path": "fake",
  "catalog_path": "index.html",
  "meeting_count": 1
}
EOF
}

case "$cmd" in
  doctor)
    exit 0
    ;;
  record)
    out=""
    while [[ "$#" -gt 0 ]]; do
      case "$1" in
        --out)
          out="$2"
          shift 2
          ;;
        *)
          shift
          ;;
      esac
    done
    [[ -n "$out" ]]
    printf '%s\n' "talk recorder running: room=fake duration_limit=none stop_when_room_empty=true room_empty_grace=30s final=$out/recording.mkv segments=$out/segments"
    trap 'write_recording "$out"; exit 0' TERM INT
    while :; do sleep 0.2; done
    ;;
  build)
    out=""
    while [[ "$#" -gt 0 ]]; do
      case "$1" in
        --out)
          out="$2"
          shift 2
          ;;
        *)
          shift
          ;;
      esac
    done
    [[ -n "$out" ]]
    write_meeting "$out"
    ;;
  publish)
    out=""
    while [[ "$#" -gt 0 ]]; do
      case "$1" in
        --out)
          out="$2"
          shift 2
          ;;
        *)
          shift
          ;;
      esac
    done
    [[ -n "$out" ]]
    write_site "$out"
    ;;
  *)
    echo "unexpected fake cassini command: $cmd" >&2
    exit 1
    ;;
esac
SH
  chmod +x "$FAKE_CASSINI"
}

build_operator_if_needed() {
  if [[ -n "${CASSINI_OPERATOR_BIN:-}" ]]; then
    if [[ ! -x "$CASSINI_OPERATOR_BIN" ]]; then
      echo "CASSINI_OPERATOR_BIN is not executable: $CASSINI_OPERATOR_BIN" >&2
      exit 2
    fi
    return
  fi

  log "Building cassini-operator test binary"
  GOCACHE="${GOCACHE:-/tmp/gocassini-go-build-cache}" \
    GOMODCACHE="${GOMODCACHE:-/tmp/gocassini-go-mod-cache}" \
    go -C "$REPO_ROOT/cassini-operator" build -o "$OPERATOR_BIN" ./cmd/cassini-operator
}

start_operator() {
  log "Starting temporary cassini-operator on $OPERATOR_BIND"
  CASSINI_REPO_ROOT="$REPO_ROOT" \
    CASSINI_BIN="$FAKE_CASSINI" \
    FAKE_CASSINI_LOG="$FAKE_LOG" \
    CASSINI_TALK_RECORDING_SECRET="$CASSINI_TALK_RECORDING_SECRET" \
    CASSINI_TALK_BACKEND_URL="$NEXTCLOUD_URL" \
    "$OPERATOR_BIN" \
      --bind "$OPERATOR_BIND" \
      --db "$RUN_ROOT/jobs.sqlite3" \
      --work-root "$RUN_ROOT/jobs" \
      --site-root "$RUN_ROOT/site" \
      >"$OPERATOR_LOG" 2>&1 &
  OPERATOR_PID="$!"

  local end_time=$((SECONDS + 30))
  until (( SECONDS >= end_time )); do
    if curl -fsS "$OPERATOR_URL/healthz?check=record" >/dev/null 2>&1; then
      log "cassini-operator is ready"
      return 0
    fi
    if ! kill -0 "$OPERATOR_PID" >/dev/null 2>&1; then
      echo "cassini-operator exited early" >&2
      sed -n '1,160p' "$OPERATOR_LOG" >&2 || true
      exit 1
    fi
    sleep 0.5
  done

  echo "cassini-operator did not become ready at $OPERATOR_URL" >&2
  sed -n '1,160p' "$OPERATOR_LOG" >&2 || true
  exit 1
}

configure_nextcloud_recording_backend() {
  local recording_url="${CASSINI_TALK_RECORDING_URL:-}"
  if [[ -z "$recording_url" || "$recording_url" == "http://127.0.0.1:"* || "$recording_url" == "http://localhost:"* ]]; then
    local gateway
    gateway="$(docker network inspect "${PROJECT_NAME}_default" -f '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null || true)"
    if [[ -n "$gateway" ]]; then
      recording_url="http://$gateway:$OPERATOR_PORT"
    else
      recording_url="$OPERATOR_URL"
    fi
  fi

  log "Configuring Nextcloud Talk recording backend for lifecycle test: $recording_url"
  CASSINI_TALK_RECORDING_URL="$recording_url" "$SCRIPT_DIR/bootstrap.sh"
}

create_and_activate_room() {
  local call_url
  call_url="$(create_room_with_retry "$ROOM_NAME" 8)"
  ROOM_TOKEN="${call_url##*/}"
  log "Lifecycle room token: $ROOM_TOKEN"

  local active_response
  active_response="$(guest_ocs_form POST "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v4/room/$ROOM_TOKEN/participants/active" --data-urlencode "force=false" --data-urlencode "displayName=D263LifecycleGuest")"
  assert_ocs_ok "mark participant active" "$active_response"

  local call_body='{"flags":7,"silent":false,"recordingConsent":false,"silentFor":[]}'
  local response
  response="$(guest_ocs_json POST "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v4/call/$ROOM_TOKEN" "$call_body")"
  assert_ocs_ok "activate call" "$response"
}

start_recording() {
	local response
	log "Starting Talk recording via Nextcloud API"
	local attempt=1
	while ((attempt <= START_RECORDING_RETRIES)); do
		response="$(ocs_form POST "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v1/recording/$ROOM_TOKEN" --data-urlencode "status=$RECORDING_STATUS")"
		if [[ "$(json_value "ocs.meta.status" "$response")" == "ok" ]]; then
			return 0
		fi
		if ((attempt == START_RECORDING_RETRIES)); then
			assert_ocs_ok "start recording" "$response"
		fi
		log "start recording attempt $attempt/$START_RECORDING_RETRIES failed; retrying in 2s"
		sleep 2
		attempt=$((attempt + 1))
	done
}

stop_recording() {
  local response
  log "Stopping Talk recording via Nextcloud API"
  response="$(ocs_form DELETE "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v1/recording/$ROOM_TOKEN")"
  assert_ocs_ok "stop recording" "$response"
}

operator_jobs_json() {
  curl -fsS "$OPERATOR_URL/jobs"
}

single_job_field() {
  local field="$1"
  local raw="$2"
  python3 - "$field" "$raw" <<'PY'
import json
import sys

field = sys.argv[1]
raw = sys.argv[2]
jobs = json.loads(raw)
if not jobs:
    print("")
    raise SystemExit(0)
value = jobs[0].get(field)
print("" if value is None else value)
PY
}

wait_for_job_state() {
  local want_state="$1"
  local timeout_s="$2"
  local end_time=$((SECONDS + timeout_s))
  local raw state stage job_id

  until (( SECONDS >= end_time )); do
    raw="$(operator_jobs_json || true)"
    state="$(single_job_field state "$raw")"
    stage="$(single_job_field stage "$raw")"
    job_id="$(single_job_field id "$raw")"
    if [[ "$state" == "$want_state" ]]; then
      log "Job $job_id reached state=$state stage=$stage"
      return 0
    fi
    if [[ "$state" == "failed" ]]; then
      echo "job failed while waiting for state=$want_state" >&2
      echo "$raw" >&2
      sed -n '1,220p' "$OPERATOR_LOG" >&2 || true
      exit 1
    fi
    sleep 1
  done

  echo "timed out waiting for job state=$want_state" >&2
  operator_jobs_json >&2 || true
  sed -n '1,220p' "$OPERATOR_LOG" >&2 || true
  exit 1
}

wait_for_done_succeeded() {
  local timeout_s="$1"
  local end_time=$((SECONDS + timeout_s))
  local raw state stage job_id

  until (( SECONDS >= end_time )); do
    raw="$(operator_jobs_json || true)"
    state="$(single_job_field state "$raw")"
    stage="$(single_job_field stage "$raw")"
    job_id="$(single_job_field id "$raw")"
    if [[ "$state" == "succeeded" && "$stage" == "done" ]]; then
      log "Job $job_id completed state=$state stage=$stage"
      return 0
    fi
    if [[ "$state" == "failed" ]]; then
      echo "job failed before completion" >&2
      echo "$raw" >&2
      sed -n '1,260p' "$OPERATOR_LOG" >&2 || true
      exit 1
    fi
    sleep 1
  done

  echo "timed out waiting for job completion" >&2
  operator_jobs_json >&2 || true
  sed -n '1,260p' "$OPERATOR_LOG" >&2 || true
  exit 1
}

main() {
  wait_for_nextcloud 420
  write_fake_cassini
  build_operator_if_needed
  start_operator
  configure_nextcloud_recording_backend
  create_and_activate_room
  start_recording
  wait_for_job_state "running" "$LIFECYCLE_TIMEOUT"
  stop_recording
  wait_for_done_succeeded "$LIFECYCLE_TIMEOUT"

  log "D-263 Nextcloud lifecycle test passed"
  log "Room token: $ROOM_TOKEN"
}

main "$@"
