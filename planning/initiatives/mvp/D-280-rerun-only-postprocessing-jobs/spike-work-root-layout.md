---
shaping: true
---

## X2 Spike: Work-root layout and publish contract impact

### Context

A natural way to think about the problem is to split a job into:

- one non-repeatable recording boundary
- many repeatable downstream attempts

But the current repo does not model that split explicitly. Today the operator writes flat attempt outputs under one work root, and publish scans that same root for immediate child `.meeting` bundles.

Before we choose a new filesystem model, we need to know which contracts are actually coupled to the current flat layout and what the lowest-cost migration path would be.

### Goal

Identify the path/layout assumptions that matter today and recommend the smallest viable filesystem/path model for stage-aware rerun.

### Questions

| # | Question |
|---|----------|
| **X2-Q1** | Which current operator code paths, tests, docs, and read models assume flat attempt outputs like `<work-root>/<job-id>--attempt-XXX.run|.meeting|.logs/`? |
| **X2-Q2** | What exactly does `cassini publish <work-root> --out <site-root>` require from the filesystem shape, and can it consume nested job-centric meeting paths without changes? |
| **X2-Q3** | If we move to a stable `current/` + `runs/` shape under one `work_root`, what should live in each root and what should publish see? |
| **X2-Q4** | How should the initial `.run` be staged/promoted so `current/` contains only successful artifacts? |
| **X2-Q5** | Which layout best supports: preserved logs, honest attempt history, stable control-panel reads, no duplicate publish inputs, and minimal churn to existing `record` / `build` / `publish` functionality? |

### Findings

### X2-Q1 — Current flat-layout assumptions

The current operator runtime is tightly coupled to a single flat `work root`:

- `cassini-operator/internal/operator/attempt_paths.go`
  - `attemptRunPath(workRoot, jobID, attemptNumber)`
  - `attemptMeetingPath(workRoot, jobID, attemptNumber)`
  - `attemptLogsDir(workRoot, jobID, attemptNumber)`
- `record_runtime.go` writes `.run` bundles and record logs under that root.
- `build_runtime.go` writes attempt `.meeting` bundles and build logs under that root.
- `publish_runtime.go` currently shells out to:
  - `cassini publish <work-root> --out <site-root>`
- `cassini-operator/README.md` documents publish as scanning the whole work root.
- tests under `cassini-operator/internal/operator/run_test.go` assume attempt artifacts live directly under `rt.cfg.WorkRoot`.

So the current shape is not just documentation; it is embedded in path helpers, runtime wiring, tests, and README diagrams.

### X2-Q2 — What publish actually requires

`cassini publish` is more constrained than the current operator README suggests.

From `cassini-go-recorder/internal/cassini/publish.go`:

- when given a directory, it scans **immediate child directories only**
- each immediate child is considered a candidate meeting bundle
- ready `.meeting` bundles are copied into a staging dir for export
- nested job-centric meeting paths are **not** discovered recursively

That means a layout like:

```text
<root>/<job-id>/runs/003/output.meeting
```

will **not** be consumed by current publish behavior unless:

- the operator builds a flat publish-input shim/staging directory, or
- `cassini publish` itself is changed to recurse or follow a different contract

So a nested `recording/` + `runs/N/` model is conceptually clean, but it is **not compatible with the current publish contract as-is**.

### X2-Q3 / X2-Q5 — Assessed stable split-root model

The user-proposed model **does work**, and it looks like the lowest-churn structure so far:

- keep one configured `work_root`
- derive two fixed child roots from it:
  - `current/` = canonical successful artifacts only
  - `runs/` = attempt history and logs
- publish always reads only from `current/`
- attempt-local artifacts stay under `runs/`

### Recommended structure

```text
cassini-operator/runtime/
  jobs/                          # configured work_root
    current/                     # canonical successful artifacts only
      <job-id>.run/              # canonical ready record output for the job
      <job-id>.meeting/          # canonical latest successful meeting bundle
      .staging/                  # hidden promotion workspace

    runs/                        # attempt history and logs
      <job-id>--attempt-001.run/ # retained attempt-local record output
        ...
      <job-id>--attempt-001.logs/
        record.log
        build.log
        publish.log
      <job-id>--attempt-001.meeting/
        ...
      <job-id>--attempt-002.logs/
        build.log
        publish.log
      <job-id>--attempt-002.meeting/
        ...

  site/
```

