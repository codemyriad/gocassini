---
shaping: true
---

# Operator rerun from captured recording — Breadboarding

Derived from `./shaping.md`.

This breadboard details selected Shape A:

- one stable `work_root`
- derived `current/` + `runs/`
- rerun only from canonical ready `.run`
- fresh `build` + `publish` on every accepted rerun
- existing `jobs` + `job_attempts` + `/events` read backbone preserved

## Places

| # | Place | Description |
|---|-------|-------------|
| P1 | Caller / Control Panel | Existing clients that trigger jobs, request reruns, query status, and watch live updates. |
| P2 | Operator HTTP API | Existing `/jobs` and `/events` surface, extended with new rerun semantics. |
| P3 | Operator store + event hub | Existing `jobs`, `job_attempts`, and SSE broadcast path; semantics change but the reporting system stays the same. |
| P4 | Work-root filesystem | Stable `<work_root>/current`, `<work_root>/runs`, and promotion staging area. |
| P5 | Record runtime | Initial live record path that produces the only reusable `.run` boundary for the first cut. |
| P6 | Build runtime | Builds from canonical `current/<job-id>.run` into attempt-local `runs/*.meeting`. |
| P7 | Publish runtime | Publishes only from `<work_root>/current` into `<site_root>`. |

## Workflow guide

| Step | Action | Where to look |
|------|--------|---------------|
| **1** | Caller triggers the initial job | U1 → N1 → N14 → N5/N6/N7 |
| **2** | Initial record attempt writes retained attempt-local `.run` and, on success, promotes canonical `.run` | N6 → N7 → N8 |
| **3** | Build runs from canonical `.run`, writes attempt-local `.meeting`, then promotes canonical `.meeting` | N9 → N10 → N11 |
| **4** | Publish runs only from `current/` | N12 |
| **5** | If build/publish fails, caller requests rerun; rerun is admitted only if canonical `.run` exists | U2 → N2 → N9 |
| **6** | Caller/control panel keeps querying list/detail state and watching updates through existing surfaces | U3/U4/U5 → N3/N4/N15 |

## UI Affordances

| # | Place | Component | Affordance | Control | Wires Out | Returns To | Status |
|---|-------|-----------|------------|---------|-----------|------------|--------|
| U1 | P1 | caller / control-panel | `POST /jobs?provider=nextcloud-talk` | call | → N1 | → U3, → U4 | ✅ Exists, extended |
| U2 | P1 | caller / control-panel | `POST /jobs/:id/rerun` | call | → N2 | → U4 | ✅ Exists, extended |
| U3 | P1 | caller / control-panel | `GET /jobs` | call | → N3 | → caller list view | ✅ Exists |
| U4 | P1 | caller / control-panel | `GET /jobs/:id` | call | → N3 | → caller detail view | ✅ Exists |
| U5 | P1 | caller / control-panel | `GET /events` | observe | → N4 | → caller live updates | ✅ Exists |

## Code Affordances

| # | Place | Component | Affordance | Control | Wires Out | Returns To | Status |
|---|-------|-----------|------------|---------|-----------|------------|--------|
| N1 | P2 | operator-api | initial trigger admission | call | → N14, → N5, → N6, → N7, → N15 | → U1 | ✅ Exists, extended |
| N2 | P2 | operator-api | rerun admission gate | call | → N3, → N5, → N9, → N15 | → U2 | ✅ Exists, extended |
| N3 | P2/P3 | operator-api + store | list/detail reads | call | → S1, → S2 | → U3, → U4 | ✅ Exists, extended |
| N4 | P2/P3 | operator-api + event hub | SSE stream handler | observe | → S6 | → U5 | ✅ Exists |
| N5 | P4 | runtime-paths | derive `currentRoot`, `runsRoot`, canonical artifact paths | call | → S3, → S4 | → N1, → N2, → N8, → N11, → N12 | New |
| N6 | P5/P4 | record-runtime | allocate attempt-local record paths under `runs/` | call | → S4 | → N7, → N14 | Extended |
| N7 | P5 | record-runtime | run live record into `runs/<job-id>--attempt-001.run` and persist record logs/state | call | → S4, → N14, → N15, → N8 | → N1 | ✅ Exists, extended |
| N8 | P5/P4 | record-runtime | promote ready attempt-local `.run` into canonical `current/<job-id>.run` while retaining the attempt-local copy | call | → S3, → S4, → N9, → N15 | → N7 | New |
| N9 | P6/P3 | build-runtime + store | queue build from canonical `.run` | call | → S1, → S2, → S3, → N10, → N15 | → N2, → N8 | Extended |
| N10 | P6 | build-runtime | build canonical `.run` into attempt-local `runs/<job-id>--attempt-00N.meeting` and persist logs/state | call | → S4, → N11, → N14, → N15 | → N9 | ✅ Exists, extended |
| N11 | P6/P4 | build-runtime | promote successful attempt-local `.meeting` into canonical `current/<job-id>.meeting` | call | → S3, → S4, → N12, → N15 | → N10 | New |
| N12 | P7 | publish-runtime | publish from `<work_root>/current` to `<site_root>` and persist publish logs/state | call | → S3, → S5, → N14, → N15 | → N11 | ✅ Exists, extended |
| N13 | P3 | store | canonical summary projection | write | → S1 | → N3, → N15 | Extended |
| N14 | P3 | store | attempt-history persistence | write | → S2 | → N1, → N7, → N9, → N10, → N12, → N3 | Extended |
| N15 | P3 | event hub | emit `job.updated` / `attempt.updated` from store write boundaries | call | → S6, → N4 | → U5 | ✅ Exists |

