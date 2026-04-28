# V1 Brief — Trigger jobs, job records, and publish refresh

## What this slice is

V1 is the first backend control-plane slice for the MVP.

The outcome is simple and operator-facing:

- an operator can send a trigger request
- the system returns a job identifier immediately
- the work continues asynchronously in the background
- job state can be checked later
- seeded meeting artifacts can be published into the hosted library through the same flow

This slice is deliberately **not** about real Nextcloud Talk capture yet. It exists to prove the trigger → job record → worker → publish-refresh loop before V2 swaps seeded artifacts for live recording.

## Why this matters now

Right now Cassini already has the core artifact flow (`record`, `build`, `publish`, `serve`), but there is no lightweight operator surface for starting work remotely and tracking it after the request returns.

V1 creates that missing backbone. Later slices depend on it:

- V2 uses the same job flow for real Talk capture
- V5 extends the same job records for failure inspection and reruns
- V6 packages this flow for self-hosted pilots

## Scope of the slice

This slice should deliver the minimum backend surface needed to prove background execution and publish refresh:

- `POST /jobs?provider=nextcloud-talk` to accept a trigger request and create a job
- provider-specific request validation before a job is accepted
- persisted job records in **SQLite** in a single `jobs` table with:
  - stable **ULID** job id
  - original request payload
  - current stage and state
  - per-stage timestamps
  - artifact references needed for status and publish inspection
- asynchronous worker pickup of accepted jobs inside the same long-running process
- worker stages for:
  - record (placeholder in V1)
  - build
  - publish
- record-stage admission control with a busy response when max active recording workers is exceeded
- build queueing behind a configurable build worker pool
- sequential publish through a single publish worker
- publish refresh using **seeded meeting artifacts** rather than live capture
- `GET /jobs` to list jobs (newest first)
- `GET /jobs/:id` to return full persisted job data, including transition timestamp fields

## Explicitly out of scope

These belong to later slices and should not be pulled into V1:

- real Nextcloud Talk capture work from the worker
- summary generation
- rerun endpoint / retry semantics
- deployment packaging
- a full operator UI or dashboard

## Expected effort

**Effort: medium**

This is still a focused backend slice, but it now has a clearer internal shape: HTTP handling, SQLite persistence, staged worker orchestration, and publish integration.

A reasonable planning read is:

- **core implementation effort:** medium
- **coordination / decision risk:** medium
- **overall confidence:** moderate, because the repo already has build/publish machinery but not the job/control plane yet

If kept intentionally thin and built on top of existing Cassini commands and bundle/manifest paths, this should stay manageable. If the implementation tries to over-design queueing, retries, auth, or a broader operator product surface up front, effort will expand quickly.

## What “done” should look like

A reviewer should be able to:

1. send `POST /jobs?provider=nextcloud-talk`
2. get back a job id immediately
3. see that a job record was persisted in SQLite
4. observe background processing without holding the request open
5. call `GET /jobs` and `GET /jobs/:id`
6. see stage / state transitions for accepted jobs
7. confirm that V1 created a destination artifact from a fixture as the recording placeholder
8. confirm that build ran through the existing Cassini flow
9. confirm that seeded artifacts were published into the hosted library after the worker ran

## Likely code areas

Based on the current repo shape, the likely touch points are:

- `cassini-go-recorder/cmd/cassini/main.go`
- new `cassini operator` service / worker code under `cassini-go-recorder/internal/cassini`
- job persistence and status/log handling around a small SQLite `jobs` store
- build orchestration around `cassini-go-recorder/internal/cassini/build.go`
- publish orchestration around `cassini-go-recorder/internal/cassini/publish.go`
- configuration for database path, `FIXTURE_URL`, `FIXTURE_PATH`, artifact/work roots, and published output

## Clarified implementation choices

These points are now intentionally chosen for V1 rather than left open:

### 1. Job persistence uses SQLite

The job log should be durable and queryable without introducing a separate database service.

SQLite is the selected persistence mechanism for V1.

### 2. API and worker live in the same process

V1 should run as one long-lived process that owns both:

- the HTTP server
- the worker orchestration

This keeps the slice locally demoable and self-hostable without coordinating separate API and worker roles yet.

### 3. Worker stages are separated

The execution model is intentionally staged:

- **record workers** — placeholder in V1, capped by a configurable max; overflow returns busy and creates no job row
- **build workers** — configurable max; excess work waits in a queue
- **publish worker** — single worker to keep publish sequential

### 4. Recording is simulated in V1, but execution is end-to-end

V1 does not include real Nextcloud Talk capture.

Instead:

- the record worker uses `FIXTURE_PATH` if present, otherwise pulls `FIXTURE_URL` into `FIXTURE_PATH`
- startup verifies `FIXTURE_PATH` ends with `.mkv`
- `FIXTURE_PATH` defaults sensibly to `harness/runtime/operator-fixture.mkv`
- it materializes a fresh per-job `.run` artifact (`recording.mkv` + `cassini.json`) as if recording had happened
- build then runs the real `cassini build` flow via CLI invocation from `cassini-operator`
- publish then runs the real `cassini publish` flow via CLI invocation from `cassini-operator`
- Cassini CLI resolution uses configured `CASSINI_BIN` when set; otherwise dev defaults to `<reporoot>/bin/cassini`

This keeps V1 honest about job execution without pulling live capture into scope.

### 5. Minimal API surface is now clearer

V1 should expose:

- `POST /jobs?provider=nextcloud-talk`
- `GET /jobs`
- `GET /jobs/:id`
- `cassini operator start` as a convenience launcher for the separate operator package/binary

`cassini operator start` resolves `CASSINI_OPERATOR_BIN` first, otherwise defaults to `<reporoot>/bin/cassini-operator`, and fails fast if the selected path does not exist or is not executable.

`GET /jobs` should return newest first.

The request body stays provider-shaped, with a minimal V1 `nextcloud-talk` form:

- `platform: "nextcloud-talk"`
- `url`

## Remaining unknowns and blanks that should stay explicit

The slice shape is much clearer now, but several implementation details are still open and should remain explicit.

### 1. SQLite shape is now mostly chosen, with one remaining sufficiency check

The selected persistence technology and broad single-table model are known.

Selected shape:

- job ids use **ULID**
- `stage` uses: `record`, `build`, `publish`, `done`
- `state` uses: `queued`, `running`, `succeeded`, `failed`, `interrupted`
- the `jobs` row should carry:
  - `id`
  - `provider`
  - `request_json`
  - `stage`
  - `state`
  - `artifact_run_path`
  - `artifact_meeting_path`
  - `artifact_site_path`
  - `error`
  - `created_at`
  - `updated_at`
  - `record_queued_at`
  - `record_started_at`
  - `record_finished_at`
  - `build_queued_at`
  - `build_started_at`
  - `build_finished_at`
  - `publish_queued_at`
  - `publish_started_at`
  - `publish_finished_at`
  - `interrupted_at`
  - `completed_at`

Still open:

- whether this concrete `jobs` shape is sufficient in practice
- how artifact references are normalized

### 2. Startup interruption marking is selected

V1 should perform a startup pass that marks every non-completed persisted job as interrupted, including queued jobs.

Selected:

- `completed` means terminal `succeeded` or `failed`
- queued jobs and in-flight jobs both become `interrupted` on startup while preserving their last stage

### 3. Exact request contract is still only partially fixed

Known:

- requests are provider-shaped
- initial `nextcloud-talk` payload includes at least `platform` and `url`
- validation happens before a job is accepted

Still open:

- whether any aliasing exists or only the exact `nextcloud-talk` provider name is accepted
- whether idempotency matters in V1

### 4. Seeded artifact source contract is now partly chosen, but still open in detail

The flow will use `FIXTURE_URL` and `FIXTURE_PATH` for a configured generated fixture MKV rather than a checked-in fixture artifact.

Known:

- startup verifies `FIXTURE_PATH` ends with `.mkv`
- if `FIXTURE_PATH` exists, workers reuse it
- if `FIXTURE_PATH` does not exist, workers fetch `FIXTURE_URL` into `FIXTURE_PATH`

Still open:

- how local/demo setup prefers to prewarm or generate that fixture
- whether future slices allow request-selected fixture variants

### 5. Build / publish invocation now uses the CLI boundary from the separate operator package

V1 should keep `cassini-operator` separate and invoke the existing `cassini build` / `cassini publish` CLI flows rather than refactor shared orchestration now.

Known:

- this preserves the current build orchestration behavior, including doctor checks
- this keeps V1 minimal despite `cassini-go-recorder/internal/cassini` being inaccessible to a separate sibling package
- the operator should use process stdout/stderr as the sensible V1 logging default
- on failure, the operator should read partial bundle `cassini.json` manifests from known output paths to recover `stage` and manifest `error`

Selected:

- `cassini operator start` is a pure launcher
- it resolves `CASSINI_OPERATOR_BIN` first, otherwise defaults to `<reporoot>/bin/cassini-operator`
- it fails fast if the selected path is missing or not executable
- it prints one dev-friendly launch line before `exec`, e.g. `operator -> /abs/path/to/cassini-operator`

Still open:

- whether manifest `stage` + manifest `error` is sufficient V1 failure detail in practice

### 6. Endpoint protection expectations are still open

V1 assumes reverse-proxy or other external protection only.

Still open:

- whether rate limiting matters in this slice

### 7. Status payload detail is still only partially defined

`GET /jobs/:id` must return full persisted job data, including transition timestamp fields.

Known:

- current stage/state must be visible
- job identity is a stable ULID
- timestamps and artifact references matter

Still open:

- whether the full-data response needs any pagination or size guardrails in V1
- whether `GET /jobs` returns exactly the same row shape as detail view or a strict subset/superset

Selected:

- worker logs are omitted from the persisted V1 response shape; process stdout/stderr is the sensible logging default instead

### 8. V1 vs V5 boundary still needs discipline

V1 should preserve enough job metadata to make V5 possible, but it should not try to fully solve failure inspection and rerun semantics now.

That boundary remains:

- **V1:** create jobs, persist state, run in background, refresh publish output, expose status
- **V5:** deepen failure detail, preserve rerun inputs, add rerun behavior

## Guidance for whoever picks this up

Keep this slice thin and orchestration-focused.

Good V1 behavior is:

- minimal API
- SQLite-backed durable job state
- staged workers with explicit admission / queue behavior
- fixture-backed recording placeholder
- build and publish through the existing Cassini CLI flow from the separate operator package
- clear status transitions
- failure detail sourced cheaply from partial bundle manifests and process output

Bad V1 behavior is:

- redesigning the core Cassini pipeline
- overbuilding a distributed job system
- solving live capture, retries, and packaging all at once

The main job here is to establish the control surface and state model that the rest of the MVP can safely build on.
