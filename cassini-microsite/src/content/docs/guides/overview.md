---
title: Overview
description: A detailed look at the Cassini recording pipeline and how the pieces fit together.
---

This guide explains the full Cassini workflow — from joining a room to opening a replay in the browser — and what happens at each stage.

## The pipeline

Cassini's `record` command is a three-stage pipeline. Each stage produces an artifact that the next stage consumes.

```
cassini record
  └── Stage 1: Capture   →  .run bundle
  └── Stage 2: Build     →  .meeting artifact
  └── Stage 3: Publish   →  .opus file
```

You can run each stage manually if you need more control. See [Advanced usage](#advanced-usage) below.

## Stage 1 — Capture

Cassini joins the Nextcloud Talk room as a silent participant and records the audio stream. It writes everything into a `.run` bundle: raw audio, participant events, and timing metadata.

The capture stage ends when the call ends or when you interrupt Cassini with `Ctrl+C`.

## Stage 2 — Build

The build stage processes the `.run` bundle. It runs the audio through:

- **Transcription** — a local speech-to-text model produces a time-aligned transcript
- **Diarisation** — the transcript is annotated with speaker turns where possible
- **Packaging** — the transcript and audio are combined into a `.meeting` artifact

This stage is the most time-consuming. On a modern machine, expect roughly 0.5× real-time — a one-hour meeting takes around 30 minutes to process.

## Stage 3 — Publish

Publish takes a `.meeting` artifact and writes the final `.opus` file. This step is fast — it is mostly a repackaging operation with no additional processing.

## Resumability

Cassini stores intermediate state in a `.cassini-work/` directory next to your `--out` file:

```
My Meetings/
  2026-07-01 Team Sync.opus        ← final output
  .cassini-work/
    2026-07-01 Team Sync.run       ← stage 1 output (kept)
    2026-07-01 Team Sync.meeting   ← stage 2 output (kept)
```

If the command fails at stage 2, rerunning it skips stage 1 (capture already done) and retries from stage 2. This means you never lose a recording to a transcription failure.

## The viewer

The Cassini viewer is a static web app bundled separately. Use `cassini serve` to start a local server pointed at a directory of `.opus` files:

```bash
./bin/cassini serve "./My Meetings"
```

The viewer gives you:

- **Timeline scrubbing** — click any point in the waveform to jump to it
- **Transcript navigation** — click any line in the transcript to seek to that moment
- **Full-text search** — search across the transcript to find when something was said
- **Speaker filtering** — filter the transcript by participant

## Advanced usage

You can run each pipeline stage explicitly if you need to inspect intermediate artifacts or retry a specific step:

```bash
# Stage 1: capture only
./bin/cassini record --call "$CALL_URL" --out ./runs/meeting.run

# Stage 2: build from a .run bundle
./bin/cassini build ./runs/meeting.run --out ./meetings/meeting.meeting

# Stage 3: publish to .opus
./bin/cassini publish ./meetings --out ./site

# Inspect any artifact
./bin/cassini inspect ./runs/meeting.run
./bin/cassini inspect ./meetings/meeting.meeting
```

## Nextcloud app install

Cassini can also run as a Nextcloud ExApp — registered via AppAPI, it records meetings server-side without requiring a separate machine. See the [ExApp install guide](https://github.com/codemyriad/cassini/blob/main/docs/exapp-install.md) for the full setup.
