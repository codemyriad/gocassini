# Remuxing

## Layout options

- `multitrack`: all logical tracks in a single container
- `per-participant`: one output per participant

## Current state

- live capture remains `.csr` only
- per-session `.ivf/.ogg/.mkv` intermediate assets are still used for immediate playback path
- future phase: plug `pkg/core/mux.Muxer` with multiple backends:
  - pure-Go MKV/WebM (minimal deps)
  - FFmpeg `-c copy` plugin for broader container support

## Why this reduces debug cost

By splitting `rtplog` into deterministic, independent segments and remuxing from
that truth later, drift fixes become a replay problem instead of a lossy production patch.
