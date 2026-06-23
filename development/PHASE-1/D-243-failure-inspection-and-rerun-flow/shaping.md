---
shaping: true
---

# V5 — Shaping

This document shapes **V5: Failure inspection and rerun flow**.

It elaborates the slice already defined in:

- `planning/initiatives/mvp/shaping.md`
- `planning/initiatives/mvp/slices.md`
- `planning/initiatives/mvp/slices/V5-failure-inspection-and-rerun-flow/brief.md`

The goal here is to extend the current operator runtime with a real recovery loop for failed jobs while staying inside the MVP slice boundary.

## Selected implementation shape

The selected V5 shape is now:

- keep one stable top-level job identity per operator trigger
- add explicit per-attempt persistence for failure details and rerun history
- add `POST /jobs/:id/rerun` as an operator-explicit action on a failed job
- rerun from the record stage again rather than trying to resume arbitrary partial subprocess state
- keep failed attempt artifacts and logs available for inspection
- keep `GET /jobs` as a summary surface and extend `GET /jobs/:id` to return attempt history
- let successful reruns flow through the normal build/publish pipeline and refresh the shared hosted output

This is **Shape A**, now selected.

---

## Requirements (R)

| ID | Requirement | Status |
|----|-------------|--------|
| R0 | An operator can inspect a failed job and trigger a rerun from the API without reconstructing the original request manually. | Core goal |
| R1 | Failure context must be durable across process restarts and visible through persisted API-readable state rather than depending on transient process memory or scrolling daemon logs. | Must-have |
| R2 | One logical operator job keeps one stable API identity across reruns while still preserving visible per-attempt history. | Must-have |
| R3 | Rerun is explicit and conservative: it is accepted only from an eligible failed terminal job, never automatic, and does not pretend to resume arbitrary partial subprocess state. | Must-have |
| R4 | The rerun path must reuse the current operator architecture: SQLite-backed persistence, the existing record/build/publish stage model, and CLI execution boundaries in `cassini-operator`. | Must-have |
| R5 | A rerun must preserve the original failed attempt for inspection while running a fresh new attempt in isolated attempt-scoped artifacts and logs. | Must-have |
| R6 | If a rerun succeeds, the job's effective artifact references and the shared hosted output must reflect the successful attempt through the normal publish path. | Must-have |
| R7 | The operator read surface must remain simple: `GET /jobs` stays a one-row-per-logical-job summary view, while `GET /jobs/:id` can expose the fuller attempt history needed for inspection. | Must-have |
| R8 | V5 stays narrowly scoped to failure inspection and explicit rerun; it does not add a new dashboard, automatic retry policy, or generalized resumability beyond what is needed for operator recovery. | Must-have |

---

## CURRENT: V2 operator with single-attempt job rows

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **CURRENT1** | `cassini-operator` already persists top-level jobs in SQLite and exposes `POST /jobs?provider=nextcloud-talk`, `POST /jobs/:id/stop`, `GET /jobs`, and `GET /jobs/:id`. | |
| **CURRENT2** | The operator already shells out to `cassini doctor`, `cassini record`, `cassini build`, and `cassini publish` and persists stage/state timestamps plus artifact pointers. | |
| **CURRENT3** | V2 already persists stop metadata such as `stop_reason`, `stop_requested_at`, `stop_signal_sent_at`, `record_exit_code`, and `record_stop_detail` on the single `jobs` row. | |
| **CURRENT4** | Failure reporting today is still single-attempt and summary-shaped: one `error` field on the job plus lightweight downstream manifest extraction. | |
| **CURRENT5** | There is no `POST /jobs/:id/rerun`, no explicit attempt history, and no attempt-scoped persistence model for preserving multiple failures/successes under one logical job. | |
| **CURRENT6** | Startup interruption marking is honest but non-resumptive: non-terminal jobs become `interrupted`, and current V2 intentionally does not retry or resume them automatically. | |

