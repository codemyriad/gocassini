# V5 — Testing and validation

This document records the final validation surface for the implemented V5 operator flow.

It complements:
- `implementation.md`
- `slices.md`
- `cassini-operator/README.md`

## Automated checks

### Operator unit tests

```bash
cd cassini-operator
go test ./internal/operator
```

Current operator-focused coverage includes:
- migration `0003` bootstrap and `job_attempts` table creation
- backfill of legacy V2-shaped rows into synthetic `attempt_number = 1`
- initial trigger persistence of attempt `1`
- startup interruption mirroring onto attempt rows
- normalized trigger defaults
- explicit forwarding of `guestName`, `duration`, `stopWhenRoomEmpty`, and `roomEmptyGrace`
- accepted stop behavior for a running record subprocess
- rerun rejection for unknown and non-failed jobs
- failed first attempt plus successful rerun attempt
- preservation of original failure evidence after rerun
- detail read surface returning `job` plus `attempts[]`
- build failure manifest extraction
- publish failure manifest extraction

### Optional broader module checks

```bash
cd cassini-operator
go test ./...
go build ./...
```

This confirms the wider operator module still builds and tests cleanly around the V5 changes.

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
CALL_URL="$(./bin/cassini dev room create --name "V5 rerun validation" | tail -n1)"
echo "$CALL_URL"
```

Expected outcome:
- a usable Nextcloud Talk room URL is printed

## 4. Submit a live recording job

```bash
curl -s -X POST \
  'http://127.0.0.1:19080/jobs?provider=nextcloud-talk' \
  -H 'content-type: application/json' \
  -d "{\"platform\":\"nextcloud-talk\",\"url\":\"$CALL_URL\"}"
```

Expected outcome:
- HTTP `202`
- response body contains a ULID job id
- operator logs show `cassini doctor --target record`
- operator logs show `cassini record --call ...`

## 5. Force or reproduce a failed attempt

Any deterministic failure path is acceptable here. Typical options:
- run with a deliberately broken downstream configuration so `build` fails
- stop the operator environment in a way that makes `publish` fail
- temporarily point `CASSINI_BIN` at a controlled wrapper that forces `build` failure after record succeeds

Expected outcome:
- the top-level job summary reaches `done/failed`
- `GET /jobs/:id` shows exactly one failed attempt

Inspect the state:

```bash
curl -s http://127.0.0.1:19080/jobs
curl -s http://127.0.0.1:19080/jobs/<job-id>
```

Expected detail response outcome:
- `job.state = "failed"`
- `job.current_attempt_number = 1`
- `job.rerun_count = 0`
- `attempts[0].attempt_number = 1`
- `attempts[0].state = "failed"`
- attempt-level artifact and failure fields are present where applicable

## 6. Trigger a rerun

```bash
curl -s -X POST http://127.0.0.1:19080/jobs/<job-id>/rerun
```

Expected outcome:
- HTTP `202`
- response contains the same logical `id`
- response includes `attempt_number: 2`
- the operator reuses the preserved normalized request
- the job re-enters `record/queued`

Also validate rejection cases:

Unknown job:

```bash
curl -s -i -X POST http://127.0.0.1:19080/jobs/missing/rerun
```

Expected:
- `404`

Non-failed job:

```bash
curl -s -i -X POST http://127.0.0.1:19080/jobs/<successful-job-id>/rerun
```

Expected:
- `409`

## 7. Validate successful rerun path

Let the rerun complete successfully.

Expected top-level lifecycle:

```text
record/queued
-> record/running
-> build/queued
-> build/running
-> publish/queued
-> publish/running
-> done/succeeded
```

Inspect again:

```bash
curl -s http://127.0.0.1:19080/jobs
curl -s http://127.0.0.1:19080/jobs/<job-id>
```

Expected detail outcome:
- `job.state = "succeeded"`
- `job.current_attempt_number = 2`
- `job.rerun_count = 1`
- `attempts[0].attempt_number = 2`
- `attempts[0].state = "succeeded"`
- `attempts[1].attempt_number = 1`
- `attempts[1].state = "failed"`

This is the main V5 proof:
- the summary row reflects the winning attempt
- the original failure remains visible

## 8. Inspect attempt-scoped artifacts and logs

Inspect the work root:

```bash
find cassini-operator/.runtime/jobs -maxdepth 2 | sort
```

Expected outcome:
- attempt-specific `.run` bundles exist, such as:
  - `<job-id>--attempt-001.run`
  - `<job-id>--attempt-002.run`
- attempt-specific `.meeting` bundles exist where build succeeded far enough to produce them
- attempt-specific log directories exist, such as:
  - `<job-id>--attempt-001.logs`
  - `<job-id>--attempt-002.logs`

Inspect detail response fields for log paths:
- `record_log_path`
- `build_log_path`
- `publish_log_path`

Expected outcome:
- later attempts do not overwrite earlier attempt paths
- failure inspection can point you at the exact attempt-local logs

## 9. Validate that hosted output follows the winning attempt

Inspect the published site output:

```bash
find cassini-operator/.runtime/site -maxdepth 3 | sort
./bin/cassini serve ./cassini-operator/.runtime/site
```

Expected outcome:
- the hosted site reflects the successful rerun output
- the top-level `job.artifact_site_path` points at the shared site root
- the successful attempt row also records that site output path

## 10. Verify restart honesty still holds with attempt history

Start a job and interrupt the operator before terminal completion.
Restart against the same DB and inspect:

```bash
curl -s http://127.0.0.1:19080/jobs/<job-id>
```

Expected outcome:
- the logical job summary keeps its last stage
- its state becomes `interrupted`
- the current attempt row is also marked `interrupted`
- completed `succeeded` and `failed` attempts remain unchanged

## Final V5 success expectation

With the implemented V5 slices complete, the main recovery loop is now:

```text
attempt 1
-> done/failed

POST /jobs/:id/rerun

attempt 2
-> done/succeeded
```

And the operator read surfaces now have distinct roles:

```text
GET /jobs
-> one summary row per logical job

GET /jobs/:id
-> summary row + attempts[] newest first
```
