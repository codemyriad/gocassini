---
shaping: true
---

# Operator Control Panel — Shaping

This document shapes Linear **D-266**.

It elaborates:

- `./framing.md`
- `planning/initiatives/mvp/shaping.md`
- `cassini-operator/README.md`
- `cassini-viewer/README.md`

## Working position

**Provisional working shape: B — dedicated `cassini-control-panel` app + operator snapshots + live SSE job updates.**

Reason: it best matches the explicit note that we will probably need live stage/state subscriptions, while keeping the control panel thin and leaving `cassini-operator` as the owner of job state.

**Fallback if scope tightens:** Shape A (polling) is the obvious cutline if live subscription work turns out to be too much for the first slice.

---

## Requirements (R)

| ID | Requirement | Status |
|----|-------------|--------|
| R0 | An operator can start a new job from the browser by entering a meeting URL and submitting it. | Core goal |
| R1 | An operator can watch jobs play out from the browser after triggering a job or opening the control panel mid-run. | Core goal |
| R2 | The control panel talks only to `cassini-operator`; it does not read SQLite, work-root bundles, or published-site storage directly. | Must-have |
| R3 | **Live observability contract** | |
| R3.1 | The UI shows current/recent jobs with at least the high-level lifecycle the operator already uses: `record`, `build`, `publish`, `done`. | Must-have |
| R3.2 | Active-job changes should come from an operator-owned live subscription surface rather than manual refresh or poll-only as the planned shape. | Leaning yes |
| R3.3 | 🟡 The first version should present a full jobs history/list view in the style of a GitHub Actions run history: many jobs visible from day one, with a selected run detail model rather than a single-job dashboard. | Must-have |
| R4 | The dashboard lives in `<root>/cassini-control-panel` as a Svelte app following `cassini-viewer` conventions, not as a feature bolted into another package. | Must-have |
| R5 | Configuration is explicit via `CASSINI_OPERATOR_URL`. | Must-have |
| R6 | The solution reuses the current operator trigger/read model and extends `cassini-operator` only where the current API is insufficient for live observability. | Must-have |
| R7 | 🟡 The first cut stays narrow around operator control and observability: start job, stop job, and observe job history/state. Styling, Nextcloud embedding, rerun controls, and debug/failure-simulation controls stay out of the initial slice. | Must-have |
| R8 | 🟡 The shape should leave room for later rerun/failure-detail/substage expansion without forcing all of that into the first vertical cut. | Leaning yes |

---

## CURRENT: Repo and runtime baseline

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **CURRENT1** | `cassini-operator` already exposes `POST /jobs?provider=nextcloud-talk`, `POST /jobs/:id/stop`, `POST /jobs/:id/rerun`, `GET /jobs`, and `GET /jobs/:id`. | |
| **CURRENT2** | The operator already persists logical jobs plus attempt history, with high-level stage/state transitions around `record`, `build`, `publish`, and terminal `done`. | |
| **CURRENT3** | The current read surface is snapshot-oriented JSON. There is no browser-facing live subscription endpoint yet. | |
| **CURRENT4** | `cassini-operator` is already the correct owner of runtime state; SQLite, work-root bundles, and published-site output are operator internals. | |
| **CURRENT5** | `cassini-viewer` provides the right frontend conventions: Svelte + Vite, a single app shell, simple local state, and repo-local scripts. | |
| **CURRENT6** | `cassini-viewer` is an artifact-consumption app, not an operator workflow UI. | |
| **CURRENT7** | There is no `cassini-control-panel` package yet, and nothing in the repo currently consumes a `CASSINI_OPERATOR_URL` frontend config. | |
| **CURRENT8** | The current operator stage model is already sufficient for the minimum required dashboard view (`record`, `build`, `publish`), while richer substages are not exposed yet. | |

---

