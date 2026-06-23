# V5 — Implementation reflection

This document captures what was actually built for `planning/initiatives/mvp/slices/V5-failure-inspection-and-rerun-flow` after the shaped implementation slices were completed.

It is the implementation-side companion to:
- `brief.md`
- `shaping.md`
- `slices.md`
- `spike-job-attempts-schema.md`

## Outcome summary

The V5 operator recovery loop is now implemented.

A caller can now:
1. trigger a live Nextcloud Talk recording through `POST /jobs?provider=nextcloud-talk`
2. let the operator persist both a logical job summary and attempt `1`
3. inspect compact logical-job summaries through `GET /jobs`
4. inspect one logical job plus full attempt history through `GET /jobs/:id`
5. reproduce or observe a failure on attempt `1`
6. rerun that failed job through `POST /jobs/:id/rerun`
7. preserve the failed attempt while running a fresh new attempt with isolated artifact and log paths
8. let a successful rerun refresh the shared hosted output through the normal publish path

## What shipped

### I1 — Attempt-history schema and summary projection

The operator no longer stores all meaningful runtime history in a single logical-job row.

Instead, V5 adds a two-level persistence model:

- `jobs`
  - the logical-job summary row
  - still powers `GET /jobs`
  - still carries the projected current/winning state
- `job_attempts`
  - the canonical per-attempt history table
  - keyed by `(job_id, attempt_number)`
  - stores attempt-scoped artifact paths, stop metadata, and log-path fields

The migration surface now includes:
- `0001_initial_jobs.*.sql`
- `0002_record_stop_metadata.*.sql`
- `0003_job_attempts.*.sql`

The V5 migration also backfills all existing V2 rows into synthetic `attempt_number = 1` history rows, so older databases land in a consistent shape.

### I2 — Explicit rerun execution path with attempt-scoped artifacts/logs

The operator now exposes:

```http
POST /jobs/:id/rerun
```

The implemented rerun contract is intentionally conservative:
- only `done/failed` jobs are eligible
- rerun is explicit, never automatic
- rerun reuses the preserved normalized `request_json`
- rerun starts from `record` again, even if the prior failure was in `build` or `publish`

The runtime is now attempt-aware:
- record/build/publish still use the same high-level pipeline
- each execution now carries `(job_id, attempt_number)` through the runtime
- stage transitions keep updating the top-level `jobs` summary row
- the active attempt row in `job_attempts` is updated in parallel

The operator also now isolates attempt outputs:
- `.run` path: `<work-root>/<job-id>--attempt-XXX.run`
- `.meeting` path: `<work-root>/<job-id>--attempt-XXX.meeting`
- logs: `<work-root>/<job-id>--attempt-XXX.logs/`

That turned out to be the right cutline.

It preserves old failures and reruns cleanly without trying to invent resumability semantics the operator still does not actually own.

### I3 — Failure-inspection read surface

The read surface now splits cleanly:

- `GET /jobs`
  - one row per logical job
  - compact summary surface
- `GET /jobs/:id`
  - returns:
    - `job`
    - `attempts[]`

The detail surface now exposes attempt history newest-first, including:
- `attempt_number`
- `trigger_kind`
- `request_json`
- `stage`
- `state`
- attempt-scoped artifact paths
- stop metadata
- attempt log-path fields
- timestamps

This gives the operator the actual inspection surface V5 was shaped to provide, without bloating the list endpoint into a history dump.

## Where the shape matched implementation well

Several shaping decisions translated directly into code with little churn.

### 1. Stable logical job id plus separate attempt history

This held up well.

It preserved the operator-facing job concept while giving the runtime enough room to keep multiple failures/successes visible.

The rejected alternatives would have been materially worse:
- creating a new top-level job id per rerun would have made the list surface noisy
- overwriting one row in place would have destroyed the original failure evidence

### 2. Keeping `jobs` as the summary read model

This also held up well.

The implementation did not need to rewrite the whole operator around a history-first model.
`GET /jobs` stayed cheap and simple, while `GET /jobs/:id` became the richer inspection surface.

### 3. Fresh rerun from `record`

This was the right conservative rule for V5.

It avoided ambiguous behavior like:
- "resume build from the last partial output"
- "reuse the previous run bundle only sometimes"
- "guess which prior artifacts are safe"

The runtime behavior stays explicit and explainable:
- rerun means run the job again from the beginning of the operator-owned pipeline

