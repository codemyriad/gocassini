# Operator runtime

The operator is Cassini's long-running control-plane service.

## What it owns

The operator owns:

- HTTP admission and read APIs
- SQLite persistence
- job and attempt state transitions
- recording-slot admission control
- build and publish queues
- subprocess execution of `cassini` commands
- attempt-local logs and artifacts
- promotion of successful publish output into the live site
- SSE state-change events for the control panel

It does not own recording internals, transcription internals, or viewer rendering.

## Jobs, attempts, and canonical artifacts

### Jobs

A **job** is the stable logical unit of work.

The job summary carries:

- current stage/state
- current attempt number
- rerun count
- canonical artifact pointers

### Attempts

An **attempt** is one execution pass for a job.

Attempts preserve:

- attempt number
- trigger kind (`initial` or `rerun`)
- stage/state history
- attempt-local artifact paths
- stop metadata
- log paths

### `current/`

`current/` is the canonical reusable artifact library.

Think of it as:

- latest successful `.run` per job
- latest successful `.meeting` per job

Publish reads from this canonical library rather than from a single attempt directory.

## Stage and state model

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

## Concurrency model

### Recording slots

Recording is admission-controlled by `max-record-workers`.

Behavior:

- admission happens immediately
- there is no durable record queue
- overflow returns `503`
- rejected admission creates no job row

### Build workers

Build uses an in-memory queue plus a configurable worker pool.

### Publish worker

Publish uses an in-memory queue plus exactly one worker.

Publish stays serialized because every successful publish can replace the shared live site.

## Runtime flow

### Initial attempt

For a newly accepted job, the operator:

1. reserves a recording slot
2. inserts the job row and attempt `1`
3. runs `cassini doctor --target record`
4. runs `cassini record`
5. promotes a usable `.run` into `current/`
6. queues build against the canonical `.run`
7. runs `cassini build`
8. promotes a successful `.meeting` into `current/`
9. queues publish
10. runs `cassini publish` from the canonical current library
11. promotes the retained attempt-local `.site` into the live site root

### Reruns

Current reruns are downstream-only.

That means a rerun:

- does not re-record the meeting
- requires a canonical ready `.run`
- starts at `build/queued`
- creates fresh attempt-local `.meeting` and `.site` outputs
- can replace the canonical `.meeting` on success

## Stop behavior

A stop request is valid only while a job is `record/running`.

The operator:

- sends `SIGTERM` to the live `cassini record` subprocess first
- waits for a bounded grace window
- may escalate if the subprocess does not exit
- continues into build and publish if a usable `.run` was finalized

Stopping recording is not the same as cancelling the whole job.

## Restart behavior

On startup the operator:

1. opens SQLite
2. runs embedded migrations
3. marks non-terminal jobs and attempts `interrupted`
4. starts HTTP and worker runtimes

It does not automatically resume queued or running work.

## Current operational limitations

Important current limitations:

- no durable worker queue
- no automatic retry or resume after restart
- no retention/pruning policy yet
- no built-in log download endpoint
- no full-job re-record rerun mode
- no delete/archive lifecycle for completed jobs

## Where to go next

- Storage layout and live-site swaps: [Storage and promotion](./storage-and-promotion.md)
- Operational tasks: [Day-2 operations](./day-2-operations.md)
- Exact API surface: [Operator API](./reference/api.md)
