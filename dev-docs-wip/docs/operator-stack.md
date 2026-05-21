# Operator stack

This page explains the long-running Cassini runtime: the operator, the control panel, and the viewer.

If you want to see this running first, start here:

- [Quick start](./quick-start.md)

## What this stack is for

The operator stack turns the Cassini file pipeline into a service.

It adds:

- persisted jobs
- preserved attempts
- recording-slot admission control
- queued build workers
- serialized publish
- safe promotion into a shared live site
- browser visibility through the control panel

## The three services

### Operator

The operator is the only runtime service that mutates state.

It owns:

- job admission
- SQLite persistence
- work-root artifacts
- record/build/publish execution
- current artifact promotion
- live-site promotion

### Control panel

The control panel is the browser UI for operating the operator.

It owns:

- job creation
- stop and rerun actions
- job and attempt inspection
- live status updates

### Viewer

The viewer is the browser UI for consuming published meetings.

It owns:

- serving the static meeting library
- playback and transcript review

It does **not** talk to the operator.

## Topology

```text
browser
  -> control panel
  -> viewer

control panel
  -> operator API

operator
  -> SQLite
  -> work root
  -> shared published-site storage
  -> cassini CLI subprocesses

viewer
  -> shared published-site storage (read-only)
```

## The operator’s most important boundary

The operator is an **orchestrator**, not a second implementation of the media pipeline.

It shells out to the Cassini CLI for stage execution:

- `cassini doctor --target record`
- `cassini record`
- `cassini build`
- `cassini publish`

That means:

- the CLI remains the source of truth for artifact production
- operator mode and standalone mode stay aligned
- the operator can stay focused on persistence, lifecycle, and scheduling

## Jobs, attempts, and `current/`

The operator persists two related views of work.

### Jobs

A **job** is the stable logical unit of work.

A job summary answers questions like:

- what room URL was requested?
- what stage/state is the job currently in?
- what is the current attempt number?
- what are the canonical current artifact paths?

### Attempts

An **attempt** is one execution pass for that job.

Attempts preserve:

- attempt number
- trigger kind (`initial` or `rerun`)
- stage/state transitions
- attempt-local artifacts
- attempt-local logs
- failure history

### `current/`

The operator’s `current/` directory is the canonical artifact library.

Think of it as:

- the latest successful `.run` per logical job
- the latest successful `.meeting` per logical job

This matters because publish does **not** export from “the latest attempt folder”. It exports from the canonical current meeting library.

## Why `current/` matters

The easiest way to understand publish in operator mode is:

1. each attempt may create attempt-local outputs under `runs/`
2. successful artifacts are promoted into `current/`
3. publish reads the whole canonical `current/` library
4. the live site is rebuilt from the ready `.meeting` bundles in that library

So the live site is based on the latest successful artifacts per job, not on a single attempt directory.

## Concurrency model

The operator uses different rules for each stage.

### Recording slots

Recording is controlled by `max-record-workers`.

Behavior:

- admission happens immediately
- there is no durable recording queue
- if capacity is full, create-job returns `503`
- no job row is created on recording-capacity rejection

Why it works this way:

- live meetings are time-sensitive
- queueing a meeting for much later is usually the wrong behavior

### Build workers

Build uses:

- an in-memory queue
- a configurable worker pool

Build can run in parallel when configured to do so.

### Publish worker

Publish uses:

- an in-memory queue
- exactly one worker

Publish is serialized because every successful publish can replace the shared live site.

## Record, build, publish inside the operator

### Initial attempt

For a new job, the operator:

1. reserves a recording slot
2. persists job and attempt `1`
3. runs record preflight
4. runs `cassini record`
5. promotes a usable `.run` into `current/`
6. queues build
7. runs `cassini build`
8. promotes a successful `.meeting` into `current/`
9. queues publish
10. runs `cassini publish` from the canonical `current/` library
11. promotes the retained attempt-local `.site` into the live site root

### Rerun attempt

Current reruns are **downstream-only**.

That means a rerun:

- does not re-record the meeting
- requires a canonical ready `.run`
- creates a new attempt starting at `build/queued`
- produces fresh attempt-local `.meeting` and `.site` outputs
- can replace the canonical current `.meeting` on success

This is useful when:

- build logic changed
- publish logic changed
- a previous downstream attempt failed

## Stop behavior

The stop action is only valid while a job is:

- `record/running`

A stop request:

- targets the live `cassini record` subprocess
- asks the recorder to terminate cleanly first
- may still leave a usable `.run`

Important nuance:

- stopping recording is not the same thing as cancelling the whole job
- if a usable `.run` exists, build and publish may still continue

## Safe publish and live-site promotion

The operator never publishes directly into the live site root.

Instead it:

1. publishes into a retained attempt-local `.site`
2. stages that site next to the live root
3. swaps the staged site into place on success

This gives Cassini an important failure boundary:

- failed publish attempts do not corrupt the currently served site
- the previous live site can remain available
- each successful attempt keeps its own `.site` output for inspection

## What the control panel sees

The control panel consumes:

- snapshot reads such as `GET /jobs` and `GET /jobs/:id`
- live updates from `GET /events`

See more:

- [Control panel](./components/control-panel.md)
- [Operator API reference](./reference/api.md)

## What the viewer sees

The viewer reads only the published static output.

It does not:

- inspect operator state
- read SQLite
- read work-root artifacts directly
- call the operator API

See more:

- [Viewer](./components/viewer.md)

## Current operational limitations

Important current limitations include:

- no durable worker queue
- no automatic resume after restart
- no automatic retry
- no full re-record rerun mode
- no built-in artifact pruning policy yet

If the operator stops mid-flight:

- queued and running work is marked `interrupted` on next startup
- recovery is explicit through rerun

## Where to go next

- Want the stage details: [Core pipeline](./core-pipeline.md)
- Want exact endpoints: [Operator API reference](./reference/api.md)
- Want exact runtime paths: [Artifacts and filesystem](./reference/artifacts-and-filesystem.md)
