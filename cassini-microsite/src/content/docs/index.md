---
title: Introduction
description: Cassini is a Nextcloud app that records Talk calls, transcribes them on your own server, and publishes each meeting as one file in Nextcloud Files.
source: docs/README.md
copied: "2026-09-17"
---

Cassini is a Nextcloud app. It registers as Nextcloud Talk's recording server, so
your people press the Record button they already have. It records the call,
transcribes it on your own infrastructure, and publishes each meeting as one
file in Nextcloud Files.

## What happens when you press Record

1. Talk asks Cassini to record the conversation. Cassini joins as an internal
   client of the signalling server, not as a participant: nobody sees an extra
   person in the call.
2. Every participant arrives as a separate audio stream, carrying the display
   name Talk sent with it.
3. When the call ends, Cassini transcribes the audio in-process, on the hardware
   you gave it, and puts each participant's name on their own lines.
4. If you have configured a language model, it writes a summary.
5. The meeting is published into Nextcloud Files as one Ogg Opus file, and
   appears in the Cassini entry in the Nextcloud app menu.

## What you get

- A transcript with the right name on every line, playable against the audio.
- One portable file per meeting, holding the audio, the transcript, the summary
  and any tags.
- Search, tags and insights across the meetings you can read.
- The same meetings through a CLI, for a script or an agent outside Nextcloud.

## Three things that are always true

- **Transcription never leaves your server.** Speech-to-text runs in-process
  with sherpa-onnx and NVIDIA Parakeet models. When you switch a language model
  on, the transcript text goes to that endpoint — that step, and only that step,
  sends anything anywhere.
- **Speaker labels come from signalling.** One audio stream per participant,
  with the name Talk sent. Nothing is inferred from the sound of a voice.
- **Nextcloud decides who can see a recording.** Recordings are ordinary files
  in Nextcloud Files, under one of two audiences an administrator picks.

Cassini records audio, not video, and it works after the call rather than
during it: there is no live transcription and there are no live captions.

## Where to go next

- [Install on Nextcloud](/docs/getting-started/install) — the production install:
  deploy daemon, registration, the Talk handoff.
- [Who can see a recording](/docs/guides/who-can-see-a-recording) — the two
  audiences and how to switch between them.
- [AI providers, summaries and insights](/docs/guides/ai-providers) — what to
  configure, and what leaves your deployment when you do.
- [Privacy and data processing](/docs/guides/privacy) — the full note for an
  administrator deciding whether Cassini is acceptable on their instance.
