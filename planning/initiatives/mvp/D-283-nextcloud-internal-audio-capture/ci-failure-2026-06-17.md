# CI failure 2026-06-17

## Failing runs

- CI run `27682264398` failed every `Integration` matrix leg.
- Build + publish ExApp image run `27682264365` failed `E2E Talk record-button roundtrip (CPU)`.

## Root cause

D-283 made `hpb-internal` the default Talk auth mode. That mode intentionally fails closed unless both process-scoped secrets are present:

- `CASSINI_TALK_RECORDING_SECRET`
- `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`

The local harness configured Nextcloud Talk with the dev recording secret, but did not export that secret to child recorder processes. `SIGNALING_INTERNAL_SECRET` was also only a harness variable, not the canonical recorder env var.

The ExApp roundtrip had the same split: the operator container received `TALK_RECORDING_SECRET`, which is enough for the Talk recording-backend HMAC adapter, but its child `cassini record` process reads `CASSINI_TALK_RECORDING_SECRET` and `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`.

## CI symptoms

The direct integration jobs failed before media capture:

```text
talk recorder stopping: talk auth mode hpb-internal requires CASSINI_TALK_RECORDING_SECRET to be set
run error: talk auth mode hpb-internal requires CASSINI_TALK_RECORDING_SECRET to be set
```

The ExApp Talk roundtrip failed the same way inside the operator-launched record job:

```text
record failed: talk auth mode hpb-internal requires CASSINI_TALK_RECORDING_SECRET to be set
```

## Local reproduction

The host workspace did not have `go` on `PATH`, so reproduction used the Go Docker image:

```bash
docker run --rm -v "$PWD":/work -w /work/cassini-go-recorder golang:1.24 \
  bash -c 'env -u CASSINI_TALK_RECORDING_SECRET -u CASSINI_TALK_SIGNALING_INTERNAL_SECRET bash -c "source ../harness/bin/common.sh; go run ./cmd/gocassini --mode talk --call-url http://127.0.0.1:28080/call/localrepro --duration 1 --output /tmp/cassini-ci-secret-repro.mkv --final-output /tmp/cassini-ci-secret-repro-final.mkv"'
```

Before the fix this fails with:

```text
talk auth mode hpb-internal requires CASSINI_TALK_RECORDING_SECRET to be set
```

This reproduces the failing seam without needing a full Nextcloud stack: `common.sh` had the correct harness default in the shell, but the recorder child process did not receive it.

After the fix, the same command gets past secret validation and fails later at the expected local-only boundary because no Nextcloud stack is running:

```text
recording-auth signaling settings failed: ... dial tcp 127.0.0.1:28080: connect: connection refused
```

## Fix

- Export `CASSINI_TALK_RECORDING_SECRET` from `harness/bin/common.sh` so direct harness recorder processes inherit it.
- Derive and export `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` from the harness `SIGNALING_INTERNAL_SECRET` default.
- Pass `CASSINI_TALK_RECORDING_SECRET` and `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` into the ExApp roundtrip container so the operator-launched recorder inherits them.
