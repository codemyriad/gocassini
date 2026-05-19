# Operator API reference

The operator API is the HTTP and SSE surface behind Cassini's managed runtime.

It is primarily an operational backend surface.

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
-> publish/queued
-> publish/running
-> done/succeeded
```

Typical rerun lifecycle:

```text
build/queued
-> build/running
-> publish/queued
-> publish/running
-> done/succeeded
```

## What the API does not try to be

The current API is not:

- a viewer content API
- a direct filesystem browser
- a durable work-queue API
- a multi-user workflow API

It is the operator’s control and inspection surface.
