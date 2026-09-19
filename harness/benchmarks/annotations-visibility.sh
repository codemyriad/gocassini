#!/usr/bin/env bash
# annotations-visibility.sh — measure POST-to-GET visibility for meeting tags.
#
# This is a harness benchmark, not a unit or implementation benchmark. It
# seeds a fresh reserved recording with a clean portable .opus fixture, sends the
# same public annotation POST the UI sends, then polls the public GET endpoint
# until that mark is visible. It deliberately measures the whole deployed path:
# AppAPI proxy, operator, Nextcloud, the portable file, and the read path.
#
# The script is intentionally not wired into CI. It needs a running local
# harness stack and adds one tag per sample and waits for final archive synchronization.
#
#   harness/benchmarks/annotations-visibility.sh \
#     --tries 20
#
# Environment variables mirror the flags:
#   CASSINI_ANNOTATION_BENCH_FIXTURE
#   CASSINI_ANNOTATION_BENCH_NEXTCLOUD_URL
#   CASSINI_ANNOTATION_BENCH_USER / CASSINI_ANNOTATION_BENCH_PASSWORD
#   CASSINI_ANNOTATION_BENCH_BURN_IN
#   CASSINI_ANNOTATION_BENCH_TRIES
#   CASSINI_ANNOTATION_BENCH_MEETING_ID
#   CASSINI_ANNOTATION_BENCH_POLL_INTERVAL_MS
#   CASSINI_ANNOTATION_BENCH_POLL_TIMEOUT_SECONDS
#   CASSINI_ANNOTATION_BENCH_REPORT_DIR

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
# shellcheck disable=SC1091 # dynamic repository root, validated by this script's location
source "$REPO_ROOT/harness/bin/lib/stack.sh"

FIXTURE="${CASSINI_ANNOTATION_BENCH_FIXTURE:-}"
NEXTCLOUD_URL="${CASSINI_ANNOTATION_BENCH_NEXTCLOUD_URL:-}"
BENCH_USER="${CASSINI_ANNOTATION_BENCH_USER:-admin}"
BENCH_PASSWORD="${CASSINI_ANNOTATION_BENCH_PASSWORD:-admin}"
TRIES="${CASSINI_ANNOTATION_BENCH_TRIES:-20}"
BURN_IN="${CASSINI_ANNOTATION_BENCH_BURN_IN:-5}"
MEETING_ID="${CASSINI_ANNOTATION_BENCH_MEETING_ID:-cassini-annotations-benchmark}"
POLL_INTERVAL_MS="${CASSINI_ANNOTATION_BENCH_POLL_INTERVAL_MS:-100}"
POLL_TIMEOUT_SECONDS="${CASSINI_ANNOTATION_BENCH_POLL_TIMEOUT_SECONDS:-60}"
REPORT_DIR="${CASSINI_ANNOTATION_BENCH_REPORT_DIR:-}"
PERSIST_REPORT=0
DRY_RUN=0
TEMP_DIR=""

usage() {
  cat <<'EOF'
Usage:
  harness/benchmarks/annotations-visibility.sh [--fixture <portable.opus>] [options]

Measure elapsed time from adding a whole-meeting tag through the public POST
endpoint until the public GET response contains that tag. The target must be a
running local Cassini harness stack: setup creates a fresh benchmark
recording, then leaves it in place with the sampled tags for inspection.

Options:
  --fixture PATH                 clean portable-meeting .opus to seed; default:
                                first .opus in harness/runtime/seed/prod/meetings
  --nextcloud-url URL            Nextcloud origin (default: harness URL)
  --user USER                    Nextcloud user (default: admin)
  --password PASSWORD            user's password (default: admin)
  --burn-in N                    warm-up writes excluded from samples (default: 5)
  --tries N                      samples to collect (default: 20)
  --meeting-id ID                fixture ID prefix (a unique run suffix is added)
                                (default: cassini-annotations-benchmark)
  --poll-interval-ms N           delay between unsuccessful GETs (default: 100)
  --poll-timeout-seconds N       per-sample visibility deadline (default: 60)
  --report-dir DIR               write samples.tsv and summary.txt there
  --persist-report               write the report under benchmarks/tagging-roundtrip/
                                <current-commit-hash>/ in this repository
  --dry-run                      validate the target and describe fixture setup only
  -h, --help                     show this help

All options have a CASSINI_ANNOTATION_BENCH_* equivalent; see this file's
header. Harness topology variables such as PROJECT_NAME and NEXTCLOUD_HOST_PORT
are also honoured.
EOF
}

