---
shaping: true
---

# Operator rerun from captured recording — Shaping

This document shapes a follow-up to the current operator rerun behavior.

It elaborates:

- `planning/initiatives/mvp/slices/V5-failure-inspection-and-rerun-flow/shaping.md`
- `planning/initiatives/mvp/slices/V5-failure-inspection-and-rerun-flow/implementation.md`
- `planning/initiatives/mvp/slices/V5-failure-inspection-and-rerun-flow/testing.md`
- `cassini-operator/README.md`
- `cassini-operator/internal/operator/record_runtime.go`
- `cassini-operator/internal/operator/attempt_store.go`
- `cassini-operator/internal/operator/build_runtime.go`
- `cassini-operator/internal/operator/publish_runtime.go`
- `cassini-go-recorder/internal/cassini/build.go`
- `cassini-go-recorder/internal/cassini/publish.go`
- `cassini-go-recorder/internal/cassini/cli.go`
- `cassini-go-recorder/docs/architecture-overview.md`

## Working position

**Selected working shape: A — keep one stable `work_root`, derive `current/` + `runs/` from it, rerun only from the canonical ready `.run`, and keep the existing `jobs` + `job_attempts` + `/events` reporting backbone.**

Reason:

- it matches the real-world ask: do not re-record a live conversation once capture has already happened
- it matches the stricter rerun rule: every accepted rerun owns fresh `build` + `publish`, so publish-only reruns are out
- it removes unnecessary source ambiguity: for the first cut, the only reusable recording boundary is the canonical successful `.run` from the initial record pass
- it keeps `publish` clean by making `current/` the only publish-visible artifact root
- it preserves attempt history and logs under `runs/`
- it keeps the control-panel/query surface intact by reusing the current `jobs`, `job_attempts`, `/jobs`, and `/events` model

This shape is now specific enough to breadboard. Post-MVP retention / movement to hard storage for accumulated artifacts remains a follow-up, not a blocker for the first cut.

---

## Requirements (R)

| ID | Requirement | Status |
|----|-------------|--------|
| R0 | Rerunning a job that failed in `build` or `publish` must not start a new live recording of the conversation. | Core goal |
| 🟡 R1 | 🟡 For the first cut, rerun must be accepted only when the operator can point at one fully completed, ready canonical `.run` bundle for the job. | Must-have |
| 🟡 R2 | **🟡 Stable job identity and inspectable status/history** | |
| 🟡 R2.1 | 🟡 One logical job keeps one stable API identity and visible attempt history; reruns append attempts rather than overwriting prior failure evidence. | Must-have |
| 🟡 R2.2 | 🟡 A user must still be able to query job status and run/attempt status through the operator read surfaces after reruns. | Must-have |
| 🟡 R2.3 | 🟡 The control panel must keep being able to list jobs, inspect attempts, and follow live updates through the existing `/jobs` + `/events` model. | Must-have |
| R3 | The solution must keep the existing CLI boundary intact: `cassini-operator` orchestrates, while `cassini record` / `build` / `publish` remain the execution backbone. | Must-have |
| 🟡 R4 | **🟡 Fresh downstream rerun attempts** | |
| 🟡 R4.1 | 🟡 Every accepted rerun attempt must own a fresh `build` and `publish` pass. Publish-only reruns are out. | Must-have |
| 🟡 R4.2 | 🟡 Attempt reads must make it obvious that `record` was not rerun while `build` and `publish` were rerun on the new attempt. | Must-have |
| 🟡 R5 | **🟡 Stable publish-visible filesystem contract** | |
| 🟡 R5.1 | 🟡 `current/` must contain only successful canonical downstream artifacts, so publish never sees historical or failed bundles. | Must-have |
| 🟡 R5.2 | 🟡 The operator must derive a stable `<work_root>/current` and `<work_root>/runs` layout from `work_root`; publish input is always `<work_root>/current`, not a dynamically chosen path. | Must-have |
| R6 | The solution should preserve the current publish contract unless a rewrite is clearly justified; avoiding forced `record` / `build` / `publish` rewrites is preferred. | Must-have |
| R7 | **🟡 Recording-boundary selection** | |
| 🟡 R7.1 | 🟡 A record-stage post-processing pass may produce `recording.mkv`, but that raw MKV is not yet the selected rerun signal for the first cut. | Must-have |
| 🟡 R7.2 | 🟡 The only selected first-cut rerun boundary is the canonical ready `.run` produced by the initial successful record pass. | Must-have |
| 🟡 R7.3 | 🟡 If no canonical ready `.run` exists for the job, rerun must be rejected for now, even if partial raw recording artifacts may exist elsewhere on disk. | Must-have |
| 🟡 R7.4 | 🟡 Support for raw-`recording.mkv` salvage or session-artifact-only salvage should be captured as follow-up work, not silently implied by the first-cut rerun contract. | Must-have |
| R8 | Scope stays narrow around operator rerun semantics. No new dashboard, no automatic retry policy, and no generalized resumability beyond the chosen durable boundary. | Must-have |

