# Gocassini

`gocassini` is a CLI-first meeting recorder for Nextcloud Talk.
It is intentionally narrow: one job, one output contract, and strong behavior for automation.

## Current scope (v1)
- Capture audio/video RTP streams from Nextcloud Talk meetings
- Persist a per-session artifact directory with `session.json`, `streams/*.rtplog`, and `events.ndjson`
- Compose a multi-track MKV (`.mkv`) as the primary deliverable
- Embed portable Cassini meeting metadata directly in the final MKV
- Keep legacy `.csr` archive handling only for simulate mode and compatibility tooling
- Keep intermediate files deterministic and optionally cleanable
- Keep everything script-friendly and machine-friendly

## What this repo contains
- `cmd/gocassini`: main recorder command and primary product surface
- `cmd/gocassini-inspect`: diagnostic inspection utility
- `cmd/gocassini-remux`: diagnostic or recovery remux from session artifacts (`session.json` + `streams/*.rtplog`)
- `cmd/gocassini-upgrade-mkv`: compatibility upgrader for older meeting MKVs plus legacy `.mkv.json` reports
- `internal/`: codec-agnostic recorder, signaling, and Nextcloud Talk adapters
- `../cassini-player/`: suite-level room player wrappers
- `../test/`: local reproducible Nextcloud Talk lab and E2E harness
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
  --output /tmp/meeting.mkv
```

In talk mode, `--output` can point directly at the final `.mkv`. Keep `--final-output` only if you need a separate compatibility path.

By default, talk mode auto-terminates when all remote participants leave (`--stop-when-room-empty=true`, `--room-empty-grace=30s`). Add `--duration <seconds>` only if you need a hard time limit.

Use `--help` on each command to inspect all options:

```bash
go run ./cmd/gocassini --help
go run ./cmd/gocassini-inspect --help
go run ./cmd/gocassini-remux --help
go run ./cmd/gocassini-upgrade-mkv --help
```

## Integration test helper

```bash
cd ..
./test/bin/ci-e2e.sh
```

This runs:
- local Nextcloud Talk stack
- room creation
- player clients
- recorder in live mode
- output validation

You can also run:

```bash
cd cassini-go-recorder
./e2e_with_publisher.sh
```

If `CALL_URL` is unset, it reuses `test/runtime/last_call_url` or creates a fresh room in the local test stack.

Clean up:

```bash
cd test
./bin/down.sh --volumes
```

## Output contract

- Final output: `.mkv` (single deliverable for playback/transcoding/transcription)
- Session artifact: `sessions/<id>/session.json`, `streams/*.rtplog`, `events.ndjson`
  with stream segmentation on SSRC/PT churn (same logical track can produce
  multiple stream segments)
- Final output compose path prefers session-artifact remux (`streams/*.rtplog`) and falls back to legacy intermediates if needed
  with timeline-aware offset planning from SR-corrected estimates.
- Final MKV embeds Cassini meeting metadata directly:
  - container tags such as `SESSION_ID` and `CASSINI_FORMAT`,
  - per-stream tags such as `LTID`, `STREAM_ID`, participant identity,
    `offset_seconds`, and `timeline_adjust_ns`,
  - attached portable JSON report (`cassini-report.v1.json`) for richer inspection.
- Report: optional `<final>.json` legacy sidecar with session and compose status
  when `--write-report` is enabled.
- Legacy archive file: `.csr` remains supported by simulate mode and inspect tooling,
  but it is no longer the normal Talk recording artifact.
- Intermediate per-session files: `<output>-segments-*` unless cleanup is enabled
- `gocassini-inspect` prints legacy archive summaries; session artifacts are written next to `.mkv` and available at `<final>/../sessions/<id>/`.
  For session artifacts it also prints per-stream validation issues, `segment_churn` (`ssrc_changes`, `pt_changes`, `max_gap_ms`), and stream close reasons.
  It also reports timeline delta diagnostics (`mean_abs`, `max_abs`, `last`) derived from RTP/RTCP.
- `gocassini-remux` can rebuild a multitrack MKV directly from session artifacts without using capture-time intermediate files.

## Legacy MKV Upgrade

If you have an older meeting `.mkv` that predates the MKV-v1 metadata contract,
but you still have its legacy recorder sidecar at `<meeting>.mkv.json`, you can
upgrade it into a compliant MKV without re-recording:

```bash
go run ./cmd/gocassini-upgrade-mkv \
  --input /path/to/meeting.mkv
```

This writes `/path/to/meeting.v1.mkv` by default. The upgrader keeps the
existing audio/video streams, copies the container, injects the missing MKV-v1
tags, and attaches `cassini-report.v1.json` inside the MKV.

The project is designed so the primary interface remains the CLI and file artifacts.

## Artifact Remux

Rebuild a multi-track MKV directly from a session artifact directory:

```bash
go run ./cmd/gocassini-remux \
  --session /tmp/sessions/<session_id>/session.json \
  --output /tmp/session-artifact-remux.mkv
```

Current supported codecs for artifact remux are Opus audio and VP8/VP9/H264/AV1 video.
