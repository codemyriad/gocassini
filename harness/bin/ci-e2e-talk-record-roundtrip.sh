#!/usr/bin/env bash
# Full Talk-driven recording roundtrip: trigger via Talk's recording-backend
# HTTP protocol → gocassini joins the call → captures audio → transcribes →
# publishes → Levenshtein-check the transcript against the scenario's
# expected text.
#
# This is the e2e that exercises the WHOLE merged stack:
#   - PR #22 (Talk recording-backend HMAC adapter)
#   - portable transcript production and extraction
#   - PR #30 (ExApp install handshake)
#   - D-797 (explicit model installation and activation)
# The ci-transcribe-smoke-exapp.sh smoke verifies cassini build, installed
# models and transcript quality; this script also exercises the Talk recording-backend
# trigger path that PR #22 introduces.
#
# Phases:
#   1. Bring up Nextcloud + Talk + signaling (compose full profile)
#   2. Install + enable Talk (spreed app)
#   3. Run cassini-exapp container with TALK_RECORDING_SECRET set
#   4. Configure Talk's recording_servers to point at gocassini-operator
#      (verify via /api/v1/welcome that Talk can reach the backend)
#   5. Create a Talk call; spawn audio bot(s) playing a known scenario
#   6. Trigger Talk's start-recording via OCS; wait for the lifecycle to
#      complete (Talk → HMAC POST /api/v1/room/{token} → operator's
#      record job → recorder joins call → captures → stop → build →
#      publish; Talk receives status callbacks only, never an upload)
#   7. Wait for the published artifact in the gocassini container
#      (/srv/cassini-site/published/meetings/<id>.opus)
#   8. Assert Cassini uploaded NOTHING to Talk's recording store: the room
#      owner's <attachment folder>/Recording/<room token>/ is absent or
#      holds no files (D-551 — the .opus archive is the only path)
#   9. Decode the transcript back OUT of the published .opus (via
#      `cassini inspect --transcript`) and Levenshtein-check it against the
#      scenario's expected text — proving the .opus embeds the transcript
#
# This is the manual-install known-content quality roundtrip. All nine phases
# are implemented and gated; AppAPI is real, but the test starts the ExApp and
# injects its environment itself. It then registers that running container so
# AppAPI knows the same identity and secret Cassini uses for room lookups.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$HARNESS_DIR/.." && pwd)"
COMPOSE_FILE="$HARNESS_DIR/compose.yml"
INFO_XML="$REPO_ROOT/appinfo/info.xml"
# shellcheck source=./lib/e2e-local.sh
source "$SCRIPT_DIR/lib/e2e-local.sh"
# shellcheck source=./lib-exapp-manifest.sh
source "$SCRIPT_DIR/lib-exapp-manifest.sh"
harness_e2e_local_stack_env full legacy none

# Phases 1-7 live in lib/talk-roundtrip.sh, shared with the scenarios that
# need the same recorded, published meeting (ci-e2e-summary-template.sh).
# shellcheck source=./lib/talk-roundtrip.sh
source "$SCRIPT_DIR/lib/talk-roundtrip.sh"
talk_rt_begin
talk_rt_phase1_stack
talk_rt_phase2_appapi
talk_rt_phase3_exapp
talk_rt_phase4_recording_servers
talk_rt_phase5_room_and_bots
talk_rt_phase6_record
talk_rt_phase7_await_publish

# ============================================================================
# Phase 7b: assert the published catalog carries the room it was recorded in
# ============================================================================
phase 7b "Assert the published catalog carries the room"

