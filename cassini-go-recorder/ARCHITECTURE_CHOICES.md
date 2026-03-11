# Cassini Go Recorder - Architecture Choices

Date: 2026-03-02
Status: accepted for implementation start

## Goal

Build a Go/Pion recorder that captures Nextcloud Talk media with minimal CPU overhead during capture, while preserving enough timing information to compose synchronized multi-participant outputs later.

## Decisions Captured

1. Single public meeting artifact (not per-participant outputs)
- Capture produces one MKV deliverable per meeting and stores packet truth in a colocated session directory.
- Rationale: preserves cross-participant timing while keeping the operator-facing artifact simple.

2. No transcode in capture path
- Capture stores encoded RTP packets as received (with metadata + receive timestamps).
- No decode+re-encode during live recording.
- Rationale: keep CPU dominated by signaling + packet I/O, not encoding.

3. Two-phase pipeline
- Phase A (live): ingest signaling + WebRTC packets and persist session artifacts.
- Phase B (offline): compose the final MKV and any other derivatives from that packet truth.
- Rationale: reliability and low runtime cost during meetings.

4. Bot-as-participant remains primary ingestion path
- The recorder joins as a Talk participant and subscribes to remote tracks.
- Rationale: aligns with product behavior and avoids Janus-only post-processing limits already observed.

5. Timing model for anti-drift post-processing
- Persist both RTP timing (sequence/timestamp in packet bytes) and wallclock receive time per packet.
- Persist track lifecycle events (start/end) and stable participant/session IDs.
- Rationale: enables deterministic timeline reconstruction and drift correction later.

6. Explicit fallback for today
- Keep Go recorder usage behind correctness gates until live validation is complete.
- Promote as default recorder path only after validation criteria are met.

7. Practicality gate
- If full no-transcode output composition proves impractical, fallback remains:
  - keep no-transcode archive as source-of-truth;
  - generate lightweight derived outputs using low-resource encoding presets only where required.

## Near-term Milestones

1. Implement archive writer + parser (single file, packet-level).
2. Integrate Pion track ingestion into archive writer.
3. Port Nextcloud Talk signaling/session negotiation from Python.
4. Add deterministic replay/composition tooling from archive to deliverable media.
5. Validate sync on multi-participant joins/rejoins and packet-loss scenarios.

## Long-term Migration Decision

- adopt a session-directory capture model with session-wide metadata (`session.json`)
- capture packet truth at stream granularity with `.rtplog` and optional `.idx`
- split identity mapping by `MID/RID` before SSRC and keep SSRC as segment identity
- keep `.csr` only as a compatibility mode for simulate/legacy tooling

## Acceptance Gate Before Switching Production

- Stable capture with 2+ concurrent participants.
- No fatal recorder crashes under join/leave churn.
- Deterministic archive parse/replay.
- Post-processed output shows no obvious drift in beginning/middle/end spot checks.

## Expected Debugging Time Savings

The segmentation-first capture path is intended to reduce drift debugging effort
from "reproduce live and guess" to "replay deterministic packet truth":

- previous failure mode: one long stream timeline, drift discovered at tail,
  often requiring a full meeting rerun to inspect a single timeline seam
- current direction: segment on RTP identity churn (SSRC/PT), then inspect/remux
  each seam offline from `streams/*.rtplog` and `events.ndjson`

Estimated impact for a typical drift incident:

- before: ~60-180 minutes (reproduce call + capture + manual triage)
- after: ~10-30 minutes (inspect + remux from existing session artifact)

These are engineering targets, not guarantees, and are validated through the
deterministic + local E2E harness in `harness/bin`.