## A: Dedicated control panel with polling over existing operator reads

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **A1** | Create a new `cassini-control-panel` Svelte/Vite app at repo root, following `cassini-viewer` conventions. | |
| **A2** | Read `CASSINI_OPERATOR_URL` from frontend config and centralize operator HTTP calls in one small client layer. | |
| **A3** | Provide a simple trigger form that submits `POST /jobs?provider=nextcloud-talk` with the meeting URL and selects the accepted job. | |
| **A4** | Load `GET /jobs` on boot and poll `GET /jobs` / `GET /jobs/:id` on an interval while non-terminal jobs exist. | |
| **A5** | Render job cards/detail from existing snapshot fields: stage, state, timestamps, error summary, and attempt history when requested. | |
| **A6** | Keep operator changes minimal: no new live API, only whatever small headers/config are needed to let the app call the operator. | |

## B: Dedicated control panel with operator snapshots + live SSE updates

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **B1** | Create a new `cassini-control-panel` Svelte/Vite app at repo root, following `cassini-viewer` conventions. | |
| **B2** | Read `CASSINI_OPERATOR_URL` from frontend config and centralize both HTTP and SSE operator access in one small client layer. | |
| **B3** | 🟡 Provide trigger + stop actions: submit `POST /jobs?provider=nextcloud-talk` with the meeting URL, and expose `POST /jobs/:id/stop` on running record-stage jobs. | |
| **B4** | 🟡 Extend `cassini-operator` with a tagged SSE event feed sourced from operator-owned DB writes. Events reuse the persisted job/attempt vocabulary (`job_id`, attempt, stage, state, timestamps, error/stop fields) so snapshots and live updates stay shape-compatible. | |
| **B5** | 🟡 Bootstrap from `GET /jobs`, render a GitHub-Actions-like run history list from day one, and keep that list warm from the SSE feed; on reconnect, refresh snapshots before resuming the stream. | |
| **B6** | 🟡 Use `GET /jobs/:id` for selected-job detail and attempt history. The same tagged event feed can update selected-job local state client-side; if that feels too complex in implementation, cut back to snapshot refresh for detail while keeping live history. | |
| **B7** | First cut renders high-level stages first; richer substages later extend the event/detail payloads without changing the basic UI shape. | |

## C: Dashboard shortcut via direct runtime storage inspection

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **C1** | Build a dashboard that reads operator-owned runtime data directly from SQLite, bundle manifests, or derived files instead of treating `cassini-operator` as the only state API. | |
| **C2** | Reconstruct job progress from storage artifacts and filesystem changes rather than from operator-owned read models. | |
| **C3** | Trigger work through a dashboard-owned side path instead of the existing operator HTTP control surface. | |

---

## Fit Check

| Req | Requirement | Status | A | B | C |
|-----|-------------|--------|---|---|---|
| R0 | An operator can start a new job from the browser by entering a meeting URL and submitting it. | Core goal | ✅ | ✅ | ✅ |
| R1 | An operator can watch jobs play out from the browser after triggering a job or opening the control panel mid-run. | Core goal | ✅ | ✅ | ✅ |
| R2 | The control panel talks only to `cassini-operator`; it does not read SQLite, work-root bundles, or published-site storage directly. | Must-have | ✅ | ✅ | ❌ |
| R3.1 | The UI shows current/recent jobs with at least the high-level lifecycle the operator already uses: `record`, `build`, `publish`, `done`. | Must-have | ✅ | ✅ | ✅ |
| R3.2 | Active-job changes should come from an operator-owned live subscription surface rather than manual refresh or poll-only as the planned shape. | Leaning yes | ❌ | ✅ | ❌ |
| R3.3 | Attempt/substage detail is welcome when available, but it is the first thing to cut if scope gets tight. | Nice-to-have | ✅ | ✅ | ❌ |
| R4 | The dashboard lives in `<root>/cassini-control-panel` as a Svelte app following `cassini-viewer` conventions, not as a feature bolted into another package. | Must-have | ✅ | ✅ | ✅ |
| R5 | Configuration is explicit via `CASSINI_OPERATOR_URL`. | Must-have | ✅ | ✅ | ❌ |
| R6 | The solution reuses the current operator trigger/read model and extends `cassini-operator` only where the current API is insufficient for live observability. | Must-have | ✅ | ✅ | ❌ |
| R7 | The first cut stays narrow: start job + observe job. Styling, Nextcloud embedding, and debug/failure-simulation controls stay out of the initial slice. | Must-have | ✅ | ✅ | ❌ |
| R8 | The shape should leave room for later stop/rerun/detail actions without forcing those controls into the first vertical cut. | Leaning yes | ✅ | ✅ | ❌ |