### How the flow would work

#### 1. Record

- the initial record attempt writes its attempt-local `.run` under:
  - `runs/<job-id>--attempt-001.run`
- failed or partial `.run` bundles stay retained there for inspection
- only on success does the operator promote a canonical reusable copy into:
  - `current/<job-id>.run`
- record logs for attempt 1 still live under:
  - `runs/<job-id>--attempt-001.logs/record.log`
- later reruns do **not** create new `.run` bundles

#### 2. Build

- build attempt `N` reads from:
  - `current/<job-id>.run`
- build attempt `N` writes to:
  - `runs/<job-id>--attempt-00N.meeting`
- if build succeeds, promote that ready attempt bundle into:
  - `current/<job-id>.meeting`

This keeps the full build history while maintaining exactly one publish-visible meeting bundle per job.

#### 3. Publish

- publish always runs:
  - `cassini publish <work-root>/current --out <site-root>`
- the operator should derive that path internally from `work_root`; it should not become a user-configurable or dynamic per-attempt input
- publish logs remain attempt-scoped under `runs/<job-id>--attempt-00N.logs/publish.log`

Because publish sees only the canonical `.meeting` bundles under `current/`, it never sees duplicate ready bundles from historical attempts.

### Why this model fits well

1. **Stable filesystem contract**
   - one configured `work_root`
   - two fixed derived children: `current/` and `runs/`
   - easy to document and easy for the operator to derive consistently

2. **`current/` is always safe for publish**
   - only successful canonical artifacts live there
   - historical or failed attempt outputs stay out of publish discovery

3. **Simple rerun eligibility rule**
   - if `current/<job-id>.run` exists and is ready, rerun may start at `build`
   - if it does not, rerun is rejected

4. **No publish rewrite needed**
   - `publish` already accepts a directory of immediate child `.meeting` bundles
   - `current/` satisfies that contract directly

5. **Current build contract still works**
   - `cassini build` already accepts an explicit `.run` bundle path
   - the canonical run bundle can be reused across build reruns

6. **Logs and attempt history stay preserved**
   - each build/publish attempt still gets its own log directory
   - attempt-local `.meeting` outputs remain inspectable under `runs/`

7. **Control-panel reads stay plausible**
   - the stable `current/` + `runs/` split maps cleanly onto top-level job summary versus attempt history

### Important implementation consequences

#### A. `work_root` stays stable; sub-roots are derived

The operator config should still expose one `WorkRoot`.
Internally it should derive:

- `currentRoot = filepath.Join(WorkRoot, "current")`
- `runsRoot = filepath.Join(WorkRoot, "runs")`

So the filesystem contract is stable, but we do **not** add a new dynamic operator knob for publish input.

#### B. `current/` becomes the rerun gate

Because only successful canonical artifacts live under `current/`:

- `current/<job-id>.run` is the simple rerun-eligibility signal
- `current/<job-id>.meeting` is the simple publish-visible latest successful meeting signal

That is much easier to reason about than scanning attempt rows or partial bundles for recoverability.

#### C. Summary vs attempt path semantics must split cleanly

Today the top-level job summary and the current attempt often point at the same artifact paths.

With this model, they naturally diverge:

- job summary artifact paths want to mean:
  - `current/<job-id>.run`
  - `current/<job-id>.meeting`
- attempt artifact paths want to mean:
  - `runs/<job-id>--attempt-00N.meeting`
  - attempt-local logs

That means the store/update model should stop treating summary artifact paths as “latest attempt output path”.
Instead, they should become **effective canonical paths**, while attempt rows keep the per-attempt outputs.

#### D. The initial `.run` needs promotion semantics too

The same safety rule we want for `.meeting` promotion also applies to `.run`:

- do not expose failed or partial record output under `current/`
- keep the attempt-local `.run` under `runs/`
- stage first, then promote a successful ready copy into `current/`

