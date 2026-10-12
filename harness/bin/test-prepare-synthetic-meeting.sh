#!/usr/bin/env bash
# Offline contract test for prepare-synthetic-meeting.py's self-overlap slide.
#
# A scenario can schedule a turn before the same speaker's previous one has
# finished; the generator slides such a turn to the previous turn's end instead
# of aborting. This suite proves the slide cannot write false ground truth:
#
#   - the manifest and reference.txt record the SLID start, not the scenario's
#     original start_seconds, so reference timestamps agree with the audio;
#   - the declared duration_seconds is recomputed AFTER sliding, and every
#     participant's WAV has exactly that common extent;
#   - the mixed audio really is at the slid position (signal where the slid
#     turn lands, silence in the trailing pad);
#   - a well-formed scenario comes through with its starts untouched and no
#     slide note;
#   - `--scenario /dev/stdin` fed from a pipe works — the invocation used to
#     probe this script in review; it used to fail because the path was
#     resolve()d into a dead /proc/<pid>/fd/pipe:[...] target before reading;
#   - a run that fails partway leaves no stale manifest or reference behind;
#   - cached output is reused only for the backend that produced it;
#   - every committed fixture manifest under harness/media/processed was
#     generated with kokoro, not the mock backend's tones, and every committed
#     media file is the one that manifest's run produced (sha256).
#
# Coverage given up: ffmpeg and ffprobe are STUBS, so rendering the mp4/ivf/ogg
# assets is not exercised — only the schedule, the WAV mix, and the manifest/
# reference contract. Only the mock TTS backend runs: no kokoro, no model
# download, no network, no Docker.

set -euo pipefail

fail() { echo "FAIL: $*" >&2; exit 1; }

command -v python3 >/dev/null 2>&1 || fail "python3 is required"
python3 -c 'import numpy' 2>/dev/null \
  || fail "python3 numpy is required (apt: python3-numpy; see harness/requirements-synthetic.txt)"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GEN="$SCRIPT_DIR/prepare-synthetic-meeting.py"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

# Stub ffmpeg/ffprobe: the generator requires both on PATH and shells out to
# render mp4/ivf/ogg from the WAVs it mixes itself. The stub creates the
# output file (ffmpeg's last argument) so the run completes; nothing here
# asserts on rendered assets.
mkdir -p "$TMP_DIR/stub"
cat > "$TMP_DIR/stub/ffmpeg" <<'STUB'
#!/usr/bin/env bash
out=""
for arg in "$@"; do out="$arg"; done
printf 'stub' > "$out"
STUB
cat > "$TMP_DIR/stub/ffprobe" <<'STUB'
#!/usr/bin/env bash
exit 0
STUB
chmod +x "$TMP_DIR/stub/ffmpeg" "$TMP_DIR/stub/ffprobe"
export PATH="$TMP_DIR/stub:$PATH"

# ana's second turn starts before her first one ends (10 words at the mock
# backend's 0.26 s/word = 2.6 s from 1.0 s; the scenario says 2.0 s), so it
# must slide to 3.6 s. ben never overlaps. duration_seconds is deliberately
# too short: the slid turn pushes the real extent past it, so the declared
# duration must be recomputed after sliding.
cat > "$TMP_DIR/overlap.json" <<'JSON'
{
  "scenario_id": "test-self-overlap",
  "title": "Self Overlap",
  "duration_seconds": 4,
  "participants": [
    {"id": "ana", "display_name": "Ana", "voice": "af_heart"},
    {"id": "ben", "display_name": "Ben", "voice": "am_adam"}
  ],
  "turns": [
    {"speaker": "ana", "start_seconds": 1.0, "text": "one two three four five six seven eight nine ten"},
    {"speaker": "ben", "start_seconds": 0.5, "text": "a quick clean interjection"},
    {"speaker": "ana", "start_seconds": 2.0, "text": "this starts before my previous turn ended"}
  ]
}
JSON

OUT_DIR="$TMP_DIR/overlap-out"
STDERR_LOG="$TMP_DIR/overlap.stderr"
python3 "$GEN" --scenario "$TMP_DIR/overlap.json" --output-dir "$OUT_DIR" \
  --backend mock --force >/dev/null 2>"$STDERR_LOG" \
  || fail "generator failed on the self-overlap scenario: $(cat "$STDERR_LOG")"

