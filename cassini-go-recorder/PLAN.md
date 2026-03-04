# Cassini Go Recorder - Implementation Plan

Date: 2026-03-02

## Phase 0 (Done in this iteration)

- Architecture choices documented.
- Single-file packet archive format implemented (`.csr`, Cassini Stream Recording v1).
- Archive writer/reader + roundtrip test added.
- Pion track capture boundary added (`CaptureTrack`).
- Deterministic `simulate` mode added for local verification without live infrastructure.
- OCS bootstrap (`talk` mode): room check, participants/active, guest naming, signaling settings fetch, call join.

## Phase 1 (Completed)

- Ported standalone signaling websocket client with request/response correlation.
- Added one subscriber `PeerConnection` per remote signaling session.
- Implemented `requestoffer` retry/backoff + explicit `endOfCandidates` logic.
- Wired remote tracks to `CaptureTrack` goroutines and single-file `.csr` archive output.
- Added live validation against a real cloud call URL (1-user and 2-user publisher scenarios).
- Added repeatable Go e2e harness (`e2e_with_publisher.sh`) and archive inspector CLI (`gocassini-inspect`).

## Phase 2 (Completed)

- Added intermediate per-session files (`.ogg`/`.ivf` and per-session `.mkv`).
- Added final single multi-track MKV composition with session start offsets.
- Default behavior keeps intermediate files for forensic inspection in unique non-hidden temp dirs (`--cleanup-intermediate` opt-in).
- Added JSON sidecar recording report (`<final>.json`) with output state, packet counters, and per-session artifact stats.
- Added repeatable Go publisher integration harness (`e2e_with_publisher.sh`) for live validation.

## Phase 3

- Add recovery behavior for abrupt disconnects and partial recordings.
- Extend e2e harness with pass/fail assertions on expected track counts and minimum packet counts.
- Enrich participant identity mapping beyond session-derived placeholders.

## Phase 4

- Improve composed MKV metadata and deterministic track labeling.
- Second derivative target: optional composed grid MP4.

## Architecture Migration Path (2026-03 onward)

- add `pkg/core/session` as the index model for session-wide truth
- add `pkg/core/store` as the canonical stream log schema (`.rtplog` + optional `.idx`)
- add `pkg/core/timeline` estimator (recv-time canonical + bounded SR correction) and `pkg/core/mux` remux interface
- add `pkg/core/validate` invariants for deterministic drift triage (`recvMonoNS` monotonicity + payload-type consistency)
- add `pkg/core/depacket` + `pkg/core/remux` and `cmd/gocassini-remux` for offline artifact-based MKV reconstruction
- keep current `.csr` flow unchanged while adding compatibility tooling so we can compare outputs side-by-side
- next phase: move capture writes from `internal/cassette` into `pkg/core/store` and switch `cmd/gocassini-inspect` + remux to consume the new schema

## Go/No-Go Criteria

Proceed with full migration only if:
- live capture is stable under join/leave churn;
- no-transcode archive remains parseable/replayable across test calls;
- offline composition yields acceptable sync in start/middle/end checks.

If not met, keep Go recorder in experimental mode and continue iterations before production promotion.
