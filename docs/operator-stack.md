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
- one immutable, digest-verified meeting artifact per attempt
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
- record/build/seal/publish execution
- current artifact promotion
- live-site promotion
- artifact retention

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
- the latest successful `.meeting` bundle per logical job
- the latest sealed portable `.opus` per logical job

```text
  <work-root>/current/
    <job-id>.run       the reusable source media a rerun rebuilds from
    <job-id>.meeting   the build output, and what the seal packs
    <job-id>.opus      the canonical portable meeting
```

`current/<job-id>.opus` is a **promotion** of the artifact an attempt sealed —
a hard link where the filesystem allows one — not an independent pack of the same
meeting. It is what a job's downloadable meeting file is, and it is byte-identical
to the file the live site serves.

> `.meeting` is no longer a publish input. The operator keeps it in `current/`
> because a rerun and a re-seal read it, and because it is the easiest thing to
> inspect while debugging — but the artifact that gets published is the sealed
> `.opus`. Retiring the `.meeting` bundle entirely is tracked in the D-425
> retirement inventory.

## Why `current/` matters

The easiest way to understand publish in operator mode is:

1. each attempt may create attempt-local outputs under `runs/`
2. successful artifacts are promoted into `current/`
3. the seal packs the attempt's own `.meeting` into an immutable
   `runs/<job>--attempt-NNN.seal/<job-id>.opus`, records its digest, and promotes
   it to `current/<job-id>.opus`
4. publish delivers that exact sealed file, re-checking its digest first
5. the sink merges it into the live site, catalog last

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

### Seal worker

Sealing uses:

- an in-memory queue
- exactly one worker
- a per-run timeout, so a hung `cassini pack` cannot hold the worker forever

Sealing is off the build worker's critical path — the build hands the job over and
returns — but it is **not** off the pipeline's. A job cannot become
`publish/queued` without it.

### Publish worker

Publish uses:

- an in-memory queue
- exactly one worker

Publish is serialized because every successful publish can replace the shared live site.

## Record, build, seal, publish inside the operator

```text
  record ──▶ build ──▶ seal ──▶ publish ──▶ done
                        │
                        ├─ cassini pack   runs/<job>--attempt-NNN.meeting
                        │                   -> runs/<job>--attempt-NNN.seal/<job>.opus
                        ├─ sha256(file)   -> job_attempts.artifact_opus_sha256
                        └─ promote        -> current/<job>.opus   (atomic rename)
```

### Initial attempt

For a new job, the operator:

1. reserves a recording slot
2. persists job and attempt `1`
3. runs record preflight
4. runs `cassini record`
5. promotes a usable `.run` into `current/`
6. queues build
7. runs `cassini build`
8. stamps the Talk room name into the attempt `.meeting`, then promotes it into `current/`
9. queues seal
10. runs `cassini pack` on the attempt `.meeting`, producing the attempt's immutable `.opus`
11. records that file's SHA-256 and promotes it to `current/<job>.opus`
12. queues publish
13. runs `cassini publish` on that exact sealed `.opus`, after re-checking its digest
14. hands the attempt-local `.site` to the publish sink, which verifies the staged asset against the sealed digest and upserts that meeting into the live site root (leaving every other meeting untouched)

### The seal stage

Packing the portable `.opus` used to be a background task started *after* the job
had been handed to the publish worker. It could fail, be killed by a restart, or
be overtaken by a rerun writing the same path — and none of that failed the job,
so a recording could reach `done/succeeded` with its canonical meeting file
missing or belonging to a different attempt. Publish had to prefer the `.meeting`
bundle as a result.

Sealing is now a stage of the job:

- **its success gates publishing.** The only transition into `publish/queued`
  writes the sealed artifact's path and digest in the same database transaction,
  so a queued publish always has a verified artifact behind it.
- **its artifact is attempt-scoped and immutable.** A rerun seals its own file
  under `runs/<job>--attempt-NNN.seal/`; it never rewrites what a queued publish
  is about to deliver. The file is named for the job rather than the attempt
  because the static-site exporter derives a meeting's catalog id from the input
  file's stem — an attempt-named file would publish a rerun as a second, separate
  meeting.
