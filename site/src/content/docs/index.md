---
title: Welcome to Cassini
description: Cassini records Nextcloud Talk meetings into a single portable file you can replay anywhere.
---

Cassini is a CLI tool that records Nextcloud Talk meetings and packages them into a single portable `.opus` file. Point it at a room, let it run, and walk away with everything in one place.

## What you get

- **One file** — a self-contained `.opus` archive with the recording, transcript, and metadata
- **Resumable** — if something interrupts a recording, rerunning the same command picks up where it left off
- **Offline replay** — the Cassini web viewer opens `.opus` files directly in your browser, no server needed

## How it works

```
Nextcloud Talk room  →  cassini record  →  meeting.opus  →  cassini viewer
```

Cassini handles capture, processing, and packaging automatically. The `.opus` file is the only artifact you need to keep.