---

## CURRENT: Repo and runtime baseline

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **CURRENT1** | `POST /jobs/:id/rerun` currently accepts only `done/failed` jobs and always starts a fresh `runRecordJob(...)`. | |
| **CURRENT2** | `QueueRerunAttempt(...)` resets the top-level job back to `record/queued`, clears artifact pointers, and inserts a new attempt whose first stage is always `record`. | |
| **CURRENT3** | `cassini build` already accepts either a ready `.run` bundle or a raw `.mkv` input. | |
| **CURRENT4** | The recorder persists a session artifact during live capture, then later remuxes that session artifact into `recording.mkv`, and only after that finalizes the `.run` bundle. | |
| **CURRENT5** | A failed `.run` bundle is not currently buildable through `cassini build`; `resolveBuildInput(...)` rejects non-ready `.run` bundles. | |
| **CURRENT6** | Publish does **not** accept an explicit meeting bundle input in operator runtime; it republishes from the whole work root, and `cassini publish` only scans immediate child `.meeting` directories under that root. | |
| **CURRENT7** | A publish failure may already have left behind a ready `.meeting` bundle. If a rerun rebuilds another ready `.meeting` for the same logical job, publish would see both unless we handle that explicitly. | ⚠️ |
| **CURRENT8** | On record failure the operator does not currently persist a canonical run path that can be reused without re-entering record. | ⚠️ |
| **CURRENT9** | The operator already preserves per-attempt stage-scoped logs (`record`, `build`, `publish`), and those are worth keeping. | |
| **🟡 CURRENT10** | 🟡 `/jobs`, `/jobs/:id`, and `/events` already expose stable job + attempt reads that the control panel can build on today. | |

---

## Current-state findings

1. `cassini-operator/internal/operator/record_runtime.go` hardcodes rerun back through `runRecordJob(...)` today.
2. `cassini-operator/internal/operator/attempt_paths.go` keeps attempt outputs flat: `<work-root>/<job-id>--attempt-XXX.run|.meeting|.logs/`.
3. `cassini-go-recorder/internal/cassini/build.go` already lets us build from a ready `.run`; it also supports raw `.mkv`, but that is not the selected first-cut signal.
4. `cassini-go-recorder/internal/cassini/publish.go` scans only immediate child `.meeting` directories, so a shape with historical attempt bundles under the publish root needs a visibility rule.
5. `cassini-go-recorder/internal/cassini/cli.go` and `cassini-go-recorder/internal/talk/recorder.go` confirm the capture boundary is layered: `session artifact -> recording.mkv -> ready .run`.
6. `cassini-operator` already has the stable query/reporting backbone we want to preserve: one `jobs` summary row, many `job_attempts`, plus `/events` for live control-panel updates.

---

## X1 spike update — recording boundary

The assumptions are correct, with one nuance:

- record-stage post-processing **can** produce `recording.mkv`
- that MKV can exist in a partial/untrustworthy state if remux failed, or in a usable state before the `.run` is finalized
- the current supported downstream-ready signal is still the **fully completed `.run` bundle**
- for the first cut, that reusable `.run` is treated as the one canonical successful record output of the job; reruns do **not** create another `.run`

So for this first cut:

- **selected rerun signal:** canonical ready `.run`
- **not selected yet:** raw `recording.mkv`
- **not supported yet:** session-artifact-only salvage

## X2 spike update — filesystem shape

- The operator should keep one stable configured `work_root` and derive two fixed sub-roots from it:
  - `<work_root>/current`
  - `<work_root>/runs`
- `publish` should always receive `<work_root>/current` as its input root, derived by the operator from `work_root` rather than chosen dynamically.
- `current/` must contain only successful canonical artifacts:
  - `current/<job-id>.run`
  - `current/<job-id>.meeting`
- Historical attempt outputs and logs belong under `runs/`, outside publish discovery.
- The initial record attempt keeps its attempt-local `.run` under `runs/` in all cases.
- If that `.run` becomes ready, the operator also promotes a canonical copy into `current/<job-id>.run` and retains the attempt-local copy under `runs/`.
- Longer-term retention / movement to hard storage for accumulated artifacts is a follow-up.

## X3 spike update — reporting and `/events`

- The existing `jobs` + `job_attempts` model is already the right backbone for attempts 2, 3, ...
- `/events` can continue to stream rerun attempts without a new endpoint or schema family.
- For the first cut, we can keep the current DB schema if we change semantics carefully:
  - `job` = canonical/effective summary
  - `attempt` = attempt-local execution state
  - `trigger_kind=rerun` + empty `record_*` fields already show that a rerun started at `build`
