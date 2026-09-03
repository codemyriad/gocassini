#!/usr/bin/env bash
#
# Offline guard for the browser leg's acceptance contract.
#
# ci-e2e-browser-call-capture.sh needs a full stack — Nextcloud, Talk, an HPB,
# the installed ExApp and two real Chromium participants — so the one thing
# nobody can check while editing it is whether its jq acceptance expression
# still rejects the runs it is supposed to reject. This test extracts that
# expression verbatim from the leg and evaluates it against synthesized
# result.json documents, so a contract that has quietly stopped requiring
# something fails here in a second instead of passing a stack run for the wrong
# reason.
#
# The contract under test says capture follows Talk's official recording: BOTH
# participants record themselves, BOTH upload, and NEITHER browser stores
# anything of its own. Each mutation below removes exactly one of those and must
# be refused.
#
# Run directly:
#   ./harness/bin/test-browser-capture-contract.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LEG_SH="$SCRIPT_DIR/ci-e2e-browser-call-capture.sh"
DRIVER_MJS="$SCRIPT_DIR/browser-call-capture.mjs"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

command -v jq >/dev/null 2>&1 || fail "jq is required"
[[ -f "$LEG_SH" ]] || fail "browser leg is missing at $LEG_SH"
[[ -f "$DRIVER_MJS" ]] || fail "browser driver is missing at $DRIVER_MJS"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

# The contract, taken from the leg rather than restated here: a copy would go
# stale exactly when it mattered.
CONTRACT="$(awk '
  /^BROWSER_RESULT_CONTRACT='"'"'$/ { collecting = 1; next }
  collecting && /^'"'"'$/ { exit }
  collecting { print }
' "$LEG_SH")"
[[ -n "$CONTRACT" ]] \
  || fail "could not extract BROWSER_RESULT_CONTRACT from $LEG_SH; the markers around it moved"
# shellcheck disable=SC2016  # a literal dollar in a grep pattern matching the leg's own source
grep -q 'jq -e "\$BROWSER_RESULT_CONTRACT"' "$LEG_SH" \
  || fail "the leg no longer evaluates BROWSER_RESULT_CONTRACT, so this test guards nothing"

# Both participants are subjects of the leg, not one plus a control.
# shellcheck disable=SC2016  # a literal dollar in a grep pattern matching the leg's own source
grep -q 'verify_owner_capture alice "\$ALICE" 3' "$LEG_SH" \
  || fail "the leg no longer verifies Alice's stored multi-segment capture"
# shellcheck disable=SC2016  # a literal dollar in a grep pattern matching the leg's own source
grep -q 'verify_owner_capture bob "\$BOB" 1' "$LEG_SH" \
  || fail "the leg no longer verifies Bob's stored capture"
if grep -q 'setCaptureConsent' "$DRIVER_MJS"; then
  fail "the browser driver still sets a per-participant capture opt-in"
fi

cat >"$TMP_DIR/passing.json" <<'JSON'
{
  "result": "passed",
  "recording": { "callRecording": 2, "polls": 3 },
  "alice": {
    "preRecordingOPFS": [],
    "joinedBeforeRecordingOPFS": [],
    "duringRecordingOPFS": [
      { "dirName": "capture-room-1", "files": [
        { "name": "capture.json.partial", "kind": "file", "size": 800 },
        { "name": "segment-0.webm", "kind": "file", "size": 42000 },
        { "name": "segment-1.webm", "kind": "file", "size": 31000 },
        { "name": "segment-2.webm", "kind": "file", "size": 28000 }
      ] }
    ],
    "reload": {
      "capturesBefore": ["capture-room-1"],
      "capturesAfter": ["capture-room-1"],
      "segmentsBefore": 2,
      "segmentsAfter": 3,
      "preservedPreReloadBytes": true
    },
    "mediaAfterReload": { "rejoined": true, "audioBytesSent": 26000 },
    "afterLeaveOPFS": [],
    "captureStorageKeys": [],
    "mediaBeforeRecording": {
      "audioBytesSent": 9000,
      "mediaDialogClicked": true,
      "connections": [
        { "connectionState": "connected", "iceConnectionState": "connected",
          "liveAudioSenders": 1, "audioBytesSent": 9000 }
      ]
    },
    "mediaImmediatelyAfterSwitch": { "audioBytesSent": 12000 },
    "mediaAfterSwitch": { "audioBytesSent": 20000 },
    "microphoneSwitch": {
      "mode": "distinct-device",
      "before": { "deviceId": "device-a", "trackId": "track-a" },
      "after": { "deviceId": "device-b", "trackId": "track-b" }
    },
    "upload": { "status": 202, "body": "{\"status\":\"accepted\",\"room\":\"r\",\"segments\":3,\"bytes\":101000}" },
    "observedUploadRequestCount": 1,
    "observedUploadResponseCount": 1
  },
  "bob": {
    "preRecordingOPFS": [],
    "joinedBeforeRecordingOPFS": [],
    "duringRecordingOPFS": [
      { "dirName": "capture-room-2", "files": [
        { "name": "capture.json.partial", "kind": "file", "size": 700 },
        { "name": "segment-0.webm", "kind": "file", "size": 55000 }
      ] }
    ],
    "afterLeaveOPFS": [],
    "captureStorageKeys": [],
    "mediaBeforeRecording": {
      "audioBytesSent": 8000,
      "mediaDialogClicked": true,
      "connections": [
        { "connectionState": "connected", "iceConnectionState": "completed",
          "liveAudioSenders": 1, "audioBytesSent": 8000 }
      ]
    },
    "upload": { "status": 202, "body": "{\"status\":\"accepted\",\"room\":\"r\",\"segments\":1,\"bytes\":55000}" },
    "observedUploadRequestCount": 1,
    "observedUploadResponseCount": 1
  }
}
JSON

