---
title: The meeting file
description: Each published meeting is one Ogg Opus file that plays anywhere and carries its own transcript, summary and tags.
source: docs/portable-meeting-format.md
copied: "2026-09-17"
---

Cassini publishes each meeting as one file. It is an ordinary Ogg Opus file, so
it plays in any audio software, and it also carries everything Cassini knows
about the meeting in its metadata header. Downloading a meeting is downloading
the whole meeting; there is no database to keep beside it and no central app
needed to read it.

## What the file carries

- **The audio**, one Opus stream in an Ogg container.
- **The transcript**, with word-level timings, so a reader can follow the words
  against the audio.
- **The speaker roster**, with the names Talk sent for each participant.
- **The summary**, when a language model wrote one.
- **Tags and marks** — a label such as `hiring` on the whole meeting or on a
  stretch of it, with the Nextcloud account that made each mark and whether a
  person or an agent made it.
- **Provenance and an integrity hash**, binding the transcript to this exact
  audio.

A conforming file declares `CASSINI_FORMAT=org.cassini.portable-meeting/1`. A
reader that does not recognise the tag treats the file as ordinary audio; a
reader that recognises it but cannot make sense of the version or layout is
required to stop and say so rather than guess.

## What it does not carry

- **Tag colours and icons.** They are stored for the installation, not inside
  the recordings, so recolouring or renaming a tag rewrites nothing. A file
  shared elsewhere carries the tag's label, not its appearance.
- **The room's display name.** A name is editable and a sealed recording is not,
  so the name lives in the archive's catalogue. The file carries the room's
  derived id.

## Two things to know before sharing one

- **Marks travel with the file.** A recording shared outside Nextcloud carries
  who marked what and when, as the Nextcloud user id of each mark's author, and
  the tag labels people chose. The file already carries speaker names.
- **A reader that ignores annotations still works.** Nothing refuses a recording
  because of its tags, and a tool that rewrites other metadata carries them
  through unchanged.

## The specification

The format is published, versioned and independent of Cassini:
**<https://format.gocassini.com>**. Read it if you are writing something that
produces or consumes these files.

What Cassini itself writes, including the manifest layout and the annotation
rules, is in
[`docs/portable-meeting-format.md`](https://github.com/codemyriad/gocassini/blob/main/docs/portable-meeting-format.md).

## Related

- [Agent access via the CLI](/docs/guides/agent-access) — download a meeting
  file with `cassini meetings fetch`, and read one with `cassini inspect`.
- [Who can see a recording](/docs/guides/who-can-see-a-recording) — who may
  download it in the first place.