**Notes:**

- **A** fails R3.2 only. It is still the clean fallback if we decide a poll-based first cut is enough.
- **B** is the best current fit because it keeps state ownership in `cassini-operator`, supports the desired “see it play out” feel, and still keeps the UI thin.
- **C** fails the explicit storage boundary and would create a second, drift-prone interpretation of operator state.

---

## Working shape: B

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **B1** | **Control-panel package** | |
| B1.1 | Create `<root>/cassini-control-panel` as a small Svelte/Vite app modeled after `cassini-viewer`'s repo conventions. | |
| B1.2 | Keep the first UI intentionally plain: trigger form, jobs list, active-job emphasis, basic status messaging. | |
| **B2** | **Config + operator client** | |
| B2.1 | Read `CASSINI_OPERATOR_URL` from frontend env and fail clearly when it is missing. | |
| B2.2 | Centralize `fetch` + SSE handling in one operator client module so UI components stay dumb about transport details. | |
| **B3** | **Trigger flow** | |
| B3.1 | Submit the meeting URL to `POST /jobs?provider=nextcloud-talk`. | |
| B3.2 | On `202 Accepted`, add/select the returned job immediately and let live updates fill in the stage transitions. | |
| B3.3 | On `400`/`503`, show the operator-shaped error in the panel rather than inventing a second validation model. | |
| **B4** | **Snapshot + stream read model** | |
| B4.1 | Use `GET /jobs` for initial load and reconnect reconciliation. | |
| B4.2 | Use one tagged SSE feed for live operator updates; the control panel filters and applies those events into local list/detail state. | |
| B4.3 | Keep `GET /jobs/:id` as the on-demand detail surface for attempt history, terminal error detail, and truth-refresh when the selected job becomes active or the stream reconnects. | |
| **B5** | **Dashboard vocabulary** | |
| B5.1 | The first cut centers on the existing high-level stages: `record`, `build`, `publish`, `done`. | |
| B5.2 | Attempt/substage detail is additive: show it only if the operator exposes it cleanly, and cut it first if needed. | |
| **B6** | **Operator extension** | |
| B6.1 | Emit an SSE event on every successful operator DB write that changes job or attempt state. | |
| B6.2 | Keep event payloads shape-compatible with the persisted `Job` / `JobAttempt` read models rather than inventing a second event schema. | |
| B6.3 | Leave room to add non-DB internal process events later, but keep v1 limited to operator-controlled/persisted state transitions. | |
| **B7** | **Scope guardrails** | |
| B7.1 | No direct storage reads from the browser. | |
| B7.2 | 🟡 First cut includes start + stop, but still excludes rerun/debug/failure-simulation controls. | |
| B7.3 | No styling pass or Nextcloud shell work in this ticket. | |

---

## Detail B: Concrete affordances

### Places

| # | Place | Description |
|---|-------|-------------|
| P1 | Cassini Operator | Existing HTTP/runtime owner of job and attempt state; extended with live event broadcast. |
| P2 | Control Panel — history/list | GitHub-Actions-like run history surface showing many jobs from day one. |
| P3 | Control Panel — selected run detail | Selected-job inspector showing current state, attempt history, and stop control when eligible. |
| P4 | Control Panel — trigger bar | Minimal operator action area for entering a meeting URL and starting a job. |

### UI Affordances

| # | Place | Component | Affordance | Control | Wires Out | Returns To | Status |
|---|-------|-----------|------------|---------|-----------|------------|--------|
| U1 | P4 | control-panel | meeting URL input | type | → N4 | — | New |
| U2 | P4 | control-panel | start job button | click | → N4 | → U3, → U5 | New |
| U3 | P2 | control-panel | jobs history list | render | → U4 | — | New |
| U4 | P2 | control-panel | job history row | click | → N3 | → U5 | New |
| U5 | P3 | control-panel | selected job detail | render | → U6 | — | New |
| U6 | P3 | control-panel | stop button | click | → N5 | → U5 | New |
| U7 | P2/P3 | control-panel | live connection status | render | → N6 | — | New |

