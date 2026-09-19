---
title: Introduction
description: Cassini is a recording backend for Nextcloud Talk that records, transcribes and publishes each meeting as one file on your own server.
source: docs/README.md
copied: "2026-09-17"
---

> [!WARNING]
> **Cassini is in beta.** We run it daily on our own Nextcloud, but expect rough edges. Read the [changelog](https://github.com/codemyriad/gocassini/blob/main/CHANGELOG.md) before you update, and please [open an issue](https://github.com/codemyriad/gocassini/issues) if something breaks.

Cassini is a recording backend for Nextcloud Talk that puts you in control of
your meeting data. It installs as a Nextcloud app, shows up as a new app icon
inside your Nextcloud suite, and takes over the Record button your people
already have. Meetings are where decisions get made, and they are easily lost
unless someone writes them down afterwards.

## When you press Record

Cassini works with any Nextcloud Talk room, group calls and 1:1 calls. Just
press _Record_ and Cassini is listening.

1. Cassini joins the call as an internal client of the signalling server, not as
   a participant, so nobody sees an extra person in the room.
2. Each participant arrives as their own audio stream, carrying the name Talk
   sent with it.
3. When the call ends, Cassini transcribes the audio on the hardware you gave
   it, in-process, with sherpa-onnx and NVIDIA Parakeet models.
4. The meeting is published into Nextcloud Files as one `.opus` file, with a
   summary if you have configured a language model.

## What you get

- Transcripts and synced audio playback with speaker IDs.
- Access control scoped to the room: a published meeting is readable by that
  room's participants and no one else, using Nextcloud Files permissions.
- One portable meeting file per meeting that can be opened without Cassini.
- Search and tags across meetings.
- In-app AI providers for per-meeting summaries and cross-meeting insights.
- A CLI and an agent skill, to build workflows with an external harness.

## Limitations

- **No live transcription or captions.** Transcription starts when the call
  ends, so it never competes with the call for resources.
- **Audio only.** Cassini records the video streams, but the meeting file, the
  transcript and the viewer are audio only for now.

## What leaves your server

Recording and transcription run on your own hardware. No audio and no transcript
leaves the host for those steps.

If you configure a language model, transcript text goes to it in two cases:
automatically, to summarise each meeting, and on request, when someone asks a
question about meetings they have access to.

There is no telemetry.

## Where next

- [Install on Nextcloud](/docs/getting-started/install) — the requirements and
  the five install steps in full.
- [Who can see a recording](/docs/guides/who-can-see-a-recording) — the two
  audiences, and how to switch.
- [AI providers, summaries and insights](/docs/guides/ai-providers) — what to
  configure, and what a model then sees.
- [The meeting file](/docs/guides/meeting-file) — what is inside the `.opus`.
- [Agent access via the CLI](/docs/guides/agent-access) — reading meetings from
  outside Nextcloud.