die() { printf '[annotation-benchmark] error: %s\n' "$*" >&2; exit 1; }
info() { printf '[annotation-benchmark] %s\n' "$*"; }

cleanup() {
  if [[ -n "$TEMP_DIR" && -d "$TEMP_DIR" ]]; then
    rm -rf "$TEMP_DIR"
  fi
}
trap cleanup EXIT

while [[ $# -gt 0 ]]; do
  case "$1" in
    --fixture) [[ $# -ge 2 ]] || die "--fixture needs a value"; FIXTURE="$2"; shift 2 ;;
    --fixture=*) FIXTURE="${1#--fixture=}"; shift ;;
    --nextcloud-url) [[ $# -ge 2 ]] || die "--nextcloud-url needs a value"; NEXTCLOUD_URL="$2"; shift 2 ;;
    --nextcloud-url=*) NEXTCLOUD_URL="${1#--nextcloud-url=}"; shift ;;
    --user) [[ $# -ge 2 ]] || die "--user needs a value"; BENCH_USER="$2"; shift 2 ;;
    --user=*) BENCH_USER="${1#--user=}"; shift ;;
    --password) [[ $# -ge 2 ]] || die "--password needs a value"; BENCH_PASSWORD="$2"; shift 2 ;;
    --password=*) BENCH_PASSWORD="${1#--password=}"; shift ;;
    --burn-in) [[ $# -ge 2 ]] || die "--burn-in needs a value"; BURN_IN="$2"; shift 2 ;;
    --burn-in=*) BURN_IN="${1#--burn-in=}"; shift ;;
    --tries) [[ $# -ge 2 ]] || die "--tries needs a value"; TRIES="$2"; shift 2 ;;
    --tries=*) TRIES="${1#--tries=}"; shift ;;
    --meeting-id) [[ $# -ge 2 ]] || die "--meeting-id needs a value"; MEETING_ID="$2"; shift 2 ;;
    --meeting-id=*) MEETING_ID="${1#--meeting-id=}"; shift ;;
    --poll-interval-ms) [[ $# -ge 2 ]] || die "--poll-interval-ms needs a value"; POLL_INTERVAL_MS="$2"; shift 2 ;;
    --poll-interval-ms=*) POLL_INTERVAL_MS="${1#--poll-interval-ms=}"; shift ;;
    --poll-timeout-seconds) [[ $# -ge 2 ]] || die "--poll-timeout-seconds needs a value"; POLL_TIMEOUT_SECONDS="$2"; shift 2 ;;
    --poll-timeout-seconds=*) POLL_TIMEOUT_SECONDS="${1#--poll-timeout-seconds=}"; shift ;;
    --report-dir) [[ $# -ge 2 ]] || die "--report-dir needs a value"; REPORT_DIR="$2"; shift 2 ;;
    --report-dir=*) REPORT_DIR="${1#--report-dir=}"; shift ;;
    --persist-report) PERSIST_REPORT=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "unknown option: $1" ;;
  esac
done

if (( PERSIST_REPORT )); then
  [[ -z "$REPORT_DIR" ]] || die "--persist-report cannot be combined with --report-dir"
  REPORT_DIR="$REPO_ROOT/benchmarks/tagging-roundtrip/$(git -C "$REPO_ROOT" rev-parse HEAD)"
fi

require() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }
for dependency in curl docker ffprobe jq python3 tar; do require "$dependency"; done

if [[ -z "$FIXTURE" ]]; then
  FIXTURE="$(find "$REPO_ROOT/harness/runtime/seed/prod/meetings" -type f -name '*.opus' -print -quit 2>/dev/null || true)"
fi

