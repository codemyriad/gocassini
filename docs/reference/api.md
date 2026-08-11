# Operator API reference

This page covers the operator’s HTTP API and SSE event stream.

The operator API is primarily an operational backend surface.

In normal browser use:

- the control panel calls this API
- the viewer does not

See also:

- [Control panel](../components/control-panel.md)
- [Operator stack](../operator-stack.md)

## Create a job

```http
POST /jobs?provider=nextcloud-talk
Content-Type: application/json

{
  "platform": "nextcloud-talk",
  "url": "https://...",
  "guestName": "CassiniRecorder",
  "duration": 120,
  "stopWhenRoomEmpty": true,
  "roomEmptyGrace": 30
}
```

Behavior:

- accepts only `provider=nextcloud-talk`
- requires `platform = nextcloud-talk`
- requires `url`
- supports optional `guestName`, `duration`, `stopWhenRoomEmpty`, and `roomEmptyGrace`
- normalizes defaults before persistence
- requires an available recording slot
- returns `202` on acceptance
- returns `503` when recording capacity is full
- creates **no** job row on recording-capacity rejection

## List jobs

```http
GET /jobs
```

Returns newest-first logical job summaries.

This is the summary read model, not the full attempt history view.

## Get one job

```http
GET /jobs/:id
```

Returns:

- `job` — the logical summary row
- `attempts` — newest-first attempt history

Useful for:

- current stage/state
- current artifact pointers
- preserved failures and reruns
- attempt-local artifact and log paths

### Seal-stage fields

The `seal` stage adds these fields to the response.

On `job`:

- `seal_queued_at`, `seal_started_at`, `seal_finished_at`
- `artifact_opus_path` — the canonical promotion, `current/<id>.opus`
- `artifact_opus_sha256` — the SHA-256 of the sealed file

On each entry of `attempts`:

- the same five fields, where `artifact_opus_path` is that attempt’s own immutable
  sealed file, `runs/<id>--attempt-NNN.seal/<id>.opus`
- `seal_log_path` — `runs/<id>--attempt-NNN.logs/seal.log`

All of them are `null` until the stage that writes them runs.

The split is the same one every other stage uses:

```text
job row      current/<id>.opus                 what is canonical now
attempt row  runs/<id>--attempt-NNN.seal/...   what this attempt sealed
```

`artifact_opus_sha256` is written by the seal and re-checked by the publish that
follows it, so the two rows together say which exact bytes were delivered.

## Stop a job

```http
POST /jobs/:id/stop
```

Behavior:

- valid only for `record/running`
- returns `404` for unknown jobs
- returns `409` when the job is not stoppable
- returns `202` when stop is accepted or already in progress
- sends `SIGTERM` to the live `cassini record` subprocess first
- may escalate if the subprocess does not exit in time

Important meaning:

- this stops recording
- it does not automatically mean the whole job is abandoned
- if the recorder finalized a usable `.run`, the job can still continue through build and publish

## Rerun a job

```http
POST /jobs/:id/rerun
```

Behavior:

- valid only for terminal jobs
- requires a canonical ready `.run`
- creates a new attempt
- queues that attempt directly at `build/queued`
- returns `202` with the new attempt number

Current reruns are downstream-only:

- they do not re-record the meeting
- they reuse the preserved canonical `.run`
- they create fresh attempt-local `.meeting` and `.site` outputs

## Event stream

```http
GET /events
Accept: text/event-stream
```

Current event types:

- `job.created`
- `job.updated`
- `attempt.updated`

Each event carries:

- the current summary-row `job`
- the current `attempt` when available

The control panel uses this together with snapshot reads from `GET /jobs` and `GET /jobs/:id`.

## Summary of stage and state values

Stage values:

- `record`
- `build`
- `seal` — packing the portable `.opus` this attempt will publish
- `publish`
- `done`

State values:

- `queued`
- `running`
- `succeeded`
- `failed`
- `interrupted`

Typical successful initial lifecycle:

```text
record/queued
-> record/running
-> build/queued
-> build/running
-> seal/queued
-> seal/running
-> publish/queued
-> publish/running
-> done/succeeded
```

Typical rerun lifecycle:

```text
build/queued
-> build/running
-> seal/queued
-> seal/running
-> publish/queued
-> publish/running
-> done/succeeded
```

`publish/queued` is reachable only through a completed seal. A job that reaches
`done/failed` from `seal` did not produce a verifiable portable meeting, and its
`error` carries the pack failure; the retry is a rerun, exactly as it is for a
failed build or publish.

A restart mid-pipeline resolves like this:

```text
  at crash            after restart          resumes?
  ────────────────    ───────────────────    ──────────────────────────────
  record/*            record/interrupted     no  — rerun
  build/queued        build/queued           yes — requeue dispatcher
  seal/queued         seal/queued            yes — requeue dispatcher
  publish/queued      publish/queued         yes — requeue dispatcher
  build|seal|publish  <stage>/interrupted    no  — the subprocess died; rerun
    /running
```

## What the API does not try to be

The current API is not:

- a viewer content API
- a direct filesystem browser
- a durable work-queue API
- a multi-user workflow API

It is the operator’s control and inspection surface.
