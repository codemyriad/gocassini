# V1 — Testing and validation

This document records the final validation surface for the implemented V1 operator flow.

It complements:
- `implementation.md`
- `slices.md`
- `cassini-operator/README.md`

## Automated checks

### Operator unit tests

```bash
cd cassini-operator
go test ./...
```

Current operator-focused coverage includes:
- empty read surface
- missing job detail
- newest-first ordering
- config fail-fast for missing `CASSINI_BIN`
- accepted job success through publish
- provider/body rejection
- record busy admission with no second row
- build failure manifest extraction
- publish failure manifest extraction
- startup interruption marking with stage preservation

### Recorder-side CLI tests

```bash
cd cassini-go-recorder
go test ./internal/cassini/...
```

This keeps the launcher and existing build/publish CLI surface covered alongside the operator runtime.

### CI

CI now runs operator unit tests in the shared Go unit matrix:
- `.github/workflows/ci.yml`
- module entry: `cassini-operator`

## Manual validation checklist

All commands below assume repo root:

```bash
cd <reporoot>
```

## S1 — bootstrap and read surface

### Start directly

```bash
rm -rf cassini-operator/.runtime
mkdir -p cassini-operator/.runtime
./bin/cassini-operator --bind 127.0.0.1:19080
```

Expected startup log lines include:
- `listening -> http://127.0.0.1:19080`
- `db -> .../cassini-operator/.runtime/jobs.sqlite3`
- `work_root -> .../cassini-operator/.runtime/jobs`
- `site_root -> .../cassini-operator/.runtime/site`
- `fixture_path -> .../cassini-operator/.runtime/operator-fixture.mkv`
- `cassini_bin -> .../bin/cassini`

In another terminal:

```bash
curl -s http://127.0.0.1:19080/jobs
curl -s -i http://127.0.0.1:19080/jobs/missing
```

Expected:
- `GET /jobs` returns `[]`
- `GET /jobs/:id` for a missing row returns `404`

### Start through launcher

```bash
./bin/cassini operator start --bind 127.0.0.1:19080
```

Expected first line:

```text
operator -> /abs/path/to/bin/cassini-operator
```

## S2 — trigger admission and record-compatible `.run`

### Create a local fixture

```bash
ffmpeg -loglevel error -y \
  -f lavfi -i sine=frequency=440:duration=1 \
  -c:a pcm_s16le \
  cassini-operator/.runtime/operator-fixture.mkv
```

### Start operator with the local fixture

```bash
FIXTURE_PATH="$PWD/cassini-operator/.runtime/operator-fixture.mkv" \
./bin/cassini-operator --bind 127.0.0.1:19082
```

### Submit a valid job

```bash
curl -s -X POST \
  'http://127.0.0.1:19082/jobs?provider=nextcloud-talk' \
  -H 'content-type: application/json' \
  -d '{"platform":"nextcloud-talk","url":"https://example.test/call-1"}'
```

Expected:
- `202`
- ULID id returned immediately

### Validate rejection cases

Unknown provider:

```bash
curl -s -i -X POST \
  'http://127.0.0.1:19082/jobs?provider=zoom' \
  -H 'content-type: application/json' \
  -d '{"platform":"nextcloud-talk","url":"https://example.test/call-2"}'
```

Invalid body:

```bash
curl -s -i -X POST \
  'http://127.0.0.1:19082/jobs?provider=nextcloud-talk' \
  -H 'content-type: application/json' \
  -d '{"platform":"nextcloud-talk"}'
```

Busy admission:
- start with `MAX_RECORD_WORKERS=1`
- issue two POSTs quickly
- second request should return `503`
- only one row should exist

## S3 — build queue and meeting artifact generation

The easiest deterministic validation path uses a wrapper `CASSINI_BIN`.

### Success wrapper

Use a fake `CASSINI_BIN` that:
- accepts `build`
- writes a ready meeting bundle under `--out`
- exits `0`

Then start:

```bash
FIXTURE_PATH="$PWD/cassini-operator/.runtime/operator-fixture.mkv" \
CASSINI_BIN=/tmp/cassini-build-success \
./bin/cassini-operator --bind 127.0.0.1:19083
```

Submit a job and verify:
- record timestamps are populated
- `build_queued_at` and `build_started_at` appear before completion
- final row has `artifact_meeting_path`
- final row reaches `stage=done`, `state=succeeded`

### Failure wrapper

Use a fake `CASSINI_BIN` that:
- writes a partial meeting `cassini.json`
- exits non-zero during `build`

Then verify final row has:
- `stage=done`
- `state=failed`
- `artifact_meeting_path`
- `error` derived from manifest `stage` + manifest `error`

## S4 — publish queue and hosted output refresh

Use a wrapper `CASSINI_BIN` that supports both `build` and `publish`.

### Success path

The success wrapper should:
- write a ready meeting bundle for `build`
- write a ready site bundle for `publish`

Start operator:

```bash
FIXTURE_PATH="$PWD/cassini-operator/.runtime/operator-fixture.mkv" \
SITE_ROOT="$PWD/cassini-operator/.runtime/site" \
CASSINI_BIN=/tmp/cassini-build-publish-success \
./bin/cassini-operator --bind 127.0.0.1:19084
```

Submit a job and verify final row has:
- `artifact_run_path`
- `artifact_meeting_path`
- `artifact_site_path`
- `publish_queued_at`
- `publish_started_at`
- `publish_finished_at`
- `completed_at`
- `stage=done`
- `state=succeeded`

Also inspect site output:

```bash
find cassini-operator/.runtime/site -maxdepth 3 -type f | sort
cat cassini-operator/.runtime/site/cassini.json
```

### Failure path

The failure wrapper should:
- let `build` succeed
- write a partial site `cassini.json` during `publish`
- exit non-zero

Verify final row has:
- `stage=done`
- `state=failed`
- `artifact_site_path`
- `error` derived from site manifest `stage` + `error`

## S5 — restart recovery and final semantics

Use a wrapper `CASSINI_BIN` that hangs during `build` after creating a partial meeting output.

### Start first operator process

```bash
FIXTURE_PATH="$PWD/cassini-operator/.runtime/operator-fixture.mkv" \
CASSINI_BIN=/tmp/cassini-hang-build \
./bin/cassini-operator --bind 127.0.0.1:19085
```

Submit a job and wait until `GET /jobs/:id` shows:
- `stage=build`
- `state=running`

Then kill the operator.

### Restart against the same DB

```bash
FIXTURE_PATH="$PWD/cassini-operator/.runtime/operator-fixture.mkv" \
CASSINI_BIN=/tmp/cassini-hang-build \
./bin/cassini-operator --bind 127.0.0.1:19086
```

Verify the same job row now shows:
- `stage=build`
- `state=interrupted`
- `interrupted_at` populated

Also verify completed rows remain unchanged.

## Final end-to-end success expectation

With the implemented slices complete, the happy-path lifecycle is now:

```text
record/queued
-> record/running
-> build/queued
-> build/running
-> publish/queued
-> publish/running
-> done/succeeded
```

And the restart honesty rule is now:

```text
any non-terminal row present at startup
-> same stage
-> state=interrupted
```
