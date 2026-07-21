#!/usr/bin/env bash
#
# Control for verify-av-drift.sh's pair requirement (D-510).
#
# The drift check had two ways to report success without measuring anything:
#
#   1. No audio/video pair found  -> log "skipped"; exit 0.
#   2. Pairs found, but every one shorter than --min-elapsed -> each `continue`s,
#      zero drift comparisons happen, and the script falls through to exit 0.
#
# Skipping is a legitimate ACT -- drift is meaningless without both clocks --
# but whether it is an acceptable OUTCOME is the caller's call. --require-pairs
# lets a run declare "I expect both tracks", and it is asserted against pairs
# actually COMPARED rather than pairs found, which is what closes hole 2 as
# well as hole 1. This control pins both, and pins that the opt-down still works
# for the one leg (rejoin) that legitimately runs video-less.
#
# Hermetic by design: verify-av-drift.sh shells out to ffprobe, which is NOT on
# the lint runner, and building real MKVs would mean apt-installing ffmpeg and
# encoding fixtures. Instead a stub ffprobe on PATH replays canned metadata and
# timestamps, so the REAL script's real logic (pairing, counting, assertions) is
# exercised with no media tooling at all. Run:
#   ./harness/bin/test-av-drift-pair-requirement.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VERIFIER="$SCRIPT_DIR/verify-av-drift.sh"

[[ -x "$VERIFIER" ]] || { echo "FAIL: verifier not found/executable: $VERIFIER" >&2; exit 1; }
for cmd in python3 awk; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "FAIL: $cmd is required" >&2; exit 1; }
done

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

CASES_RUN=0

# --- the stub ffprobe ---------------------------------------------------------
#
# Mimics exactly the three invocations verify-av-drift.sh makes:
#   -show_streams -of json                              -> $SCENARIO/streams.json
#   -select_streams N -show_entries packet=pts_time     -> $SCENARIO/ts_N.txt
#   -select_streams N -show_entries frame=best_effort…  -> $SCENARIO/ts_N.txt
# Anything else is a hard error, so a future change to the caller's ffprobe
# usage surfaces here loudly instead of being silently mis-stubbed.
STUB_DIR="$TMP_ROOT/stub"
mkdir -p "$STUB_DIR"
cat > "$STUB_DIR/ffprobe" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
select_stream=""
mode=""
for ((i = 1; i <= $#; i++)); do
  case "${!i}" in
    -show_streams) mode="streams" ;;
    -select_streams) j=$((i + 1)); select_stream="${!j}" ;;
    -show_entries)
      j=$((i + 1))
      case "${!j}" in
        packet=pts_time) mode="timestamps" ;;
        frame=best_effort_timestamp_time) mode="timestamps" ;;
        *) echo "stub ffprobe: unexpected -show_entries ${!j}" >&2; exit 64 ;;
      esac
      ;;
  esac
done
case "$mode" in
  streams) cat "$SCENARIO/streams.json" ;;
  timestamps)
    if [[ -f "$SCENARIO/ts_${select_stream}.txt" ]]; then
      cat "$SCENARIO/ts_${select_stream}.txt"
    fi
    ;;
  *) echo "stub ffprobe: unrecognised invocation: $*" >&2; exit 64 ;;
esac
STUB
chmod +x "$STUB_DIR/ffprobe"

# build_scenario <name> <pair_spec>...
#
# Each pair spec is "<pid>:<elapsed_audio>:<elapsed_video>" and produces one
# LTID-tagged audio+video pair (p:<pid>:audio:0 / p:<pid>:video:0) whose
# timestamps span the given elapsed seconds. Echoes the scenario dir.
build_scenario() {
  local name="$1"; shift
  local dir="$TMP_ROOT/$name"
  mkdir -p "$dir"
  python3 - "$dir" "$@" <<'PY'
import json
import os
import sys

dir_ = sys.argv[1]
streams = []
index = 0

for spec in sys.argv[2:]:
    pid, audio_elapsed, video_elapsed = spec.split(":")
    for kind, elapsed in (("audio", float(audio_elapsed)), ("video", float(video_elapsed))):
        streams.append({
            "index": index,
            "codec_type": kind,
            "tags": {"LTID": f"p:{pid}:{kind}:0", "STREAM_ID": f"s-{pid}-{kind}"},
        })
        # Two timestamps spanning `elapsed`; the caller's awk takes first/last.
        with open(os.path.join(dir_, f"ts_{index}.txt"), "w", encoding="utf-8") as f:
            f.write("0.000000\n")
            f.write(f"{elapsed:.6f}\n")
        index += 1

with open(os.path.join(dir_, "streams.json"), "w", encoding="utf-8") as f:
    json.dump({"streams": streams}, f)

# verify-av-drift.sh requires --input to exist; the stub never reads it.
with open(os.path.join(dir_, "in.mkv"), "wb") as f:
    f.write(b"\x00" * 64)
PY
  echo "$dir"
}

