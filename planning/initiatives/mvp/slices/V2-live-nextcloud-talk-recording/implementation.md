# V2 — Implementation reflection

This document captures what was actually built for `planning/initiatives/mvp/slices/V2-live-nextcloud-talk-recording` after the shaped implementation slices were completed.

It is the implementation-side companion to:
- `brief.md`
- `shaping.md`
- `slices.md`
- `spike.md`
- `spike-stop-metadata-and-migrations.md`

## Outcome summary

The V2 operator path is now implemented through the three planned implementation slices.

A caller can now:
1. trigger a real Nextcloud Talk recording through `POST /jobs?provider=nextcloud-talk`
2. pass live-record controls such as `guestName`, `duration`, `stopWhenRoomEmpty`, and `roomEmptyGrace`
3. have the operator run `cassini doctor --target record` and then `cassini record`
4. optionally stop a running record-stage job through `POST /jobs/:id/stop`
5. let a finalized live `.run` continue through the existing build → publish backbone
6. inspect persisted stop metadata through `GET /jobs` and `GET /jobs/:id`
7. restart the operator and still get the same honest interruption behavior from V1

## What shipped

### I1 — Live Talk capture end-to-end happy path

The V1 fixture-backed record placeholder is gone from the operator runtime.

The record stage now:
- validates and normalizes the V2 trigger body
- runs `cassini doctor --target record`
- launches `cassini record --call <url> --out <job>.run --name <guestName>`
- appends `--duration`, `--stop-when-room-empty`, and `--room-empty-grace` only when explicitly requested
- keeps one canonical per-job `.run` path under the operator work root
- reuses the existing build and publish subprocess boundaries once a usable `.run` exists

The explicit stop path also shipped as part of this runtime slice:
- `POST /jobs/:id/stop`
- valid only while the job is `record/running`
- returns `404`, `409`, or `202` per the shaped contract
- sends `SIGTERM` first
- waits a bounded grace window
- falls back to hard kill only if the recorder does not exit
- continues into build/publish when the run finalizes cleanly

### I2 — Migration runner + baseline bootstrap

The operator no longer depends on a hardcoded `CREATE TABLE IF NOT EXISTS jobs` string as its long-term schema source.

Instead it now:
- embeds numbered SQL migrations from `cassini-operator/internal/operator/migrations/`
- tracks applied versions in `schema_migrations`
- auto-applies pending **up** migrations on startup
- never auto-runs down migrations
- baselines existing V1-shaped databases before migrating upward
- fails fast if migration history is inconsistent

The implemented migration set is:
- `0001_initial_jobs.*.sql` — extracted V1 baseline jobs schema
- `0002_record_stop_metadata.*.sql` — V2 stop metadata fields

### I3 — Stop metadata persistence + read surface

The operator job row now persists stop metadata directly.

The implemented fields are:
- `stop_reason`
- `stop_requested_at`
- `stop_signal_sent_at`
- `record_exit_code`
- `record_stop_detail`

Those fields are now returned through both:
- `GET /jobs`
- `GET /jobs/:id`

The current operator classification model is:
- `operator_requested` when the operator accepted stop and the recorder finalized successfully
- recorder-derived classifications such as `room_empty`, `duration_limit`, `signaling_connection_error`, `join_failed`, and `record_process_exit_nonzero` inferred from subprocess exit status plus recorder log text

## Where the shape matched implementation well

Several shaping decisions translated directly into code with little churn.

### 1. Keeping the operator as a thin orchestration wrapper

This held up well.

The operator still owns:
- HTTP admission and read APIs
- SQLite persistence
- worker orchestration
- subprocess lifecycle

The recorder still owns:
- Talk join behavior
- recorder-side resilience
- live media capture
- `.run` artifact production

That boundary kept the implementation focused and avoided a larger in-process recorder rewrite.

### 2. Reusing the existing build/publish backbone

This also held up well.

No new downstream artifact model was needed. Once a usable `.run` existed, the live path behaved like the old fixture path from the build/publish stages onward.

### 3. Using migrations rather than another schema special case

This was the right call.

The V2 work needed schema evolution anyway, and moving to explicit SQL migrations made the stop metadata changes much easier to reason about and test.

### 4. Keeping operator-owned stop state on the job row

