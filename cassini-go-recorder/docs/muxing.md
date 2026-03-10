# Remuxing

## Layout options

- `multitrack`: all logical tracks in a single container
- `per-participant`: one output per participant

## Current state

- live Talk capture writes session artifacts (`session.json`, `events.ndjson`,
  `streams/*.rtplog`) and composes the final meeting MKV
- legacy `.csr` archives remain only for simulate mode and compatibility tooling
- recorder final-output compose now prefers session-artifact remux and falls back
  to legacy per-session `.ivf/.ogg/.mkv` composition on remux failure
- offline artifact remux is available via `cmd/gocassini-remux` and rebuilds a
  multitrack MKV from `streams/*.rtplog` (Opus + VP8/VP9/H264/AV1)
- VP8/VP9 elementary generation now preserves RTP clock directly in IVF
  (`timebase=1/clockRate`, frame PTS in RTP ticks) instead of deriving a
  fixed-FPS timeline. This removes long-run video stretch that can make audio
  lead over time.
- Opus/VP8/VP9/H264 depacketization now retimestamps packets from `recvMonoNS`
  so mute/silence receive gaps survive offline remux.
- merge planning now accepts corrected timeline starts (from SR-aware estimator)
  and applies bounded per-stream start adjustments before final `-itsoffset`
  mapping, reducing long-run sync skew without re-encoding
- single-track remux now uses `ffmpeg -copyts` (no `+genpts`) so sparse packet
  timelines are not flattened during intermediate MKV generation.
- future phase: plug `pkg/core/mux.Muxer` with multiple backends:
  - pure-Go MKV/WebM (minimal deps)
  - FFmpeg `-c copy` plugin for broader container support

## Why this reduces debug cost

By splitting `rtplog` into deterministic, independent segments and remuxing from
that truth later, drift fixes become a replay problem instead of a lossy production patch.

`gocassini-inspect` now reports per-logical-track segment churn (`ssrc_changes`,
`pt_changes`, and `max_gap_ms`) to make timeline seams explicit during triage.
It also reports per-stream timeline delta metrics (`mean_abs`, `max_abs`, `last`)
from RTP/RTCP timeline estimation so drift regressions are visible quickly.

Artifact remux CLI:

```bash
go run ./cmd/gocassini-remux \
  --session /tmp/sessions/<session_id>/session.json \
  --output /tmp/session-artifact-remux.mkv
```

The current artifact remux implementation supports Opus + VP8/VP9/H264/AV1 streams.

Recorder reports now include applied remux plans:

- `artifact_remux.stream_plans[].timeline_adjust_ns`
- `artifact_remux.stream_plans[].offset_seconds`
- aggregate stats (`adjusted_streams`, `max_abs_adjust_ns`)

The final MKV now also embeds the portable subset of that information directly:

- container tags such as `SESSION_ID`, `CASSINI_FORMAT`, and embedded report filename
- per-stream tags such as `LTID`, `STREAM_ID`, participant identity,
  `timeline_adjust_ns`, and `offset_seconds`
- an attached portable JSON report (`cassini-report.v1.json`)

Drift verification helper:

```bash
./test/bin/verify-av-drift.sh \
  --input /tmp/meeting.mkv
```

This checks paired audio/video track elapsed timelines and fails when absolute
A/V drift exceeds tolerance.

Note: this helper is strict and works best for controlled bot scenarios where
audio/video are expected to stay continuously active. Human meetings with long
mute periods can legitimately produce large elapsed differences per track.