grep -q "sliding it" "$STDERR_LOG" \
  || fail "generator did not report the slide on stderr"

python3 - "$OUT_DIR" <<'PY' || exit 1
import json
import math
import struct
import sys
import wave
from pathlib import Path

out_dir = Path(sys.argv[1])
manifest = json.loads((out_dir / "manifest.json").read_text(encoding="utf-8"))
sample_rate = 24_000


def check(condition, message):
    if not condition:
        raise SystemExit(f"FAIL: {message}")


turns = manifest["turns"]
ana = sorted(
    (t for t in turns if t["speaker"] == "ana"), key=lambda t: t["start_seconds"]
)
ben = [t for t in turns if t["speaker"] == "ben"]
check(len(ana) == 2 and len(ben) == 1, f"unexpected turn counts: {turns}")
first, second = ana

# Ground truth records the SLID start — the previous turn's end — not the
# scenario's written 2.0s.
previous_end = first["start_seconds"] + first["actual_duration_seconds"]
check(
    second["start_seconds"] != 2.0,
    "slid ana turn still records the scenario's start_seconds",
)
check(
    abs(second["start_seconds"] - previous_end) <= 0.001,
    f"slid start {second['start_seconds']} != previous turn end {previous_end}",
)
check(ben[0]["start_seconds"] == 0.5, f"non-overlapping turn moved: {ben[0]}")

# The declared duration is recomputed from the slid schedule: furthest
# scheduled end plus the 0.6s pad, past the scenario's written 4s.
declared = manifest["duration_seconds"]
expected = max(
    4.0, *(t["start_seconds"] + t["actual_duration_seconds"] + 0.6 for t in turns)
)
check(declared > 4.0, f"declared duration not extended past the scenario's: {declared}")
check(
    abs(declared - expected) <= 0.002,
    f"declared duration {declared} != schedule extent {expected}",
)

# Every participant's WAV shares that one declared extent.
frames = {}
for participant in manifest["participants"]:
    wav_path = out_dir / participant["paths"]["wav_source"]
    with wave.open(str(wav_path), "rb") as handle:
        check(handle.getframerate() == sample_rate, "unexpected WAV sample rate")
        frames[participant["id"]] = handle.getnframes()
check(len(set(frames.values())) == 1, f"track extents differ: {frames}")
audio_seconds = next(iter(frames.values())) / sample_rate
check(
    abs(audio_seconds - declared) <= 0.002,
    f"audio extent {audio_seconds}s != declared duration {declared}s",
)

# The audio itself moved: signal at the slid position, silence before the
# first turn and in the trailing pad.
with wave.open(str(out_dir / "_work" / "ana.wav"), "rb") as handle:
    pcm = handle.readframes(handle.getnframes())
samples = struct.unpack(f"<{len(pcm) // 2}h", pcm)


def rms(begin_s, end_s):
    window = samples[int(begin_s * sample_rate) : int(end_s * sample_rate)]
    return math.sqrt(sum(v * v for v in window) / max(1, len(window))) / 32767.0


slid_mid = second["start_seconds"] + second["actual_duration_seconds"] / 2
check(rms(slid_mid - 0.2, slid_mid + 0.2) > 0.01, "no audio at the slid position")
check(rms(declared - 0.5, declared) < 0.001, "trailing pad is not silent")
check(rms(0.0, first["start_seconds"] - 0.1) < 0.001, "audio before the first turn")

# reference.txt carries the slid timestamp, not the scenario's.
reference = (out_dir / "reference.txt").read_text(encoding="utf-8")
needle = f"[{second['start_seconds']:.1f}s] Ana:"
check(needle in reference, f"reference.txt lacks {needle!r}:\n{reference}")
check("[2.0s] Ana:" not in reference, "reference.txt still shows the scenario start")

print("slide contract holds")
PY