This matched the repo precedent and worked cleanly in implementation.

The operator did not need to mutate recorder-owned `.run` or session artifacts. The persisted job row is enough for operator-facing observability in V2.

## What implementation clarified further

### 1. Stop classification is currently operator-side and heuristic

The implementation intentionally keeps `operator_requested` as operator-owned classification.

For non-operator stop reasons, the current implementation classifies outcomes from:
- whether stop was accepted by the operator
- subprocess exit code
- recorder log text such as `talk recorder stopping: ...`

That is sufficient for V2, but it is not yet a fully structured recorder-to-operator contract.

### 2. The migration surface is intentionally internal

The selected V2 scope was correct here.

There are no extra public operator DB commands yet. Down migrations exist for explicit test/manual use inside the codebase, but the normal runtime model is still simply:
- start operator
- code expects latest schema
- startup migrates up if needed

### 3. The most valuable automated tests are wrapper-driven orchestration tests

The implementation did not try to run full live Talk capture inside operator unit tests.

Instead, tests use fake `CASSINI_BIN` wrappers to verify:
- doctor invocation
- record invocation and flag wiring
- stop behavior
- migration bootstrap
- stop metadata persistence
- read-surface exposure

That proved to be the fastest and most stable way to validate operator behavior slice by slice.

## Deliberate V2 limitations that remain

These are intentional omissions, not regressions.

- no broad operator-level retry or re-entry policy
- no separate terminal success state for intentionally stopped jobs
- no operator mutation of recorder-owned filesystem artifacts
- no public migration-management CLI for the operator
- no invite automation or broader Nextcloud user/product lifecycle
- no full end-to-end automated browser participant path in operator tests

## Test and verification outcome

### Automated coverage completed

The implemented operator tests now cover:
- migration bootstrap on a fresh DB
- legacy V1 DB baselining into `schema_migrations`
- explicit down migration in test code
- inconsistent migration history refusal
- normalized trigger defaults
- explicit record option forwarding
- accepted stop behavior on a live record subprocess
- `404` / `409` stop-path behavior
- stop metadata persistence and read-surface exposure
- existing build/publish success and failure paths
- existing startup interruption behavior

Implementation-time validation also included:
- `cd cassini-operator && go test ./...`
- `cd cassini-operator && go build ./...`

### Manual live validation status

The canonical manual V2 validation path is now documented, but it was not fully executed by the agent as part of this implementation reflection.

That means:
- automated operator/runtime verification is complete
- the intended browser-driven Talk-room validation flow is documented and ready
- final live-room acceptance should still be performed through the harness/user-driven path in `testing.md`

## Files and areas touched

The main implementation landed in:
- `cassini-operator/internal/operator/run.go`
- `cassini-operator/internal/operator/record_runtime.go`
- `cassini-operator/internal/operator/migrations.go`
- `cassini-operator/internal/operator/migrations/0001_initial_jobs.up.sql`
- `cassini-operator/internal/operator/migrations/0001_initial_jobs.down.sql`
- `cassini-operator/internal/operator/migrations/0002_record_stop_metadata.up.sql`
- `cassini-operator/internal/operator/migrations/0002_record_stop_metadata.down.sql`
- `cassini-operator/internal/operator/run_test.go`
- `cassini-operator/README.md`

## Final implementation shape

The implemented happy-path lifecycle is now:

```text
record/queued
-> record/running
-> build/queued
-> build/running
-> publish/queued
-> publish/running
-> done/succeeded
```

With intentional stop, the lifecycle remains the same if the recorder finalizes a usable run:

```text
record/running
-> POST /jobs/:id/stop accepted
-> recorder receives SIGTERM
-> finalized .run remains usable
-> build/queued
-> build/running
-> publish/queued
-> publish/running
-> done/succeeded
```

The distinction is carried in stop metadata rather than a different terminal state.

## Suggested next move after V2

The most natural next step is not more reshaping of the live-record slice.

V2 now has:
- real live operator-driven Talk capture
- explicit stop behavior
- versioned schema evolution
- persisted stop observability

The next leveraged moves are likely around:
- live-room manual acceptance and operational polish
- broader recovery/rerun semantics deferred to V5
- any future recorder-to-operator structured stop-reason contract if heuristic classification becomes too thin
