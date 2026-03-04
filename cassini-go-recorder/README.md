# Gocassini

`gocassini` is a CLI-first meeting recorder for Nextcloud Talk.
It is intentionally narrow: one job, one output contract, and strong behavior for automation.

## Current scope (v1)
- Capture audio/video RTP streams from Nextcloud Talk meetings
- Persist a raw archive (`.csr`) with packet metadata
- Persist a per-session artifact directory with `session.json`, `streams/*.rtplog`, and `events.ndjson`
- Compose a multi-track MKV (`.mkv`) as the primary deliverable
- Keep intermediate files deterministic and optionally cleanable
- Keep everything script-friendly and machine-friendly

## What this repo contains
- `cmd/gocassini`: main recorder command
- `cmd/gocassini-inspect`: archive inspection utility
- `cmd/gocassini-remux`: offline remux from session artifacts (`session.json` + `streams/*.rtplog`)
- `internal/`: codec-agnostic recorder, signaling, and Nextcloud Talk adapters
- `test/`: local reproducible Nextcloud Talk stack + publisher harness
- `docs/architecture-migration-status.md`: current migration goals, effort, and status

## Quickstart

```bash
cd cassini-go-recorder
go run ./cmd/gocassini --mode simulate --output /tmp/gocassini.csr
go run ./cmd/gocassini-inspect /tmp/gocassini.csr
```

## Live Talk capture

```bash
cd cassini-go-recorder
go run ./cmd/gocassini \
  --mode talk \
  --call-url https://cloud.example.com/call/<ROOM_TOKEN> \
  --name GocassiniObserver \
  --duration 55 \
  --output /tmp/meeting.csr \
  --final-output /tmp/meeting.mkv
```

Use `--help` on each command to inspect all options:

```bash
go run ./cmd/gocassini --help
go run ./cmd/gocassini-inspect --help
go run ./cmd/gocassini-remux --help
```

## Integration test helper

```bash
cd ..
./test/bin/ci-e2e.sh
```

This runs:
- local Nextcloud Talk stack
- room creation
- publisher bot
- recorder in live mode
- output validation

You can also run:

```bash
cd cassini-go-recorder
./e2e_with_publisher.sh
```

If `CALL_URL` is unset, it reuses `test/runtime/last_call_url` or creates a fresh room in the local test stack.

### Review output rendering

- Render a synchronized 3-grid MP4 review file from a final MKV session:
  - `./test/bin/compose-recording.sh --input <recording.mkv> --publisher-log <recording.mkv.publisher.log>`
  - output defaults to `<recording>.composed.mp4`
- Validate end-of-playback audio duration with the same verifier used in CI:
  - `./test/bin/verify-audio-tail.sh --input <recording>.composed.mp4`
  - if a scenario intentionally ends with all participants muted, use a lower threshold:
    `./test/bin/verify-audio-tail.sh --input <recording>.composed.mp4 --min-mean-db -90`

Clean up:

```bash
cd test
./bin/down.sh --volumes
```

## Output contract

- Archive file: `.csr` (source-of-truth stream log)
- Session artifact: `sessions/<id>/session.json`, `streams/*.rtplog`, `events.ndjson`
  with stream segmentation on SSRC/PT churn (same logical track can produce
  multiple stream segments)
- Final output: `.mkv` (single deliverable for playback/transcoding/transcription)
- Final output compose path prefers session-artifact remux (`streams/*.rtplog`) and falls back to legacy intermediates if needed
- Report: `<final>.json` with session and compose status
- Intermediate per-session files: `<output>-segments-*` unless cleanup is enabled
- `gocassini-inspect` prints legacy archive summaries; session artifacts are written next to `.mkv` and available at `<final>/../sessions/<id>/`.
  For session artifacts it also prints per-stream validation issues, `segment_churn` (`ssrc_changes`, `pt_changes`, `max_gap_ms`), and stream close reasons.
- `gocassini-remux` can rebuild a multitrack MKV directly from session artifacts without using capture-time intermediate files.

The project is designed so the primary interface remains the CLI and file artifacts.

## Artifact Remux

Rebuild a multi-track MKV directly from a session artifact directory:

```bash
go run ./cmd/gocassini-remux \
  --session /tmp/sessions/<session_id>/session.json \
  --output /tmp/session-artifact-remux.mkv
```

Current supported codecs for artifact remux are Opus audio and VP8/VP9/H264/AV1 video.
