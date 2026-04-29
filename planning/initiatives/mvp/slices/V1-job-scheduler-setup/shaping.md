---
shaping: true
---

# V1 — Shaping

This document shapes **V1: Trigger jobs, job records, and publish refresh**.

It stays within the slice boundary already defined in:

- `planning/initiatives/mvp/tickets.md`
- `planning/initiatives/mvp/slices.md`
- `planning/initiatives/mvp/shaping.md`

The goal here is not to re-scope V1. The goal is to choose a concrete implementation shape for the slice.

## Requirements (R)

| ID | Requirement | Status |
|----|-------------|--------|
| R0 | An operator can trigger Cassini work remotely and receive a stable job identifier immediately, without keeping the request open for the full run. | Core goal |
| R1 | Accepted jobs persist in SQLite in a single `jobs` table with the original request payload, current stage/state, per-stage timestamps, and artifact references needed for status and publish inspection. | Must-have |
| R2 | The first cut runs as one long-lived process that owns both the API server and worker orchestration, while continuing job work asynchronously after the trigger request returns. | Must-have |
| R3 | Job execution is stage-separated: recording admission is capped and returns busy when full, build work waits behind a configurable worker pool, and publish runs sequentially through a single worker. | Must-have |
| R4 | A small API surface supports provider-specific validation and operator reads: `POST /jobs?provider=nextcloud-talk`, `GET /jobs`, and `GET /jobs/:id`, with clear unknown-provider, missing-data, and busy errors. Both read endpoints return full persisted job rows, including transition timestamp fields. | Must-have |
| R5 | V1 recording is a placeholder that uses fixtures / seeded input to materialize a destination artifact, after which build and publish run end-to-end through the real pipeline. | Must-have |
| R6 | Build and publish reuse existing Cassini build/publish machinery through CLI invocation from the separate `cassini-operator` package, rather than introducing a parallel implementation. | Must-have |
| R7 | The V1 implementation leaves a clean extension path for V2 live capture and V5 failure inspection/rerun work. | Must-have |
| R8 | V1 stays narrowly scoped to trigger/status/orchestration and does not absorb live capture, summary generation, reruns, or a full operator UI. | Must-have |

---

## CURRENT: Manual CLI flow with no durable job surface

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **CURRENT1** | Operator runs Cassini commands manually (`record`, `build`, `publish`, `serve`) from shell access. | |
| **CURRENT2** | Existing `build` and `publish` logic already create and finalize bundle/manifest state on disk. | |
| **CURRENT3** | There is no trigger API, no SQLite-backed job model, and no staged worker loop for operator-facing orchestration. | |
| **CURRENT4** | There is no admission/queue behavior for remote recording or build work, and no operator-facing status endpoint. | |

## A: Single-process trigger service with SQLite jobs and staged worker pools

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **A1** | Run one long-lived service process that exposes `POST /jobs`, `GET /jobs`, and `GET /jobs/:id`. | |
| **A2** | Persist accepted jobs in a single SQLite `jobs` table with a ULID job id, request payload, `stage` (`record`/`build`/`publish`/`done`), `state` (`queued`/`running`/`succeeded`/`failed`/`interrupted`), per-stage timestamps, and artifact references. | |
| **A3** | Admit work through a record stage with configurable concurrency; when all record workers are busy, `POST /jobs?provider=nextcloud-talk` logs the busy rejection in a repo-compatible way and returns a busy error without creating a job row. | |
| **A4** | Record workers are goroutines that do V1 fixture materialization: verify `FIXTURE_PATH` ends with `.mkv`, default it sensibly to `harness/runtime/operator-fixture.mkv`, reuse it if present, otherwise fetch `FIXTURE_URL` into `FIXTURE_PATH`, then create a fresh per-job `.run` artifact around that MKV as if recording had happened. | |
| **A5** | Build workers are goroutines behind a configurable queue; they run the real Cassini build flow and persist build-stage transitions back to SQLite. | |
| **A6** | A single publish worker processes ready jobs sequentially, reusing the existing Cassini publish flow and persisting publish outputs / terminal state. | |
| **A7** | API reads come entirely from SQLite so status/list responses do not depend on process memory. | |