[[ -n "$FIXTURE" ]] || die "no fixture found; pass --fixture or add an .opus under harness/runtime/seed/prod/meetings"
[[ -f "$FIXTURE" ]] || die "fixture does not exist: $FIXTURE"
FIXTURE="$(cd "$(dirname "$FIXTURE")" && pwd)/$(basename "$FIXTURE")"
[[ "$BURN_IN" =~ ^[0-9]+$ ]] || die "--burn-in must be non-negative"
[[ "$TRIES" =~ ^[1-9][0-9]*$ ]] || die "--tries must be a positive integer"
[[ "$POLL_INTERVAL_MS" =~ ^[0-9]+$ ]] || die "--poll-interval-ms must be a non-negative integer"
[[ "$POLL_TIMEOUT_SECONDS" =~ ^[1-9][0-9]*$ ]] || die "--poll-timeout-seconds must be a positive integer"
[[ "$MEETING_ID" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] \
  || die "--meeting-id must be 1-128 letters, digits, dots, underscores, or dashes"
[[ -n "$BENCH_USER" ]] || die "--user must not be empty"

format_tag="$(ffprobe -v error -show_entries format_tags=CASSINI_FORMAT:stream_tags=CASSINI_FORMAT -of default=noprint_wrappers=1:nokey=1 "$FIXTURE" | tr -d '\r' | sort -u || true)"
[[ "$format_tag" == "org.cassini.portable-meeting/1" ]] \
  || die "fixture is not a portable Cassini meeting (CASSINI_FORMAT=$format_tag)"

harness_stack_env_resolve
# Keep the harness helpers and the seeder on the same selected stack. The
# seed step needs its local Docker Compose storage root even when the origin is
# reachable through a LAN or HTTPS harness URL.
export NEXTCLOUD_URL="${CASSINI_ANNOTATION_BENCH_NEXTCLOUD_URL:-$NEXTCLOUD_URL}"
harness_stack_init
NEXTCLOUD_URL="${CASSINI_ANNOTATION_BENCH_NEXTCLOUD_URL:-$NEXTCLOUD_URL}"
NEXTCLOUD_URL="${NEXTCLOUD_URL%/}"

# A fresh catalog ID prevents a fixture reset from bypassing durable desired state.
MEETING_ID="${MEETING_ID:0:90}-$(date -u +%s)-$$"
ANNOTATIONS_URL="$NEXTCLOUD_URL/index.php/apps/app_api/proxy/gocassini/annotations/meetings/$MEETING_ID"
MEETING_FILE="$MEETING_ID.opus"
SAMPLES_FILE=""

now_ns() { python3 -c 'import time; print(time.monotonic_ns())'; }

request() {
  local output="$1"
  shift
  curl -sS --connect-timeout 5 --max-time 90 -o "$output" -w '%{http_code}' "$@"
}

assert_reachable() {
  local status_body="$TEMP_DIR/status.json" status_code nextcloud_cid
  nextcloud_cid="$(compose ps -q nextcloud)"
  [[ -n "$nextcloud_cid" ]] || die "the harness Nextcloud container is not running; run cassini dev stack up first"
  status_code="$(request "$status_body" "$NEXTCLOUD_URL/status.php")" \
    || die "cannot reach $NEXTCLOUD_URL/status.php"
  [[ "$status_code" == "200" ]] || die "Nextcloud status endpoint returned HTTP $status_code"
  jq -e '.installed == true' "$status_body" >/dev/null \
    || die "Nextcloud is reachable but is not installed"
}

locate_recordings_dir() {
  local data_dir folders_json folder_id
  data_dir="$(occ config:system:get datadirectory 2>/dev/null | tr -d '\r' || true)"
  [[ -n "$data_dir" ]] || die "could not read Nextcloud's datadirectory"
  if harness_storage_mode_is_acl; then
    folders_json="$(occ groupfolders:list --output=json 2>/dev/null)" \
      || die "could not list Team folders; has the Cassini ExApp finished provisioning?"
    folder_id="$(jq -r --arg mount_point Cassini \
      'map(select(.mountPoint == $mount_point)) | first | .id // empty' <<<"$folders_json")"
    [[ -n "$folder_id" ]] || die "the harness has no Cassini Team folder; finish ExApp provisioning first"
    printf '%s\n' "$data_dir/__groupfolders/$folder_id/files/Recordings"
  else
    printf '%s\n' "$data_dir/cassini/files/CassiniNoACL/Recordings"
  fi
}

scaffold_fixture() {
  local nextcloud_cid pack_dir catalog
  locate_recordings_dir >/dev/null
  nextcloud_cid="$(compose ps -q nextcloud)"
  [[ -n "$nextcloud_cid" ]] || die "the harness Nextcloud container is not running"

  info "seeding fresh benchmark fixture $MEETING_ID"
  if (( DRY_RUN )); then
    info "dry run: would seed $MEETING_FILE and wait for its annotation import"
    return
  fi

  pack_dir="$TEMP_DIR/pack"
  mkdir -p "$pack_dir/meetings"
  cp "$FIXTURE" "$pack_dir/meetings/$MEETING_FILE"
  catalog="$pack_dir/catalog.json"
  jq -n --arg id "$MEETING_ID" --arg audio "meetings/$MEETING_FILE" \
    '{version: "cassini.viewer.catalog.v1", meetings: [{id: $id, title: "Annotation visibility benchmark", dateLabel: "Harness benchmark", audioPath: $audio}]}' \
    >"$catalog"
  "$REPO_ROOT/harness/bin/seed-nc-files.sh" --pack "$pack_dir"
}

assert_clean_fixture() {
  local body="$TEMP_DIR/clean.json" code deadline
  deadline=$((SECONDS + POLL_TIMEOUT_SECONDS))
  while true; do
    code="$(request "$body" -u "$BENCH_USER:$BENCH_PASSWORD" "$ANNOTATIONS_URL")" || die "fixture GET failed"
    if [[ "$code" == "200" ]]; then break; fi
    [[ "$code" == "503" && "$SECONDS" -lt "$deadline" ]] || die "fixture GET returned $code: $(cat "$body")"
    poll_sleep
  done
  jq -e '.annotations == null' "$body" >/dev/null || die "fixture already carries annotations"
}

wait_for_archive() {
  local body="$TEMP_DIR/archive.json" code deadline sync_state
  deadline=$((SECONDS + POLL_TIMEOUT_SECONDS))
  while true; do
    code="$(request "$body" -u "$BENCH_USER:$BENCH_PASSWORD" "$ANNOTATIONS_URL")" || die "archive status request failed"
    if [[ "$code" == "200" ]]; then
      # Earlier synchronous implementations do not expose a sync state. Their
      # successful read is already the durable result, while the asynchronous
      # implementation must report that its archive write has settled.
      sync_state="$(jq -r '.sync.state // empty' "$body")"
      [[ -z "$sync_state" || "$sync_state" == "saved" ]] && return
      [[ "$sync_state" != "blocked" ]] || die "archive sync blocked: $(cat "$body")"
    fi
    (( SECONDS < deadline )) || die "archive sync did not settle: $(cat "$body")"
    poll_sleep
  done
}

poll_sleep() {
  local seconds whole milliseconds
  whole=$((POLL_INTERVAL_MS / 1000))
  milliseconds=$((POLL_INTERVAL_MS % 1000))
  printf -v seconds '%d.%03d' "$whole" "$milliseconds"
  sleep "$seconds"
}

tag_is_visible() {
  local body="$1" label="$2"
  jq -e --arg label "$label" '
    (.annotations // {}) as $annotations |
    [($annotations.tags // [])[] | select(.label == $label) | .id] as $tag_ids |
    ($tag_ids | length == 1) and
    ([($annotations.items // [])[]
      | select(.tagId == $tag_ids[0] and .target.kind == "meeting")] | length >= 1)
  ' "$body" >/dev/null
}

benchmark() {
  local index label post_body post_response get_response post_code get_code
  local start_ns end_ns deadline_ns elapsed_ms polls post_end_ns request_seconds timing measured phase
  local total=$((TRIES + BURN_IN))
  SAMPLES_FILE="$TEMP_DIR/samples.tsv"
  : >"$SAMPLES_FILE"
  : >"$TEMP_DIR/burn-in.tsv"

  for ((index = 1; index <= total; index++)); do
    measured=$((index - BURN_IN))
    phase="sample"; (( measured > 0 )) || phase="burn-in"
    label="tag_$index"
    post_body="$(jq -n --arg label "$label" --arg request_id "${MEETING_ID: -35}-$index" \
      '{requestId: $request_id, ops: [{op: "mark", tag: {label: $label}, target: {kind: "meeting"}}]}')"
    post_response="$TEMP_DIR/post-$index.json"
    get_response="$TEMP_DIR/get-$index.json"
    start_ns="$(now_ns)"
    timing="$(curl -sS --connect-timeout 5 --max-time 90 -o "$post_response" -w '%{http_code} %{time_total}' \
      -u "$BENCH_USER:$BENCH_PASSWORD" -X POST -H 'Content-Type: application/json' --data "$post_body" "$ANNOTATIONS_URL")" \
      || die "$phase $index POST could not reach the annotation API"
    read -r post_code request_seconds <<<"$timing"
    post_end_ns="$(now_ns)"
    [[ "$post_code" == "200" ]] \
      || die "sample $index POST returned HTTP $post_code: $(tr '\n' ' ' <"$post_response")"

    polls=0
    deadline_ns=$((start_ns + POLL_TIMEOUT_SECONDS * 1000000000))
    while true; do
      polls=$((polls + 1))
      get_code="$(request "$get_response" -u "$BENCH_USER:$BENCH_PASSWORD" "$ANNOTATIONS_URL")" \
        || die "sample $index poll $polls could not reach the annotation API"
      if [[ "$get_code" == "200" ]] && tag_is_visible "$get_response" "$label"; then
        end_ns="$(now_ns)"
        elapsed_ms="$(python3 - "$start_ns" "$end_ns" <<'PY'
import sys
print(f"{(int(sys.argv[2]) - int(sys.argv[1])) / 1_000_000:.3f}")
PY
)"
        if (( measured > 0 )); then
          printf '%s\t%s\t%s\t%s\n' "$measured" "$elapsed_ms" "$polls" "$request_seconds" >>"$SAMPLES_FILE"
        else
          printf '%s\t%s\t%s\t%s\n' "$index" "$elapsed_ms" "$polls" "$request_seconds" >>"$TEMP_DIR/burn-in.tsv"
        fi
        info "$phase $index/$total: ${elapsed_ms} ms visibility, ${request_seconds}s POST, $polls poll(s)"
        break
      fi
      if (( $(now_ns) >= deadline_ns )); then
        die "sample $index did not expose $label after $polls polls in ${POLL_TIMEOUT_SECONDS}s (last HTTP $get_code)"
      fi
      poll_sleep
    done
    if (( index == BURN_IN )); then wait_for_archive; fi
  done
  wait_for_archive
  ARCHIVE_SETTLE_MS="$(( ($(now_ns) - post_end_ns) / 1000000 ))"
}

report() {
  local summary="$TEMP_DIR/summary.txt"
  python3 - "$SAMPLES_FILE" "$BURN_IN" "$FIXTURE" "$MEETING_ID" "$ARCHIVE_SETTLE_MS" >"$summary" <<'PY'
import math
import os
import statistics
import sys

rows = []
with open(sys.argv[1], encoding="utf-8") as samples:
    for line in samples:
        index, elapsed_ms, polls, request_seconds = line.rstrip("\n").split("\t")
        rows.append((int(index), float(elapsed_ms), int(polls), float(request_seconds) * 1000))

times = sorted(row[1] for row in rows)
polls = [row[2] for row in rows]

def nearest_rank(percent, times=times):
    return times[max(0, math.ceil(percent / 100 * len(times)) - 1)]

print("Annotation visibility benchmark")
print(f"meeting: {sys.argv[4]}")
print(f"fixture_bytes: {os.path.getsize(sys.argv[3])}")
print(f"burn_in: {sys.argv[2]} (excluded)")
print(f"samples: {len(rows)}")
print("POST roundtrip (curl time_total):")
requests = sorted(row[3] for row in rows)
for percentile in (55, 90, 99):
    print(f"  p{percentile}: {nearest_rank(percentile, requests):.3f} ms")
print("time from POST start until GET contains the new mark:")
print(f"  p55: {nearest_rank(55):.3f} ms")
print(f"  p90: {nearest_rank(90):.3f} ms")
print(f"  p99: {nearest_rank(99):.3f} ms")
print("GET polls until the new mark is visible:")
print(f"  min: {min(polls)}")
print(f"  avg: {statistics.mean(polls):.3f}")
print(f"  max: {max(polls)}")
print(f"final archive settle after last POST: {sys.argv[5]} ms")
print("percentiles use nearest-rank selection.")
PY
  cat "$summary"
  if [[ -n "$REPORT_DIR" ]]; then
    mkdir -p "$REPORT_DIR"
    cp "$SAMPLES_FILE" "$REPORT_DIR/samples.tsv"
    cp "$TEMP_DIR/burn-in.tsv" "$REPORT_DIR/burn-in.tsv"
    cp "$summary" "$REPORT_DIR/summary.txt"
    info "wrote metrics to $REPORT_DIR"
  fi
}

TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/cassini-annotation-benchmark.XXXXXX")"
assert_reachable
scaffold_fixture
if (( DRY_RUN )); then
  exit 0
fi
assert_clean_fixture
benchmark
report
