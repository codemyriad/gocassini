#!/usr/bin/env bash
#
# Negative control for verify-session-artifact.sh (D-510).
#
# The guard's job is to catch a capture that produced no media (D-454:
# "intermittent empty capture / no remuxable streams / blank"). Before this
# control existed, the guard checked headers, magic, and `idx_size % 16 == 0`
# -- and `0 % 16 == 0`, so an artifact with a 0-byte index and a header-only
# rtplog printed "session artifact verification passed". The exact regression
# class the guard exists for could slip straight through it.
#
# So this file feeds the REAL verifier synthetic artifacts and asserts the
# verdict both ways: RED on captures with no media, GREEN on healthy ones. A
# control that only ever passes proves nothing -- that is the disease being
# cured here, so every red case below is paired with a green one that differs
# only in the packets.
#
# Load-bearing detail: every fixture is STRUCTURALLY VALID. Valid magic, valid
# version, index size a clean multiple of 16, all sidecar files present. They
# satisfy every check the old guard made (asserted explicitly in case 0). The
# only thing wrong with the red fixtures is that they contain no media -- so a
# red verdict can only come from the packet floor, not from the fixture being
# malformed junk.
#
# Fast, hermetic, offline: no Docker, no stack, no Go build, no MKV. Fixtures
# are synthesized from the on-disk format as pkg/core/store/writer.go emits it
# and fed via the verifier's legacy --report path (jq only, no ffprobe). Run:
#   ./harness/bin/test-session-artifact-packet-guard.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VERIFIER="$SCRIPT_DIR/verify-session-artifact.sh"

[[ -x "$VERIFIER" ]] || { echo "FAIL: verifier not found/executable: $VERIFIER" >&2; exit 1; }
for cmd in python3 jq rg; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "FAIL: $cmd is required" >&2; exit 1; }
done

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

CASES_RUN=0

# build_fixture <dir> <spec>...
#
# Each spec is "<stream_id>:<rtp_count>:<rtcp_count>[:idx_entries]". idx_entries
# defaults to rtp+rtcp (a consistent index); pass it explicitly to synthesize a
# desynced one. Writes streams/, session.json, events.ndjson and the legacy
# report, then echoes the report path.
build_fixture() {
  local dir="$1"; shift
  mkdir -p "$dir"
  python3 - "$dir" "$@" <<'PY'
import json
import os
import struct
import sys

MAGIC = b"RTPL0\x00\x00\x00"
KIND_RTP, KIND_RTCP = 1, 2

# Mirrors pkg/core/store/writer.go:39-123 -- big-endian throughout, flags==0
# (production NewWriter passes no WithCRC, session_artifact.go:194), so no CRC
# trailer. If this drifts from the Go writer the control stops being a control,
# so the framing is spelled out rather than borrowed.
def write_rtplog(path, stream_id, rtp, rtcp):
    header = json.dumps({"version": 1, "stream_id": stream_id}).encode("utf-8")
    with open(path, "wb") as f:
        f.write(MAGIC)
        f.write(struct.pack(">H", 1))            # version
        f.write(struct.pack(">H", 0))            # flags (no CRC)
        f.write(struct.pack(">I", len(header)))  # header length
        f.write(header)
        n = 0
        for kind, count, payload in (
            (KIND_RTP, rtp, b"\x80\x60" + b"\x11" * 158),   # RTP-ish media
            (KIND_RTCP, rtcp, b"\x80\xc9\x00\x01" * 4),     # receiver report
        ):
            for _ in range(count):
                f.write(struct.pack(">q", 1_000_000 + n))   # recvMonoNS
                f.write(bytes([kind]))                       # kind
                f.write(struct.pack(">I", len(payload)))     # wire length
                f.write(payload)
                n += 1

# BuildIndex writes recv(8) + offset(8) per record regardless of kind
# (pkg/core/store/index.go:154-158) -> 16 bytes/record, 0 records = 0 bytes.
def write_idx(path, entries):
    with open(path, "wb") as f:
        for i in range(entries):
            f.write(struct.pack(">q", 1_000_000 + i))
            f.write(struct.pack(">q", i * 32))

dir_ = sys.argv[1]
session = os.path.join(dir_, "sessions", "s1")
streams = os.path.join(session, "streams")
os.makedirs(streams, exist_ok=True)

for spec in sys.argv[2:]:
    parts = spec.split(":")
    sid, rtp, rtcp = parts[0], int(parts[1]), int(parts[2])
    entries = int(parts[3]) if len(parts) > 3 else rtp + rtcp
    write_rtplog(os.path.join(streams, f"{sid}.rtplog"), sid, rtp, rtcp)
    write_idx(os.path.join(streams, f"{sid}.idx"), entries)

with open(os.path.join(session, "session.json"), "w", encoding="utf-8") as f:
    json.dump({"session_id": "s1"}, f)
with open(os.path.join(session, "events.ndjson"), "w", encoding="utf-8") as f:
    f.write(json.dumps({"type": "session_started", "session_id": "s1"}) + "\n")

# A stub .mkv: ffprobe finds no SESSION_ID tag, so the verifier falls back to
# the legacy --report path. That fallback is what keeps this control hermetic.
with open(os.path.join(dir_, "out.mkv"), "wb") as f:
    f.write(b"\x00" * 64)

report = os.path.join(dir_, "out.mkv.json")
with open(report, "w", encoding="utf-8") as f:
    json.dump({"session_artifact": {
        "enabled": True,
        "session_json": os.path.join(session, "session.json"),
        "events_ndjson": os.path.join(session, "events.ndjson"),
        "streams_dir": streams,
    }}, f)
print(report)
PY
}