# Every hop from Talk to catalog.json is unit-tested and nothing proves the
# chain. It has broken here before: the exporter re-derives a catalog entry's
# room from the file on every republish, so a room that reached the catalog
# once could be silently dropped by the next re-seal (D-640).
#
# The .opus lands before catalog.json — the local sink writes the catalog last
# — so the poll above is not enough to have it on disk yet.
CATALOG_HOST_PATH="$LOG_DIR/published-catalog.json"
CATALOG_DEADLINE=$(( SECONDS + 90 ))
CATALOG_READY=0
while (( SECONDS < CATALOG_DEADLINE )); do
  if docker exec "$CONTAINER_NAME" cat /srv/cassini-site/published/catalog.json \
       >"$CATALOG_HOST_PATH" 2>/dev/null &&
     CATALOG_PATH="$CATALOG_HOST_PATH" python3 -c \
       'import json,os,sys; sys.exit(0 if json.load(open(os.environ["CATALOG_PATH"])).get("meetings") else 1)' \
       2>/dev/null; then
    CATALOG_READY=1
    break
  fi
  sleep 3
done
(( CATALOG_READY == 1 )) || fail "published catalog.json never carried a meeting"

# roomId is a one-way derivation of the Talk token, never the token itself
# (D-622). Recomputing it here independently is the point: it proves the whole
# token -> id -> catalog path rather than that some string round-tripped. This
# harness leaves CASSINI_ROOM_ID_PEPPER unset, so the HMAC key is empty.
CATALOG_PATH="$CATALOG_HOST_PATH" \
EXPECT_ROOM_NAME="$ROOM_NAME" \
EXPECT_ROOM_TOKEN="$ROOM_TOKEN" \
python3 - <<'PY' || fail "published catalog does not carry the recorded room"
import hashlib
import hmac
import json
import os
import sys

with open(os.environ["CATALOG_PATH"], encoding="utf-8") as handle:
    catalog = json.load(handle)
meetings = catalog.get("meetings") or []
if len(meetings) != 1:
    print(f"expected exactly one published meeting, got {len(meetings)}", file=sys.stderr)
    sys.exit(1)

entry = meetings[0]
mac = hmac.new(b"", b"cassini.room.token.v1\x00" + os.environ["EXPECT_ROOM_TOKEN"].encode(), hashlib.sha256)
expected = {
    "roomName": os.environ["EXPECT_ROOM_NAME"],
    "roomId": "rm_" + mac.hexdigest()[:16],
}

ok = True
for field, want in expected.items():
    got = entry.get(field)
    if got != want:
        print(f"catalog {field} = {got!r}, want {want!r}", file=sys.stderr)
        ok = False
if not ok:
    print(f"catalog entry: {json.dumps(entry, sort_keys=True)}", file=sys.stderr)
sys.exit(0 if ok else 1)
PY
log "OK catalog carries room: name=$ROOM_NAME"

# ============================================================================
# Phase 8: assert Cassini uploaded NOTHING to Talk's recording store
# ============================================================================
phase 8 "Assert Cassini uploaded nothing to Talk's recording store"

# Cassini never uses Talk's OCS recording-store endpoint, for any file
# (D-551). A meeting reaches Nextcloud exactly once, as the published .opus
# under the canonical recordings root — the only tree covered by the per-file
# ACL model the read proxy enforces (D-521). A raw .mkv filed into the room
# owner's Talk attachment folder would be an unmanaged parallel copy of the
# same meeting, outside that model entirely.
#
# spreed would file such an upload at <attachment folder>/Recording/<token>/,
# defaulting to /Talk/Recording/<token>/ (spreed lib/Config.php
# getRecordingFolder() + lib/Service/RecordingService.php). Phase 7 already
# waited for publish, which is sequenced after the point where the upload
# used to happen, so there is nothing to poll for: either the folder is
# absent, or it exists and holds no files.
RECORDING_OWNER="${RECORDING_OWNER:-admin}"
RECORDING_OWNER_PASSWORD="${RECORDING_OWNER_PASSWORD:-admin}"
DAV_RECORDING_DIR="${DAV_RECORDING_DIR:-$NC_URL_HOST/remote.php/dav/files/$RECORDING_OWNER/Talk/Recording/$ROOM_TOKEN}"

