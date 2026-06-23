---
shaping: true
---

## X3 Spike: Jobs reporting model and `/events` compatibility for build+publish reruns

### Context

We now have a provisional filesystem shape for rerun:

- one stable configured `work_root`
- `<work_root>/current` is the canonical successful artifact root
- `<work_root>/runs` is the attempt-history root
- only the initial successful record pass can produce the reusable `.run`
- every accepted rerun always reruns `build` + `publish`
- rerun never starts at `publish` only

That means the operator reporting model can no longer assume that the top-level job row and the current attempt row describe the same artifact paths.

We need to understand whether the existing jobs database and `/events` feed can support this shape honestly, and what minimum changes are needed.

### Goal

Determine the minimum database/reporting changes needed so that:

- the top-level job remains a stable summary row
- attempts 2, 3, ... remain inspectable and stream correctly through `/events`
- canonical artifacts and attempt-local artifacts are not confused
- users and the control panel can keep querying job / run status without a new reporting system
- we avoid a migration unless it buys us something the first cut actually needs

### Questions

| # | Question |
|---|----------|
| **X3-Q1** | Can the existing `jobs` + `job_attempts` model represent canonical `current/` artifacts and attempt-local `runs/` artifacts without becoming misleading? |
| **X3-Q2** | Which current store/update behaviors must change even if the schema stays the same? |
| **X3-Q3** | Can we keep the current DB schema for the first cut, and if so what semantics make that honest enough? |
| **X3-Q4** | Does the current `/events` feed shape already support attempts 2, 3, ... for control-panel usage? |
| **X3-Q5** | If we later widen rerun boundary types or entry stages, what migration would become worth doing? |

### Findings

### X3-Q1 — Existing model is a strong base, but summary and attempt semantics must split

The current schema already has the right high-level shape:

- one stable `jobs` row per logical job
- many `job_attempts` rows keyed by `(job_id, attempt_number)`
- attempt-scoped stage/state/timestamps/log paths
- `/events` payloads that already carry:
  - `job_id`
  - `attempt_number`
  - full `job`
  - optional full `attempt`

So we do **not** need a second reporting system.

But under the new filesystem shape, the semantics must diverge:

- **`jobs` row** = canonical/effective job summary
  - `artifact_run_path` should mean canonical `current/<job-id>.run`
  - `artifact_meeting_path` should mean canonical `current/<job-id>.meeting`
- **`job_attempts` row** = attempt-local execution record
  - on the initial record attempt, `artifact_run_path` should mean `runs/<job-id>--attempt-001.run`
  - `artifact_meeting_path` should mean `runs/<job-id>--attempt-XXX.meeting`
  - log paths should stay attempt-local

So the data model can work, but only if we stop assuming that the current attempt row and the job summary row mirror the same paths.

### X3-Q2 — Required behavior changes even without a migration

Even if we keep the schema intact, the store/update behavior must change.

#### 1. `QueueRerunAttempt(...)` must stop resetting the job back to `record/queued`

Today it:

- inserts the next attempt as `record/queued`
- clears `artifact_run_path`
- clears record timestamps

Under the new shape that is wrong.

For a build+publish rerun, it should instead:

- insert the next attempt as `build/queued`
- keep the canonical recording path on the top-level job row
- keep the top-level record timestamps from the original capture
- clear/reset only the build/publish current-attempt summary fields

#### 2. Top-level job summary must stop pretending it is just “latest attempt row copied upward”

The job summary becomes a mixed effective view:

- original/canonical record info persists across reruns
- build/publish stage + state track the active attempt
- canonical meeting path may still point at the last promoted successful build even if the current attempt later fails
- canonical site path may still point at the last successful publish even if a later rerun fails

That is a behavioral change in the store/update rules even if no new columns are added.

#### 3. Attempt rows must remain honest about skipped record

For rerun attempts:

- `trigger_kind` already remains `rerun`
- `record_*` timestamps should stay empty
- `record_log_path` should stay empty
- `build_*` and `publish_*` fields should be fresh for that attempt