# build_unpaired: streams exist but never both kinds under one LTID -> 0 pairs.
build_unpaired() {
  local dir="$TMP_ROOT/unpaired"
  mkdir -p "$dir"
  python3 - "$dir" <<'PY'
import json
import os
import sys

dir_ = sys.argv[1]
# Audio-only: no LTID has both an audio and a video stream, so the pairing
# step emits nothing -- the "no paired streams found" path.
streams = [{
    "index": 0,
    "codec_type": "audio",
    "tags": {"LTID": "p:1:audio:0", "STREAM_ID": "s-1-audio"},
}]
with open(os.path.join(dir_, "ts_0.txt"), "w", encoding="utf-8") as f:
    f.write("0.000000\n30.000000\n")
with open(os.path.join(dir_, "streams.json"), "w", encoding="utf-8") as f:
    json.dump({"streams": streams}, f)
with open(os.path.join(dir_, "in.mkv"), "wb") as f:
    f.write(b"\x00" * 64)
PY
  echo "$dir"
}

run_verifier() {
  local scenario="$1"; shift
  local rc=0
  OUTPUT="$(SCENARIO="$scenario" PATH="$STUB_DIR:$PATH" \
    "$VERIFIER" --input "$scenario/in.mkv" "$@" 2>&1)" || rc=$?
  return $rc
}

expect_red() {
  local name="$1" scenario="$2" want_msg="$3"; shift 3
  CASES_RUN=$((CASES_RUN + 1))
  if run_verifier "$scenario" "$@"; then
    echo "FAIL [$name]: drift check PASSED a run it must reject" >&2
    echo "--- output ---" >&2; echo "$OUTPUT" >&2
    exit 1
  fi
  if ! grep -q -- "$want_msg" <<<"$OUTPUT"; then
    echo "FAIL [$name]: failed, but not for the expected reason" >&2
    echo "  expected message containing: $want_msg" >&2
    echo "--- output ---" >&2; echo "$OUTPUT" >&2
    exit 1
  fi
  echo "  ok (red)   $name"
}

expect_green() {
  local name="$1" scenario="$2"; shift 2
  CASES_RUN=$((CASES_RUN + 1))
  if ! run_verifier "$scenario" "$@"; then
    echo "FAIL [$name]: drift check REJECTED a run it must accept" >&2
    echo "--- output ---" >&2; echo "$OUTPUT" >&2
    exit 1
  fi
  echo "  ok (green) $name"
}

echo "av drift pair requirement control"

# --- sanity: the stub actually drives the real script ------------------------
HEALTHY="$(build_scenario healthy "1:30.0:30.0")"
CASES_RUN=$((CASES_RUN + 1))
if ! run_verifier "$HEALTHY" --tolerance 0.80 --min-elapsed 15; then
  echo "FAIL [stub]: healthy scenario should pass; the stub or scenario is wrong" >&2
  echo "$OUTPUT" >&2
  exit 1
fi
grep -q "pairs_compared=1" <<<"$OUTPUT" \
  || { echo "FAIL [stub]: expected pairs_compared=1 in output" >&2; echo "$OUTPUT" >&2; exit 1; }
echo "  ok (green) healthy 30s pair compares and passes (stub drives the real script)"

# --- hole 1: no pair found ----------------------------------------------------
UNPAIRED="$(build_unpaired)"
expect_red "no A/V pair found, default --require-pairs=1 => red" \
  "$UNPAIRED" "demands at least 1" --tolerance 0.80 --min-elapsed 15

# --- hole 1, opted down: the rejoin leg's declared policy ---------------------
#
# ci-e2e-rejoin.sh passes --require-pairs 0 because it legitimately runs
# video-less. Pinned here so the opt-down cannot be silently dropped or broken.
expect_green "no A/V pair found, --require-pairs 0 (the rejoin opt-down) => green" \
  "$UNPAIRED" --tolerance 0.80 --min-elapsed 15 --require-pairs 0

# --- hole 2: pairs found, all too short, ZERO comparisons ---------------------
#
# The subtle one. A "pairs found > 0" assertion would go green here: the pair
# exists, it is just never measured. Only counting COMPARISONS catches it.
ALLSHORT="$(build_scenario allshort "1:3.0:3.0" "2:2.0:2.0")"
expect_red "pairs found but all shorter than min_elapsed (0 comparisons) => red" \
  "$ALLSHORT" "compared 0 pair(s)" --tolerance 0.80 --min-elapsed 15
CASES_RUN=$((CASES_RUN + 1))
grep -q "pairs found=2" <<<"$OUTPUT" \
  || { echo "FAIL: the all-short failure must report that pairs WERE found (found=2)" >&2; echo "$OUTPUT" >&2; exit 1; }
echo "  ok (msg)   ...and says pairs were found but not compared (found=2, compared=0)"

# --- a real drift failure still fails (the check's original job) --------------
DRIFTY="$(build_scenario drifty "1:30.0:34.0")"
expect_red "drift beyond tolerance => red" \
  "$DRIFTY" "av drift beyond tolerance" --tolerance 0.80 --min-elapsed 15

# --- mixed: one short pair, one good pair -> 1 comparison satisfies default ---
MIXED="$(build_scenario mixed "1:2.0:2.0" "2:30.0:30.0")"
expect_green "one short pair + one measured pair, --require-pairs=1 => green" \
  "$MIXED" --tolerance 0.80 --min-elapsed 15

# ...but demanding 2 comparisons catches that only 1 happened.
expect_red "same run, --require-pairs 2 => red (only 1 pair was actually compared)" \
  "$MIXED" "compared 1 pair(s)" --tolerance 0.80 --min-elapsed 15 --require-pairs 2

echo "PASS: verify-av-drift.sh requires pairs to be COMPARED, not merely found (${CASES_RUN} cases)"
