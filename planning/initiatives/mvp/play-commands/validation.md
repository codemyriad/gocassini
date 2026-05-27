---
shaping: true
---

# Play Commands — Validation Plan

This document defines how to validate `cassini dev play` against the existing Multipass VM harness and the Cassini recording bundle.

## Explored environment

From the host repo checkout:

```bash
multipass list
```

Current discovered VM:

| Item | Value |
|---|---|
| VM name | `dev-vm` |
| VM IP | `192.168.252.21` |
| Operator API | `http://192.168.252.21:4000` |
| Control panel | `http://192.168.252.21:4173` |
| Viewer | `http://192.168.252.21:8765` |
| Expected Nextcloud harness URL | `http://192.168.252.21:28080` |

The Cassini recording bundle is reachable now at `:4000` (`GET /jobs` returns `200`).

At exploration time the deployment compose stack was running, but the VM Talk harness stack was not listening on `:28080`; `tmux` history for window 2 shows it had been started and then stopped with `CASSINI_HARNESS_VM=true ./bin/cassini dev stack down`. The validation flow below includes a preflight/start step for the harness.

---

## Validation goal

Validate a recorded 20-second feed into a local Nextcloud Talk room:

1. Resolve the Multipass VM IP.
2. Ensure the VM Talk harness is up and Talk recording backend config is enabled.
3. Create a room before playback so recording can be started before any feed enters the room.
4. Start a Cassini operator recording job for that room.
5. Run the new play command with `--duration 20`.
6. Stop or let recording auto-stop.
7. Verify the operator job succeeds and the viewer/catalog contains the resulting meeting.

Important boundary: `cassini dev play` remains a playback command. For this recorded validation, room creation and recording start happen before invoking `cassini dev play`; otherwise playback would create the room and begin streaming before the validation harness has a chance to start recording.

---

## Preflight

Run from the host repo root.

```bash
set -euo pipefail

VM_NAME="${VM_NAME:-dev-vm}"
VM_IP="$(multipass list | awk -v vm="$VM_NAME" '$1 == vm {print $3; exit}')"
if [[ -z "$VM_IP" ]]; then
  echo "Could not resolve VM IP for $VM_NAME" >&2
  exit 1
fi

NEXTCLOUD_URL="http://$VM_IP:28080"
OPERATOR_URL="http://$VM_IP:4000"
VIEWER_URL="http://$VM_IP:8765"
ROOM="cassini-play-validation-$(date -u +%Y%m%d-%H%M%S)"

echo "VM_IP=$VM_IP"
echo "NEXTCLOUD_URL=$NEXTCLOUD_URL"
echo "OPERATOR_URL=$OPERATOR_URL"
echo "ROOM=$ROOM"
```

Check the operator bundle:

```bash
curl -fsS "$OPERATOR_URL/jobs" >/dev/null
echo "operator OK"
```

Ensure the Talk harness is up. If this status check fails, start the VM harness:

```bash
if ! curl -fsS "$NEXTCLOUD_URL/status.php" >/dev/null; then
  multipass exec "$VM_NAME" -- bash -lc '
    cd /home/ubuntu/dev/workspace
    CASSINI_HARNESS_VM=true ./bin/cassini dev stack up
  '
fi

curl -fsS "$NEXTCLOUD_URL/status.php" >/dev/null
echo "nextcloud OK"
```

Optionally confirm Talk recording backend config is enabled by bootstrap:

```bash
multipass exec "$VM_NAME" -- bash -lc '
  cd /home/ubuntu/dev/workspace
  docker compose -p spreedtest-vm -f harness/vm/compose.yml exec -T -u www-data nextcloud \
    php occ config:app:get spreed call_recording
'
```

Expected output:

```text
yes
```

---

## Create the room before playback

Use the native harness script, not the VM wrapper, so runtime state lands in `harness/runtime` as shaped.

```bash
CALL_URL="$(multipass exec "$VM_NAME" -- bash -lc "
  cd /home/ubuntu/dev/workspace
  NEXTCLOUD_URL='$NEXTCLOUD_URL' \
  NEXTCLOUD_STATUS_URL='$NEXTCLOUD_URL/status.php' \
    ./harness/bin/create-room.sh --name '$ROOM' | tail -n1
")"
ROOM_TOKEN="${CALL_URL##*/}"

echo "CALL_URL=$CALL_URL"
echo "ROOM_TOKEN=$ROOM_TOKEN"
```

This step both creates the Talk room and writes:

- `harness/runtime/last_room_token`
- `harness/runtime/last_call_url`

Because `/home/ubuntu/dev/workspace` is mounted from the host checkout, these runtime files should be visible from the host repo too.

---

## Start recording before feed

Use the operator API running in the deployment bundle on the VM.

The operator container reaches the VM Talk harness through `host.docker.internal:28080`, while browser/player-facing URLs use the VM IP.