### 4. Attempt-scoped log-path fields instead of inline log blobs

This also held up well.

The operator continues to stream logs to stdout/stderr, while attempt rows carry enough path metadata for later inspection.
That gives V5 useful observability without turning SQLite into a log store.

## What implementation clarified further

### 1. Summary and attempt rows must move together

The biggest implementation lesson was that V5 could not stop at "insert attempt `1` and rerun rows."

If only the summary row moved while `job_attempts` stayed stale:
- restart interruption behavior would become wrong
- detail inspection would drift from the actual runtime
- rerun history would look fake

So the implementation now updates both:
- `jobs`
- current `job_attempts` row

through the same record/build/publish transitions.

### 2. SQLite locking becomes more visible once writes become transactional

V5 made the store do more transactional work:
- insert logical job + attempt `1`
- update summary row + active attempt row together
- queue rerun attempt + reset summary row together

That exposed `SQLITE_BUSY` during tests until the store was explicitly constrained to a single open connection.

For this embedded operator runtime, that tradeoff is appropriate.

### 3. Attempt path layout has to respect the existing publish contract

The shaped docs allowed a nested attempt directory story conceptually, but the implementation had to respect how `cassini publish` currently discovers ready `.meeting` bundles.

That means the final path layout keeps attempt `.meeting` directories as immediate children under the work root, using attempt-suffixed names:
- `<job-id>--attempt-001.meeting`

This preserved compatibility with the existing publish path while still isolating attempts.

## Deliberate V5 limitations that remain

These are intentional omissions, not regressions.

- no automatic retry or retry policy
- no rerun-with-edits request mutation surface
- no rerun for `interrupted` jobs
- no generalized resumability from arbitrary partial state
- no dedicated log download endpoint yet
- no operator UI/dashboard for browsing attempts

## Test and verification outcome

### Automated coverage completed

The operator tests now cover:
- migration `0003` bootstrap and presence of `job_attempts`
- backfill of legacy rows into synthetic `attempt_number = 1`
- initial trigger creation of attempt `1`
- startup interruption mirroring onto attempt rows
- rerun rejection for unknown and non-failed jobs
- failure on attempt `1` plus successful rerun on attempt `2`
- preservation of the original failed attempt after rerun
- detail read surface returning summary plus attempt history

Implementation-time verification included:
- `cd cassini-operator && go test ./internal/operator`

### Manual validation status

The code path is now ready for manual operator validation, but the agent did not run a full live-room manual acceptance flow for V5.

That means:
- automated operator/runtime verification is complete
- the V5 manual validation path is documented in `testing.md`
- final live operational acceptance should still be performed against a real local Talk stack

## Files and areas touched

The main implementation landed in:
- `cassini-operator/internal/operator/migrations/0003_job_attempts.up.sql`
- `cassini-operator/internal/operator/migrations/0003_job_attempts.down.sql`
- `cassini-operator/internal/operator/attempt_store.go`
- `cassini-operator/internal/operator/attempt_paths.go`
- `cassini-operator/internal/operator/run.go`
- `cassini-operator/internal/operator/record_runtime.go`
- `cassini-operator/internal/operator/build_runtime.go`
- `cassini-operator/internal/operator/publish_runtime.go`
- `cassini-operator/internal/operator/build_store.go`
- `cassini-operator/internal/operator/publish_store.go`
- `cassini-operator/internal/operator/startup_store.go`
- `cassini-operator/internal/operator/run_test.go`
- `cassini-operator/README.md`

## Final implementation shape

The logical-job summary lifecycle is still:

```text
record/queued
-> record/running
-> build/queued
-> build/running
-> publish/queued
-> publish/running
-> done/succeeded
```

But V5 now preserves the attempt-level history behind that summary.

A typical rerun lifecycle is now:

```text
attempt 1
-> record/running
-> build/running
-> done/failed

POST /jobs/:id/rerun

attempt 2
-> record/queued
-> record/running
-> build/queued
-> build/running
-> publish/queued
-> publish/running
-> done/succeeded
```

The top-level `jobs` row now reflects attempt `2`, while attempt `1` remains visible in `job_attempts`.

## Suggested next move after V5

The most natural next move is not more persistence reshaping.

The main follow-on opportunities are:
- manual live-stack validation of the full V5 rerun path
- operator README/API polish if external callers need examples of the new detail response
- deciding whether `interrupted` jobs should later become rerunnable
- deciding whether attempt log download deserves its own endpoint