# The review probe: the scenario arriving on /dev/stdin from a real pipe.
PROBE_DIR="$TMP_DIR/probe-out"
overlap_json="$(< "$TMP_DIR/overlap.json")"
printf '%s' "$overlap_json" \
  | python3 "$GEN" --scenario /dev/stdin --output-dir "$PROBE_DIR" \
      --backend mock --force >/dev/null 2>&1 \
  || fail "--scenario /dev/stdin from a pipe failed"
[[ -f "$PROBE_DIR/manifest.json" ]] || fail "piped /dev/stdin run wrote no manifest"

# A well-formed scenario comes through untouched: no slide note, the
# scenario's own starts and duration recorded verbatim.
cat > "$TMP_DIR/clean.json" <<'JSON'
{
  "scenario_id": "test-clean",
  "title": "Clean",
  "duration_seconds": 12,
  "participants": [
    {"id": "ana", "display_name": "Ana", "voice": "af_heart"},
    {"id": "ben", "display_name": "Ben", "voice": "am_adam"}
  ],
  "turns": [
    {"speaker": "ana", "start_seconds": 0.5, "text": "hello everyone and welcome to the meeting"},
    {"speaker": "ben", "start_seconds": 4.0, "text": "thanks glad to be here today"},
    {"speaker": "ana", "start_seconds": 7.5, "text": "let us get started with the agenda"}
  ]
}
JSON

CLEAN_DIR="$TMP_DIR/clean-out"
CLEAN_LOG="$TMP_DIR/clean.stderr"
python3 "$GEN" --scenario "$TMP_DIR/clean.json" --output-dir "$CLEAN_DIR" \
  --backend mock --force >/dev/null 2>"$CLEAN_LOG" \
  || fail "generator failed on the clean scenario: $(cat "$CLEAN_LOG")"
if grep -q "sliding it" "$CLEAN_LOG"; then
  fail "generator slid a turn in a scenario with no overlap"
fi

python3 - "$CLEAN_DIR" <<'PY' || exit 1
import json
import sys
from pathlib import Path

manifest = json.loads(
    (Path(sys.argv[1]) / "manifest.json").read_text(encoding="utf-8")
)
starts = sorted(t["start_seconds"] for t in manifest["turns"])
if starts != [0.5, 4.0, 7.5]:
    raise SystemExit(f"FAIL: clean scenario starts changed: {starts}")
if manifest["duration_seconds"] != 12.0:
    raise SystemExit(
        f"FAIL: clean scenario duration changed: {manifest['duration_seconds']}"
    )
print("clean scenario untouched")
PY

# A run that stops partway must not leave the previous run's manifest behind
# to describe media it no longer matches. Rerun the clean scenario with an
# ffmpeg that fails on the second participant: the first participant's media
# is already replaced by then, so the old manifest has to be gone.
mkdir -p "$TMP_DIR/failing-stub"
cat > "$TMP_DIR/failing-stub/ffmpeg" <<'STUB'
#!/usr/bin/env bash
out=""
for arg in "$@"; do out="$arg"; done
case "$out" in
  */ben.*) echo "stub ffmpeg: refusing $out" >&2; exit 1 ;;
esac
printf 'stub' > "$out"
STUB
chmod +x "$TMP_DIR/failing-stub/ffmpeg"
[[ -f "$CLEAN_DIR/manifest.json" ]] || fail "clean run left no manifest to go stale"
if PATH="$TMP_DIR/failing-stub:$PATH" python3 "$GEN" --scenario "$TMP_DIR/clean.json" \
    --output-dir "$CLEAN_DIR" --backend mock --force >/dev/null 2>&1; then
  fail "generator succeeded although ffmpeg failed"
fi
[[ ! -e "$CLEAN_DIR/manifest.json" ]] \
  || fail "a partial run left the previous manifest describing replaced media"
[[ ! -e "$CLEAN_DIR/reference.txt" ]] \
  || fail "a partial run left the previous reference.txt behind"

# Reuse of earlier output: only a complete run from the SAME backend counts,
# and the manifest's relative paths resolve against its own directory, not
# the caller's cwd.
python3 - "$GEN" "$PROBE_DIR" <<'PY' || exit 1
import importlib.util
import os
import sys
from pathlib import Path