- **its failure is a job failure.** `cassini pack` verifies its own output before
  renaming it into place, so a zero exit means packed *and* integrity-checked; the
  operator adds the two things pack cannot report — the file exists, and it is not
  empty — and then records its digest. Anything else ends the job at
  `done/failed` with the reason recorded. A rerun is the retry, and it rebuilds
  from `current/<job>.run`, so nothing is lost.
- **it survives a restart.** A `seal/queued` row is excluded from the startup
  interrupted-sweep and re-delivered by the requeue dispatcher, exactly like
  `build/queued` and `publish/queued`. A `seal/running` row lost its subprocess
  and becomes `seal/interrupted`, which is a rerun candidate.

The digest travels the whole way: it is recorded at seal time, re-checked before
the publish subprocess spawns, and re-checked on the staged copy before the sink
commits it. That is what makes "the meeting you download is the meeting this
attempt sealed" a verified claim rather than a naming convention.

### Rerun attempt

Current reruns are **downstream-only**.

That means a rerun:

- does not re-record the meeting
- requires a canonical ready `.run`
- creates a new attempt starting at `build/queued`
- produces fresh attempt-local `.meeting`, `.seal` and `.site` outputs
- can replace the canonical current `.meeting` and `.opus` on success

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
2. hands that site to the publish sink
3. the sink merges the one meeting it contains into the live site root

The merge order is the failure boundary, and it runs in this direction on purpose:

```text
  1. asset     stage beside its destination, verify its digest, rename into place
  2. shell     index.html / assets, staged and swapped (only when the attempt
               site carries a shell — the ExApp image serves it from the image)
  3. manifest  cassini.json lineage
  4. catalog   catalog.json, written atomically, LAST
```

A crash before step 4 leaves an unreferenced file: invisible, harmless. A crash
after it has everything the catalog names already on disk. The reverse order
would publish a catalog pointing at audio that is not there yet.

This gives Cassini an important failure boundary:

- failed publish attempts do not corrupt the currently served site
- a failed shell refresh leaves the previous, working viewer in place
- an asset whose digest does not match what the job sealed is refused before the
  catalog can name it
- each successful attempt keeps its own `.site` output for inspection, subject to
  the retention policy below

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

## Artifact retention

Per-attempt working files under `runs/` used to accumulate forever. An explicit
policy now bounds them, selected with `--artifact-retention` /
`CASSINI_ARTIFACT_RETENTION`:

| Policy | Prunes |
|--------|--------|
| `all` | nothing — the behaviour before this policy existed, and the escape hatch |
| `superseded` | the heavy payloads of attempts a rerun has replaced |
| `sealed` **(default)** | `superseded`, plus a succeeded attempt's `.run`, `.meeting` and `.site` |

```text
  <work-root>/
    current/                      NEVER pruned — canonical .run/.meeting/.opus,
                                  which is what reruns and debugging read
    runs/
      <job>--attempt-NNN.run      ┐
      <job>--attempt-NNN.meeting  ├─ prunable: duplicated in current/, or transient
      <job>--attempt-NNN.site     ┘
      <job>--attempt-NNN.seal     kept — the artifact that was published
      <job>--attempt-NNN.logs     NEVER pruned — the forensic record
  <site-root>/                    NEVER pruned — deleting published recordings is
                                  a separate, user-facing decision
```

Every removal is guarded on the artifact that replaces it existing, so nothing
here removes the last copy of anything: a record that failed before promotion
keeps its attempt `.run`, and a failed job keeps everything. Removals are logged
with the reason. The sweep runs after a successful publish and once at startup.
An unrecognised policy name is rejected at startup with exit code 2.

Attempt rows keep the paths of pruned artifacts — the row records what the
attempt produced, the policy governs whether the bytes are still there — so under
`sealed` a succeeded attempt's `artifact_site_path` names a directory that has
been reclaimed. The `artifact retention removed` log line is what says why.

## Current operational limitations

Important current limitations include:

- no durable worker queue
- no automatic resume after restart
- no automatic retry
- no full re-record rerun mode
- retention is attempt-scoped only: the number of jobs, `current/`, and the live
  site are still unbounded, with no byte or age cap over the work root

If the operator stops mid-flight:

- queued and running work is marked `interrupted` on next startup
- recovery is explicit through rerun

## Where to go next

- Want the stage details: [Core pipeline](./core-pipeline.md)
- Want exact endpoints: [Operator API reference](./reference/api.md)
- Want exact runtime paths: [Artifacts and filesystem](./reference/artifacts-and-filesystem.md)