## B: Split API process and worker process over a shared SQLite queue

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **B1** | Run one API service process that accepts `POST /jobs`, `GET /jobs`, and `GET /jobs/:id` but does not execute jobs itself. | |
| **B2** | Persist jobs and transitions in SQLite. | |
| **B3** | Run a separate worker process that polls SQLite, claims jobs, and advances record/build/publish stages asynchronously. | |
| **B4** | Keep the same staged execution model: recording admission, queued builds, and single-threaded publish. | |
| **B5** | Reuse the existing Cassini build/publish flow from the worker process. | |

## C: Single-process API with direct ad-hoc goroutines per request

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **C1** | `POST /jobs?provider=nextcloud-talk` validates input, inserts a SQLite job row, and immediately spawns chained goroutines for record/build/publish work for that request. | |
| **C2** | Recording limits are enforced only at request admission time; build and publish coordination are handled ad hoc rather than by explicit stage queues. | |
| **C3** | Job rows are updated as work progresses, but restart/recovery and publish serialization depend on in-memory coordination rather than a clear staged execution model. | |
| **C4** | V1 recording still uses fixtures and build/publish still reuse existing Cassini flow. | |

---

## Fit Check

| Req | Requirement | Status | CURRENT | A | B | C |
|-----|-------------|--------|---------|---|---|---|
| R0 | An operator can trigger Cassini work remotely and receive a stable job identifier immediately, without keeping the request open for the full run. | Core goal | ❌ | ✅ | ✅ | ✅ |
| R1 | Accepted jobs persist in SQLite in a single `jobs` table with the original request payload, current stage/state, per-stage timestamps, and artifact references needed for status and publish inspection. | Must-have | ❌ | ✅ | ✅ | ✅ |
| R2 | The first cut runs as one long-lived process that owns both the API server and worker orchestration, while continuing job work asynchronously after the trigger request returns. | Must-have | ❌ | ✅ | ❌ | ✅ |
| R3 | Job execution is stage-separated: recording admission is capped and returns busy when full, build work waits behind a configurable worker pool, and publish runs sequentially through a single worker. | Must-have | ❌ | ✅ | ✅ | ❌ |
| R4 | A small API surface supports provider-specific validation and operator reads: `POST /jobs?provider=nextcloud-talk`, `GET /jobs`, and `GET /jobs/:id`, with clear unknown-provider, missing-data, and busy errors. Both read endpoints return full persisted job rows, including transition timestamp fields. | Must-have | ❌ | ✅ | ✅ | ✅ |
| R5 | V1 recording is a placeholder that uses fixtures / seeded input to materialize a destination artifact, after which build and publish run end-to-end through the real pipeline. | Must-have | ❌ | ✅ | ✅ | ✅ |
| R6 | Build and publish reuse existing Cassini build/publish machinery instead of introducing a parallel implementation. | Must-have | ✅ | ✅ | ✅ | ✅ |
| R7 | The V1 implementation leaves a clean extension path for V2 live capture and V5 failure inspection/rerun work. | Must-have | ❌ | ✅ | ✅ | ❌ |
| R8 | V1 stays narrowly scoped to trigger/status/orchestration and does not absorb live capture, summary generation, reruns, or a full operator UI. | Must-have | ✅ | ✅ | ✅ | ✅ |

**Notes:**
- CURRENT fails R0, R1, R2, R3, R4, R5, and R7 because it still depends on manual shell orchestration and has no job/control surface.
- B fails R2 because it introduces separate API and worker roles before V1 has proven the simpler single-process control loop.
- C fails R3 because direct per-request goroutines do not clearly express the required staged queueing/serialization model, and it fails R7 because restart/recovery and future extension work stay too implicit.

---

## Selected shape

**Selected shape: A — Single-process trigger service with SQLite jobs and staged worker pools**

Why A currently fits best:

- it satisfies the full V1 requirement set without adding multi-process coordination
- it matches the explicit V1 constraint that server and workers live in the same long-running process
- it gives the slice a durable SQLite-backed job surface for later V2/V5 work
- it makes the worker model concrete: capped record admission, queued build work, and serialized publish
- it preserves the existing Cassini build/publish path rather than re-implementing pipeline logic