spec = importlib.util.spec_from_file_location("generator", sys.argv[1])
generator = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = generator  # dataclasses look their module up here
spec.loader.exec_module(generator)
manifest_path = Path(sys.argv[2]) / "manifest.json"

os.chdir("/")
if not generator.cached_manifest_is_complete(manifest_path, "mock"):
    raise SystemExit("FAIL: complete mock output not reusable from another cwd")
if generator.cached_manifest_is_complete(manifest_path, "kokoro"):
    raise SystemExit("FAIL: mock-backend output accepted for a kokoro request")
print("cache reuse is backend- and cwd-safe")
PY

# Media and manifest of a fixture must come from one run. The generator records
# each streamed file's sha256; check a fresh run's manifest against its files.
python3 - "$PROBE_DIR" <<'PY' || exit 1
import hashlib
import json
import sys
from pathlib import Path

out = Path(sys.argv[1])
manifest = json.loads((out / "manifest.json").read_text(encoding="utf-8"))
for participant in manifest["participants"]:
    hashes = participant.get("sha256") or {}
    if set(hashes) != {"video_ivf", "audio_ogg"}:
        raise SystemExit(f"FAIL: {participant['id']} records hashes for {sorted(hashes)}")
    for key, recorded in hashes.items():
        actual = hashlib.sha256((out / participant["paths"][key]).read_bytes()).hexdigest()
        if actual != recorded:
            raise SystemExit(f"FAIL: {participant['id']} {key} hash does not match its file")
print("manifest records the streamed files' hashes")
PY

# The committed fixtures are what the harness streams and transcribes; tones
# from the mock backend there transcribe to nothing (four of six showcase
# voices did). Every committed fixture must be real speech, and every committed
# media file must be the one its manifest recorded: a manifest restored with
# git after a run that stopped partway would otherwise vouch for whatever media
# the directory holds. Without LFS the media are pointer files, whose oid is
# the content's sha256, so this runs on a plain checkout too. Only tracked
# files count: locally generated fixtures are not committed and not checked.
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
python3 - "$REPO_ROOT" <<'PY' || exit 1
import hashlib
import json
import subprocess
import sys
from collections import defaultdict
from pathlib import Path

root = Path(sys.argv[1])
tracked = subprocess.run(
    ["git", "-C", str(root), "ls-files", "--", "harness/media/processed"],
    check=True, capture_output=True, text=True,
).stdout.split()
by_dir = defaultdict(set)
for rel in tracked:
    path = Path(rel)
    if len(path.parts) == 5:  # harness/media/processed/<fixture>/<file>
        by_dir[path.parent].add(path.name)
fixtures = sorted(d for d, names in by_dir.items() if "manifest.json" in names)
if not fixtures:
    raise SystemExit("FAIL: no committed fixture manifests found")


def content_sha256(path: Path) -> str:
    data = path.read_bytes()
    if data.startswith(b"version https://git-lfs.github.com/spec/"):
        for line in data.decode("utf-8").splitlines():
            if line.startswith("oid sha256:"):
                return line.split(":", 1)[1].strip()
    return hashlib.sha256(data).hexdigest()


for fixture in fixtures:
    manifest = json.loads((root / fixture / "manifest.json").read_text(encoding="utf-8"))
    backend = manifest.get("backend")
    if backend != "kokoro":
        raise SystemExit(
            f"FAIL: {fixture}/manifest.json was generated with backend {backend!r}; "
            "committed fixtures must be real speech (--backend kokoro)"
        )
    recorded = {}
    for participant in manifest["participants"]:
        for key, digest in (participant.get("sha256") or {}).items():
            recorded[participant["paths"][key]] = digest
    for name in sorted(by_dir[fixture] - {"manifest.json", "reference.txt"}):
        if name not in recorded:
            raise SystemExit(f"FAIL: {fixture}/{name} is committed but its manifest records no hash for it")
        if content_sha256(root / fixture / name) != recorded[name]:
            raise SystemExit(
                f"FAIL: {fixture}/{name} is not the file its manifest was generated with; "
                "regenerate the whole fixture"
            )
print(f"{len(fixtures)} committed fixture(s) use kokoro, media matching their manifest")
PY

echo "PASS: prepare-synthetic-meeting.py slide keeps ground truth honest"