That already fits the existing schema and already tells the truth the control panel needs.

#### 4. Attempt rows can keep using `artifact_run_path` without a new source field

For the first cut, only the initial successful record pass can ever produce a reusable ready `.run`.
That means there is no real ambiguity about rerun source provenance:

- the canonical reusable recording boundary is `jobs.artifact_run_path`
- the initial successful attempt still keeps its own retained `.run` at the attempt level under `runs/`
- rerun attempts can either leave `artifact_run_path` empty until build queueing, or set it to that same canonical ready `.run` when they enter build
- either way, `trigger_kind=rerun` plus empty `record_*` fields already make it obvious that the rerun did not create a new `.run`

So a dedicated `source_attempt_number` field is not required for the first cut.

### X3-Q3 — No migration is required for the first cut

Given the narrower contract, we can keep the current schema for now.

Why that works:

1. **Only one attempt can ever produce the reusable `.run`**
   - the initial successful record attempt
   - if record never succeeded, rerun is rejected

2. **`trigger_kind` already distinguishes initial vs rerun**
   - initial attempt = record path
   - rerun attempt = downstream-only path

3. **Sparse stage fields already show what actually ran**
   - rerun attempts have no record timestamps/logs
   - build/publish timestamps/logs are fresh

4. **`current_attempt_number` and `rerun_count` already support list/detail UIs**
   - no new control-panel identity mechanism is needed

So the first-cut recommendation is:

- **keep `jobs` and `job_attempts` as-is**
- **change store/update semantics only**

### X3-Q4 — `/events` feed already supports attempts 2, 3, ...

Yes — the current feed shape is already compatible with repeated reruns.

From `cassini-operator/internal/operator/events.go`:

- every event already includes `job_id`
- every event may include `attempt_number`
- the current attempt snapshot is included as `attempt`
- the top-level summary snapshot is included as `job`

That means attempts 2, 3, ... will already stream correctly **as long as** we keep emitting events from the same DB write boundaries.

So for control-panel usage, we do **not** need:

- a new event endpoint
- a new event routing model
- a separate event schema for reruns

What we **do** need is a clearer reporting contract:

- `job` in the event = canonical/effective job summary
- `attempt` in the event = attempt-local state for the attempt being updated

That distinction becomes more important after the `current/` + `runs/` split.

### X3-Q5 — What migration would become worth doing later?

A migration becomes more attractive only if we widen the contract beyond the first cut, for example:

- rerun can start from multiple entry stages
- rerun can reuse either ready `.run` or validated raw `recording.mkv`
- the UI needs explicit source-boundary fields instead of inference

At that point fields like these become worthwhile:

- `entry_stage`
- `source_artifact_kind`
- `source_artifact_path`

But those are **future-proofing** fields, not current blockers.

### Recommendation

#### Minimum required reporting changes

1. Treat `jobs` as the canonical/effective summary row, not a mirror of the latest attempt.
2. Insert rerun attempts as `build/queued`, not `record/queued`.
3. Preserve canonical record fields on the top-level job row across reruns.
4. Keep attempt-local build/publish logs and meeting outputs under `runs/`.
5. Continue using the existing `/jobs` and `/events` surfaces for user/control-panel inspection.

#### First-cut DB recommendation

- **Prefer no migration.**
- Keep the current `jobs` + `job_attempts` schema.
- Revisit schema additions only if rerun source types or entry stages become more flexible later.
- Treat long-term retention / hard-storage movement for accumulated artifacts as a follow-up outside the first-cut reporting contract.

### Acceptance

Spike is complete when we can describe:

- how the current DB/reporting model maps onto `current/` + `runs/`
- which changes are behavior-only versus schema-level
- whether `/events` can continue to serve attempts 2, 3, ...
- the smallest honest path that preserves control-panel compatibility

This spike now answers those questions provisionally:

- the existing `jobs` + `job_attempts` + `/events` model is already the right backbone
- behavior changes are definitely required in rerun/store updates
- a schema migration is **not required** for the first cut
- `/events` should continue to work for runs 2, 3, ... without any new endpoint or feed shape