# Runs the real verifier against a fixture, capturing output. Returns its exit code.
run_verifier() {
  local report="$1"
  local dir
  dir="$(dirname "$report")"
  local rc=0
  OUTPUT="$("$VERIFIER" --final-output "$dir/out.mkv" --report "$report" 2>&1)" || rc=$?
  return $rc
}

expect_red() {
  local name="$1" report="$2" want_msg="$3"
  CASES_RUN=$((CASES_RUN + 1))
  if run_verifier "$report"; then
    echo "FAIL [$name]: verifier PASSED an artifact it must reject" >&2
    echo "--- verifier output ---" >&2
    echo "$OUTPUT" >&2
    exit 1
  fi
  if ! grep -q -- "$want_msg" <<<"$OUTPUT"; then
    echo "FAIL [$name]: verifier failed, but not for the expected reason" >&2
    echo "  expected message containing: $want_msg" >&2
    echo "--- verifier output ---" >&2
    echo "$OUTPUT" >&2
    exit 1
  fi
  echo "  ok (red)   $name"
}

expect_green() {
  local name="$1" report="$2"
  CASES_RUN=$((CASES_RUN + 1))
  if ! run_verifier "$report"; then
    echo "FAIL [$name]: verifier REJECTED a healthy artifact (false-fail)" >&2
    echo "--- verifier output ---" >&2
    echo "$OUTPUT" >&2
    exit 1
  fi
  echo "  ok (green) $name"
}

echo "session artifact packet guard control"

# --- case 0: the fixtures are structurally valid -------------------------------
#
# This is what makes every red verdict below meaningful. The 0-packet fixture
# must satisfy every assertion the PRE-D-510 guard made -- sidecars present,
# 8-byte magic intact, idx size a clean multiple of 16 -- so that when the
# current guard rejects it, the rejection can only be about missing media.
# Without this, the control could be "proving" the guard rejects corrupt junk.
EMPTY_DIR="$TMP_ROOT/empty"
EMPTY_REPORT="$(build_fixture "$EMPTY_DIR" "a:0:0")"
EMPTY_STREAMS="$EMPTY_DIR/sessions/s1/streams"
CASES_RUN=$((CASES_RUN + 1))
[[ -f "$EMPTY_STREAMS/a.rtplog" && -f "$EMPTY_STREAMS/a.idx" ]] \
  || { echo "FAIL [fixture]: 0-packet fixture is missing its stream files" >&2; exit 1; }
[[ "$(stat -c '%s' "$EMPTY_STREAMS/a.idx")" -eq 0 ]] \
  || { echo "FAIL [fixture]: 0-packet fixture's idx must be 0 bytes (the ticket's case)" >&2; exit 1; }
(( $(stat -c '%s' "$EMPTY_STREAMS/a.idx") % 16 == 0 )) \
  || { echo "FAIL [fixture]: 0-byte idx must satisfy the old size%16 check" >&2; exit 1; }
cmp -s <(printf 'RTPL0\0\0\0') <(head -c 8 "$EMPTY_STREAMS/a.rtplog") \
  || { echo "FAIL [fixture]: 0-packet fixture must carry valid magic" >&2; exit 1; }
rg -q '"type"[[:space:]]*:[[:space:]]*"session_started"' "$EMPTY_DIR/sessions/s1/events.ndjson" \
  || { echo "FAIL [fixture]: fixture must carry a session_started event" >&2; exit 1; }
echo "  ok (valid) fixture is structurally sound: valid magic, idx size%16==0, sidecars present"
echo "             => a red verdict below can only mean 'no media', not 'malformed'"

# --- the ticket's case: 0 packets, 0-byte idx, valid headers ------------------
expect_red "0-packet capture (0-byte idx, header-only rtplog) => red" \
  "$EMPTY_REPORT" "no captured media"

# --- RTCP-only: passes an idx-record floor, still zero media ------------------
#
# 40 receiver reports => 40 idx entries => `idx_records > 0` (the ticket's own
# proposed fix) would go GREEN here despite zero media. Kind-aware counting is
# what makes this red, so it is pinned.
RTCP_REPORT="$(build_fixture "$TMP_ROOT/rtcp-only" "b:0:40")"
expect_red "RTCP-only stream (rtp=0, rtcp=40) => red" \
  "$RTCP_REPORT" "no captured media"

