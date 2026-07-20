#!/usr/bin/env bash
# Require recorder-side accepted media for one participant after a phase boundary.

set -euo pipefail

EVENTS=""
BOUNDARY_NS=""
PARTICIPANT=""
REMOTE_SESSION_ID=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --events) EVENTS="$2"; shift 2 ;;
    --boundary-ns) BOUNDARY_NS="$2"; shift 2 ;;
    --participant) PARTICIPANT="$2"; shift 2 ;;
    --remote-session-id) REMOTE_SESSION_ID="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

[[ -n "$EVENTS" ]] || { echo "missing --events <events.ndjson>" >&2; exit 2; }
[[ -f "$EVENTS" ]] || { echo "events log not found: $EVENTS" >&2; exit 1; }
if [[ ! "$BOUNDARY_NS" =~ ^[0-9]+$ ]] || (( BOUNDARY_NS <= 0 )); then
  echo "--boundary-ns must be a positive integer" >&2
  exit 2
fi
if [[ -z "$PARTICIPANT" && -z "$REMOTE_SESSION_ID" ]]; then
  echo "provide --participant <id-or-name> and/or --remote-session-id <id>" >&2
  exit 2
fi

python3 - "$EVENTS" "$BOUNDARY_NS" "$PARTICIPANT" "$REMOTE_SESSION_ID" <<'PY'
import json
import sys

path, raw_boundary, expected, expected_remote = sys.argv[1:]
boundary = int(raw_boundary)
matched = []
stream_events = 0

with open(path, encoding="utf-8") as source:
    for line_number, raw in enumerate(source, 1):
        if not raw.strip():
            continue
        try:
            event = json.loads(raw)
        except json.JSONDecodeError as exc:
            raise SystemExit(f"malformed events log at line {line_number}: {exc}")
        if not isinstance(event, dict):
            raise SystemExit(f"malformed events log at line {line_number}: event is not an object")
        if event.get("type") != "stream_opened":
            continue
        stream_events += 1
        mono_ns = event.get("mono_ns")
        if isinstance(mono_ns, bool) or not isinstance(mono_ns, int):
            raise SystemExit(
                f"malformed stream_opened at line {line_number}: mono_ns is not an integer"
            )
        kind = str(event.get("kind") or "").strip().lower()
        identities = {
            str(event.get("participant_id") or "").strip(),
            str(event.get("participant_name") or "").strip(),
        }
        remote = str(event.get("remote_session_id") or "").strip()
        identity_matches = bool(expected and expected in identities)
        remote_matches = bool(expected_remote and expected_remote == remote)
        if mono_ns >= boundary and kind in {"audio", "video"} and (identity_matches or remote_matches):
            matched.append(
                {
                    "line": line_number,
                    "mono_ns": mono_ns,
                    "kind": kind,
                    "participant_id": event.get("participant_id", ""),
                    "participant_name": event.get("participant_name", ""),
                    "remote_session_id": event.get("remote_session_id", ""),
                    "stream_id": event.get("stream_id", ""),
                }
            )

if not matched:
    raise SystemExit(
        f"no audio/video stream_opened for participant {expected!r} / remote session "
        f"{expected_remote!r} at/after boundary {boundary} "
        f"(stream_opened events inspected: {stream_events})"
    )

print(json.dumps({"boundary_ns": boundary, "match": matched[0]}, separators=(",", ":")))
PY