jq -e "$CONTRACT" "$TMP_DIR/passing.json" >/dev/null \
  || fail "the contract rejects a run where both participants captured and uploaded"

# Each mutation is one requirement removed. The name says what a run that looked
# like this would actually mean.
reject() {
  local name="$1" filter="$2"
  jq "$filter" "$TMP_DIR/passing.json" >"$TMP_DIR/mutated.json" \
    || fail "could not build the '$name' document"
  if jq -e "$CONTRACT" "$TMP_DIR/mutated.json" >/dev/null 2>&1; then
    fail "the contract accepts a run where $name"
  fi
}

reject "Bob captured nothing"                    '.bob.duringRecordingOPFS = []'
reject "Bob buffered no audio segment"           '.bob.duringRecordingOPFS[0].files |= map(select(.name == "capture.json.partial"))'
reject "Bob never uploaded"                      '.bob.observedUploadRequestCount = 0'
reject "Bob's upload was refused"                '.bob.upload.status = 403'
reject "Alice never uploaded"                    '.alice.observedUploadRequestCount = 0'
reject "Alice's upload was refused"              '.alice.upload.status = 403'
reject "Alice's microphone switch cut no second segment" \
  '.alice.duringRecordingOPFS[0].files |= map(select(.name != "segment-1.webm"))'
reject "the microphone switch reused one device" '.alice.microphoneSwitch.after.deviceId = "device-a"'
reject "a browser kept capture state of its own" '.bob.captureStorageKeys = ["cassini.sourceCapture.consent"]'
reject "Alice kept capture state of her own"     '.alice.captureStorageKeys = ["cassini.sourceCapture.consent"]'
reject "a buffer survived an accepted upload"    '.alice.afterLeaveOPFS = .alice.duringRecordingOPFS'
reject "Bob's buffer survived his upload"        '.bob.afterLeaveOPFS = .bob.duringRecordingOPFS'
reject "someone captured before the recording"   '.bob.joinedBeforeRecordingOPFS = .bob.duringRecordingOPFS'
reject "Talk never confirmed the recording"      '.recording.callRecording = 4'
reject "Bob's audio never reached the SFU"       '.bob.mediaBeforeRecording.connections[0].audioBytesSent = 0'
reject "the browser process itself failed"       '.result = "failed"'
# The reload seam. A run that looked like any of these would mean the page that
# came back filed a second capture, or never got its own audio into the one it
# inherited — which is the reload's audio lost, exactly what this leg exists to
# catch.
reject "the reload filed a second capture" \
  '.alice.reload.capturesAfter = ["capture-room-1", "capture-room-9"]'
reject "the rejoined page abandoned the pre-reload buffer" \
  '.alice.reload.capturesAfter = ["capture-room-9"]'
reject "the rejoined page added no segment of its own" \
  '.alice.reload.segmentsAfter = .alice.reload.segmentsBefore'
reject "Alice's reload lost her the third segment" \
  '.alice.duringRecordingOPFS[0].files |= map(select(.name != "segment-2.webm"))'
reject "the resumed capture overwrote pre-reload audio" \
  '.alice.reload.preservedPreReloadBytes = false'
reject "Alice never got back into the call" '.alice.mediaAfterReload.rejoined = false'
reject "Alice sent no audio after rejoining" '.alice.mediaAfterReload.audioBytesSent = 0'

echo "PASS: the browser leg's contract requires a capture, a resumed reload and an accepted upload from every participant"
