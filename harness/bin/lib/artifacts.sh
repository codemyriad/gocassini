# Safe Cassini recorder/session artifact helpers.

if [[ "${CASSINI_HARNESS_LIB_ARTIFACTS_SOURCED:-0}" == "1" ]]; then
  return 0
fi
CASSINI_HARNESS_LIB_ARTIFACTS_SOURCED=1

# shellcheck source=./base.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/base.sh"

cassini_session_id_from_mkv() {
  local input="$1"
  ffprobe -v error -show_entries format_tags -of default=nw=1 "$input" 2>/dev/null \
    | awk -F= '/^TAG:(SESSION_ID|session_id)=/ {print $2; exit}'
}

cassini_session_dir_from_mkv() {
  local input="$1"
  local session_id
  session_id="$(cassini_session_id_from_mkv "$input")"
  if [[ -z "$session_id" ]]; then
    return 1
  fi
  printf '%s/sessions/%s\n' "$(dirname "$input")" "$session_id"
}

cassini_session_json_from_mkv() {
  local session_dir
  session_dir="$(cassini_session_dir_from_mkv "$1")" || return 1
  printf '%s/session.json\n' "$session_dir"
}

cassini_events_log_from_mkv() {
  local session_dir
  session_dir="$(cassini_session_dir_from_mkv "$1")" || return 1
  printf '%s/events.ndjson\n' "$session_dir"
}

cassini_streams_dir_from_mkv() {
  local session_dir
  session_dir="$(cassini_session_dir_from_mkv "$1")" || return 1
  printf '%s/streams\n' "$session_dir"
}

cassini_unique_participant_count_from_mkv() {
  local input="$1"
  ffprobe -v error -show_entries stream_tags -of default=nw=1 "$input" 2>/dev/null \
    | awk -F= '/^TAG:(PARTICIPANT_ID|participant_id)=/ {print $2}' \
    | sort -u \
    | awk 'NF{count++} END {print count+0}'
}
