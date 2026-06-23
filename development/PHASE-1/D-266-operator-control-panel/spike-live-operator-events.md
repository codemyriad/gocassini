---
shaping: true
---

# Spike: Live operator events for control-panel history + selected job detail

## Context

D-266's first cut now includes:

- a full jobs/run history view from day one
- start job
- stop job
- live observation while jobs move through `record`, `build`, `publish`, and `done`

`cassini-operator` already exposes snapshot APIs (`GET /jobs`, `GET /jobs/:id`) and owns the durable job/attempt state. It does **not** yet expose a live subscription surface.

The current shaping direction from discussion is:

- keep snapshots and live events **shape-compatible**
- treat live events as operator-owned tagged state-change logs
- emit events from the same places that persist job/attempt changes
- keep v1 limited to what the operator already controls and persists
- defer richer internal build/publish substage events to follow-up work

## Goal

Determine the smallest viable live-events contract in `cassini-operator` that supports:

- a GitHub-Actions-like jobs history list that updates live
- a selected-job detail view that can also update live without requiring a second routed stream first
- clean reconnect/bootstrap behavior without making the control panel the owner of state truth
- future compatibility with OTEL/log export thinking, where these events can be treated as structured operator logs

## Assessment of candidate shapes

### Option A — global snapshot + stream, plus particular-job snapshot + stream routed within the operator

**What it is**
- `GET /jobs` + `GET /jobs/:id`
- global SSE stream for job summaries
- separate per-job SSE stream for selected-job detail (or an operator-routed filter mode)

**Pros**
- simple for the UI consumer at read time
- smaller payloads per subscriber when detail streams are scoped
- operator can hide some filtering/state-assembly complexity from the client

**Cons**
- pushes subscription-routing complexity into `cassini-operator`
- likely duplicates delivery paths: summary stream shape vs detail stream shape
- increases operator fan-out surface area and testing matrix early
- moves away from the “events as logs” intuition; the operator becomes a view router, not just an event source

**Assessment**
- viable, but heavier than needed for v1
- better if we later prove very high event volume or many subscribers, neither of which is the current problem

### Option B — tagged event-only feed; every subscriber receives everything and filters locally

**What it is**
- `GET /jobs` + `GET /jobs/:id` remain the snapshot truth
- one SSE feed emits tagged structured events whenever persisted job/attempt state changes
- subscriber maintains local filtered list/detail state from the feed

**Pros**
- best match for your OTEL/log-shape intuition
- one delivery mechanism only
- simplest operator contract: emit events, don’t route views
- snapshots + live feed can share vocabulary and object shape
- easy cutline: if detail handling is too much in UI, still keep same feed and just reduce client behavior

**Cons**
- client must maintain filtered state locally
- every subscriber receives all events, which can be wasteful at scale
- reconnect does not include replay unless we later add event ids / resume semantics

**Assessment**
- best fit for v1
- event volume is currently low enough that local filtering is a cheap trade
- strongest alignment with “every DB write emits an event” and future OTEL export

### Option C — tagged event-only feed, filtered within the operator per subscriber

**What it is**
- runtime emits tagged events
- operator keeps subscriber filter registrations and only forwards matching events to each subscriber

**Pros**
- preserves one underlying event vocabulary
- reduces per-client payload volume relative to Option B
- can still evolve toward richer subscription semantics later

**Cons**
- still adds routing/filter state to `cassini-operator`
- more moving parts than needed for the first slice
- more complex subscriber lifecycle and test burden than simple broadcast

**Assessment**
- a reasonable step after B if event volume or subscriber count becomes a real concern
- not the best first cut

## Recommendation

**Recommend Option B for v1:**

- keep **snapshot GET endpoints** as truth/bootstrap
- add **one tagged SSE feed** for live updates
- emit an event **every time operator-owned DB state is successfully written**
- let the **control panel** maintain filtered local list/detail state from that feed

This is the smallest shape that satisfies:

- full jobs history view from day one
- selected-job live feel without requiring a second operator-routed stream
- shape compatibility between snapshot and live state
- future OTEL/log export alignment

## Recommended v1 contract

### Snapshots

- `GET /jobs` → current job-summary list
- `GET /jobs/:id` → selected-job summary + attempt history

### Live feed

- one SSE endpoint, e.g. `GET /events`
- broadcast structured tagged events for persisted job/attempt updates
- no replay/resume in v1 beyond reconnect + snapshot refresh

### Event source rule

**Every successful DB write that changes persisted job/attempt state should emit one event.**

