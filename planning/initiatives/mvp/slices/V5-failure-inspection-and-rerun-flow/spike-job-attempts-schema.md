## V5 Spike: exact `job_attempts` schema and summary split

### Context

The V5 implementation shape is now selected.

We have already selected that V5 will:

- keep one stable top-level logical job id
- add explicit per-attempt persistence
- add `POST /jobs/:id/rerun`
- rerun from `record` again rather than pretending to resume arbitrary partial subprocess state
- keep `GET /jobs` as a summary surface
- extend `GET /jobs/:id` into the failure-inspection surface

That leaves one persistence seam worth locking before implementation:

- **the exact split between `jobs` and `job_attempts`**
- **the exact columns that belong on every attempt**
- **which existing V2 columns remain on `jobs` as summary mirrors**
- **the minimum query/API consequences of that split**

This seam matters because the current operator runtime is still shaped around one row per job:

1. the V2 `jobs` table currently mixes identity, current state, artifact pointers, stop metadata, and timestamps in one row
2. the V2 read surface returns that row directly from `GET /jobs` and `GET /jobs/:id`
3. V5 needs to preserve multiple failures/successes for one logical job without breaking the simple operator-facing job summary

So if we want V5 reruns to be inspectable and implementable without churn, we need one explicit schema pass here rather than spreading column decisions across implementation commits.

### Goal

Define the exact `job_attempts` schema and the minimum `jobs` summary fields clearly enough to implement V5 I1 and I3 without reopening the data model mid-build.

### Outcome

This spike is now complete enough to lock the V5 persistence direction.

Selected for V5:

- **Table split:** keep `jobs` as the logical-job summary table and add `job_attempts` as the canonical attempt-history table
- **Compatibility direction:** keep the existing V2 summary-style columns on `jobs` so current handlers and tests can evolve incrementally instead of being rewritten all at once
- **Attempt key:** `job_attempts` uses composite primary key `(job_id, attempt_number)`
- **Attempt numbering:** first trigger creates `attempt_number = 1`; each accepted rerun increments by 1
- **Request preservation:** each attempt stores its own `request_json`, copied from the normalized request used to launch that attempt
- **Log persistence:** store attempt-scoped log file paths, not inline log blobs, on the attempt row
- **Stop metadata location:** persist stop metadata on the attempt row and mirror the latest/current attempt's stop summary onto `jobs` for list/read compatibility
- **Summary projection:** `jobs` keeps `stage`, `state`, `artifact_*`, `error`, stop metadata, and timestamp fields as the projection of the current/most-relevant attempt, plus new explicit rerun summary fields
- **API consequence:** `GET /jobs` continues to read only from `jobs`; `GET /jobs/:id` returns the `jobs` summary plus `job_attempts` rows newest-first

## Answered questions

| # | Decision | Answer |
|---|----------|--------|
| **V5-S1** | Table split | Keep `jobs` as the logical-job summary table and add `job_attempts` as the canonical history table. Do not replace `jobs` entirely. |
| **V5-S2** | Attempt identity | Use `(job_id, attempt_number)` as the primary identifier for an attempt. `attempt_number` is monotonic per job and starts at `1`. |
| **V5-S3** | Request preservation | Persist `request_json` on every attempt row, even if it duplicates the top-level job row, because rerun must preserve the exact normalized launch request used for that attempt. |
| **V5-S4** | Log storage | Persist file paths for attempt-scoped logs (`record_log_path`, `build_log_path`, `publish_log_path`) rather than storing raw log bodies in SQLite. |
| **V5-S5** | Stop metadata split | Persist stop metadata on `job_attempts` as the source of truth and mirror the latest/current attempt's stop summary on `jobs` for compatibility and list-view summaries. |
| **V5-S6** | Summary columns on `jobs` | Keep current V2 columns on `jobs` and add only the minimum new summary fields needed for rerun/attempt awareness. |
| **V5-S7** | Effective artifact pointers | Keep `artifact_run_path`, `artifact_meeting_path`, and `artifact_site_path` on `jobs` as the effective current/winning attempt pointers. Attempt rows carry their own attempt-scoped artifact paths separately. |
| **V5-S8** | Query shape | `GET /jobs` should continue to query only `jobs`. `GET /jobs/:id` should query one `jobs` row plus all matching `job_attempts` rows ordered by `attempt_number DESC`. |
| **V5-S9** | Migration style | Add a numbered V5 migration that creates `job_attempts`, adds the selected summary fields to `jobs`, and backfills attempt `1` from every existing V2 job row. |
| **V5-S10** | Backfill policy | Existing V2 databases should get one synthetic `attempt_number = 1` row per existing job, copied from the current `jobs` row, so history starts in a consistent shape after migration. |