```bash
JOB_BODY="$(
  NEXTCLOUD_URL="$NEXTCLOUD_URL" ROOM_TOKEN="$ROOM_TOKEN" python3 - <<'PY'
import json
import os

print(json.dumps({
    "platform": "nextcloud-talk",
    "baseURL": os.environ["NEXTCLOUD_URL"],
    "talkConnectURL": "http://host.docker.internal:28080",
    "roomToken": os.environ["ROOM_TOKEN"],
    "talkAuthMode": "hpb-internal",
    "guestName": "CassiniValidationRecorder",
    "duration": 90,
    "stopWhenRoomEmpty": True,
    "roomEmptyGrace": 30,
}))
PY
)"

JOB_RESPONSE="$(curl -fsS -X POST \
  "$OPERATOR_URL/jobs?provider=nextcloud-talk" \
  -H 'content-type: application/json' \
  -d "$JOB_BODY")"
JOB_ID="$(printf '%s' "$JOB_RESPONSE" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"

echo "JOB_ID=$JOB_ID"
```

Wait until the operator has at least entered the record stage:

```bash
for i in $(seq 1 30); do
  STATE_LINE="$(curl -fsS "$OPERATOR_URL/jobs/$JOB_ID" | python3 -c '
import json, sys
raw = json.load(sys.stdin)
job = raw.get("job", raw)
print(f"{job.get('stage')}/{job.get('state')}")
')"
  echo "job=$JOB_ID state=$STATE_LINE"
  case "$STATE_LINE" in
    record/running|build/*|publish/*|done/*) break ;;
  esac
  sleep 1
done
```

---

## Run the 20-second feed

After `cassini dev play` is implemented, validate the default `full` mode with a 20-second feed:

```bash
CASSINI_HARNESS_HOST="$VM_IP" \
./bin/cassini dev play \
  --nextcloud-host "$VM_IP" \
  --room "$ROOM" \
  --duration 20
```

Expected behavior:

- The command resolves `$VM_IP` to `http://$VM_IP:28080`.
- The command finds the already-created room named `$ROOM`.
- It uses `--mode full` by default.
- It delegates to `harness/bin/stream-synthetic-meeting.sh` with the pinned showcase fixture and `--duration 20`.
- It exits `0` after the feed completes.

### Pre-implementation equivalent

Until `cassini dev play` exists, the equivalent low-level command is:

```bash
PREPARE=0 \
./harness/bin/stream-synthetic-meeting.sh \
  --call-url "$CALL_URL" \
  --scenario "$PWD/harness/scenarios/showcase-lantern-festival.v1.json" \
  --output-dir "$PWD/harness/media/processed/showcase-lantern-festival-v1" \
  --duration 20 \
  --skip-prepare
```

This validates the existing streamer and recorder path, but not the new `cassini dev play` dispatch/room-resolution code.

---

## Stop/finalize recording

After playback exits, either wait for room-empty auto-stop or explicitly stop the operator job to speed up finalization.

```bash
curl -sS -X POST "$OPERATOR_URL/jobs/$JOB_ID/stop" >/dev/null || true
```

Then poll until terminal state:

```bash
for i in $(seq 1 120); do
  STATE_LINE="$(curl -fsS "$OPERATOR_URL/jobs/$JOB_ID" | python3 -c '
import json, sys
raw = json.load(sys.stdin)
job = raw.get("job", raw)
print(f"{job.get('stage')}/{job.get('state')}")
')"
  echo "job=$JOB_ID state=$STATE_LINE"
  case "$STATE_LINE" in
    done/succeeded) break ;;
    done/failed) echo "job failed" >&2; exit 1 ;;
  esac
  sleep 5
done
```

---

## Acceptance checks

### 1. Operator job succeeded

```bash
curl -fsS "$OPERATOR_URL/jobs/$JOB_ID" | python3 -m json.tool
```

Pass when the job summary shows:

- `stage: "done"`
- `state: "succeeded"`
- non-empty `artifact_run_path`
- non-empty `artifact_meeting_path`
- non-empty `artifact_site_path`

### 2. Published viewer catalog includes the meeting

```bash
curl -fsS "$VIEWER_URL/catalog.json" | python3 -c '
import json
import sys

job_id = sys.argv[1]
catalog = json.load(sys.stdin)
ids = [m.get("id") for m in catalog.get("meetings", [])]
if job_id not in ids:
    raise SystemExit(f"{job_id} not in viewer catalog: {ids}")
print(f"catalog OK: {job_id}")
' "$JOB_ID"
```

### 3. Optional browser check

Open:

```text
http://<VM_IP>:8765/
```

Expected: the new meeting appears in the viewer and contains audio/transcript artifacts from the 20-second feed.

---

## Notes / risks

- If the harness stack is down, `:28080` and `:28082` will not listen even though the operator bundle at `:4000` is up.
- The direct operator job uses `baseURL=http://<VM_IP>:28080` plus `talkConnectURL=http://host.docker.internal:28080`; this mirrors successful existing operator jobs in the current VM.
- The validation starts recording before playback. If we instead trigger Talk's native recording OCS endpoint, Talk may reject recording before anyone is active in the call.
- The `cassini dev play` command should not grow a recording flag for this validation unless recording becomes an explicit product requirement; keep validation orchestration outside playback.
