## V2 Spike: stop metadata persistence and operator schema migrations

### Context

The V2 implementation shape is now selected.

We have already selected that V2 will:

- keep the V1 operator trigger/status/job backbone
- execute live capture by running `cassini record` as a subprocess
- use a guest-first request model
- add `POST /jobs/:id/stop`
- treat explicit stop as a happy-path outcome when recording finalizes cleanly and build/publish succeed
- persist structured stop metadata
- replace the current hardcoded schema creation path with versioned SQLite migrations

That leaves one remaining design seam that is still worth shaping before implementation:

- **how stop metadata is modeled and persisted**
- **how migrations are structured, applied, and operated**

This seam matters because the current repo has two important constraints:

1. `cassini-operator` still hardcodes schema creation with `CREATE TABLE IF NOT EXISTS jobs`.
2. The recorder already exits gracefully on signal/context cancellation, but today that stop path is observed as generic cancellation behavior, not a structured operator-visible `operator_requested` reason.

So if we want V2 to report honest stop reasons and evolve schema safely, we need one explicit design pass here rather than ad hoc edits during implementation.

### Goal

Define the V2 stop-metadata persistence model and the operator migration surface clearly enough to implement S3 without reopening core design questions mid-build.

### Outcome

This spike is now complete enough to lock the S3 persistence direction.

Selected for V2:

- **Migration layout:** numbered SQL files named `NNNN_name.up.sql` and `NNNN_name.down.sql`, tracked in `schema_migrations`
- **Baseline policy:** the existing hardcoded schema is the initial baseline and should be extracted from the Go string into the baseline SQL migration
- **Startup behavior:** auto-apply pending up migrations only; never auto-run down migrations
- **DB command surface:** no extra operator DB command surface is required for current V2 scope
- **Stop metadata location:** persist on the operator job row; do not mirror operator-owned stop state into recorder FS artifacts unless there is existing repo precedent for wrapper-owned artifact mutation
- **Artifact investigation result:** current build/publish wrappers and CI/harness flows read recorder outputs and write downstream meeting/site artifacts, but they do not write operator-owned state back into the recorder `.run` bundle or session artifact paths
- **Stop reason ownership:** `operator_requested` is operator-owned classification, not recorder-owned classification
- **Migration tool direction:** there is no existing migration library in `cassini-operator` today; given the small scope and direct SQLite usage, an in-repo migration runner over versioned SQL files is the sensible default unless implementation reveals a strong need for a third-party tool

## Answered questions

| # | Decision | Answer |
|---|----------|--------|
| **V2-M1** | Modify to selected convention | Use numbered SQL files named `NNNN_name.up.sql` and `NNNN_name.down.sql`, applied in order and tracked in `schema_migrations`. During implementation, briefly evaluate whether a third-party migration tool is justified, but the working default is an in-repo runner. |
| **V2-M2** | Modify to selected baseline policy | The existing hardcoded schema is the baseline. Extract it from the Go string into the initial SQL migration and stop treating the Go string as the long-term schema source. |
| **V2-M3** | Accept suggestion | On normal operator start, auto-apply pending **up** migrations only; never auto-run down migrations. Fail fast if migration state is invalid. |
| **V2-M4** | Modify to out-of-scope for V2 | Extra operator DB commands are not necessary for the current V2 scope. Manual migrate up/down surfaces are not useful yet because the running code expects the latest schema anyway. |
| **V2-M5** | Accept suggestion | Persist at least: `stop_reason`, `stop_requested_at`, `stop_signal_sent_at`, `record_exit_code` (nullable), and `record_stop_detail` (nullable fallback text). |
| **V2-M6** | Accept suggestion | Persist: `room_empty`, `duration_limit`, `operator_requested`, `signaling_connection_error`, `join_failed`, and `record_process_exit_nonzero`. |
| **V2-M7** | Accept suggestion | `operator_requested` is operator-owned classification. If the operator accepted `POST /jobs/:id/stop`, sent SIGTERM, and the recorder finalized cleanly, persist `stop_reason=operator_requested` even if the recorder internally reports generic cancellation. |
| **V2-M8** | Modify to selected strict-no default | Investigate repo precedent first. Current evidence from build/publish code, CI, and harness scripts shows wrapper processes read recorder outputs and write downstream `.meeting` / `.site` artifacts, but do **not** write operator-owned state back into recorder `.run` artifacts. Therefore V2 should keep operator stop metadata on the job row only. |
| **V2-M9** | Modify to deferred unless M8 changes | Artifact-visible stop metadata is unnecessary for V2. Reconsider only if future work introduces an established precedent for wrapper-owned recorder-artifact mutation. |
| **V2-M10** | Modify to selected baseline bootstrap | If a DB created before migrations already exists, create or update `schema_migrations` to the initial baseline version derived from the existing schema, then migrate up from there if needed. |
| **V2-M11** | Accept suggestion | Test at least: fresh DB bootstrap, upgrade from current V1-shaped DB to V2, explicit down migration in a test path, and refusal on inconsistent migration history. |

## Evidence behind V2-M8

The current repo shape points to a strict separation:

- recorder-owned processes write the `.run` bundle / session artifact / recorder report
- downstream wrapper processes read those outputs and write new downstream artifacts (`.meeting`, `.site`, static exports)

Relevant evidence in the current repo:

- `cassini-go-recorder/internal/cassini/run_bundle.go` owns `.run` manifest writes
- `cassini-go-recorder/internal/cassini/build.go` reads `.run` input and writes a separate `.meeting` bundle
- `cassini-go-recorder/internal/cassini/publish.go` reads `.meeting` input and writes a separate `.site` bundle
- `cassini-go-recorder/e2e_with_publisher.sh` runs the recorder, then separate downstream publish/verification steps
- `harness/bin/roundtrip-synthetic-meeting.sh` records first, then runs the artifact pipeline against the final MKV and separate artifact output root
- CI integration runs recorder/harness flows but does not introduce a wrapper process that writes operator-owned state back into the recorder `.run`

So the current repo does **not** show a precedent for external wrapper-owned state being written into recorder artifacts. That makes job-row-only stop metadata the correct V2 default.

## Acceptance

This spike is complete because:

- each V2-M question now has an accepted or modified answer
- the migration file/layout convention is selected
- startup auto-migration behavior is selected
- the current-scope decision on DB commands is selected
- the minimum stop metadata fields are selected
- operator-owned versus recorder-owned stop reason is selected
- artifact-visible stop metadata is explicitly rejected for V2 based on repo evidence
- existing dev/local DB bootstrap onto the migration system is selected
- the minimum migration test matrix is selected

## Reassessment

This spike no longer points to more exploratory shaping.

It now serves as the decision record that justifies:

- tightening the selected V2 shape in `shaping.md`
- narrowing S3 in `slices.md`
- moving next into implementation planning rather than another persistence-design spike