# V1 — Implementation reflection

This document captures what was actually built for `planning/initiatives/mvp/slices/V1-job-scheduler-setup` after the shaped slices were implemented through **S5**.

It is the implementation-side companion to:
- `brief.md`
- `shaping.md`
- `slices.md`
- `spike.md`
- `spike-operator-package.md`

## Outcome summary

The V1 operator path is now implemented end to end.

A caller can:
1. trigger work through `POST /jobs?provider=nextcloud-talk`
2. receive a stable ULID immediately
3. inspect persisted full job rows through `GET /jobs` and `GET /jobs/:id`
4. let the operator process the job asynchronously through record → build → publish
5. observe published output refresh under the operator-owned site root
6. restart the operator and see any non-terminal in-flight work marked `interrupted`

## What shipped

### Packaging and launcher

- a separate sibling Go module at `cassini-operator/`
- a dedicated operator binary entrypoint at `cassini-operator/cmd/cassini-operator`
- a repo-root wrapper at `bin/cassini-operator`
- a product CLI convenience launcher at `cassini operator start`

The launcher stayed intentionally narrow:
- it resolves the binary
- prints one launch line
- `exec`s the operator
- forwards args unchanged

That decision held up well in implementation. It avoided inventing a second operator CLI contract.

### Control-plane runtime

The operator is a single long-running process that owns:
- HTTP handlers
- SQLite access
- record admission
- build queue and workers
- publish queue and worker

That single-process decision also held up well for V1. It kept local verification and restart semantics simple and made the shaped slices easy to land incrementally.

### Persistence model

The implementation kept the selected single-table SQLite model.

The `jobs` table now carries:
- request payload
- stage/state
- artifact paths
- lightweight error text
- per-stage timestamps
- interruption/completion timestamps

No `job_events` table was introduced.

That tradeoff remained appropriate for V1. The row shape is large, but it keeps reads and verification straightforward.

### Record placeholder

V1 still does not capture a live Talk meeting. Instead it:
- validates a fixture `.mkv` path
- lazily downloads the fixture when needed
- guards acquisition with one process-local mutex
- builds a fresh `.run` bundle per accepted job

This turned out to be the right level of fidelity for V1. It proved the artifact contract without dragging live Talk capture into the control-plane slice.

### Build and publish reuse

The operator reuses existing Cassini behavior through CLI invocation:
- `cassini build`
- `cassini publish`

This was the most important implementation validation in the effort.

It confirmed that keeping `cassini-operator` separate did not require a large refactor of existing recorder internals. The CLI boundary was slightly noisier than a direct Go API would have been, but it preserved existing orchestration and kept the slice small enough to finish.

### Failure detail strategy

The operator now reads partial manifests on failure:
- partial meeting `cassini.json` for build failures
- partial site `cassini.json` for publish failures

This also held up well in practice. It gave lightweight persisted error detail without adding log sinks, stderr tail capture, or new schema.

### Restart honesty

On startup, every non-terminal job is now marked `interrupted` while preserving its previous stage.

That behavior is intentionally blunt, but it is honest. It avoids implying retry or resume semantics that do not exist yet.

## Where the shape matched implementation well

Several shaping decisions translated directly into code with little churn:

1. **Separate `cassini-operator` package**
   - good boundary
   - low coupling with recorder internals
   - easy to test in isolation

2. **CLI reuse for build/publish**
   - minimal integration work
   - preserved current Cassini behavior
   - kept operator code focused on orchestration

3. **SQLite single-row job state**
   - easy list/detail API
   - easy restart verification
   - easy timestamp assertions in tests

4. **In-memory stage queues with different concurrency rules**
   - record busy admission
   - configurable build concurrency
   - single publish worker
   - all easy to reason about in tests

5. **Stdout/stderr as the logging default**
   - no sink design tax
   - subprocess output naturally visible during validation

## What implementation clarified further

A few points became clearer only once the code was real.

### 1. The runtime root matters a lot

Moving operator-owned state under `cassini-operator/.runtime/` was a good cleanup.

It made the module feel self-contained and made it easier to explain:
- DB location
- job artifact root
- fixture cache
- published site output

### 2. Temporary cutlines were useful during implementation, but are now historical

The slice plan used temporary terminal cutlines:
- S2 ended after record
- S3 ended after build

That was valuable during delivery because each slice stayed runnable and verifiable.

Now that S4 and S5 are complete, those cutlines are only historical. The actual runtime behavior is the full record → build → publish pipeline plus startup interruption handling.

### 3. Manifest-based failure extraction is enough for V1

The shape left this as a sufficiency check. The implementation suggests it is sufficient for V1:
- useful enough for list/detail API reads
- low schema complexity
- no log persistence needed yet

It may eventually need stderr tailing or richer error structure, but not for this slice.

## Deliberate V1 limitations that remain

These are not regressions; they are intentional omissions that still fit the shaped scope.

- no automatic retry or resume after interruption
- no auth built into the operator
- no durable queue beyond persisted current job state
- no publish history table
- no per-job log capture path in SQLite
- no live Talk recording yet
- no rerun endpoint
- no multi-tenant or user-facing operator UI

## Test and verification outcome

The effort finished with both automated and manual validation paths.

### Automated coverage

Implemented operator tests cover:
- read surface
- launcher config failure
- admission/busy behavior
- build success path
- build failure extraction
- publish success path
- publish failure extraction
- startup interruption marking

CI now runs operator unit tests in `.github/workflows/ci.yml` alongside the existing Go modules.

### Manual validation

The implementation was also verified with wrapper `CASSINI_BIN` scripts so build/publish success and failure behavior could be proven without depending on the full real toolchain for every check.

That validation confirmed:
- newest-first `GET /jobs`
- full row persistence
- artifact path persistence across `.run`, `.meeting`, and site output
- manifest-derived lightweight error text
- startup interruption behavior after killing the operator mid-flight

See `testing.md` for the repo-compatible validation guide.

## Files and areas touched

The main code landed in:
- `cassini-operator/`
- `cassini-go-recorder/internal/cassini/operator_cli.go`
- `cassini-go-recorder/internal/cassini/cli.go`
- `cassini-go-recorder/internal/cassini/cli_test.go`
- `bin/cassini`
- `bin/cassini-operator`
- `.github/workflows/ci.yml`

The main operator runtime areas are now split roughly by concern:
- bootstrap, config, HTTP, store schema, and basic handlers in `run.go`
- build orchestration in `build_runtime.go` and `build_store.go`
- publish orchestration in `publish_runtime.go` and `publish_store.go`
- bundle-manifest readers in `meetingbundle.go` and `sitebundle.go`
- startup interruption pass in `startup_store.go`

## Suggested next move after V1

If work continues from here, the most natural next step is not more control-plane infrastructure. The shaped V1 control plane is already good enough for the current MVP goal.

The most leveraged next move is to swap the record placeholder for real capture while preserving the job model and operator runtime semantics.

That keeps the current investment intact:
- same API
- same persistence model
- same staged execution
- same restart honesty
- same published output contract
