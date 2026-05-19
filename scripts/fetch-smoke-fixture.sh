#!/usr/bin/env bash
# fetch-smoke-fixture.sh — one-shot regenerator for harness/media/parakeet-smoke.mkv
#
# Picks the first LibriSpeech test-clean clip with duration ≥ MIN_SECONDS from
# the Hugging Face datasets-server rows API, transcodes to Matroska/FLAC 16k mono,
# and writes harness/media/parakeet-smoke.mkv + refreshes harness/media/NOTICE.
#
# Why LibriSpeech (not Common Voice as the plan originally specified):
# Mozilla Common Voice moved to gated distribution (Mozilla Data Collective) in
# Oct 2025. LibriSpeech is the closest freely-accessible substitute. See NOTICE.
#
# Usage:
#   ./scripts/fetch-smoke-fixture.sh
#
# Requires: curl, python3, ffmpeg, ffprobe.

set -euo pipefail

MIN_SECONDS="${MIN_SECONDS:-10}"
BATCH_SIZE="${BATCH_SIZE:-30}"
OUT_DIR="$(git rev-parse --show-toplevel)/harness/media"
OUT_FILE="${OUT_DIR}/parakeet-smoke.mkv"
NOTICE_FILE="${OUT_DIR}/NOTICE"
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

echo "Fetching ${BATCH_SIZE} LibriSpeech test-clean rows..."
curl -fsSL \
  "https://datasets-server.huggingface.co/rows?dataset=openslr%2Flibrispeech_asr&config=clean&split=test&offset=0&length=${BATCH_SIZE}" \
  > "${TMPDIR}/rows.json"

python3 - "$TMPDIR" "$MIN_SECONDS" <<'PY'
import json, os, subprocess, sys, urllib.request

tmp = sys.argv[1]
min_dur = float(sys.argv[2])

with open(os.path.join(tmp, 'rows.json')) as f:
    rows = json.load(f).get('rows', [])

for i, r in enumerate(rows):
    row = r.get('row', {})
    audio = row.get('audio')
    if isinstance(audio, list) and audio:
        audio = audio[0]
    if not isinstance(audio, dict):
        continue
    src = audio.get('src')
    if not src:
        continue
    dst = os.path.join(tmp, f'clip_{i:02d}.flac')
    try:
        urllib.request.urlretrieve(src, dst)
    except Exception as e:
        print(f'  [{i}] fetch failed: {e}', file=sys.stderr)
        continue
    try:
        out = subprocess.check_output(
            ['ffprobe','-v','error','-show_entries','format=duration',
             '-of','default=noprint_wrappers=1:nokey=1', dst]).decode().strip()
        dur = float(out)
    except Exception as e:
        print(f'  [{i}] probe failed: {e}', file=sys.stderr)
        continue
    if dur >= min_dur:
        meta = {
            'index': i,
            'path':  dst,
            'duration': dur,
            'id':       row.get('id'),
            'speaker':  row.get('speaker_id'),
            'chapter':  row.get('chapter_id'),
            'text':     row.get('text', ''),
        }
        with open(os.path.join(tmp, 'meta.json'), 'w') as mf:
            json.dump(meta, mf, indent=2)
        print(f'SELECTED [{i}] dur={dur:.2f}s id={meta["id"]}')
        break
else:
    sys.exit('no clip ≥ {} seconds in first {} rows; raise BATCH_SIZE'.format(min_dur, len(rows)))
PY

META="${TMPDIR}/meta.json"
SRC=$(python3 -c "import json; print(json.load(open('${META}'))['path'])")
DUR=$(python3 -c "import json; print(f\"{json.load(open('${META}'))['duration']:.3f}\")")
CLIP_ID=$(python3 -c "import json; print(json.load(open('${META}'))['id'])")
SPEAKER=$(python3 -c "import json; print(json.load(open('${META}'))['speaker'])")
CHAPTER=$(python3 -c "import json; print(json.load(open('${META}'))['chapter'])")
TEXT=$(python3 -c "import json; print(json.load(open('${META}'))['text'])")

mkdir -p "${OUT_DIR}"
echo "Encoding to ${OUT_FILE} (Matroska/FLAC 16k mono)..."
ffmpeg -y -loglevel error -i "${SRC}" -c:a flac -ar 16000 -ac 1 "${OUT_FILE}"

# Refresh NOTICE with the new clip's exact metadata.
cat > "${NOTICE_FILE}" <<EOF
Test fixture attribution
========================

File: parakeet-smoke.mkv
  Duration: ${DUR}s
  Format:   Matroska container, FLAC audio, 16 kHz mono
  Source:   LibriSpeech ASR corpus (test-clean split, id ${CLIP_ID})
  Speaker:  ${SPEAKER}  Chapter: ${CHAPTER}
  Text:     "${TEXT}"
  License:  CC-BY 4.0
            https://creativecommons.org/licenses/by/4.0/
  Origin:   Vassil Panayotov, Daniel Povey et al.,
            "LibriSpeech: an ASR corpus based on public domain audio books",
            ICASSP 2015. https://www.openslr.org/12/

Why LibriSpeech instead of Mozilla Common Voice:
  The eng-review for bundled-parakeet-images (D5) chose Mozilla Common Voice.
  As of October 2025, Mozilla Common Voice is no longer freely downloadable —
  Mozilla moved distribution to the Mozilla Data Collective (gated registration).
  LibriSpeech is the closest substitute that remains freely accessible without
  account creation: real spoken English, permissive license (CC-BY 4.0 vs CC0),
  short individual clips. Attribution is required under CC-BY; this NOTICE file
  satisfies the requirement.

Regeneration:
  scripts/fetch-smoke-fixture.sh re-derives this file from the LibriSpeech
  test-clean dataset on Hugging Face. Run it if the binary is ever lost or
  needs to be replaced with a different sample.
EOF

echo "Done."
echo "  ${OUT_FILE}"
echo "  ${NOTICE_FILE}"