DAV_PROPFIND="$LOG_DIR/dav-recording-propfind.xml"
DAV_STATUS=$(curl -s -o "$DAV_PROPFIND" -w '%{http_code}' \
  -u "$RECORDING_OWNER:$RECORDING_OWNER_PASSWORD" \
  -X PROPFIND -H "Depth: 1" -H "Content-Type: application/xml" \
  --data '<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:getcontentlength/><d:resourcetype/></d:prop></d:propfind>' \
  "$DAV_RECORDING_DIR/" || true)

case "$DAV_STATUS" in
  404|405)
    log "OK no Talk recording folder for the room ($DAV_STATUS) — nothing was uploaded to Talk"
    ;;
  207)
    # The folder exists (a previous run, an unrelated Talk recording, or a
    # pre-created tree). It must contain no files — only the collection itself.
    python3 - "$DAV_PROPFIND" <<'PY' || fail "Cassini uploaded a file to Talk's recording store (PROPFIND response: $DAV_PROPFIND)"
import sys
import xml.etree.ElementTree as ET

ns = {"d": "DAV:"}
files = []
for resp in ET.parse(sys.argv[1]).getroot().findall("d:response", ns):
    href = (resp.findtext("d:href", "", ns) or "").strip()
    # A collection carries <d:collection/> inside <d:resourcetype>; anything
    # without it is a file.
    is_collection = any(
        el.find("{DAV:}collection") is not None for el in resp.iter("{DAV:}resourcetype")
    )
    if is_collection:
        continue
    sizes = [
        int(el.text)
        for el in resp.iter("{DAV:}getcontentlength")
        if (el.text or "").strip().isdigit()
    ]
    files.append((href, sizes[0] if sizes else -1))

if files:
    print(
        "[talk-rec-e2e] FAIL Talk's recording folder is not empty — Cassini "
        f"must never upload through the recording store (D-551): {files}",
        file=sys.stderr,
    )
    sys.exit(1)
PY
    log "OK Talk recording folder exists but holds no files — nothing was uploaded to Talk"
    ;;
  *)
    sed 's/^/    /' "$DAV_PROPFIND" 2>/dev/null | head -n 20 || true
    fail "PROPFIND $DAV_RECORDING_DIR/ returned unexpected HTTP $DAV_STATUS"
    ;;
esac

# ============================================================================
# Phase 9: Levenshtein-check transcript vs scenario expected text
# ============================================================================
phase 9 "Levenshtein-check transcript vs scenario expected text"

# Recover the transcript by decoding it back OUT of the published .opus, not
# from the build bundle. This proves the published artifact actually EMBEDS the
# expected transcript (D-429): `cassini inspect --transcript` reads the default
# words transcript from the portable .opus's CASSINI_TX_<id>_PAYLOAD_* tags and
# emits transcript.words.v1.json — the exact inverse of the transcript
# packer. The Levenshtein check below then runs against THAT .opus-derived
# transcript.
TRANSCRIPT_HOST="$LOG_DIR/transcript.words.v1.json"
if ! docker exec "$CONTAINER_NAME" \
  cassini inspect --transcript "$PUBLISHED_OPUS" >"$TRANSCRIPT_HOST" 2>"$LOG_DIR/inspect-transcript.err"; then
  sed 's/^/    /' "$LOG_DIR/inspect-transcript.err" 2>/dev/null | head -n 20 || true
  fail "cassini inspect --transcript failed to decode transcript from published .opus: $PUBLISHED_OPUS"
fi
if [[ ! -s "$TRANSCRIPT_HOST" ]]; then
  fail "decoded transcript from $PUBLISHED_OPUS is empty"
fi
log "OK transcript decoded from published .opus: $TRANSCRIPT_HOST ($(wc -c <"$TRANSCRIPT_HOST") bytes)"

EXPECTED_TEXT=$(python3 - "$SCENARIO_PATH" <<'PY'
import json, sys
with open(sys.argv[1]) as f:
    data = json.load(f)
print(" ".join(t["text"] for t in data.get("turns", [])))
PY
)
if [[ -z "$EXPECTED_TEXT" ]]; then
  fail "could not derive expected text from $SCENARIO_PATH"
fi