That means the operator remains the owner of truth:

1. write to DB
2. read/assemble the updated persisted shape if needed
3. publish the event

This keeps snapshots and live updates aligned by construction.

## Event-shape direction

For v1, keep event payloads compatible with current read models rather than inventing a second schema.

Suggested envelope:

```json
{
  "type": "job.updated",
  "job_id": "01...",
  "at": "2026-05-06T...Z",
  "job": { "... current Job summary row ..." },
  "attempt": { "... current JobAttempt row when relevant ..." }
}
```

Notes:
- `job` is the main payload for history/list updates
- `attempt` is optional, included when the write affected the current attempt or when attempt detail is relevant
- tags live in the payload naturally through existing fields: `stage`, `state`, `current_attempt_number`, `stop_reason`, etc.
- this is already close to “structured logs the UI can consume”

Possible event types for readability, while still using the same payload shape:
- `job.created`
- `job.updated`
- `attempt.updated`

But even a single `job.updated` event type would work if the payload is consistent.

## Q-by-Q conclusions

### X1-Q1 — smallest viable SSE surface

**Conclusion:** one tagged broadcast feed + snapshot GETs.

### X1-Q2 — what emits events

**Conclusion:** all operator-controlled persisted state transitions.

For v1 that means:
- every successful write to `jobs`
- every successful write to `job_attempts`
- startup interruption marking updates
- stop metadata updates
- stage/state queue/running/succeeded/failed/interrupted updates

**Explicitly out for now:** internal build/transcriber/publish subprocess milestones that are not already persisted through operator-owned state.

### X1-Q3 — can current read models be reused

**Conclusion:** yes.

That is the recommended direction.

### X1-Q4 — reconnect model

**Conclusion:** keep it simple:
- `GET` for snapshot/bootstrap
- SSE for live feed
- on reconnect, refresh snapshot and continue listening

No v1 replay buffer or durable event cursor required.

### X1-Q5 — where event fan-out hooks live

**Conclusion:** at the same write boundaries that persist DB state.

In the current codebase this points to store methods such as:
- `InsertQueuedJob`
- `MarkRecordRunning`
- `MarkRecordStopRequested`
- `UpdateRecordOutcome`
- `MarkRecordFailed`
- `MarkBuildQueued`
- `MarkBuildRunning`
- `MarkBuildSucceeded`
- `MarkBuildFailed`
- `MarkPublishQueued`
- `MarkPublishRunning`
- `MarkPublishSucceeded`
- `MarkPublishFailed`
- rerun/attempt insertion paths in `attempt_store.go`
- startup interruption updates in `startup_store.go`

This is attractive because operator writes are already fairly centralized in store methods.

### X1-Q6 — clean fallback cutline

**Conclusion:** keep the same feed, but reduce the UI behavior.

Fallback shape:
- history list stays live from SSE
- selected-job detail uses `GET /jobs/:id` on selection and after meaningful events/reconnect
- no second operator event mechanism required

### X1-Q7 — local-dev serving / CORS

Clarification:
- if `cassini-control-panel` is a pure browser SPA, then the browser connects **directly** to `cassini-operator` for both `fetch` and SSE, so **operator-side CORS** (or a Vite dev proxy) is the relevant concern
- if you instead want a separate control-panel server/BFF that consumes operator events and re-streams to the browser, that is a materially different shape and adds another runtime boundary

**Recommended v1 dev path:**
- keep `cassini-control-panel` as a plain Svelte app
- either use a Vite proxy in dev, or allow a small explicit CORS policy in `cassini-operator`
- do **not** add a separate control-panel backend just to filter/rebroadcast events in v1

## Concrete operator work implied

1. Add an in-process event hub / broadcaster to `cassini-operator`
2. Add one SSE HTTP handler, e.g. `GET /events`
3. Publish an event after each successful persisted job/attempt write
4. Reuse existing `Job` / `JobAttempt` reads to assemble event payloads
5. Add minimal dev-serving support via proxy or CORS

## Acceptance

Spike is complete because we can now describe:

- the recommended v1 SSE contract for `cassini-operator` → **one tagged broadcast feed + snapshot GETs**
- whether selected-job detail should stream live in v1 → **yes, client-side from the same feed when convenient; fallback to snapshot detail without changing operator contract**
- the reconnect/bootstrap model → **GET snapshot, then SSE; refresh snapshot on reconnect**
- the concrete operator code areas for event fan-out → **store write paths and startup interruption updates**
- the explicit cutline → **history live, detail snapshot**