## A: Stable job row plus explicit attempt records — selected

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **A1** | Keep one stable top-level `jobs` row per logical operator job and extend persistence with a separate `job_attempts` table keyed by job id plus attempt number. | |
| **A2** | Every first run and every rerun becomes an explicit persisted attempt with its own stage/state, timestamps, artifact paths, error summary, log paths, and preserved normalized request payload. | |
| **A3** | `POST /jobs/:id/rerun` validates that the current logical job is in an eligible failed terminal state, then inserts a new queued attempt for the same job id and updates job-summary fields to reflect that the job is active again. | |
| **A4** | Rerun always starts from the record stage again and writes to attempt-scoped outputs and logs, so previous attempt evidence is preserved and partial-state reuse stays out of scope. | |
| **A5** | `GET /jobs` stays a summary surface driven from the top-level job row, while `GET /jobs/:id` returns that summary plus full attempt history for inspection. | |
| **A6** | A successful rerun updates the top-level job's effective artifact pointers and terminal state to the winning attempt, while failed attempts remain queryable as history. | |

## B: New job row per rerun linked to the original failed job — not selected

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **B1** | `POST /jobs/:id/rerun` creates an entirely new job id and new top-level job row while linking back to the original failed job via a parent/reference field. | ⚠️ |
| **B2** | Each job row remains single-attempt, and retry history is reconstructed by following cross-job links. | ⚠️ |
| **B3** | `GET /jobs` shows separate rows for the original failure and the rerun job. | ⚠️ |

## C: In-place rerun on the existing job row with minimal history — not selected

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **C1** | Add `POST /jobs/:id/rerun` but reuse only the existing `jobs` row, clearing and rewriting stage/state/error fields in place. | ⚠️ |
| **C2** | Preserve at most a small rerun counter or last-failure fields, without a real per-attempt persistence model. | ⚠️ |
| **C3** | Logs and artifacts are reused or overwritten under the same paths as the old failed attempt. | ⚠️ |

---

## Why A is selected

Shape A best matches the repo's current operator surface:

- the top-level API already treats a job as the main operator-facing unit
- the user story is "rerun this failed job", not "fork a new job chain and make me follow links"
- V5 explicitly needs preserved failure evidence, which is awkward if rerun overwrites the same row or artifact paths
- the existing V2 `jobs` row can remain the summary surface while a new `job_attempts` table carries the richer history V5 needs

Shape B was rejected because it weakens stable job identity and makes `GET /jobs` noisier and more confusing for operators.

Shape C was rejected because it hides or destroys the original failure evidence and makes rerun history too implicit.

---

## Fit Check

| Req | Requirement | Status | A | B | C |
|-----|-------------|--------|---|---|---|
| R0 | An operator can inspect a failed job and trigger a rerun from the API without reconstructing the original request manually. | Core goal | ✅ | ✅ | ✅ |
| R1 | Failure context must be durable across process restarts and visible through persisted API-readable state rather than depending on transient process memory or scrolling daemon logs. | Must-have | ✅ | ✅ | ❌ |
| R2 | One logical operator job keeps one stable API identity across reruns while still preserving visible per-attempt history. | Must-have | ✅ | ❌ | ❌ |
| R3 | Rerun is explicit and conservative: it is accepted only from an eligible failed terminal job, never automatic, and does not pretend to resume arbitrary partial subprocess state. | Must-have | ✅ | ✅ | ✅ |
| R4 | The rerun path must reuse the current operator architecture: SQLite-backed persistence, the existing record/build/publish stage model, and CLI execution boundaries in `cassini-operator`. | Must-have | ✅ | ✅ | ✅ |
| R5 | A rerun must preserve the original failed attempt for inspection while running a fresh new attempt in isolated attempt-scoped artifacts and logs. | Must-have | ✅ | ✅ | ❌ |
| R6 | If a rerun succeeds, the job's effective artifact references and the shared hosted output must reflect the successful attempt through the normal publish path. | Must-have | ✅ | ✅ | ✅ |
| R7 | The operator read surface must remain simple: `GET /jobs` stays a one-row-per-logical-job summary view, while `GET /jobs/:id` can expose the fuller attempt history needed for inspection. | Must-have | ✅ | ❌ | ✅ |
| R8 | V5 stays narrowly scoped to failure inspection and explicit rerun; it does not add a new dashboard, automatic retry policy, or generalized resumability beyond what is needed for operator recovery. | Must-have | ✅ | ✅ | ✅ |

