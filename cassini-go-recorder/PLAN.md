# Cassini Go Recorder - Sync Recovery Plan

Date: 2026-03-04

## Status Update (2026-03-05)

- Composition ownership remains in `test/bin/compose-rs` (Rust + GStreamer).
- Deterministic synthetic gate exists (`test/bin/ci-sync-composition.sh`) and currently passes.
- A real bug was reproduced and fixed in the compositor graph:
- symptom: pipeline stalls/hangs when one video track ends before others.
- cause: per-branch `videorate` before `compositor` blocks sparse/ended pads.
- fix: remove branch-level `videorate`/per-branch framerate forcing; keep framerate normalization only after compositor.
- This bug class is now documented as a known composition-stage failure mode.
- Remaining open issue: real meeting composition can still show inter-track skew even when multitrack MKV appears correct.
- Current verifier coverage is strong for deterministic markers, but not yet sufficient to prove sparse join/leave/rejoin timing preservation in all real captures.

### Sparse-track Reproducer (kept for handoff)

1. Build a two-track sparse MKV where one video track ends early.
2. Compose with `test/bin/compose-recording.sh --preview`.
3. Before the fix, render hangs around the short track end time.
4. After removing branch `videorate`, the same repro completes.

## Current Assessment

- Current pipeline appears correct through multitrack MKV output.
- The composition stage is where sync gets scrambled.
- Strong signal: manual VLC track switching on the multitrack file appears in sync.
- Main risks observed:
- wrong participant pairing (audio/video) when relying on stream order.
- timing rebuilt from weak metadata (`ffprobe start_time`) instead of artifact truth.
- hardware-path differences masking timeline bugs.

## Decision

- `session.json + streams/*.rtplog` is source of truth.
- Artifact remux multitrack MKV is the sync reference.
- Composition must consume artifact mapping/offsets, not infer timing.

## Target State

1. Deterministic local Talk test orchestration.
2. Recorder captures truth and stable track identity.
3. Remux produces multitrack MKV with validated sync.
4. Composition preserves sync from multitrack/session artifact.
5. Automated verifier gates both multitrack and composed outputs.

## Work Plan

### Phase 1 - Local Talk Orchestration First

- Add one entrypoint script to run a full local sync test lifecycle:
- create or reuse local Nextcloud Talk room.
- start deterministic publishers/bots.
- start recorder.
- stop all processes deterministically and collect artifacts.
- integrate with existing tooling (`e2e_with_publisher.sh`, `test/bin/create-room.sh`, `test/bin/stream-video.sh`).
- produce a single run folder with:
- recorder logs.
- publisher schedule manifest.
- session artifact path.
- multitrack output path.
- composed output path.

Exit criteria:
- one command runs local end-to-end and always produces inspectable artifacts.

### Phase 2 - Deterministic Signal Design (Audio + Video)

- Use known, machine-detectable signals per participant.
- Audio signal requirements:
- unique frequency per participant, but not pure continuous sine only.
- scheduled short chirps/beeps with known timestamps.
- optional speech-like envelope/noise bursts to resemble real codec behavior.
- Video signal requirements:
- unique base color per participant, but not flat static frame only.
- moving marker + frame counter + wallclock/mono timestamp overlay.
- periodic visual event aligned with audio chirp schedule.
- Persist full schedule manifest (`signals.json`) for verifier ground truth.

Exit criteria:
- signal generator can produce deterministic runs and manifest reproducibly.

### Phase 3 - Sync Verifier for MKV and Composition

- Add `cmd/gocassini-verify-sync`:
- input: `session.json`, optional `signals.json`, multitrack MKV, composed output.
- checks on multitrack:
- participant mapping consistency (ltid <-> audio/video).
- monotonic PTS per track.
- audio chirp detections vs expected schedule per participant.
- video marker detections vs expected schedule per participant.
- checks on composed output:
- no participant schedule collapse (no "everyone starts together" failure class).
- bounded per-participant A/V skew.
- bounded inter-participant skew relative to schedule.
- emit machine-readable report JSON + non-zero exit on threshold failure.

Exit criteria:
- verifier fails on injected skew fixtures and passes on known-good fixtures.

### Phase 4 - Composer Rewrite to Artifact-Driven Sync

- Remove composition-time participant pairing heuristics.
- Pair tracks using `session.json` logical track IDs / participant IDs.
- Use remux stream plans for offsets/timeline.
- Keep hardware acceleration as execution optimization only.
- if VAAPI decode unsupported: software decode + VAAPI encode fallback.
- identical timeline math across hardware/software code paths.

Exit criteria:
- same sync metrics for multitrack and composed outputs in verifier report.

### Phase 5 - CI/Local Gate

- Gate success on verifier for both artifacts:
- multitrack MKV must pass.
- composed output must pass.
- add one regression fixture for the current bug class:
- out-of-order speech / all-audio-at-once collapse.

Exit criteria:
- PASS/FAIL is automated; manual VLC inspection becomes optional.

## Critique of the Proposed Signal Idea

- Good direction overall: known audio/video patterns are exactly what we need.
- Not sufficient if done as only pure sine + only flat color:
- continuous pure sine is too forgiving and can hide packet-loss/reorder behavior.
- static single-color video can hide frame timing/path issues.
- stronger variant (recommended):
- chirp schedule + unique frequencies + speech-like modulation.
- color identity + moving marker + frame/time overlay.

## Tests To Add (Red/Green Priority)

1. `pkg/core/remux`: participant mapping tests where stream order differs from participant order.
2. `pkg/core/remux`: churn/SSRC/PT change fixtures with monotonic and bounded-skew assertions.
3. Composer unit tests: artifact-driven mapping independent from ffprobe order.
4. Verifier tests: chirp and visual marker detection against `signals.json`.
5. End-to-end local test: Talk + bots -> record -> remux -> compose -> verify.

## What Is No Longer Considered “Done”

- Composition sync is not considered complete without verifier pass.
- Manual playback checks are supporting evidence only, not pass criteria.

## Immediate Next Slice

1. Implement Phase 1 orchestration command and artifact bundle output.
2. Implement deterministic signal generator + `signals.json` manifest.
3. Implement first verifier checks on multitrack MKV (before composition).