## Data Stores

| # | Place | Store | Description |
|---|-------|-------|-------------|
| S1 | P3 | `jobs` | Stable logical-job summary row. Artifact paths become canonical/effective `current/*` paths. |
| S2 | P3 | `job_attempts` | Attempt execution history. Initial attempt keeps retained attempt-local `.run`; rerun attempts keep fresh build/publish state with empty `record_*` fields. |
| S3 | P4 | `<work_root>/current` | Canonical successful artifacts only: `current/<job-id>.run`, `current/<job-id>.meeting`, plus hidden staging area. |
| S4 | P4 | `<work_root>/runs` | Retained attempt-local artifacts and logs, including all `.run` artifacts and per-attempt `.meeting` outputs. |
| S5 | P7 | `<site_root>` | Published site output refreshed from `current/`. |
| S6 | P3 | `event subscribers` | Existing in-process SSE subscriber registry fed from store write boundaries. |

## Wiring by place

| Place | Wiring |
|-------|--------|
| P1 Caller / Control Panel | U1 → N1 ; U2 → N2 ; U3/U4 → N3 ; U5 → N4 |
| P2 Operator HTTP API | N1 → N14 → N5/N6/N7 ; N2 → N3 (eligibility read) → N5 → N9 ; N3 → S1/S2 ; N4 ↔ S6 |
| P3 Store + event hub | N13/N14 write S1/S2 ; each write boundary emits N15 → S6 → N4 |
| P4 Work-root filesystem | N5 derives S3/S4 paths ; N6 writes attempt-local `.run` under S4 ; N8 promotes successful `.run` into S3 while retaining S4 ; N10 writes attempt-local `.meeting` under S4 ; N11 promotes successful `.meeting` into S3 |
| P5 Record runtime | N7 runs the only live record pass for the job ; success flows through N8 ; failure leaves retained `.run` only under S4 and does not create S3 canonical `.run` |
| P6 Build runtime | N9/N10 always read canonical `current/<job-id>.run` from S3 ; N10 writes attempt-local `.meeting` into S4 ; N11 promotes latest successful `.meeting` into S3 |
| P7 Publish runtime | N12 runs `cassini publish <work_root>/current --out <site_root>` so only canonical successful meeting bundles are publish-visible |

## What this breadboard clarifies

- The first cut needs **one new filesystem split**, not a new dynamic path model.
- The initial record pass is the only place that creates `.run`, but the first cut still **retains all `.run` artifacts** under `runs/`.
- Rerun admission is now simple and concrete: failed job + canonical ready `current/<job-id>.run`.
- We do **not** need a new reporting system or first-cut DB migration; the change is mainly in store semantics and runtime pathing.
- `current/` and `runs/` are no longer just storage details; they are the concrete contract that keeps publish clean while preserving attempt history.
- Long-term artifact retention / movement to hard storage is explicitly deferred and does not block implementation breadboarding or slicing.

Implementation slicing for this breadboard now lives in `./slices.md`.