# --- the rejoin scenario (shaping.md Decision 5) ------------------------------
#
# Video announced but silent: the recorder opens an rtplog at OnTrack
# (recorder.go:1636-1637) before any packet arrives, and the RTCP reader
# goroutine starts immediately -- so a track that never receives media still
# leaves rtp=0/rtcp>0 on disk. ci-e2e-rejoin.sh:147-149 blesses a missing VIDEO
# TRACK in the MKV ("[WARN] ... continuing"), and this case makes the harder
# claim explicit: a *captured stream with no media* is red even there, because
# that is D-454's fingerprint rather than a benign video-less run. Pinned here
# so the policy is committed, not discovered in CI.
REJOIN_REPORT="$(build_fixture "$TMP_ROOT/rejoin" "audio:1600:12" "video:0:12")"
expect_red "video-less-but-announced stream (rtp=0, rtcp=12) alongside healthy audio => red" \
  "$REJOIN_REPORT" "video: rtp=0"

# --- desynced index -----------------------------------------------------------
DESYNC_REPORT="$(build_fixture "$TMP_ROOT/desync" "d:5:0:99")"
expect_red "index desynced from log (5 records, 99 idx entries) => red" \
  "$DESYNC_REPORT" "index desynced"

# --- corrupt magic (the check the old guard did make -- keep it working) ------
BADMAGIC_DIR="$TMP_ROOT/badmagic"
BADMAGIC_REPORT="$(build_fixture "$BADMAGIC_DIR" "c:10:0")"
printf 'XXXXX\0\0\0' | dd of="$BADMAGIC_DIR/sessions/s1/streams/c.rtplog" bs=8 count=1 conv=notrunc status=none
expect_red "corrupt stream magic => red" \
  "$BADMAGIC_REPORT" "invalid stream header magic"

# --- truncated record framing -------------------------------------------------
TRUNC_DIR="$TMP_ROOT/truncated"
TRUNC_REPORT="$(build_fixture "$TRUNC_DIR" "t:10:0")"
python3 - "$TRUNC_DIR/sessions/s1/streams/t.rtplog" <<'PY'
import os
import sys
# Lop 40 bytes off the tail: the final record's payload is now short, which the
# walker must notice rather than silently under-count.
path = sys.argv[1]
os.truncate(path, os.path.getsize(path) - 40)
PY
expect_red "truncated record payload => red" \
  "$TRUNC_REPORT" "truncated record"

# --- MIN_RTP_PACKETS floor is real, not decorative (R8) -----------------------
FLOOR_REPORT="$(build_fixture "$TMP_ROOT/floor" "e:5:0")"
CASES_RUN=$((CASES_RUN + 1))
if MIN_RTP_PACKETS=10 run_verifier "$FLOOR_REPORT"; then
  echo "FAIL [floor]: MIN_RTP_PACKETS=10 accepted a 5-packet stream" >&2
  echo "$OUTPUT" >&2
  exit 1
fi
echo "  ok (red)   MIN_RTP_PACKETS=10 vs a 5-packet stream => red"

# --- ...and the opt-down works, so the knob is exercised BOTH ways (R8) -------
#
# No call site sets MIN_RTP_PACKETS=0 today (deliberately -- see the verifier's
# header comment). It is proven here so the escape hatch is known to work if a
# leg ever earns one, rather than being dead code discovered mid-incident.
CASES_RUN=$((CASES_RUN + 1))
if ! MIN_RTP_PACKETS=0 run_verifier "$EMPTY_REPORT"; then
  echo "FAIL [opt-down]: MIN_RTP_PACKETS=0 should disable the floor" >&2
  echo "$OUTPUT" >&2
  exit 1
fi
echo "  ok (green) MIN_RTP_PACKETS=0 opts the floor down on the 0-packet fixture"

# --- the green half: the guard must not false-fail healthy captures -----------
HEALTHY_REPORT="$(build_fixture "$TMP_ROOT/healthy" "audio:1600:40" "video:900:40")"
expect_green "healthy audio+video capture => green" "$HEALTHY_REPORT"

# R3: keying the floor on OBSERVED streams (not a hardcoded track list) is what
# lets an audio-only caller (record-three-songs.sh) stay green with nothing to
# declare. A "must have video" rule would false-fail it.
AUDIO_ONLY_REPORT="$(build_fixture "$TMP_ROOT/audio-only" "audio:1600:40")"
expect_green "audio-only capture (no video stream at all) => green" "$AUDIO_ONLY_REPORT"

# A stream may legitimately carry RTCP alongside media; only rtp==0 is fatal.
MINIMAL_REPORT="$(build_fixture "$TMP_ROOT/minimal" "audio:1:500")"
expect_green "single RTP packet amid heavy RTCP => green (floor is 1, kind-aware)" "$MINIMAL_REPORT"

echo "PASS: verify-session-artifact.sh fails captures with no media and passes healthy ones (${CASES_RUN} cases)"
