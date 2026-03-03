# Remuxing

## Layout options

- `multitrack`: all logical tracks in a single container
- `per-participant`: one output per participant

## Current state

- live capture writes both legacy `.csr` and session artifacts (`session.json`,
  `events.ndjson`, `streams/*.rtplog`)
- per-session `.ivf/.ogg/.mkv` intermediate assets are still used for immediate playback path
- future phase: plug `pkg/core/mux.Muxer` with multiple backends:
  - pure-Go MKV/WebM (minimal deps)
  - FFmpeg `-c copy` plugin for broader container support

## Why this reduces debug cost

By splitting `rtplog` into deterministic, independent segments and remuxing from
that truth later, drift fixes become a replay problem instead of a lossy production patch.

`gocassini-inspect` now reports per-logical-track segment churn (`ssrc_changes`,
`pt_changes`, and `max_gap_ms`) to make timeline seams explicit during triage.
