#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

FINAL_OUTPUT="${FINAL_OUTPUT:-}"
REPORT="${REPORT:-}"
REQUIRE_STREAMS="${REQUIRE_STREAMS:-1}"
# Minimum RTP *media* packets required per captured stream.
#
# Counted kind-aware: RTCP (receiver reports etc.) does NOT count, because a
# stream can carry hundreds of RTCP records while receiving no media at all --
# that is exactly the D-454 shape this guard exists to catch. Once RTCP is
# excluded, a floor of 1 provably means "media flowed", so no tuned magnitude
# is needed to be meaningful.
#
# Set to 0 to opt a call site down. No call site does today, deliberately:
# an rtplog exists for every *announced* track (the recorder opens the stream
# at OnTrack, before any packet arrives), so a stream with rtp=0 is a genuine
# capture failure everywhere -- including the rejoin leg, whose "[WARN] no
# video track" blesses a missing MKV track, not a silent capture.
MIN_RTP_PACKETS="${MIN_RTP_PACKETS:-1}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --final-output)
      FINAL_OUTPUT="$2"
      shift 2
      ;;
    --report)
      REPORT="$2"
      shift 2
      ;;
    --require-streams)
      REQUIRE_STREAMS="$2"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "$FINAL_OUTPUT" ]]; then
  echo "missing --final-output <path>" >&2
  exit 1
fi

if [[ -z "$REPORT" ]]; then
  REPORT="${FINAL_OUTPUT}.json"
fi

SESSION_JSON="$(cassini_session_json_from_mkv "$FINAL_OUTPUT" || true)"
EVENTS_PATH="$(cassini_events_log_from_mkv "$FINAL_OUTPUT" || true)"
STREAMS_DIR="$(cassini_streams_dir_from_mkv "$FINAL_OUTPUT" || true)"

if [[ -z "$SESSION_JSON" || -z "$EVENTS_PATH" || -z "$STREAMS_DIR" ]]; then
  if [[ -f "$REPORT" ]]; then
    if ! command -v jq >/dev/null 2>&1; then
      echo "jq is required to read legacy report fallback: $REPORT" >&2
      exit 1
    fi
    SESSION_JSON="$(jq -r '.session_artifact.session_json // empty' "$REPORT")"
    EVENTS_PATH="$(jq -r '.session_artifact.events_ndjson // empty' "$REPORT")"
    STREAMS_DIR="$(jq -r '.session_artifact.streams_dir // empty' "$REPORT")"
    ENABLED="$(jq -r '.session_artifact.enabled // false' "$REPORT")"
    if [[ "$ENABLED" != "true" ]]; then
      echo "session artifact not enabled according to report; skipping"
      exit 0
    fi
  fi
fi

if [[ -z "$SESSION_JSON" || -z "$EVENTS_PATH" || -z "$STREAMS_DIR" ]]; then
  echo "could not derive session artifact paths from mkv metadata or legacy report" >&2
  exit 1
fi

if [[ ! -f "$SESSION_JSON" ]]; then
  echo "session json not found: $SESSION_JSON" >&2
  exit 1
fi
if [[ ! -f "$EVENTS_PATH" ]]; then
  echo "events log not found: $EVENTS_PATH" >&2
  exit 1
fi
if [[ ! -d "$STREAMS_DIR" ]]; then
  echo "streams directory not found: $STREAMS_DIR" >&2
  exit 1
fi

if ! rg -q '"type"[[:space:]]*:[[:space:]]*"session_started"' "$EVENTS_PATH"; then
  echo "session_started event not found in $EVENTS_PATH" >&2
  exit 1
fi

stream_count="$(find "$STREAMS_DIR" -type f -name '*.rtplog' | wc -l | tr -d ' ')"
if (( REQUIRE_STREAMS == 1 )) && [[ "$stream_count" -lt 1 ]]; then
  echo "no rtplog files found in $STREAMS_DIR" >&2
  exit 1
fi

if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required to walk stream records" >&2
  exit 1
fi

