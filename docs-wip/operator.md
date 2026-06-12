# Operator

The operator is Cassini's long-running control-plane service.

Its role is to take the file-driven CLI pipeline and run it as a managed runtime with:

- persisted jobs
- preserved attempts
- stage-specific concurrency control
- stop and rerun endpoints
- live browser updates
- safe live-site promotion

It is intentionally an orchestration wrapper, not a second implementation of recording or transcription.

## What the operator owns

The operator owns:

- HTTP admission and read APIs
- SQLite persistence
- job and attempt state transitions
- recording-slot admission control
- build and publish queues
- subprocess execution of `cassini` commands
- attempt-local logs and artifacts
- promotion of successful publish output into the shared live site
- SSE state-change events for the control panel

The operator does **not** own:

- Talk recording internals
- transcription internals
- viewer rendering
- direct browser playback

## Execution model

The operator shells out to the product CLI.

Current stage commands are:

- `cassini doctor --target record`
- `cassini record`
- `cassini build`
- `cassini publish`

Important nuance:

- the operator preflights only the **record** stage with `doctor`
- it does **not** separately run `doctor` for build or publish
- build and publish therefore rely on the operator environment already containing the required runtime dependencies

## Runtime topology inside the process

```text
HTTP API
  -> runtime controller
  -> SQLite store
  -> record slot pool
  -> build queue + worker pool
  -> publish queue + single worker
  -> SSE event hub
```

Everything currently runs in one process.

## Data model

### Logical jobs

A **job** is the stable top-level unit of work.

A job has:

- one stable job id
- one stored normalized request payload
- one current stage/state summary
- one current attempt number
- one rerun count
- summary-level artifact pointers

The `jobs` table is the summary read model.

### Summary-row artifact semantics

At the job-summary level:

- `artifact_run_path` points at the canonical reusable `current/<job-id>.run`
- `artifact_meeting_path` points at the canonical reusable `current/<job-id>.meeting`
- `artifact_site_path` points at the live shared site root, not at an attempt-local `.site`

### Attempts

An **attempt** is one execution pass for a job.

Attempts preserve:

- `attempt_number`
- `trigger_kind` (`initial` or `rerun`)
- stage/state transitions
- attempt-local artifact paths
- stop metadata
- per-stage log paths
- timestamps

The `job_attempts` table is the history model.

### Attempt-row artifact semantics

For initial attempts:

- `artifact_run_path` points at `runs/<job-id>--attempt-XXX.run`
- `artifact_meeting_path` points at `runs/<job-id>--attempt-XXX.meeting`
- `artifact_site_path` points at `runs/<job-id>--attempt-XXX.site`

For downstream-only rerun attempts:

- no new attempt-local `.run` is recorded
- `artifact_run_path` points at the canonical reusable `current/<job-id>.run`
- record timestamps remain empty because record is skipped
- fresh attempt-local `.meeting` and `.site` artifacts are still created

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

Typical downstream-only rerun lifecycle:

```text
build/queued
-> build/running
-> publish/queued
-> publish/running
-> done/succeeded
```

## HTTP API

The paths below are shown without an operator base-path prefix.
When `--base-path` or `CASSINI_OPERATOR_BASE_PATH` is set, the operator mounts the same API under that prefix.

### Create a job

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

### List jobs

```http
GET /jobs
```

Returns newest-first logical job summaries from the `jobs` read model.

### Get one job

```http
GET /jobs/:id
```

Returns:

- `job`: the logical summary row
- `attempts`: newest-first attempt history

### Stop a job

```http
POST /jobs/:id/stop
```

Behavior:

- valid only for `record/running`
- returns `404` for unknown jobs
- returns `409` when the job is not stoppable
- returns `202` when stop is accepted or already in progress
- sends `SIGTERM` to the live `cassini record` subprocess
- falls back to hard kill if the subprocess does not exit within the grace window

Stopping the recording is not the same thing as cancelling the whole job.
If the recorder finalizes a usable `.run`, the job can still proceed through build and publish.

### Rerun a job

```http
POST /jobs/:id/rerun
```

Behavior:

- valid for terminal jobs (`done/failed` or `done/succeeded`) and for jobs
  marked `interrupted` by the startup sweep
- requires a canonical ready `.run`
- creates a new attempt
- queues that attempt directly at `build/queued`
- returns `202` with the new attempt number
- for Talk-triggered jobs whose recording never reached Nextcloud, also
  re-attempts delivery (stopped callback + upload) from the canonical `.run`

Current reruns are downstream-only. They do not re-record the meeting.

### Events

```http
GET /events
```

Server-Sent Events stream.

Current event types:

- `job.created`
- `job.updated`
- `attempt.updated`

Each event carries:

- the current summary-row `job`
- the current `attempt` when available

The control panel consumes this together with snapshot reads from `GET /jobs` and `GET /jobs/:id`.

## Talk recording backend

The operator also implements Nextcloud Talk's recording-backend protocol, so the Talk "Start recording" button can create and stop jobs directly.

Endpoints (always mounted at the root, never under the operator base path):

- `GET /api/v1/welcome` — protocol handshake; answers `{"version":1}`
- `POST /api/v1/room/{token}` — start/stop commands from Talk

Authentication is Talk's own HMAC scheme: a checksum over the shared secret (`CASSINI_TALK_RECORDING_SECRET`), the `Talk-Recording-Random` header, and the request body. It is independent of any Nextcloud session, which is why these routes are PUBLIC at the AppAPI proxy layer.

Behavior:

- a start command synthesizes a record job for the room's call URL and binds the Talk room state to that job
- the operator notifies Talk back over `/ocs/v2.php/apps/spreed/api/v1/recording/backend` (`started` / `stopped` / `failed`)
- after the job's recording is finalized, the audio file is uploaded to Talk's `recording/{token}/store` endpoint on behalf of the requesting owner

Limitation: the recorder joins calls as a guest, so only public conversations are recordable (see [docs/exapp-install.md](../docs/exapp-install.md)).

## ExApp (Nextcloud AppAPI) surface

When the operator runs as the Nextcloud ExApp image, AppAPI wiring layers on top of the regular runtime (`cassini-operator/internal/operator/exapp.go`):

- `APP_HOST` / `APP_PORT` override the bind address; `APP_SECRET` activates the AppAPI auth middleware around every route
- lifecycle callbacks `PUT /enabled` and `POST /init` are mounted at the root, plus `GET /heartbeat` outside the middleware wrap (AppAPI probes it unauthenticated)
- static prefixes serve the control panel (`/control-panel`), the viewer SPA (`/viewer`), and the published archive (`/published`)
- when AppAPI's persistent volume is mounted (`APP_PERSISTENT_STORAGE`), the default DB, work-root, and site-root paths are redirected under it

The install and Talk-handoff runbook is [docs/exapp-install.md](../docs/exapp-install.md).

## Runtime management

### Recording slots

Recording is managed through a fixed-capacity slot pool controlled by `max-record-workers`.

Behavior:

- admission happens immediately
- there is no durable record queue
- overflow is rejected with `503`

Why this exists:

- recording is tied to real meeting time windows
- queueing a live meeting long after admission would usually be wrong

### Build workers

Build uses:

- an in-memory queue
- a configurable worker pool controlled by `max-build-workers`

Build work is parallelizable.

### Publish worker

Publish uses:

- an in-memory queue
- exactly one worker

Publish is intentionally serialized because every successful publish can replace the shared live site.

### No durable worker queue

Only the job and attempt state is durable.
The live record/build/publish queues are in-memory.

If the operator stops mid-flight:

- queued/running work is marked `interrupted` on next startup
- work is not automatically resumed
- recovery is explicit through rerun

## Record -> build -> publish inside the operator

### Initial attempt flow

For a newly accepted job:

1. reserve a recording slot
2. insert the logical job row and attempt `1`
3. mark `record/running`
4. run `cassini doctor --target record`
5. run `cassini record --call ... --out runs/<job>--attempt-001.run`
6. if a usable run exists, promote it to `current/<job>.run`
7. persist stop metadata and canonical run pointer
8. queue build against the canonical run

### Build flow

For any build-capable attempt:

1. mark `build/running`
2. run `cassini build <canonical run> --out runs/<job>--attempt-00N.meeting`
3. on success, promote that meeting to `current/<job>.meeting`
4. queue publish

### Publish flow

For any publish-capable attempt:

1. mark `publish/running`
2. run `cassini publish <work-root>/current --out runs/<job>--attempt-00N.site`
3. let publish stage only the ready `.meeting` bundles inside `current/`
4. on success, promote the retained attempt `.site` into the live `site-root`
5. persist:
   - summary-row site path = live shared site root
   - attempt-row site path = retained attempt-local `.site`
6. mark `done/succeeded`

### Important consequence of publish-from-`current/`

Every successful operator publish rebuilds the full site library from all canonical ready meetings in `current/`, not just the meeting that triggered this particular publish attempt.

That is why:

- `current/` contains one canonical meeting per job
- reruns replace one job's canonical meeting and then republish the whole library
- publish must remain serialized

## Reruns

### What rerun means today

Current reruns are **not** full meeting re-recordings.

They are:

- fresh downstream processing attempts
- starting from the preserved canonical `.run`
- preserving previous attempts and logs

### Why reruns start at build

The runtime treats:

- live capture as scarce and time-sensitive
- downstream processing as replayable

That gives a safer recovery model:

- do not rejoin the original room
- do not assume the meeting is still available
- do re-run build and publish from preserved capture

### Successful-job reruns

A successful job may also be rerun when its canonical `.run` still exists and is ready.

This supports cases like:

- downstream configuration changes
- exporter fixes
- republishing the live site from a fresher build

## Logging and observability

### Operator logs

The operator itself logs to stdout/stderr.

### Attempt-stage log files

Each attempt also gets stage log files under its attempt logs directory:

- `record.log`
- `build.log`
- `publish.log`

Typical path:

```text
<work-root>/runs/<job-id>--attempt-002.logs/record.log
```

SQLite stores log **paths**, not log bodies.

### Stop metadata

The operator persists record-stop metadata on both the summary row and the current attempt row.

Fields include:

- `stop_reason`
- `stop_requested_at`
- `stop_signal_sent_at`
- `record_exit_code`
- `record_stop_detail`

Current stop reasons include:

- `operator_requested`
- `room_empty`
- `duration_limit`
- `signaling_connection_error`
- `join_failed`
- `record_process_exit_nonzero`

Important nuance:

- `operator_requested` is an operator-owned classification
- several other stop reasons are inferred heuristically from process exit state and recorder logs
- the stop reason explains why recording ended, not whether the whole job failed

### Failure reporting

The operator prefers lightweight persisted error summaries.

On failure it tries to read partial manifests from failed outputs:

- build failure -> partial meeting `cassini.json`
- publish failure -> partial site `cassini.json`

This keeps the API small while preserving deeper logs in files.

## Filesystem layout

Assume:

- `work-root = /var/lib/cassini-operator/jobs`
- `site-root = /srv/cassini-site/published`

### Canonical reusable artifacts

```text
/var/lib/cassini-operator/jobs/current/
  <job-id>.run/
  <job-id>.meeting/
```

### Attempt-local retained artifacts

```text
/var/lib/cassini-operator/jobs/runs/
  <job-id>--attempt-001.run/
  <job-id>--attempt-001.logs/
    record.log
    build.log
    publish.log
  <job-id>--attempt-002.meeting/
  <job-id>--attempt-002.site/
```

### Live published site

```text
/srv/cassini-site/published/
```

### Staging roots

The operator also uses staging roots for promotion, including:

- `current/.staging/` for canonical `.run` and `.meeting` promotion
- `<site-root>.staging/` for live-site replacement and rollback handling

These are transient promotion workspaces, not retained user-facing artifacts.

### Live site promotion

The operator never points `cassini publish --out` directly at the live site root.

Instead it:

1. publishes into a retained attempt-local `.site`
2. copies that site into a staging area next to the live root
3. writes live-site lineage into staged `cassini.json`
4. moves the current live site aside if needed
5. renames the staged site into place
6. removes the backup on success

This exists because standalone `cassini publish` requires an empty output directory.

### Live site lineage

The live site manifest records:

- `published_by_job_id`
- `published_by_attempt_number`
- `published_at_utc`

That lets the deployed site answer which attempt produced the active deployment.

## Startup and restart behavior

On startup the operator:

1. opens SQLite
2. runs embedded schema migrations
3. marks all non-terminal jobs and attempts as `interrupted`
4. starts HTTP and worker runtimes

This is intentionally honest.

It does **not** imply:

- automatic resume
- automatic retry
- restoration of queued build/publish tasks

### Configuration surface

Flags:

- `--bind`
- `--base-path`
- `--db`
- `--work-root`
- `--site-root`
- `--cassini-bin`
- `--max-record-workers`
- `--max-build-workers`

Important environment variables:

- `CASSINI_OPERATOR_BIND_ADDR`
- `CASSINI_OPERATOR_BASE_PATH`
- `CASSINI_OPERATOR_DB_PATH`
- `CASSINI_OPERATOR_WORK_ROOT`
- `CASSINI_OPERATOR_SITE_ROOT`
- `CASSINI_BIN`
- `CASSINI_MAX_RECORD_WORKERS`
- `CASSINI_MAX_BUILD_WORKERS`

The runtime also accepts legacy short aliases for some paths and worker counts:

- `WORK_ROOT`
- `SITE_ROOT`
- `MAX_RECORD_WORKERS`
- `MAX_BUILD_WORKERS`

Default local paths outside containerized deployment resolve under:

```text
cassini-operator/runtime/
```

### Current operational limitations

Important current limitations include:

- no automatic retry or resume after restart
- no durable worker queue
- no retention/pruning policy yet for canonical or attempt-local artifacts
- no built-in log download endpoint
- no full-job re-record rerun mode
- no delete/archive lifecycle for completed jobs