- A schema migration is **not required** for the first cut unless we later widen rerun source types or entry stages.

## A: Canonical ready-`.run` rerun with stable `current/` + `runs/`

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **A1** | Accept rerun only when the job has one canonical ready `.run` under `current/`, then create attempt `N+1` queued at `build`. | |
| **A2** | Every accepted rerun attempt always reruns `build` and then `publish`; it never starts at `publish` only and never re-enters live `record`. | |
| **A3** | Derive `current/` and `runs/` from one stable `work_root`; `publish` always runs from `current/`. | |
| **A4** | Keep only successful canonical `.run` / `.meeting` bundles in `current/`; keep attempt-local `.run` / `.meeting` outputs and logs in `runs/`. | |
| **A5** | Keep the existing `jobs` + `job_attempts` + `/events` reporting backbone; job rows become canonical/effective summary, attempts remain execution history. | |
| **A6** | Defer raw-MKV salvage and session-artifact salvage to explicit follow-up work. | |

## B: Expand first-cut rerun to accept validated raw `recording.mkv`

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **B1** | Accept either a validated raw `recording.mkv` or a ready `.run` as the build-entry boundary. | |
| **B2** | Keep the same stable `work_root/current` + `work_root/runs` shape and still rerun `build` + `publish` on every accepted rerun. | |
| **B3** | Add explicit validation/sourcing rules so raw MKV reuse is honest and explainable. | ⚠️ |

## C: Explicit nested job layout (`recording/` + `runs/N/`)

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **C1** | Move the non-repeatable capture output under a dedicated recording directory per job, and put repeatable postprocess attempts under a nested `runs/N/` subtree. | |
| **C2** | Every rerun reuses the recording area and always reruns `build` + `publish` in a new run subtree. | |
| **C3** | Adapt operator path helpers, read models, and publish/build wiring around that nested layout. | ⚠️ |
| **C4** | Either rewrite publish discovery to understand nested meeting bundles or add shims/staging so the existing publisher still sees the right immediate child `.meeting` directories. | ⚠️ |

---

## Fit Check

| Req | Requirement | Status | A | B | C |
|-----|-------------|--------|---|---|---|
| R0 | Rerunning a job that failed in `build` or `publish` must not start a new live recording of the conversation. | Core goal | ✅ | ✅ | ✅ |
| R1 | For the first cut, rerun must be accepted only when the operator can point at one fully completed, ready canonical `.run` bundle for the job. | Must-have | ✅ | ❌ | ✅ |
| R2.1 | One logical job keeps one stable API identity and visible attempt history; reruns append attempts rather than overwriting prior failure evidence. | Must-have | ✅ | ✅ | ✅ |
| R2.2 | A user must still be able to query job status and run/attempt status through the operator read surfaces after reruns. | Must-have | ✅ | ✅ | ✅ |
| R2.3 | The control panel must keep being able to list jobs, inspect attempts, and follow live updates through the existing `/jobs` + `/events` model. | Must-have | ✅ | ✅ | ✅ |
| R3 | The solution must keep the existing CLI boundary intact: `cassini-operator` orchestrates, while `cassini record` / `build` / `publish` remain the execution backbone. | Must-have | ✅ | ✅ | ✅ |
| R4.1 | Every accepted rerun attempt must own a fresh `build` and `publish` pass. Publish-only reruns are out. | Must-have | ✅ | ✅ | ✅ |
| R4.2 | Attempt reads must make it obvious that `record` was not rerun while `build` and `publish` were rerun on the new attempt. | Must-have | ✅ | ✅ | ✅ |
| R5.1 | `current/` must contain only successful canonical downstream artifacts, so publish never sees historical or failed bundles. | Must-have | ✅ | ✅ | ❌ |
| R5.2 | The operator must derive a stable `<work_root>/current` and `<work_root>/runs` layout from `work_root`; publish input is always `<work_root>/current`, not a dynamically chosen path. | Must-have | ✅ | ✅ | ❌ |
| R6 | The solution should preserve the current publish contract unless a rewrite is clearly justified; avoiding forced `record` / `build` / `publish` rewrites is preferred. | Must-have | ✅ | ✅ | ❌ |
| R7.1 | A record-stage post-processing pass may produce `recording.mkv`, but that raw MKV is not yet the selected rerun signal for the first cut. | Must-have | ✅ | ❌ | ✅ |
| R7.2 | The only selected first-cut rerun boundary is the canonical ready `.run` produced by the initial successful record pass. | Must-have | ✅ | ❌ | ✅ |
| R7.3 | If no canonical ready `.run` exists for the job, rerun must be rejected for now, even if partial raw recording artifacts may exist elsewhere on disk. | Must-have | ✅ | ❌ | ✅ |
| R7.4 | Support for raw-`recording.mkv` salvage or session-artifact-only salvage should be captured as follow-up work, not silently implied by the first-cut rerun contract. | Must-have | ✅ | ❌ | ✅ |
| R8 | Scope stays narrow around operator rerun semantics. No new dashboard, no automatic retry policy, and no generalized resumability beyond the chosen durable boundary. | Must-have | ✅ | ❌ | ❌ |

