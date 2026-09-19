---
title: The meeting file
description: Each published meeting is one ordinary .opus file that plays anywhere and carries its own transcript, summary and tags.
source: docs/portable-meeting-format.md
copied: "2026-09-17"
---

Each published meeting is one ordinary `.opus` file. Any audio player plays it.

The transcript, speaker names, summary and tags travel inside the same file, so
a meeting you copy off your server is still a complete meeting, and it stays
readable without Cassini. There is no database to keep beside it.

## What travels inside

- **The audio**, one Opus stream in an Ogg container.
- **The transcript**, with word-level timings, so a reader can follow the words
  against the audio.
- **The speaker names** Talk sent for each participant.
- **The summary**, when a language model wrote one.
- **Tags and marks** — a label such as `hiring` on the whole meeting or on a
  stretch of it, with the Nextcloud account that made each mark and whether a
  person or an agent made it.
- **Provenance and an integrity hash**, binding the transcript to this exact
  audio.

A conforming file declares `CASSINI_FORMAT=org.cassini.portable-meeting/1`. A
reader that does not recognise the tag treats the file as ordinary audio.

## What does not

- **Tag colours and icons.** They are stored for the installation, not inside
  the recordings, so recolouring or renaming a tag rewrites nothing. A file
  shared elsewhere carries the tag's label, not its appearance.
- **The room's display name.** A name is editable and a sealed recording is not,
  so the name lives in the archive's catalogue. The file carries the room's
  derived id.

Before you share one: the file carries who marked what and when, as the
Nextcloud user id of each mark's author, and it carries the speaker names.

## The specification

The format is an open specification at
[format.gocassini.com](https://format.gocassini.com). Read it if you are writing
something that produces or consumes these files.

What Cassini itself writes, including the manifest layout and the annotation
rules, is in
[`docs/portable-meeting-format.md`](https://github.com/codemyriad/gocassini/blob/main/docs/portable-meeting-format.md).

## Related

- [Agent access via the CLI](/docs/guides/agent-access) — download a meeting
  file with `cassini meetings fetch`, and read one with `cassini inspect`.
- [Who can see a recording](/docs/guides/who-can-see-a-recording) — who may
  download it in the first place.
