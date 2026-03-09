# Architecture Migration Status (2026-03-04)

## Goals

1. Make drift bugs reproducible from packet truth without rerunning meetings.
2. Segment streams aggressively on RTP identity churn (SSRC/PT) so timeline seams
   are explicit.
3. Expose diagnostics that explain churn/gaps/validation issues quickly.
4. Keep local E2E checks representative of real Talk behavior.

## Implemented in this cycle

1. Segment-aware session artifact capture:
- stream logs rotate on SSRC/PT changes and preserve logical-track mapping.
- `session.json` packet streams now include `pt`.
- live capture now records RTCP alongside RTP in `streams/*.rtplog`.
- mixed RTP/RTCP writes enforce monotonic per-stream receive timestamps.

2. Deterministic inspection upgrades:
- `gocassini-inspect` prints per-stream identity (`ssrc`, `pt`) and validation
  issue counts.
- `gocassini-inspect` now reports per-stream RTP/RTCP timeline delta metrics
  (`mean_abs`, `max_abs`, `last`) for faster drift triage.
- `segment_churn` summary added per logical track (`segments`, `ssrc_changes`,
  `pt_changes`, `max_gap_ms`).
- stream close reasons are aggregated from `events.ndjson`.

2.5. Offline artifact remux:
- added `cmd/gocassini-remux` to rebuild multitrack MKV directly from
  `session.json` + `streams/*.rtplog` (Opus + VP8/VP9/H264/AV1 paths).
- recorder final-output compose now uses the same artifact remux path first,
  then falls back to legacy intermediate merge if remux fails.
- remux offset planning now uses bounded timeline-derived start adjustments
  (SR-aware), so stitching is informed by corrected timeline estimates.
- recorder reports now persist exact per-stream applied adjustments and offsets
  under `artifact_remux.stream_plans[]`.
- local E2E now exercises this artifact-based remux step.

2.6. Timeline correction:
- `pkg/core/timeline` now applies bounded SR-aware slope/intercept correction on
  top of receive-time canonical mapping.
- deterministic tests verify correction reduces synthetic long-run drift and keeps
  PTS monotonic under noisy SR input.

3. Validation package:
- added `pkg/core/validate` for `.rtplog` invariants:
  - monotonic `recvMonoNS`
  - RTP unmarshal sanity
  - payload-type consistency vs stream header snapshot

4. Local E2E hardening:
- room creation retries made observable (`stderr`) and more resilient.
- one-time bootstrap refresh on OCS `statuscode 996`.
- bootstrap now auto-resolves container-reachable signaling URL for Docker runs.
- fixed `e2e_with_publisher.sh` artifact verifier path bug.
- fixed `verify-session-artifact.sh` index filename logic (`<stream>.idx`).

## Evidence

- unit/integration tests:
  - `go test ./...` passes in `cassini-go-recorder`.
- local live E2E:
  - `./test/bin/ci-e2e.sh` covers the baseline single-publisher path.
  - `./test/bin/ci-e2e-mute.sh` covers multi-publisher mute behavior.
  - `./test/bin/ci-e2e-rejoin.sh` covers leave/rejoin behavior.
  - session artifact verification passes (`streams`, `events`, `.idx` sidecars).

## Debug-time impact

Current expected triage path for drift:

1. inspect session artifact (`gocassini-inspect session.json`)
2. locate churn seams/issues (`segment_churn`, validation output)
3. remux/replay from existing logs

This should continue reducing drift incident investigation from "rerun live call"
to "replay deterministic artifact", typically cutting triage from hours to tens
of minutes.

## Remaining migration work

1. Add optional output metadata embedding for per-stream adjustment summaries so
   forensic context survives when JSON sidecars are unavailable.
2. Extend artifact remux audio codec coverage beyond Opus where deployment
   requires it.
3. Add richer depacketization/remux modules for broader codec/container support
   and participant-level layout rendering.
