# Operator API

The operator API is the runtime control and inspection surface.

The control panel calls this API. The viewer does not.

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
- requires an available recording slot
- returns `202` on acceptance
- returns `503` when recording capacity is full
- creates no job row on recording-capacity rejection

## List jobs

```http
GET /jobs
```

Returns newest-first logical job summaries.

## Get one job

```http
GET /jobs/:id
```

Returns:

- `job` — the logical summary row
- `attempts` — newest-first attempt history

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
- it does not automatically abandon the whole job
- if a usable `.run` exists, the job can still continue through build and publish

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

Current reruns are downstream-only.

## Event stream

```http
GET /events
Accept: text/event-stream
```

Current event types:

- `job.created`
- `job.updated`
- `attempt.updated`

Each event carries the current summary-row `job` and the current `attempt` when available.

## Stage and state values

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

## Base path note

The paths above are shown without an operator base-path prefix. When `CASSINI_OPERATOR_BASE_PATH` or `--base-path` is set, the same API is mounted under that prefix.
