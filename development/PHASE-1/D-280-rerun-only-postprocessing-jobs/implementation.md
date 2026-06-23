# D-280 implementation

## Scope delivered

This implementation completed the planned D-280 operator rerun work and two extra touchups requested during implementation:

- planned slices I1, I2, and I3
- control-panel rerun button
- successful-job reruns in addition to failed-job reruns

## What changed

### 1. Stable work-root contract

The operator now derives two internal roots from one configured `WorkRoot`:

- `current/` for canonical successful artifacts
- `runs/` for retained attempt-local artifacts and logs

Implemented behavior:

- initial record writes `runs/<job-id>--attempt-001.run`
- successful record promotes a canonical ready `.run` to `current/<job-id>.run`
- build writes `runs/<job-id>--attempt-00N.meeting`
- successful build promotes a canonical `.meeting` to `current/<job-id>.meeting`
- publish always reads from `current/`

### 2. Canonical summary vs attempt history split

The store semantics now treat:

- `jobs` as the canonical/effective summary row
- `job_attempts` as attempt-local execution history

Implemented behavior:

- job-level `artifact_run_path` points at canonical `current/<job-id>.run`
- job-level `artifact_meeting_path` points at canonical `current/<job-id>.meeting`
- attempt rows keep their attempt-local `.run` / `.meeting` / log paths under `runs/`
- reruns preserve top-level record summary fields from the original capture

### 3. Downstream-only reruns

Rerun no longer re-enters live recording.

Implemented behavior:

- rerun requires a canonical ready `.run`
- rerun creates attempt `N+1` at `build/queued`
- rerun reuses the canonical `current/<job-id>.run`
- rerun creates fresh build and publish work for the new attempt
- rerun attempts keep skipped `record_*` fields empty
- rerun does not create a new attempt-local `.run`

### 4. Successful-job reruns

The original planned work only enabled reruns for failed jobs. This was extended during implementation.

Implemented behavior:

- `POST /jobs/:id/rerun` now accepts both terminal failed jobs and terminal successful jobs
- eligibility still requires a canonical ready `.run`
- successful-job reruns still execute fresh downstream `build` + `publish`
- original canonical record metadata remains preserved

### 5. Control panel rerun button

The control panel now exposes rerun directly in the selected-job detail view.

Implemented behavior:

- added a rerun action wired to `POST /jobs/:id/rerun`
- rerun button is enabled for terminal jobs with a canonical run artifact
- rerun button remains unavailable for active jobs and jobs without a canonical reusable `.run`
- stop button behavior is unchanged

## Validation

### Operator tests

Validated with fixture-backed operator tests covering:

- canonical `current/` + `runs/` artifact layout
- initial record -> build -> publish pipeline
- rerun after build failure without a second live record pass
- rerun after publish failure with rebuild, not publish-only
- rerun of already successful jobs
- rejection when no canonical ready `.run` exists
- preserved read-model semantics for job summary and attempt history

### Frontend validation

Validated by building the control panel production bundle after the rerun UI changes.

## Notes

- The planning documents were intentionally left unchanged during this follow-on work.
- Raw-session reruns and artifact-retention policy remain follow-up items.