**Notes:**
- B fails R2 and R7 because it turns one logical operator workflow into multiple top-level job ids that operators must mentally stitch together.
- C fails R1 and R5 because in-place overwrite and path reuse do not preserve enough durable failure evidence from the original failed attempt.

## Detail A: concrete V5 mechanisms

| Part | Mechanism |
|------|-----------|
| **A1.1** | Keep `jobs` as the canonical summary table and add `job_attempts` as the canonical attempt-history table through a numbered schema migration. |
| **A1.2** | `job_attempts` should carry at least: `job_id`, `attempt_number`, `trigger_kind` (`initial` or `rerun`), preserved `request_json`, `stage`, `state`, `artifact_run_path`, `artifact_meeting_path`, `artifact_site_path`, `error`, failure/log references, per-stage timestamps, `created_at`, `updated_at`, and `completed_at`. |
| **A2.1** | The first trigger inserts both the top-level `jobs` row and attempt `1`; each rerun appends attempt `2`, `3`, and so on for the same `job_id`. |
| **A2.2** | The top-level `jobs` row remains the fast read model for list views and current state. It should gain explicit summary fields such as current attempt number, rerun count, latest failure summary, and effective artifact pointers to the most relevant attempt. |
| **A3.1** | `POST /jobs/:id/rerun` is accepted only when the current job summary is in a failed terminal state and there is no active attempt already running for that job. |
| **A3.2** | Rerun reuses the preserved normalized request from the prior attempt by default. V5 does not add request mutation or "rerun with edits" semantics. |
| **A3.3** | On accepted rerun, the top-level job summary transitions back to active queue state and records rerun summary metadata without deleting the prior failure summary. |
| **A4.1** | Each attempt writes to isolated paths under the operator work root, for example `<work-root>/<job-id>/attempt-001.run`, `<work-root>/<job-id>/attempt-001.meeting`, and attempt-local log files. |
| **A4.2** | Rerun starts from `record` again even if a prior attempt failed in `build` or `publish`. This is the selected conservative rule for V5 because it keeps behavior explicit and avoids hidden partial reuse. |
| **A5.1** | Record/build/publish subprocess stdout/stderr should be captured to attempt-scoped log files, and the attempt row should store the relevant file paths plus a concise persisted failure summary for API reads. |
| **A5.2** | `GET /jobs` continues returning one summary row per logical job. `GET /jobs/:id` should return the job summary plus attempts newest-first so operators can inspect original failure and rerun outcomes together. |
| **A6.1** | A successful rerun updates the top-level job summary to `done/succeeded`, points effective artifact fields at the successful attempt's outputs, and clears "current failure" status while leaving the failed attempt rows untouched. |
| **A6.2** | Publish continues to refresh the shared hosted site from the normal work root. V5 does not add a second publish mechanism for reruns; it relies on the successful attempt entering the standard publish path. |
| **A6.3** | If a rerun fails again, that becomes another failed attempt on the same job id rather than a new top-level job chain. |

## Deferred from this V5 cut

- automatic retries, retry backoff, or retry policy configuration
- rerun-with-edits semantics that let operators modify the preserved request before retrying
- resumability from arbitrary partial build/publish state
- broad support for rerunning `interrupted` jobs
- a dedicated log download API if attempt-scoped log paths plus summary fields are sufficient
- new dashboard or front-end inspection surface

## Reassessment: where we are now

The shaping has moved past the main modeling question.

We now have:

- a selected stable-job plus attempt-history model
- a selected `POST /jobs/:id/rerun` API direction
- a selected fresh-record rerun rule rather than implicit partial-state reuse
- a selected separation between top-level job summary reads and detailed attempt-history reads
- a selected attempt-scoped artifact/log preservation strategy

The remaining work is implementation planning and schema/API cutline discipline.

## Next step

Use selected Shape A to drive the V5 implementation breakdown in:

- `planning/initiatives/mvp/slices/V5-failure-inspection-and-rerun-flow/slices.md`

The focused persistence spike is now answered in:

- `planning/initiatives/mvp/slices/V5-failure-inspection-and-rerun-flow/spike-job-attempts-schema.md`

That spike does not reopen the V5 shape. It now serves as the decision record for:

- the exact `jobs` versus `job_attempts` schema split
- the concrete summary-plus-attempts API read consequence
- the minimum attempt-scoped path layout implied by the selected schema
