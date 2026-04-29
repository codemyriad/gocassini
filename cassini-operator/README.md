# Cassini Operator

`cassini-operator` is the separate long-running control-plane binary for the MVP job flow.

It owns:
- the HTTP trigger/read API
- the SQLite-backed job store
- the in-process staged worker runtime
- the bridge to the existing `cassini build` and `cassini publish` CLI flows

Use it when you want to trigger work remotely, observe job state later, and refresh the published meeting library from one operator-owned process.

## Current scope (V1)

- `POST /jobs?provider=nextcloud-talk` to accept work
- `GET /jobs` and `GET /jobs/:id` to inspect persisted job rows
- SQLite persistence in one `jobs` table
- record placeholder from a fixture `.mkv`
- queued build stage via `cassini build`
- sequential publish stage via `cassini publish`
- startup interruption marking for any non-terminal persisted job

This is intentionally not real Nextcloud Talk capture yet. V1 proves the control-plane and artifact pipeline first.

## Quickstart

From repo root:

```bash
rm -rf cassini-operator/.runtime
mkdir -p cassini-operator/.runtime

ffmpeg -loglevel error -y \
  -f lavfi -i sine=frequency=440:duration=1 \
  -c:a pcm_s16le \
  cassini-operator/.runtime/operator-fixture.mkv

./bin/cassini operator start --bind 127.0.0.1:19080
```

Then trigger a job:

```bash
curl -s -X POST \
  'http://127.0.0.1:19080/jobs?provider=nextcloud-talk' \
  -H 'content-type: application/json' \
  -d '{"platform":"nextcloud-talk","url":"https://example.test/call"}'
```

Read job state:

```bash
curl -s http://127.0.0.1:19080/jobs
curl -s http://127.0.0.1:19080/jobs/<job-id>
```

## Launcher and binary resolution

Prefer the product CLI launcher from repo root:

```bash
./bin/cassini operator start [args...]
```

Launcher resolution is strict fail-fast:
- `CASSINI_OPERATOR_BIN` first
- otherwise `<reporoot>/bin/cassini-operator`

Inside the operator, Cassini CLI resolution is also strict fail-fast:
- `CASSINI_BIN` first
- otherwise `<reporoot>/bin/cassini`

Both selected paths must exist and be executable.

## HTTP API

### Create a job

```http
POST /jobs?provider=nextcloud-talk
Content-Type: application/json

{
  "platform": "nextcloud-talk",
  "url": "https://example.test/call"
}
```

Behavior:
- accepts only `provider=nextcloud-talk`
- requires `platform="nextcloud-talk"`
- requires `url`
- returns `202` with a ULID job id
- returns `503` with no job row when record capacity is full

### List jobs

```http
GET /jobs
```

Returns full persisted job rows ordered newest first.

### Get one job

```http
GET /jobs/:id
```

Returns the full persisted row for that job.

## Runtime defaults

By default the operator keeps its runtime-owned state under:

```text
<reporoot>/cassini-operator/.runtime/
```

| Path | Default |
|---|---|
| SQLite DB | `cassini-operator/.runtime/jobs.sqlite3` |
| Work root | `cassini-operator/.runtime/jobs` |
| Published site root | `cassini-operator/.runtime/site` |
| Fixture path | `cassini-operator/.runtime/operator-fixture.mkv` |

## Config surface

Flags:
- `--bind`
- `--db`
- `--work-root`
- `--site-root`
- `--fixture-path`
- `--fixture-url`
- `--cassini-bin`
- `--max-record-workers`
- `--max-build-workers`

Environment:
- `CASSINI_REPO_ROOT`
- `CASSINI_OPERATOR_BIN`
- `CASSINI_BIN`
- `WORK_ROOT`
- `SITE_ROOT`
- `FIXTURE_PATH`
- `FIXTURE_URL`
- `MAX_RECORD_WORKERS`
- `MAX_BUILD_WORKERS`

## Job model

A job row stores:
- stable ULID `id`
- original `request_json`
- `provider`
- `stage`
- `state`
- artifact paths for `.run`, `.meeting`, and site output
- lightweight `error`
- per-stage timestamps
- `interrupted_at`
- `completed_at`

### Stage values

- `record`
- `build`
- `publish`
- `done`

### State values

- `queued`
- `running`
- `succeeded`
- `failed`
- `interrupted`

## Worker semantics

### Record stage

- capped by `max-record-workers`
- overflow returns busy and inserts no job row
- uses one fixture `.mkv`
- materializes a fresh per-job `.run` bundle

### Build stage

- queued in memory
- worker count is configurable
- runs `cassini build <job>.run --out <job>.meeting`

### Publish stage

- queued in memory
- always processed by one publish worker
- runs `cassini publish <work-root> --out <site-root>`

## Fixture behavior

The record placeholder is intentionally simple:
- validate `.mkv` suffix at startup
- reuse `FIXTURE_PATH` when present
- otherwise lazily fetch `FIXTURE_URL`
- download to `FIXTURE_PATH.part`
- atomically rename into place
- guard acquisition with one process-local mutex

## Failure reporting

The operator keeps logging on stdout/stderr.

For build/publish failures it also tries to recover lightweight failure detail from partial bundle manifests:
- build reads partial meeting `cassini.json`
- publish reads partial site `cassini.json`
- stored error shape prefers manifest `stage` + manifest `error`

Examples:
- `build stage build: transcriber exploded`
- `publish stage publish: exporter exploded`

## Restart semantics

On startup the operator marks every non-terminal job as interrupted before serving new work:
- queued jobs become `interrupted`
- running jobs become `interrupted`
- last `stage` is preserved
- completed `succeeded` and `failed` jobs are left unchanged

This is intentionally honest rather than resumptive. Automatic retry/resume is not part of V1.

## Testing

Repo-local automated checks:

```bash
cd cassini-operator
go test ./...

cd ../cassini-go-recorder
go test ./internal/cassini/...
```

CI also runs operator unit tests through `.github/workflows/ci.yml`.

For the shaped V1 validation flow, see:
- `planning/initiatives/mvp/slices/V1-job-scheduler-setup/implementation.md`
- `planning/initiatives/mvp/slices/V1-job-scheduler-setup/testing.md`
