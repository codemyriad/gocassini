# Architecture Migration Status (2026-03-10)

## Current conclusion

We did not find a recorder sync defect.

- The current multitrack MKV output is internally consistent.
- Audio rendered from the current MKV is in sync and sounds correct on real
  meeting data.
- The apparent "sync issue" was a tooling problem: we could not find a good
  multitrack audio/video player that represented these files reliably enough
  for debugging.

That changes the framing of the work. The recorder/remux path is not currently
blocked on drift. The real gaps are artifact format discipline, transcription
quality on real data, and playback UX.

## Implemented so far

1. Segment-aware session artifact capture:
- stream logs rotate on SSRC/PT changes and preserve logical-track mapping.
- `session.json` packet streams include payload type metadata.
- live capture records RTCP alongside RTP in `streams/*.rtplog`.
- mixed RTP/RTCP writes enforce monotonic per-stream receive timestamps.

2. Deterministic inspection and remux:
- `gocassini-inspect` reports per-stream identity, validation issue counts,
  churn summaries, and RTP/RTCP timeline delta metrics.
- `cmd/gocassini-remux` rebuilds multitrack MKV directly from preserved session
  artifacts.
- recorder final compose uses the artifact remux path first, with legacy merge
  retained as fallback.
- remux offset planning uses bounded timeline-derived start adjustments and the
  recorder report persists the applied per-stream plan.

3. Timeline and validation hardening:
- `pkg/core/timeline` applies bounded SR-aware correction on top of receive-time
  mapping.
- deterministic tests cover long-run correction behavior and monotonic PTS under
  noisy sender reports.
- `pkg/core/validate` checks `.rtplog` invariants such as monotonic receive
  time, RTP decode sanity, and payload-type consistency.

4. Local E2E hardening:
- room creation/bootstrap behavior is more resilient and more observable.
- local E2E covers baseline, mute, and leave/rejoin behavior.
- session artifact verification covers `session.json`, `events.ndjson`,
  `streams/*.rtplog`, and `.idx` sidecars.

## Evidence

- `go test ./...` passes in `cassini-go-recorder`.
- local live E2E covers the baseline and multi-participant cases.
- preserved session artifacts allow deterministic replay and inspection.
- most importantly: real meeting MKVs produce correct mixed audio without
  observable sync problems.

## What changed in our priorities

The earlier migration plan assumed we were chasing a recorder drift problem.
That was the wrong primary problem.

We now treat the current MKV as basically correct and shift effort to the
places where the product still feels unfinished:

1. tighten "our format" for a meeting artifact
2. test and tune the transcriber on more real meetings
3. improve the player so multitrack playback is trustworthy and pleasant

## Current goals

1. Single meeting artifact:
- stop treating `meeting.mkv` plus `meeting.mkv.json` as the long-term public
  contract.
- make the MKV the primary durable artifact and embed more of the portable
  metadata directly into it.
- keep host-local or privacy-sensitive details out of embedded metadata when
  they do not belong in the artifact.

2. Better transcription with real data:
- run the transcriber on a larger set of real meetings instead of only synthetic
  or short samples.
- use those runs to tune chunking, silence reduction, speaker handling, and
  output quality.
- treat transcription evaluation as a product loop, not just a pipeline demo.

3. Better playback:
- accept that generic multitrack players are not a reliable source of truth for
  Cassini artifacts.
- improve `cassini-viewer` or related tooling around our actual meeting format:
  sparse tracks, late joins, repeated segments, and participant-centric review.
- optimize for trustworthy inspection first, polish second.

## Near-term migration work

1. Tighten the MKV format:
- embed additional per-stream metadata in the MKV itself, especially values that
  help explain composition and playback behavior.
- move toward a single-file artifact, with the sidecar becoming optional debug
  output rather than the default contract.
- prefer structured metadata that survives file moves and offline sharing.

2. Expand transcriber evaluation:
- build a small but representative corpus of real meetings.
- measure where current transcription output fails and tune from observed data.
- keep the artifact bundle and transcript outputs easy to inspect side by side.

3. Nail the player:
- make playback semantics match the artifact semantics.
- make it easy to inspect per-participant tracks, joins/leaves, and derived
  mixed outputs.
- stop using weak third-party player behavior as evidence of recorder defects.