# Walks one .rtplog and prints "<rtp> <rtcp> <records>".
#
# Format (pkg/core/store/writer.go:39-123, all big-endian):
#   header:  magic(8)="RTPL0\0\0\0" ver(2) flags(2) hdrLen(4) hdrJSON(hdrLen)
#   record:  recvMonoNS(8) kind(1) len(4) wire(len) [crc(4) iff flags&1]
#   kinds:   1=RTP (media), 2=RTCP (bookkeeping)   [types.go:14-15]
# Production writes flags==0 (session_artifact.go:194 passes no WithCRC), but
# honour flags&1 anyway so a CRC-enabled stream does not desync the walk.
walk_stream() {
  python3 - "$1" <<'PY'
import struct
import sys

MAGIC = b"RTPL0\x00\x00\x00"
KIND_RTP, KIND_RTCP = 1, 2

path = sys.argv[1]
with open(path, "rb") as f:
    blob = f.read()

if len(blob) < 16 or blob[:8] != MAGIC:
    sys.exit("invalid stream header magic")
ver, flags, hdr_len = struct.unpack(">HHI", blob[8:16])
if ver != 1:
    sys.exit(f"unsupported stream version: {ver}")
off = 16 + hdr_len
if off > len(blob):
    sys.exit("truncated stream header payload")

rtp = rtcp = records = 0
while off < len(blob):
    if off + 13 > len(blob):
        sys.exit(f"truncated record framing at offset {off}")
    kind = blob[off + 8]
    (wire_len,) = struct.unpack(">I", blob[off + 9 : off + 13])
    off += 13 + wire_len + (4 if flags & 1 else 0)
    if off > len(blob):
        sys.exit(f"truncated record payload at offset {off}")
    if kind == KIND_RTP:
        rtp += 1
    elif kind == KIND_RTCP:
        rtcp += 1
    else:
        sys.exit(f"unsupported record kind: {kind}")
    records += 1

print(f"{rtp} {rtcp} {records}")
PY
}

# NOTE: fed by process substitution so the loop runs in the *main* shell and a
# bare `exit 1` here genuinely aborts the script. Do not refactor into
# `find ... | while read` -- the loop would run in a subshell and the guard
# would silently stop guarding.
if [[ "$stream_count" -gt 0 ]]; then
  starved=""
  while IFS= read -r stream; do
    stream_id="$(basename "$stream" .rtplog)"
    index_path="${stream%.rtplog}.idx"
    if [[ ! -f "$index_path" ]]; then
      echo "missing index for stream: $stream" >&2
      exit 1
    fi

    index_size="$(stat -c '%s' "$index_path")"
    if (( index_size % 16 != 0 )); then
      echo "stream index has invalid entry width: $index_path (size=${index_size})" >&2
      exit 1
    fi
    index_entries=$(( index_size / 16 ))

    if ! walked="$(walk_stream "$stream")"; then
      echo "unreadable stream log: $stream" >&2
      exit 1
    fi
    read -r rtp rtcp records <<<"$walked"

    # BuildIndex writes one 16-byte entry per record regardless of kind
    # (pkg/core/store/index.go:154-158), so these must agree exactly. A
    # mismatch means a truncated or desynced index.
    if (( index_entries != records )); then
      echo "stream index desynced from log: $stream_id index_entries=${index_entries} records=${records}" >&2
      exit 1
    fi

    log "  stream_id=${stream_id} rtp=${rtp} rtcp=${rtcp} records=${records}"
    if (( rtp < MIN_RTP_PACKETS )); then
      starved+="    ${stream_id}: rtp=${rtp} rtcp=${rtcp} (need rtp >= ${MIN_RTP_PACKETS})"$'\n'
    fi
  done < <(find "$STREAMS_DIR" -type f -name '*.rtplog' -print | sort)

  if [[ -n "$starved" ]]; then
    {
      echo "session artifact contains stream(s) with no captured media:"
      printf '%s' "$starved"
      echo "a stream with rtp=0 means the track was announced but no media was ever"
      echo "captured (rtplog is created at OnTrack, before any packet) -- this is the"
      echo "D-454 empty-capture failure, not a benign short run."
    } >&2
    exit 1
  fi
fi

log "session artifact verification passed"
log "  session_json: $SESSION_JSON"
log "  events_log: $EVENTS_PATH"
log "  streams_dir: $STREAMS_DIR"
log "  stream_count: $stream_count"
log "  min_rtp_packets: $MIN_RTP_PACKETS"
