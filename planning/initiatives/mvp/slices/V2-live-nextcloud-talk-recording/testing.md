# V2 — Testing and validation

This document records the final validation surface for the implemented V2 operator flow.

It complements:
- `implementation.md`
- `slices.md`
- `cassini-operator/README.md`
- `harness/README.md`

## Automated checks

### Operator unit tests

```bash
cd cassini-operator
go test ./...
```

Current operator-focused coverage includes:
- fresh DB bootstrap through migrations
- baselining a legacy V1-shaped DB into `schema_migrations`
- explicit down migration in a test path
- refusal on inconsistent migration history
- normalized trigger defaults for V2 request bodies
- explicit forwarding of `guestName`, `duration`, `stopWhenRoomEmpty`, and `roomEmptyGrace`
- accepted stop behavior for a running record subprocess
- `404` and `409` stop-path behavior
- stop metadata persistence and read-surface exposure
- build failure manifest extraction
- publish failure manifest extraction
- startup interruption marking with stage preservation

### Operator build check

```bash
cd cassini-operator
go build ./...
```

This confirms the operator module builds cleanly with the migration and live-record changes.

### Recorder-side CLI checks

```bash
cd cassini-go-recorder
go test ./internal/cassini/...
```

This keeps the product CLI surface covered alongside the operator runtime.

## Manual validation checklist

All commands below assume repo root:

```bash
cd <reporoot>
```

## 1. Start the local Talk stack

```bash
./bin/cassini dev stack up
```

Expected outcome:
- local Nextcloud/Talk stack comes up successfully
- you can create rooms from the repo tooling

## 2. Start the operator

```bash
rm -rf cassini-operator/.runtime
mkdir -p cassini-operator/.runtime
./bin/cassini operator start --bind 127.0.0.1:19080
```

Expected startup log lines include:
- `listening -> http://127.0.0.1:19080`
- `db -> .../cassini-operator/.runtime/jobs.sqlite3`
- `work_root -> .../cassini-operator/.runtime/jobs`
- `site_root -> .../cassini-operator/.runtime/site`
- `cassini_bin -> .../bin/cassini`

## 3. Create a real Talk room

```bash
CALL_URL="$(./bin/cassini dev room create --name "V2 live capture validation" | tail -n1)"
echo "$CALL_URL"
```

Expected outcome:
- a usable Nextcloud Talk room URL is printed

## 4. Submit a live recording job

Minimal request:

```bash
curl -s -X POST \
  'http://127.0.0.1:19080/jobs?provider=nextcloud-talk' \
  -H 'content-type: application/json' \
  -d "{\"platform\":\"nextcloud-talk\",\"url\":\"$CALL_URL\"}"
```

Expanded request example:

```bash
curl -s -X POST \
  'http://127.0.0.1:19080/jobs?provider=nextcloud-talk' \
  -H 'content-type: application/json' \
  -d "{\"platform\":\"nextcloud-talk\",\"url\":\"$CALL_URL\",\"guestName\":\"CassiniRecorder\",\"duration\":120,\"stopWhenRoomEmpty\":true,\"roomEmptyGrace\":30}"
```

Expected outcome:
- HTTP `202`
- response body contains a ULID job id
- operator logs show `cassini doctor --target record`
- operator logs show `cassini record --call ...`

## 5. Join the room in the browser and speak normally

Manual action:
- open the Talk room URL in a browser
- join as a normal participant
- speak into the mic long enough to create real captured media

Expected outcome:
- recorder remains active while the meeting is live
- operator job transitions through `record/running`

## 6. Validate natural-stop happy path

Let one run end naturally via:
- room empty behavior, or
- duration limit if you passed one

Inspect the job:

```bash
curl -s http://127.0.0.1:19080/jobs
curl -s http://127.0.0.1:19080/jobs/<job-id>
```

Expected outcome:
- job advances through `build` and `publish`
- final state is `done/succeeded`
- `artifact_run_path`, `artifact_meeting_path`, and `artifact_site_path` are populated
- stop metadata reflects the natural stop reason, such as:
  - `room_empty`
  - `duration_limit`

## 7. Validate explicit stop happy path

Start a second live job and stop it while it is recording:

```bash
curl -s -X POST http://127.0.0.1:19080/jobs/<job-id>/stop
```

Expected outcome:
- first accepted stop returns `202`
- repeated stop while the same stop is in progress also returns `202`
- unknown job returns `404`
- non-stoppable job returns `409`
- operator sends `SIGTERM` to the live recorder subprocess
- if the `.run` finalizes cleanly, the job still continues through build/publish and ends `done/succeeded`
- persisted stop metadata shows:
  - `stop_reason=operator_requested`
  - `stop_requested_at` populated
  - `stop_signal_sent_at` populated

## 8. Inspect artifacts

Inspect the persisted job row and output roots:

```bash
curl -s http://127.0.0.1:19080/jobs/<job-id>
find cassini-operator/.runtime/jobs -maxdepth 2 | sort
find cassini-operator/.runtime/site -maxdepth 3 | sort
```

Expected outcome:
- `.run` bundle exists for the job
- downstream `.meeting` output exists
- site output exists
- stop metadata is present on the job row only
- recorder-owned `.run` contents are not mutated with operator stop metadata

## 9. Verify migration behavior manually if needed

Fresh bootstrap:
- remove `cassini-operator/.runtime/jobs.sqlite3`
- restart operator
- confirm the DB is recreated successfully

Legacy upgrade:
- use a V1-shaped DB if available
- restart operator against it
- confirm startup succeeds and job reads still work

The automated tests are the primary proof for migration edge cases, especially:
- explicit down migration
- inconsistent migration history rejection

## 10. Verify restart honesty still holds

Start a live job, then kill the operator before terminal completion.
Restart against the same DB and inspect the row.

Expected outcome:
- the job keeps its last stage
- the state becomes `interrupted`
- terminal `succeeded` and `failed` rows remain unchanged

## Final end-to-end success expectation

With the implemented V2 slices complete, the happy-path lifecycle is now:

```text
record/queued
-> record/running
-> build/queued
-> build/running
-> publish/queued
-> publish/running
-> done/succeeded
```

And the explicit-stop happy path is now:

```text
record/running
-> POST /jobs/:id/stop accepted
-> graceful recorder exit
-> build/queued
-> build/running
-> publish/queued
-> publish/running
-> done/succeeded
```

The job remains `succeeded`; the reason it stopped is carried by persisted stop metadata rather than a separate terminal state.
