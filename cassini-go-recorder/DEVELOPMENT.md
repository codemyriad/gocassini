# Cassini Go Recorder - Development Guide

## Scope

This service has three recording layers:

1. Packet archive (`.csr`): source-of-truth capture of inbound RTP packets with timing metadata.
2. Deliverable MKV (`.mkv`): composed from intermediate per-session files after capture ends.
3. Run report (`.json`): machine-readable summary of outputs, packet counts, compose result, and warnings.

## Pipeline

1. Join Nextcloud room and signaling.
2. Create subscriber peer per remote session.
3. Capture each remote track packet-by-packet.
4. Write packets to `.csr` archive.
5. Write per-session intermediates:
- audio: `.ogg` (Opus)
- video: `.ivf` (VP8)
- remuxed per-session `.mkv`
6. Compose final multi-track MKV with session start offsets.
7. Keep intermediates by default for forensics in a unique non-hidden temp directory.
8. Write JSON report next to the final output (`<final>.json`; fallback `<archive>.json`).

## Current Codec Assumptions

- Intermediate packet muxing is implemented for Opus audio + VP8 video.
- Other codecs are still archived in `.csr`, but may not appear in intermediate/final MKV until additional mux paths are added.

## JSON Report

- Default report path is `<final-output>.json`; fallback is `<output>.json` when no final output path is set.
- Default segments path is created via `os.MkdirTemp` next to the final output, with prefix `<final-base>-segments-`.
- Report includes:
- archive state and size
- archive packet/track counters
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
