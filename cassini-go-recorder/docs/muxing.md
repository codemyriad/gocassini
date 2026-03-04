# Remuxing

## Layout options

- `multitrack`: all logical tracks in a single container
- `per-participant`: one output per participant

## Current state

- live capture writes both legacy `.csr` and session artifacts (`session.json`,
  `events.ndjson`, `streams/*.rtplog`)
- recorder final-output compose now prefers session-artifact remux and falls back
  to legacy per-session `.ivf/.ogg/.mkv` composition on remux failure
- offline artifact remux is available via `cmd/gocassini-remux` and rebuilds a
  multitrack MKV from `streams/*.rtplog` (VP8/Opus currently)
- future phase: plug `pkg/core/mux.Muxer` with multiple backends:
  - pure-Go MKV/WebM (minimal deps)
  - FFmpeg `-c copy` plugin for broader container support

## Why this reduces debug cost

By splitting `rtplog` into deterministic, independent segments and remuxing from
that truth later, drift fixes become a replay problem instead of a lossy production patch.

`gocassini-inspect` now reports per-logical-track segment churn (`ssrc_changes`,
`pt_changes`, and `max_gap_ms`) to make timeline seams explicit during triage.

Artifact remux CLI:

```bash
go run ./cmd/gocassini-remux \
  --session /tmp/sessions/<session_id>/session.json \
  --output /tmp/session-artifact-remux.mkv
```

The current artifact remux implementation supports VP8/Opus streams.