## Selected schema

### `jobs` remains the logical-job summary table

The existing V2 `jobs` columns stay in place:

- `id`
- `provider`
- `request_json`
- `stage`
- `state`
- `artifact_run_path`
- `artifact_meeting_path`
- `artifact_site_path`
- `error`
- `stop_reason`
- `stop_requested_at`
- `stop_signal_sent_at`
- `record_exit_code`
- `record_stop_detail`
- `created_at`
- `updated_at`
- `record_queued_at`
- `record_started_at`
- `record_finished_at`
- `build_queued_at`
- `build_started_at`
- `build_finished_at`
- `publish_queued_at`
- `publish_started_at`
- `publish_finished_at`
- `interrupted_at`
- `completed_at`

Selected new V5 summary fields on `jobs`:

- `current_attempt_number INTEGER NOT NULL DEFAULT 1`
- `rerun_count INTEGER NOT NULL DEFAULT 0`

Meaning of the top-level summary row after V5:

- `request_json` is the canonical normalized request for the logical job and the default source for rerun
- `stage`, `state`, `error`, stop metadata, and timestamp fields are the projected summary of the current/most-relevant attempt
- `artifact_*` fields point at the effective current/winning outputs for that logical job
- `current_attempt_number` tells the caller which attempt the summary currently reflects
- `rerun_count` is `current_attempt_number - 1` for now, but keeping it explicit makes list reads cheaper and future policy changes less awkward

### `job_attempts` is the canonical history table

Selected exact columns:

```sql
CREATE TABLE job_attempts (
  job_id TEXT NOT NULL,
  attempt_number INTEGER NOT NULL,
  trigger_kind TEXT NOT NULL,
  request_json TEXT NOT NULL,

  stage TEXT NOT NULL,
  state TEXT NOT NULL,

  artifact_run_path TEXT,
  artifact_meeting_path TEXT,
  artifact_site_path TEXT,

  error TEXT,
  stop_reason TEXT,
  stop_requested_at TEXT,
  stop_signal_sent_at TEXT,
  record_exit_code INTEGER,
  record_stop_detail TEXT,

  record_log_path TEXT,
  build_log_path TEXT,
  publish_log_path TEXT,

  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,

  record_queued_at TEXT,
  record_started_at TEXT,
  record_finished_at TEXT,

  build_queued_at TEXT,
  build_started_at TEXT,
  build_finished_at TEXT,

  publish_queued_at TEXT,
  publish_started_at TEXT,
  publish_finished_at TEXT,

  interrupted_at TEXT,
  completed_at TEXT,

  PRIMARY KEY (job_id, attempt_number),
  FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE
);
```

Selected supporting indexes:

```sql
CREATE INDEX job_attempts_job_attempt_desc
  ON job_attempts(job_id, attempt_number DESC);

CREATE INDEX job_attempts_created_desc
  ON job_attempts(created_at DESC, job_id, attempt_number DESC);
```

### Why each attempt column exists

Minimal non-negotiable attempt fields:

- `job_id`, `attempt_number`
  - identify one attempt under one logical job
- `trigger_kind`
  - selected values: `initial`, `rerun`
  - makes it obvious which attempt came from the original trigger and which came from `POST /jobs/:id/rerun`
- `request_json`
  - preserves the exact normalized request used to launch that attempt
- `stage`, `state`
  - the current/terminal execution position of that attempt
- `artifact_run_path`, `artifact_meeting_path`, `artifact_site_path`
  - preserve attempt-local outputs for inspection
- `error`
  - concise persisted failure summary for quick reads
- stop metadata fields
  - preserve V2 record-stop observability per attempt rather than only on the summary row
- `record_log_path`, `build_log_path`, `publish_log_path`
  - enough to inspect failures without embedding logs in SQLite
- stage timestamps plus `created_at`, `updated_at`, `completed_at`, `interrupted_at`
  - keep the same operational semantics per attempt that V2 already had per job

## Selected path layout consequence

This spike only needs to lock the schema side of path storage, but the selected columns imply one path rule:

- every attempt gets its own directory under the logical job work root

Selected path convention:

```text
<work-root>/<job-id>/attempt-001/
<work-root>/<job-id>/attempt-001/run/
<work-root>/<job-id>/attempt-001/meeting/
<work-root>/<job-id>/attempt-001/logs/record.log
<work-root>/<job-id>/attempt-001/logs/build.log
<work-root>/<job-id>/attempt-001/logs/publish.log
```

That means:

- `artifact_run_path` points to the attempt-local run directory
- `artifact_meeting_path` points to the attempt-local meeting directory
- `artifact_site_path` may still point to the shared site root when that attempt successfully publishes

This keeps attempt evidence isolated even though publish output remains shared.

## Selected backfill / migration policy

For existing V2 databases:

1. add `current_attempt_number` and `rerun_count` to `jobs`
2. create `job_attempts`
3. backfill one `attempt_number = 1` row from every existing `jobs` row
4. set:
   - `trigger_kind = 'initial'`
   - `current_attempt_number = 1`
   - `rerun_count = 0`

Backfill source-of-truth rule:

- copy all current V2 per-job state fields from `jobs` into `job_attempts`
- after migration, `jobs` remains the read summary and `job_attempts` becomes the historical source

This is intentionally lossy only in one sense:

- older V2 jobs had no multi-attempt history, so migration can only synthesize one historical attempt from the current row

That is acceptable because the repo has not yet shipped rerun semantics before V5.

## Minimum query/API consequences

### `GET /jobs`

Should continue to select from `jobs` only.

Selected summary contract additions:

- `current_attempt_number`
- `rerun_count`

Everything else continues to come from the same summary columns the V2 API already exposes.

This keeps list reads cheap and keeps the operator list surface one-row-per-logical-job.

### `GET /jobs/:id`

Should return:

- the same top-level job summary shape as `GET /jobs`
- plus an `attempts` array ordered newest-first

Selected minimum attempt response fields:

- `attempt_number`
- `trigger_kind`
- `request_json`
- `stage`
- `state`
- `artifact_run_path`
- `artifact_meeting_path`
- `artifact_site_path`
- `error`
- `stop_reason`
- `stop_requested_at`
- `stop_signal_sent_at`
- `record_exit_code`
- `record_stop_detail`
- `record_log_path`
- `build_log_path`
- `publish_log_path`
- all existing stage timestamps
- `created_at`
- `updated_at`
- `interrupted_at`
- `completed_at`

### `POST /jobs/:id/rerun`

Persistence consequences only:

- look up the job summary row
- compute `next_attempt_number = current_attempt_number + 1`
- insert a queued `job_attempts` row with that attempt number and copied normalized `request_json`
- update the `jobs` summary row to:
  - `current_attempt_number = next_attempt_number`
  - `rerun_count = next_attempt_number - 1`
  - `stage = 'record'`
  - `state = 'queued'`
  - clear or replace summary fields that now belong to the new active attempt

Selected clearing rule on rerun admission:

- clear summary `error`
- clear summary `completed_at`
- clear summary `interrupted_at`
- clear summary stage timestamps for the new attempt before they are rewritten
- leave prior attempt detail preserved only on `job_attempts`

## Rejected alternatives

### No `job_attempts` table, only JSON history on `jobs`

Rejected because:

- it makes query/update logic much less explicit
- it weakens SQL-level integrity around attempt numbering
- it makes ordered attempt inspection and backfill more awkward

### Move all current columns off `jobs` and make `jobs` almost identity-only

Rejected for V5 because:

- it creates unnecessary churn in the existing handlers and tests
- it breaks the current operator summary surface more aggressively than needed
- the repo already has a working one-row summary read model that is worth preserving

### Inline logs into SQLite text columns

Rejected because:

- logs are unbounded and noisy compared to the summary information V5 actually needs
- file paths are sufficient for operator inspection in the current scope
- process output remains the live sink anyway

## Acceptance

This spike is complete because:

- the exact `jobs` versus `job_attempts` split is selected
- the exact `job_attempts` columns are selected
- the minimum new summary fields on `jobs` are selected
- the backfill rule from V2 to V5 is selected
- the minimum `GET /jobs` and `GET /jobs/:id` query consequences are selected
- the rerun-admission persistence consequences are selected

## Reassessment

This spike no longer points to another persistence-design loop.

It now serves as the decision record that justifies:

- implementing V5 I1 around the selected migration/backfill path
- implementing V5 I2 around attempt-scoped paths and log capture
- implementing V5 I3 around a summary-plus-attempts API read shape
