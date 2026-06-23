---
shaping: true
---

# V5 Brief — Failure inspection and rerun flow

## What this slice is

V5 extends the existing operator job system so failed jobs are inspectable and can be rerun safely.

V1 established the persisted job/control-plane backbone.
V2 made the record stage real and added explicit stop handling.

But the operator runtime still lacks a practical recovery loop after failure:

- failed jobs do not yet preserve enough structured detail for operator inspection
- there is no explicit rerun endpoint for an existing failed job
- there is no persisted rerun lifecycle that distinguishes the original failed attempt from the retry

The outcome we want is straightforward:

- an operator can inspect why a job failed from persisted job state and API responses
- an operator can rerun a failed job without reconstructing the original request manually
- the rerun follows explicit status transitions
- if the rerun succeeds, the published output reflects the successful rerun

## Why this matters now

The current repo has a real operator-backed pipeline:

- `POST /jobs?provider=nextcloud-talk`
- `POST /jobs/:id/stop`
- `GET /jobs`
- `GET /jobs/:id`
- persisted SQLite job rows
- stage-separated `record`, `build`, and `publish` execution

That is enough to trigger work and observe happy-path progress, but not enough for pilotable operations.

When a recording, build, or publish step fails, an operator needs two things the current runtime does not yet provide cleanly:

1. durable failure context that survives the process and can be inspected later
2. an explicit, safe way to retry the same job from the operator API

Without this slice, the operator remains usable mainly for demos and supervised development runs. V5 makes it viable as an operator workflow rather than just a background executor.

## The problem we are solving

The operator already persists lightweight job state and some failure extraction from downstream manifests, but that is not yet shaped as a proper failure-inspection and rerun model.

In practice, V5 has to answer:

- what failure details are persisted on the job row versus stored as attached log/artifact references
- how an operator inspects those details through `GET /jobs` and `GET /jobs/:id`
- how rerun reuses preserved request/context safely and explicitly
- whether rerun creates a new attempt within the same job identity or clones into a new job identity
- how status transitions remain honest across original failure and rerun execution
- how publish behaves when a rerun succeeds after a previous failure

## Selected implementation direction

The selected direction for V5 should be:

- keep the existing operator boundary intact: orchestration/persistence in `cassini-operator`, work execution in `cassini`
- extend persisted job records with enough structured failure and rerun metadata for inspection
- add `POST /jobs/:id/rerun` for explicit operator-triggered rerun
- keep rerun semantics explicit and conservative: only rerun from a failed terminal job, and reuse preserved request/context rather than inventing implicit fallback behavior
- preserve a visible attempt history on the persisted job state rather than hiding retries as if nothing happened
- keep the publish model unchanged at a high level: successful reruns still flow through the normal build/publish stages and refresh the shared hosted output

## In scope

### 1. Persisted failure inspection surface

- persist enough failure detail for operator inspection after `record`, `build`, or `publish` failure
- preserve references to the relevant artifacts, logs, and rerun inputs needed to understand what failed
- expose that persisted detail through the job API

### 2. Explicit rerun endpoint

- add `POST /jobs/:id/rerun`
- accept rerun only for jobs in an appropriate failed terminal state
- preserve the original normalized request payload and reuse it explicitly for rerun

### 3. Rerun lifecycle and state transitions

- persist clear rerun status transitions
- distinguish the original failed attempt from the rerun attempt in job state
- ensure queueing/admission rules remain consistent with the existing record/build/publish runtimes

### 4. Hosted output refresh on successful rerun

- if rerun reaches a successful publish, the hosted site should reflect the successful attempt
- artifact references on the job should point to the effective successful output after rerun

### 5. Operator-facing observability

- make failure and rerun state visible enough through `GET /jobs` and `GET /jobs/:id` that an operator can tell:
  - what failed
  - in which stage it failed
  - whether the job has been rerun
  - whether the rerun succeeded or failed

## Explicitly out of scope

These do not belong in V5:

- a new operator dashboard or front-end UI
- broad workflow redesign beyond the existing API/status surface
- automatic retries or policy-driven retry backoff
- generalized resumability across arbitrary partial subprocess state
- packaging/release work
- summary generation or viewer changes except where downstream publish verification requires them
- expanding the operator into a second implementation of record/build/publish logic

## Repo signal that already helps

The current repo already contains most of the runtime spine V5 needs to extend.

### `cassini-operator` already owns lifecycle control

The operator already:

- validates trigger requests
- persists jobs in SQLite
- marks startup interruptions
- runs `cassini doctor`, `cassini record`, `cassini build`, and `cassini publish`
- persists stop metadata
- reads downstream `cassini.json` manifests back on failure for lightweight reporting

That means V5 should extend the job model and API behavior rather than redesigning the operator.

### Stage-separated runtimes already exist

The current concurrency model already distinguishes:

- record admission with fixed slots and no durable queue
- queue-backed build workers
- a single publish worker

Rerun should plug into those same semantics rather than introducing a parallel retry subsystem.

### Publish already refreshes from the shared work root

The publish stage already rebuilds the shared hosted site from the operator work root.

That means a successful rerun should be able to refresh hosted output through the normal publish path, provided the successful attempt leaves the right meeting artifacts in place and the job record points at them honestly.

## Main shaping answers for V5

1. **Rerun is operator-explicit**
   - V5 adds `POST /jobs/:id/rerun`.
   - Rerun is never automatic.

2. **Rerun starts from a failed terminal job only**
   - V5 should reject rerun for active, queued, interrupted, or already-succeeded jobs unless later shaping explicitly expands that contract.

3. **Failure context must be durable**
   - Failure inspection should not depend on transient process memory or scrolling daemon logs.
   - The job row should retain enough structured detail and file references for later inspection.

4. **Rerun should preserve visible history**
   - A rerun should not erase the evidence of the original failure.
   - Operators need to see both that the job failed and that it was rerun.

5. **Publish semantics stay normal**
   - A successful rerun should continue through build/publish using the existing pipeline.
   - Hosted output refresh should remain a consequence of successful publish, not a special side channel.

## Likely code areas

Based on the current repo shape and operator runtime, the likely touch points are:

- `cassini-operator` HTTP handlers for job read/rerun endpoints
- `cassini-operator` job persistence schema and migrations
- `cassini-operator` job store/read models and API serialization
- `cassini-operator` worker/runtime lifecycle code for rerun admission and transition handling
- `cassini-operator` failure extraction/log persistence paths around record/build/publish subprocesses
- operator README and runtime docs describing the rerun/failure model

## What “done” should look like

A reviewer should be able to:

1. trigger or reproduce a job failure
2. inspect the failed job through persisted API-visible state
3. see preserved failure reason, relevant log/detail pointers, and the rerun inputs/context
4. call `POST /jobs/:id/rerun`
5. observe rerun-specific status transitions on the persisted job
6. confirm the rerun re-enters the normal record/build/publish pipeline safely
7. if the rerun succeeds, confirm the hosted output reflects the successful rerun

## Expected effort

**Effort: medium**

This is narrower than V2 in runtime breadth, but it reaches into persistence shape, API semantics, and lifecycle truthfulness.

A reasonable planning read is:

- **core implementation effort:** medium
- **coordination / decision risk:** medium
- **overall confidence:** moderate, because the current operator runtime already has the right stage model, but the rerun contract and persisted attempt model need clear choices before implementation

## Remaining unknowns and blanks that should stay explicit

Several design choices should remain explicit while shaping continues.

### 1. Job identity versus attempt identity

The biggest open modeling choice is whether rerun:

- mutates one stable job row with explicit attempt metadata/history, or
- creates a new child/sibling job while linking back to the original failed job

The current MVP slice summary suggests a rerun endpoint on the same job surface, but the exact persisted attempt model still needs a deliberate choice.

### 2. Failure detail granularity

We know V5 must persist enough detail for inspection, but the exact shape is still open:

- a structured error payload on the job row
- persisted log excerpts or file paths
- per-stage failure metadata
- whether API list responses expose summaries while detail responses expose the full shape

### 3. Artifact reuse rules

The issue calls for rerun inputs to be preserved, but V5 still needs a clear rule for what gets reused:

- rerun the full job from record again
- reuse some existing artifacts when safe
- or choose stage-specific reuse rules

That behavior should be explicit rather than inferred.

### 4. Interaction with interrupted jobs

V2 already marks non-terminal startup jobs as `interrupted`.

It is still open whether V5 rerun semantics should:

- apply only to `failed`, or
- also support selected `interrupted` jobs once that state is better understood

### 5. API shape breadth

The acceptance criteria require rerun and failure inspection, but not necessarily additional endpoints beyond:

- `GET /jobs`
- `GET /jobs/:id`
- `POST /jobs/:id/rerun`

Whether any narrower log/download endpoint is needed should be justified by actual inspection requirements rather than added by default.
