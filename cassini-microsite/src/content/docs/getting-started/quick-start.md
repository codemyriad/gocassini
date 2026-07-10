---
title: Quick Start
description: Get a Cassini recording in under five minutes.
---

This guide walks you from a fresh checkout to your first recorded meeting.

## Prerequisites

- Go 1.22 or later
- Network access to a Nextcloud instance with Talk enabled
- A Talk room URL (e.g. `https://cloud.example.com/call/abc123`)

## Install

Clone the repo and use the wrapper script — it builds the CLI from source on first run:

```bash
git clone https://github.com/codemyriad/cassini.git
cd cassini
./bin/cassini --help
```

## Validate your environment

Before recording, check that everything is in order:

```bash
./bin/cassini doctor
```

If `doctor` reports an unwritable cache directory, point Cassini at one that works:

```bash
export CASSINI_CACHE_ROOT="$PWD/.cache/cassini"
./bin/cassini doctor
```

Fix any issues reported before continuing.

## Record a meeting

Set the room URL and run:

```bash
export CALL_URL="https://cloud.example.com/call/<ROOM_TOKEN>"
./bin/cassini record --call "$CALL_URL" --out "./meetings/2026-07-01 Team Sync.opus"
```

Cassini will:
1. Join the room and start recording
2. Process and transcribe the audio when the meeting ends
3. Write the final `.opus` file to the path you specified

Leave it running for the duration of the meeting. When you end the call, Cassini finishes processing and exits.

## Inspect the result

```bash
./bin/cassini inspect "./meetings/2026-07-01 Team Sync.opus"
```

This prints a summary: duration, participant count, transcript word count, and file size.

## View in the browser

```bash
./bin/cassini serve "./meetings"
```

Open the URL it prints. The Cassini viewer loads your `.opus` file and gives you a navigable, searchable replay.

## Try it without a real call

If you want to test without joining a live meeting, simulate mode writes a debug bundle:

```bash
./bin/cassini record --simulate --out ./runs/demo.run
./bin/cassini inspect ./runs/demo.run
```

## If something goes wrong

Cassini keeps resumable state in a `.cassini-work/` directory next to your output file. If a recording fails mid-way, fix the issue and rerun the exact same command — Cassini will reuse finished stages and skip straight to the failed step.
