# Cassini Go Recorder - Development Guide

## Scope

This service has three recording layers:

1. Session artifact directory: source-of-truth capture of inbound RTP/RTCP with timing metadata (`session.json`, `events.ndjson`, `streams/*.rtplog`, `*.idx`).
2. Deliverable MKV (`.mkv`): single public artifact composed from the session artifact after capture ends.
3. Run report (`.json`): optional legacy machine-readable summary emitted only when `--write-report` is enabled.

Legacy `.csr` archives remain available for simulate mode and compatibility tooling, but they are no longer the normal Talk capture output.

## Pipeline

1. Join Nextcloud room and signaling.
2. Create subscriber peer per remote session.
3. Capture each remote track packet-by-packet.
4. Write packets to per-stream `.rtplog` + `.idx` files and append session/events metadata.
5. Build deterministic artifact-remux work files as needed:
- audio: `.ogg` (Opus)
- video: `.ivf` (VP8)
- remuxed per-session `.mkv`
6. Compose final multi-track MKV with session start offsets and embedded Cassini metadata.
7. Keep intermediates by default for forensics in a unique non-hidden temp directory.
8. Optionally write a legacy JSON report next to the final output (`<final>.json`).

## Current Codec Assumptions

- Artifact remux is implemented for Opus audio and VP8/VP9/H264/AV1 video.
- Unsupported codecs still land in session artifacts for forensic inspection, but may not appear in the final MKV until additional mux paths are added.

## JSON Report

- Default report path is `<final-output>.json`; fallback is `<output>.json` when no final output path is set.
- Default segments path is created via `os.MkdirTemp` next to the final output, with prefix `<final-base>-segments-`.
- Report includes:
- final output state and size
- session artifact state and stream counters
- final compose status and size
- intermediate segments retention/cleanup state
- per-session file paths, exists flags, sizes, and packet counters
- warnings list for partial/failed outputs

## End-to-End Test

Run:

```bash
./e2e_with_publisher.sh
```

This script starts the Go recorder and drives Go publisher clients through the shared
`../test/bin/stream-video.sh` harness.

## Known Gaps

- Resolution adaptation inside a single continuous publisher session is not yet simulated.
- Explicit packet-loss/jitter shaping is not yet part of the harness.

## Planned Architecture Workstream

- keep Talk capture MKV-first while tightening the embedded metadata contract
- keep compatibility diagnostics between legacy `.csr` paths and session-artifact truth where still useful
- continue expanding session artifact/event diagnostics as first-class debug artifacts