## Detail A

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **A1** | Add one small long-running `cassini-operator` service package plus a `cassini operator start` convenience command. The convenience command is a pure launcher: it resolves the operator binary and `exec`s it, forwarding args through unchanged rather than defining a second operator CLI contract. Resolution is strict fail-fast: first `CASSINI_OPERATOR_BIN`, otherwise `<reporoot>/bin/cassini-operator`; the selected path must exist and be executable. Before `exec`, it prints a single dev-friendly launch line such as `operator -> /abs/path/to/cassini-operator`. | |
| **A2** | `POST /jobs?provider=nextcloud-talk` validates the provider-specific payload, checks record-stage admission, inserts the accepted job into SQLite, enqueues record work, and returns the stable job id immediately. If record capacity is full, it logs busy and returns no job row. | |
| **A3** | `GET /jobs` lists full persisted job rows from SQLite ordered newest-first by ULID/creation time; `GET /jobs/:id` returns the full persisted job record for one job; both include all persisted transition timestamp fields directly on the job row. | |
| **A4** | Record workers are configurable by env var; in V1 they do not capture a live meeting, but instead ensure `FIXTURE_PATH` points to an `.mkv`, default it to `<reporoot>/cassini-operator/.runtime/operator-fixture.mkv`, lazily fetch `FIXTURE_URL` into `FIXTURE_PATH` when missing, then create a fresh per-job `.run` artifact around that MKV so downstream build sees a normal build-compatible recording artifact. The V1 `.run` contains `recording.mkv` + `cassini.json` only. | |
| **A5** | Build workers are configurable by a separate env var; when all build workers are busy, accepted jobs wait in a build queue until a worker invokes the existing `cassini build` CLI flow for that job. Cassini binary resolution uses configured `CASSINI_BIN` when set; otherwise dev defaults to `<reporoot>/bin/cassini`. | |
| **A6** | One publish worker processes publish-ready jobs sequentially and refreshes the hosted library by invoking the existing `cassini publish` CLI flow through the same Cassini binary resolution strategy. | |
| **A7** | `cassini-operator` uses process stdout/stderr as the V1 logging default and, on build/publish failure, reads partial bundle `cassini.json` manifests from known output paths so V1 gets lightweight failure reporting from manifest `stage` + manifest `error` without refactoring shared orchestration. | |
| **A8** | SQLite stores current job state, per-stage timestamps, request payload, artifact paths, terminal outcome, and lightweight failure detail in a single `jobs` table with concrete fields: `id`, `provider`, `request_json`, `stage`, `state`, `artifact_run_path`, `artifact_meeting_path`, `artifact_site_path`, `error`, `created_at`, `updated_at`, `record_queued_at`, `record_started_at`, `record_finished_at`, `build_queued_at`, `build_started_at`, `build_finished_at`, `publish_queued_at`, `publish_started_at`, `publish_finished_at`, `interrupted_at`, and `completed_at`. | |
| **A9** | Service startup marks every non-completed persisted job as interrupted, including queued jobs, while preserving the last job stage. In V1, `completed` means terminal `succeeded` or `failed`; everything else becomes `interrupted` on startup. Artifact cleanup or repair for interrupted jobs is explicitly out of scope for V1. | |
| **A10** | `POST /jobs?provider=nextcloud-talk` keeps a minimal V1 request body: `{ "platform": "nextcloud-talk", "url": "..." }`. | |
| **A11** | Configuration stays intentionally small: bind address, SQLite path, `FIXTURE_URL`, `FIXTURE_PATH` (with a sensible default), Cassini CLI path/lookup strategy, work/artifact roots, published-site root, max record workers, and max build workers. By default, operator SQLite/work/fixture artifacts live under `<reporoot>/cassini-operator/.runtime/`. | |

## Breadboard and implementation slices

The detailed breadboard and the implementation slicing for Shape A now live in `planning/initiatives/mvp/slices/V1-job-scheduler-setup/slices.md`.

That document is now the ground truth for:

- the UI and non-UI affordance tables
- the wiring diagram grouped by place
- the ordered implementation slices used to build and verify V1 incrementally

## Remaining open implementation checks

Most shaping decisions are now selected. The remaining checks before implementation are:

1. **SQLite sufficiency / normalization**
   - whether the selected single-row `jobs` schema is sufficient in practice
   - how artifact references are normalized

2. **Failure detail contract**
   - whether manifest `stage` + manifest `error` is enough for V1
   - whether stderr tail should also be copied into `jobs.error` or only emitted to process output

## Suggested next shaping move

The shape is breadboarded enough to move into implementation planning for:

- the separate `<reporoot>/cassini-operator` package and module
- the `cassini operator start` pure-launcher path
- CLI-based build/publish execution through resolved Cassini binaries
- fixture acquisition and per-job `.run` materialization
- the selected single-row `jobs` schema and lightweight failure extraction
