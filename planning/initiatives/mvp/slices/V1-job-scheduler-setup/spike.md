## V1 Spike: Service placement, SQLite job model, and staged worker orchestration

### Context

V1 now has a selected implementation shape with several decisions already made:

- API and worker orchestration live in the same long-running process
- job persistence uses SQLite
- accepted jobs move through separate record, build, and publish stages
- record concurrency is admission-limited and returns busy when full
- busy record-stage requests return no job row, but do log the rejection in a repo-compatible way
- build concurrency is configurable and excess build work waits in a queue
- publish is serialized through a single worker
- V1 recording is a placeholder that materializes a destination artifact from fixtures / seeded input
- build and publish should still run through the real Cassini flow via Go helpers directly
- startup should mark every non-completed persisted job as interrupted, including queued jobs, without attempting artifact cleanup or repair, while preserving the job's last stage
- provider naming should use `nextcloud-talk`, not a lingering `generic` name
- `GET /jobs` and `GET /jobs/:id` should both return full persisted job rows, including transition timestamp fields, and `GET /jobs` should return newest first
- the service entrypoint should be `cassini operator`
- fixture configuration uses `FIXTURE_URL` and `FIXTURE_PATH`; startup verifies `FIXTURE_PATH` ends with `.mkv`
- `FIXTURE_PATH` should default sensibly to `harness/runtime/operator-fixture.mkv`
- if `FIXTURE_PATH` exists, workers reuse it; otherwise they fetch `FIXTURE_URL` into `FIXTURE_PATH` and then copy it into a fresh per-job `.run`
- V1 uses a single `jobs` table only; no separate events table is needed for the happy path
- job ids should be ULIDs
- job stage/state should use `record|build|publish|done` and `queued|running|succeeded|failed|interrupted`

The repo already suggests some useful constraints:

- `cassini-go-recorder/cmd/cassini/main.go` is the product CLI entry point
- `cassini-go-recorder/internal/cassini` already owns CLI-facing orchestration such as `record`, `build`, `publish`, `serve`, and `dev`
- run/meeting/site bundles already persist state on disk through `cassini.json` manifests
- `serve.go` shows an existing pattern for running a long-lived HTTP server inside the `internal/cassini` package
- `build.go` and `publish.go` already capture the implementation boundary V1 wants to reuse rather than replace
- `resolveBuildInput` only accepts a ready `.run` bundle with an MKV recording or a direct `.mkv` file; simulate-mode `.csr` is not build-compatible
- `PrepareRunBundle(..., false)` and `FinalizeRunBundle(...)` already give V1 a natural way to materialize a fresh per-job run artifact around a fixture MKV
- repo-compatible service logging should follow the repo's existing `log.Printf(...)` pattern rather than inventing a new logger for V1

The remaining work is not to rediscover the high-level shape, but to pin down the mechanics cleanly enough to implement it.

### Goal

Identify the cleanest V1 implementation path that fits current repo patterns and makes the selected shape concrete without silently expanding scope.

### Questions

| # | Question |
|---|----------|
| **V1-S1** | How should `cassini operator` be wired into the existing CLI/package structure so it fits current Cassini patterns for long-running commands? |
| **V1-S2** | Does the selected SQLite `jobs` schema (ULID + current stage/state + per-stage timestamps + artifact paths) need any adjustment in practice? |
| **V1-S3** | Given that V1 marks every non-completed job, including queued jobs, as interrupted while preserving the last stage, what exact startup update/query logic should implement that cleanly? |
| **V1-S4** | How should the V1 record placeholder turn a fixture MKV into a fresh per-job `.run` artifact that the existing build path accepts as normal input? |
| **V1-S5** | What is the exact `FIXTURE_URL` / `FIXTURE_PATH` contract for V1, including the default path `harness/runtime/operator-fixture.mkv`, `.mkv` startup validation, and fetch-when-missing behavior? |
| **V1-S6** | Which existing Go helpers should the service call directly for build and publish, and what small helper extraction is needed from CLI-shaped code to make that boundary clean? |
| **V1-S7** | What is the minimum request/response contract for `POST /jobs?provider=nextcloud-talk`, `GET /jobs`, and `GET /jobs/:id` now that both read endpoints should return full persisted job rows with transition timestamp fields, the POST body is minimal, and list ordering is newest first? |
| **V1-S8** | What exact configuration surface should V1 expose for local/demo use: bind address, SQLite path, `FIXTURE_URL`, `FIXTURE_PATH`, work/artifact roots, published-site root, max record workers, max build workers, and any protection-boundary assumptions? |
| **V1-S9** | Beyond explicitly assuming external protection only, are there any remaining V1-level concerns around request protection such as rate limiting? |

### Acceptance

This spike is complete when:

- we can describe the recommended code placement for the `cassini operator` service inside the current Go module
- we can confirm whether the selected SQLite `jobs` schema is sufficient
- we can describe the selected ULID + stage/state model clearly enough to implement list/detail/status reads, including interrupted startup handling
- we can describe the busy-admission behavior for record-stage saturation
- we can describe how the V1 record placeholder produces a fresh build-compatible `.run` artifact from a configured fixture MKV
- we can describe the exact `FIXTURE_URL` / `FIXTURE_PATH` configuration contract V1 expects
- we can describe the preferred Go-helper invocation boundary for build and publish
- we can describe what startup interruption marking does with queued and in-flight jobs
- we can describe the minimum API contract for `POST /jobs`, `GET /jobs`, and `GET /jobs/:id`
- we can describe which decisions remain intentionally deferred to V2 or V5
