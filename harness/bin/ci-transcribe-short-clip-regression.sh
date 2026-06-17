#!/usr/bin/env bash
# Targeted regression test for the Parakeet TDT v3 short-clip transcription
# bug. Clean speech clips of ~6–9s without trailing silence return zero
# tokens — the decoder treats the abrupt end as mid-utterance and emits
# nothing. cassini-go-recorder/internal/transcribe pads short chunks with
# synthetic silence to give the decoder an utterance boundary; this
# script asserts that the fix is still in place.
#
# The script slices a known TTS source file (kokoro-generated speech) at
# 5/6/7/8/9/10/15 seconds, runs `cassini build` against each slice
# inside the supplied IMAGE_REF, and asserts a minimum word count per
# slice. The thresholds match measured behaviour with the fix
# (cassini-go-recorder/internal/transcribe/stt.go transcribeSegment
# pad-tail logic):
#
#   length   words (no fix)   words (with fix)   asserts ≥
#   5s       1                2                  1
#   6s       0                3                  1
#   7s       0                0–8 (model-flaky)  0   (informational only)
#   8s       0                14                 5
#   9s       0                14                 5
#   10s+     15               15                 5
#
# Without the fix the 6s/8s/9s rows would print "0" and the script
# would exit non-zero. The 7s row is informational because the model
# behaviour at that length is inconsistent across sherpa-onnx builds.
#
# Invocation:
#   IMAGE_REF=cassini-exapp:e2e-v3-cpu-gpu ./harness/bin/ci-transcribe-short-clip-regression.sh

set -euo pipefail

: "${IMAGE_REF:?IMAGE_REF must be set, e.g. cassini-exapp:e2e-v3-cpu-gpu}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SOURCE_OGG="${SOURCE_OGG:-$REPO_ROOT/harness/media/processed/showcase-lantern-festival-v1/mira.ogg}"
WORK_DIR="${WORK_DIR:-/tmp/cassini-short-clip-$$}"
CONTAINER_NAME="cassini-short-clip-$$"

if [[ ! -s "$SOURCE_OGG" ]]; then
  echo "FAIL: SOURCE_OGG missing or empty: $SOURCE_OGG" >&2
  echo "  Run: harness/bin/prepare-synthetic-meeting.sh --scenario harness/scenarios/showcase-lantern-festival.v1.json --output-dir $(dirname "$SOURCE_OGG")" >&2
  exit 1
fi

# The fixture is vendored via git-lfs; a checkout without LFS content leaves
# a ~132-byte text pointer that passes the -s check above but is not audio.
if head -c 100 "$SOURCE_OGG" | grep -q 'git-lfs'; then
  echo "FAIL: SOURCE_OGG is still a git-lfs pointer, not audio: $SOURCE_OGG" >&2
  echo "  Run: git lfs pull --include=\"harness/media/processed/showcase-lantern-festival-v1/mira.ogg\"" >&2
  exit 1
fi

# Slicing happens on the host (the container only remuxes + transcribes).
if ! command -v ffmpeg >/dev/null 2>&1; then
  echo "FAIL: host ffmpeg is required to slice $SOURCE_OGG (apt-get install ffmpeg)" >&2
  exit 1
fi

mkdir -p "$WORK_DIR"

cleanup() {
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

log() { printf '[short-clip] %s\n' "$*"; }

log "starting container $CONTAINER_NAME from $IMAGE_REF"
docker run -d --rm \
  --name "$CONTAINER_NAME" \
  --entrypoint /bin/sh \
  "$IMAGE_REF" -c 'tail -f /dev/null' >/dev/null

# Cases: length_seconds:min_words. Words is the lower bound at which the
# test passes; -1 means informational (do not gate).
declare -a CASES=(
  "5:1"
  "6:1"
  "7:-1"
  "8:5"
  "9:5"
  "10:5"
  "15:5"
)

pass=0
fail=0
for case in "${CASES[@]}"; do
  dur="${case%%:*}"
  min_words="${case##*:}"

  ffmpeg -hide_banner -nostdin -y -ss 0 -t "$dur" -i "$SOURCE_OGG" \
    -c copy "$WORK_DIR/clip-${dur}s.ogg" >/dev/null 2>&1

  docker cp "$WORK_DIR/clip-${dur}s.ogg" \
    "$CONTAINER_NAME:/tmp/clip.ogg" >/dev/null 2>&1

  docker exec "$CONTAINER_NAME" sh -c \
    'rm -rf /tmp/out; ffmpeg -hide_banner -nostdin -y -i /tmp/clip.ogg -c copy /tmp/clip.mkv 2>/dev/null' >/dev/null

  build_out=$(docker exec "$CONTAINER_NAME" cassini build /tmp/clip.mkv --out /tmp/out 2>&1)
  words=$(printf '%s' "$build_out" | sed -n 's/.*Speaker 1: \([0-9][0-9]*\) words.*/\1/p' | head -1)
  words="${words:-0}"

  if [[ "$min_words" == "-1" ]]; then
    log "INFO ${dur}s -> ${words} words (informational, not gated)"
  elif (( words >= min_words )); then
    log "PASS ${dur}s -> ${words} words (>= ${min_words})"
    pass=$((pass + 1))
  else
    log "FAIL ${dur}s -> ${words} words (< ${min_words})"
    fail=$((fail + 1))
  fi
done

log "result: ${pass} pass / ${fail} fail (informational rows excluded)"
if (( fail > 0 )); then
  log "short-clip transcription regression detected — check the"
  log "  decoderTailPadMinSeconds / pad length in"
  log "  cassini-go-recorder/internal/transcribe/stt.go:transcribeSegment"
  exit 1
fi