**Notes:**

- **A** is the best current fit because it matches the conservative contract: canonical ready `.run` only, fresh `build` + `publish`, stable `current/` + `runs/`, and no reporting-system rewrite.
- **B** remains a plausible follow-up because raw-MKV salvage appears possible in code, but it intentionally exceeds the selected first-cut contract.
- **C** is conceptually clean, but it currently fails the stable publish-visible filesystem requirement and would force extra publish/path work that the first cut is explicitly trying to avoid.

---

## Working shape: A

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **A1** | **🟡 Canonical rerun gate** | |
| 🟡 A1.1 | 🟡 Rerun eligibility is simply: does `current/<job-id>.run` exist as one ready canonical `.run` bundle? | |
| 🟡 A1.2 | 🟡 If yes, create attempt `N+1` at `build/queued`; if no, return conflict. | |
| **A2** | **🟡 Fresh downstream rerun lifecycle** | |
| 🟡 A2.1 | 🟡 Every accepted rerun skips live `record` entirely and reruns only `build` then `publish`. | |
| 🟡 A2.2 | 🟡 `trigger_kind=rerun`, empty `record_*` fields/logs, and fresh `build_*` / `publish_*` fields make the attempt history honest without a new reporting system. | |
| **A3** | **🟡 Stable work-root contract** | |
| 🟡 A3.1 | 🟡 The operator keeps one configured `work_root` and derives `current/` and `runs/` from it internally. | |
| 🟡 A3.2 | 🟡 `publish` always receives the derived `current/` path, never a dynamic or attempt-local directory. | |
| **A4** | **🟡 Successful-only canonical artifacts** | |
| 🟡 A4.1 | 🟡 `current/` contains only successful canonical `.run` / `.meeting` bundles. | |
| 🟡 A4.2 | 🟡 Attempt-local `.run` / `.meeting` outputs and all stage logs live under `runs/`, outside publish discovery. | |
| 🟡 A4.3 | 🟡 The initial record pass keeps its attempt-local `.run` under `runs/`, and only a successful ready copy is promoted into `current/<job-id>.run`. | |
| 🟡 A4.4 | 🟡 Failed or partial record bundles stay retained under `runs/` for inspection, but never become visible under `current/`. | |
| 🟡 A4.5 | 🟡 Post-MVP retention / movement to hard storage for accumulated `.run` and downstream artifacts is deferred to follow-up work. | ⚠️ |
| **A5** | **🟡 Existing reporting surfaces stay in place** | |
| 🟡 A5.1 | 🟡 `jobs` remains the stable summary row for job lists and job detail, but its artifact paths become canonical/effective paths under `current/`. | |
| 🟡 A5.2 | 🟡 `job_attempts` remains the execution history, with attempt-local `.run` / `.meeting` outputs and logs under `runs/`. | |
| 🟡 A5.3 | 🟡 `/jobs` and `/events` stay the control-panel contract; no new endpoint family is needed for reruns. | |
| 🟡 A5.4 | 🟡 First cut preference: keep the current DB schema and change store/update semantics only. | |
| **A6** | **🟡 Deferred recovery beyond ready `.run`** | |
| 🟡 A6.1 | 🟡 Base contract: canonical ready `.run` is the only selected rerun signal for now. | |
| 🟡 A6.2 | 🟡 Follow-up: evaluate raw-`recording.mkv` salvage as an explicit extension. | ⚠️ |
| 🟡 A6.3 | 🟡 Follow-up: support finishing/remuxing from session artifacts without re-recording when live capture succeeded but the final MKV never became reusable. | ⚠️ |

---

## Follow-ups

1. **Raw-MKV salvage**
   - allow rerun from validated `recording.mkv` when no canonical ready `.run` exists
2. **Session-artifact salvage**
   - allow finishing/remuxing from session artifacts without re-recording
3. **Artifact retention / hard storage policy**
   - define when retained `.run`, `.meeting`, and log artifacts move out of hot local storage and what cleanup contract follows

---

## Spikes

- `./spike-recording-boundary.md` — validates the capture boundary assumptions and records why the first cut selects canonical ready `.run` only.
- `./spike-work-root-layout.md` — records the preferred stable `work_root/current` + `work_root/runs` filesystem shape.
- `./spike-jobs-reporting-and-events.md` — records why the existing `jobs` + `job_attempts` + `/events` backbone can stay in place for the first cut.

Breadboarding for this selected shape now lives in `./breadboarding.md`. Implementation slicing for this shape now lives in `./slices.md`.