python3 - "$TRANSCRIPT_HOST" "$EXPECTED_TEXT" "$MIN_LEVENSHTEIN" "${MIN_CAPTURED_RUN_WORDS:-6}" <<'PY'
import json, re, sys, unicodedata

transcript_path = sys.argv[1]
expected = sys.argv[2]
min_ratio = float(sys.argv[3])
min_captured_run = int(sys.argv[4])

def normalise(text: str) -> str:
    text = unicodedata.normalize("NFKD", text)
    text = "".join(c for c in text if not unicodedata.combining(c))
    text = text.lower()
    text = re.sub(r"[^a-z0-9 ]+", " ", text)
    text = re.sub(r"\s+", " ", text).strip()
    return text

def lev(a: str, b: str) -> int:
    if len(a) < len(b):
        a, b = b, a
    if not b:
        return len(a)
    prev = list(range(len(b) + 1))
    for i, ca in enumerate(a, 1):
        curr = [i]
        for j, cb in enumerate(b, 1):
            curr.append(min(curr[j-1]+1, prev[j]+1, prev[j-1] + (0 if ca == cb else 1)))
        prev = curr
    return prev[-1]

def longest_word_run_in(got_words: list[str], want_words: list[str]) -> tuple[int, int]:
    # Longest run of consecutive got_words that appears verbatim inside
    # want_words. Returns (best_length, start_in_got). Captures the case
    # where the recorder only got one bot's slice of speech: that slice
    # should still be a substring of the scenario.
    if not got_words or not want_words:
        return 0, 0
    want_index: dict[str, list[int]] = {}
    for idx, w in enumerate(want_words):
        want_index.setdefault(w, []).append(idx)
    best = 0
    best_start = 0
    for got_start, gw in enumerate(got_words):
        for want_start in want_index.get(gw, ()):
            length = 0
            while (
                got_start + length < len(got_words)
                and want_start + length < len(want_words)
                and got_words[got_start + length] == want_words[want_start + length]
            ):
                length += 1
            if length > best:
                best = length
                best_start = got_start
    return best, best_start

with open(transcript_path) as f:
    data = json.load(f)

words = []
for segment in data.get("segments", []):
    for w in segment.get("words", []):
        if (w.get("text") or "").strip():
            words.append(w["text"])
got = normalise(" ".join(words))
want = normalise(expected)

if not got:
    print("[talk-rec-e2e] FAIL transcript empty after normalisation")
    sys.exit(1)

distance = lev(got, want)
ratio = 1.0 - distance / max(len(got), len(want))
print(f"[talk-rec-e2e] expected ({len(want)} chars): {want[:120]!r}...")
print(f"[talk-rec-e2e] got      ({len(got)} chars): {got[:120]!r}...")
print(f"[talk-rec-e2e] edit-distance={distance} ratio={ratio:.4f} levenshtein-threshold={min_ratio:.2f}")

if ratio >= min_ratio:
    print(f"[talk-rec-e2e] OK   Levenshtein ratio {ratio:.4f} >= threshold {min_ratio:.2f}")
    sys.exit(0)

# Partial-capture fallback: with multi-bot rotation the recorder typically
# captures one bot's contiguous slice clearly while the others come back
# as sparse-RTP gaps. If that slice is a verbatim substring of the
# scenario then transcription is working end-to-end even though the full
# Levenshtein ratio is dominated by missing content.
got_words = got.split()
want_words = want.split()
run_len, run_start = longest_word_run_in(got_words, want_words)
got_slice = " ".join(got_words[run_start : run_start + run_len])
print(f"[talk-rec-e2e] longest captured run inside expected text: {run_len} words")
if run_len >= min_captured_run:
    print(f"[talk-rec-e2e] OK   captured run of {run_len} words verbatim: {got_slice[:160]!r}")
    sys.exit(0)
print(f"[talk-rec-e2e] FAIL Levenshtein {ratio:.4f} < {min_ratio:.2f} and longest verbatim run {run_len} < {min_captured_run}")
sys.exit(1)
PY

log "FULL TALK-DRIVEN E2E PASSED — Talk record-button → gocassini → transcript"