### Code Affordances

| # | Place | Component | Affordance | Control | Wires Out | Returns To | Status |
|---|-------|-----------|------------|---------|-----------|------------|--------|
| N1 | P2/P3/P4 | control-panel | `loadConfig()` | init | → S1 | → N2, → N3, → N4, → N5, → N6 | New |
| N2 | P2 | control-panel | `fetchJobs()` | call | → P1, → S2 | → U3 | New |
| N3 | P3 | control-panel | `fetchJobDetail(jobId)` | call | → P1, → S3 | → U5 | New |
| N4 | P4 | control-panel | `startJob(url)` | call | → P1, → N2, → N3 | → U3, → U5 | New |
| N5 | P3 | control-panel | `stopJob(jobId)` | call | → P1, → N3 | → U5 | New |
| N6 | P2/P3 | control-panel | `openEventStream()` | observe | → P1, → N7, → N8, → S4 | → U3, → U5, → U7 | New |
| N7 | P2 | control-panel | `applyEventToJobsHistory()` | call | → S2 | → U3 | New |
| N8 | P3 | control-panel | `applyEventToSelectedJob()` | call | → S3 | → U5 | New |
| N9 | P1 | operator | `publishStateChangeEvent()` | call | → S5 | — | New |
| N10 | P1 | operator | `GET /events` SSE handler | call | → S5 | → N6 | New |
| N11 | P1 | operator | `job/attempt DB write paths` | write | → S6, → S7, → N9 | — | Extended |

### Data Stores

| # | Place | Store | Description |
|---|-------|-------|-------------|
| S1 | P2/P3/P4 | `panel config` | Browser-side resolved `CASSINI_OPERATOR_URL` and transport settings. |
| S2 | P2 | `jobs history state` | Local list state derived from `GET /jobs` plus live events. |
| S3 | P3 | `selected job state` | Local detail state derived from `GET /jobs/:id` plus live events. |
| S4 | P2/P3 | `event stream status` | Connected / reconnecting / disconnected state for the SSE feed. |
| S5 | P1 | `event subscribers` | In-process SSE subscriber registry/broadcast hub. |
| S6 | P1 | `jobs` | Existing logical-job summary persistence. |
| S7 | P1 | `job_attempts` | Existing attempt-history persistence. |

### Wiring by place

| Place | Wiring |
|-------|--------|
| P4 Trigger bar | U1/U2 → N4 → P1 ; N4 → N2/N3 for post-create refresh/select |
| P2 History list | N1 → N2 ; U3/U4 → N3 ; N6 → N7 → S2 → U3 |
| P3 Selected run detail | N1/U4 → N3 ; U6 → N5 ; N6 → N8 → S3 → U5 |
| P1 Operator | Existing write paths N11 → N9 → S5 → N10 → N6 ; snapshot reads serve N2/N3 |

---

## Decisions made

1. **🟡 Jobs history from day one:** the first version should show a full jobs/run list, more like GitHub Actions history than a single-job inspector.
2. **🟡 Detail streaming direction:** prefer live selected-job detail streaming in addition to the global list stream, but spike it and cut back to snapshot-on-demand if it complicates the first slice too much.
3. **🟡 Include stop:** first cut includes stop for eligible running jobs.

## Live-events direction after spike input

1. **🟡 Preferred v1 model:** one tagged SSE feed sourced from operator DB writes, plus snapshot GET endpoints.
2. **🟡 UI responsibility:** the control panel keeps filtered local list/detail state from that feed rather than asking the operator to route separate summary/detail streams first.
3. **🟡 Follow-up boundary:** richer internal process/build-substage events stay out of v1 because the current CLI shell-out boundary does not expose them cleanly.

## Spikes

- `./spike-live-operator-events.md` — records the assessed options, recommended v1 event model, reconnect behavior, and operator hook points.

No breadboarding yet by design. After the spike, the next artifact should be `slices.md` for the first vertical cut.