That means the first cut intentionally keeps **all `.run` artifacts** in the attempt-history root while also keeping one canonical successful reusable `.run` in `current/` when record succeeded.

Post-MVP storage retention / movement to hard storage is a separate follow-up.

#### E. Promotion should avoid partial publish-visible bundles

Promoting a successful attempt bundle into `current/<job-id>.meeting` should not copy partial files into a publish-visible directory.

Safer pattern:

1. copy or assemble into `current/.staging/<job-id>.meeting`
2. once fully prepared, switch it into `current/<job-id>.meeting`
3. keep `.staging` outside publish discovery

The same principle applies to promoting the successful canonical `.run`.

#### F. Disk usage increases for successful build attempts

This model keeps:

- the attempt-scoped successful `.meeting` bundle under `runs/`
- the canonical promoted `.meeting` bundle under `current/`

So it duplicates successful meeting-bundle bytes.

That is likely acceptable for the first cut because it avoids CLI rewrites, but it is worth calling out explicitly.
Later optimizations could use:

- hardlinks
- reflinks
- or a different promotion primitive

if storage pressure matters.

### X2-Q4 — What nested job-centric layout would still need

If we instead choose a structure like:

```text
<root>/<job-id>/recording/
<root>/<job-id>/runs/001/
<root>/<job-id>/runs/002/
```

we would still need at least one of:

- a dedicated flat publish-input directory generated by the operator, or
- a change to `cassini publish` so it can discover nested meeting bundles

So nested layout is still viable, but it is no longer the lowest-churn option.

### Recommendation

**Recommended X2 outcome:**

Use one stable configured `work_root` with two derived child roots:

1. **`current/`** — canonical successful downstream artifacts only
2. **`runs/`** — attempt history, logs, and attempt-local downstream outputs

This gives us:

- no re-record on build/publish rerun
- no duplicate publish inputs
- a simple rerun gate (`current/<job-id>.run`)
- preserved logs and attempt history
- minimal change to `cassini build` / `cassini publish`
- a much smaller change set than a fully nested job-centric filesystem rewrite

### Minimal change set implied by this recommendation

1. **Operator config**
   - keep one `WorkRoot`
   - derive `current/` and `runs/` under it internally

2. **Path helpers**
   - add canonical path helpers:
     - `<current>/<job-id>.run`
     - `<current>/<job-id>.meeting`
   - keep attempt path helpers for:
     - `<runs>/<job-id>--attempt-XXX.run`
     - `<runs>/<job-id>--attempt-XXX.meeting`
     - `<runs>/<job-id>--attempt-XXX.logs/`

3. **Record runtime**
   - stop treating the main work root as the durable direct `.run` destination
   - stage/promote the successful ready `.run` into canonical `current/`
   - keep record logs attempt-scoped under `runs/`

4. **Build runtime**
   - read from canonical `current/<job-id>.run`
   - write attempt output to `runs/`
   - promote successful attempt output into canonical `current/<job-id>.meeting`

5. **Publish runtime**
   - publish from `current/`, not from the historical attempt root
   - keep publish logs attempt-scoped under `runs/`

6. **Store/read model**
   - top-level job artifact paths become canonical/effective paths
   - attempt rows keep attempt-scoped outputs and logs
   - initial-attempt `artifact_run_path` remains the retained attempt-local `.run` under `runs/`

7. **Tests + docs**
   - update operator tests that currently assume one flat `WorkRoot`
   - update README/runtime diagrams to explain `current/` versus `runs/`

### Acceptance

Spike is complete when we can describe:

- the concrete path/layout assumptions in current code
- whether nested job-centric paths are compatible with current publish behavior
- the recommended filesystem/path model for the rerun feature
- the minimal change set implied by that recommendation

This spike now answers those questions provisionally:

- current code assumes flat attempt outputs under one work root
- nested job-centric publish input is **not** compatible with current publish discovery as-is
- a stable `current/` + `runs/` model under one `work_root` appears to be the lowest-churn structure
- the first cut keeps all `.run` artifacts under `runs/` and promotes a canonical successful copy into `current/`
