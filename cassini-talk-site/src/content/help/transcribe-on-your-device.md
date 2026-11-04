---
title: Transcribe on your device
description: cassini.talk writes a recording down on the device that made it, without a network and without sending anything anywhere.
section: Transcription
updated: 2026-11-02
appliesTo: cassini.talk 0.1.0 for iPhone and iPad
---

cassini.talk writes a recording down on the device that made it. The speech model runs on your iPhone or iPad, so transcription works without a network and nothing is sent anywhere.

## Before you start

The first transcription needs the speech model, and cassini.talk asks before it downloads anything.

- Parakeet Ultra, its speech detector and the speaker models are a one-time download of about 531 MB from Cassini’s model host, `dist.gocassini.com`.
- On a device without the memory for Parakeet Ultra, cassini.talk uses Apple Speech instead, after a download for the language you choose.

> [!WARNING]
> **Use Wi-Fi for the first download.** The model is about 531 MB. You can cancel it in Settings › On-device models and start it again later.

## Transcribe a recording

1. Finish the recording. With **Transcribe after recording or import** turned on in Settings › Transcription, cassini.talk starts straight away.
2. Otherwise, open the note and tap **Transcribe**.
3. Watch the progress card: it shows each stage, the percent, the time left and the words so far.

Transcription carries on in the background. A notification tells you when the note is done, or if iOS paused it.

## Stop and keep progress

Progress is saved every 30 seconds of audio. Tap **Stop and keep progress** to pause a long transcription. It resumes from the last checkpoint, as it does after a crash or when iOS takes the background time back.

## Change the speech language

On a device that uses Apple Speech, choose the language in **Settings › Transcription › Speech language**. A new language applies to the next transcription. Parakeet Ultra needs no setting: it recognises its 25 languages by itself.

## If something goes wrong

| You see | What to do |
|---|---|
| **All words shown as one voice** | Speakers couldn’t be told apart in this recording. Check that **Identify speakers after transcription** is on, then tap **Transcribe again**. |
| **Telling speakers apart: Not available** | The speaker models aren’t on this device yet. Download them in Settings › On-device models. |
| A notification says iOS paused the note | Open cassini.talk. It picks up from the last checkpoint. |

## Related

- [Name a speaker](/help): give a voice a name across a whole note.
- [What stays on your device](/help): what cassini.talk stores, and what it never uploads.
