---
title: Introduction
description: What Cassini is, why it exists, and the concepts behind it.
---

Cassini solves a specific problem: Nextcloud Talk meetings happen and then disappear. The recording exists somewhere on a server, but getting it out, transcribed, and into a form you can actually review is friction. Cassini removes that friction.

## The problem

Nextcloud Talk can record meetings, but the raw output is a video file that lives on the server. To review it you need to download it, find the right timestamp, scrub through video to find what was said. There is no transcript, no navigation, no way to share just the relevant portion.

## What Cassini does

Cassini attaches to a Talk room during a meeting and produces a single `.opus` file when the meeting ends. That file contains:

- The audio recording
- A time-aligned transcript generated on the fly
- Speaker identification where possible
- Meeting metadata (room, participants, duration)

The Cassini viewer — a static web app — opens that file and gives you a navigable, searchable meeting replay.

## Core concepts

### The `.opus` file

The `.opus` file is Cassini's portable meeting format. It is a self-contained archive — everything needed to replay the meeting is inside it. You can move it, email it, or put it in a shared folder. The viewer needs no server.

### The pipeline

Under the hood, Cassini runs a three-stage pipeline:

1. **Record** — captures audio from the Talk room and writes a `.run` bundle
2. **Build** — processes the recording into a `.meeting` artifact (transcription, diarisation)
3. **Publish** — packages everything into the final `.opus` file

The `cassini record` command runs all three stages automatically. The intermediate artifacts are kept in a `.cassini-work/` directory next to your output file and are reused if the command is interrupted and rerun.

### Resumability

If `cassini record` fails after capture but before the transcript is finished, rerunning the same command will skip the capture stage and pick up from where processing stopped. This matters for long meetings where transcription can take several minutes.

## What Cassini is not

- **Not a video recorder** — Cassini captures audio only
- **Not a server application** — it runs as a CLI on a machine with network access to your Nextcloud instance
- **Not a storage solution** — `.opus` files are yours to store wherever makes sense
