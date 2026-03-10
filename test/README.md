# Local Talk Test Harness (`test/`)

## Purpose

This directory is the reproducible E2E harness for Nextcloud Talk/Spreed testing:

- start/stop a local Talk stack with Docker Compose
- create rooms quickly
- run publisher tests (single stream and 3-stream sync test)
- prepare deterministic media inputs for sync validation
- generate meeting-like spoken fixtures for transcription and player validation

All generated media/runtime artifacts stay outside git-tracked content.

## Structure

- `compose.yml`: local stack (Nextcloud + Postgres + optional full WebRTC services)
- `config/`: signaling, Janus, and Coturn config used by `compose.yml`
- `bin/`: operator scripts
  - `up.sh`, `down.sh`, `status.sh`: stack lifecycle
  - `bootstrap.sh`: Nextcloud/Talk bootstrap and app settings
  - `create-room.sh`: create Talk room and print call URL
  - `prepare-media.sh`: small local sample media assets (`.mp4` + `.ivf` + `.ogg` + `.ulaw`)
  - `prepare-synthetic-meeting.sh`: meeting-like multi-speaker media fixture generation
  - `prepare-synthetic-meeting.py`: TTS-backed generator used by the shell wrapper
  - `prepare-youtube-set.sh`: YouTube download + alignment + WebRTC transcode
  - `stream-synthetic-meeting.sh`: publish the synthetic meeting fixture with realistic names and join delays
  - `stream-video.sh`: basic publisher flow (one or more clients) using Go rotator + local sample assets
  - `roundtrip-synthetic-meeting.sh`: record a real Talk meeting MKV, then build the transcriber + viewer artifact bundle
  - `stream-three-songs.sh`: 3-client synchronized publisher flow (Go rotator)
  - `stream-three-songs-until.sh`: retrying loop for continuous cloud/local soak until a wall-clock time
  - `record-three-songs.sh`: Go recorder + 3-client stream capture in one command
  - `verify-sync-from-report.sh`: compare final MKV stream start offsets against recorder report expectations
  - `smoke.sh`: end-to-end smoke run
- `go-talk-rotator/`: Go publisher used by `stream-three-songs.sh`
- `media/`: test inputs (`raw/`, `aligned/`, `webrtc/`) (gitignored except placeholders)
- `runtime/`: runtime state (`last_call_url`, `last_room_token`, logs)

## Quickstart

```bash
cd test
./bin/up.sh
CALL_URL="$(./bin/create-room.sh --name "Local smoke room" | tail -n1)"
./bin/stream-video.sh --call-url "$CALL_URL" --duration 20
```

One-command smoke test:

```bash
cd test
./bin/smoke.sh
```

## Synthetic Meeting Fixture

The synthetic meeting fixture is intended to feel closer to a real engineering
call than the old bars-and-tone sample. It gives us:

- spoken language instead of synthetic sine audio
- stable participant names and join delays
- overlaps, abbreviations, dates, filenames, and code terms
- a repeatable reference transcript for transcriber tuning

Recommended local setup:

```bash
uv run --python 3.12 --with-requirements test/requirements-tts.txt python --version
```

The wrappers default to `uv run --python 3.12`, so they can provision a
compatible interpreter on demand. The realistic backend uses `kokoro-onnx`
under the hood and caches its model files on first run under
`$XDG_CACHE_HOME/gocassini/kokoro-onnx` (or `~/.cache/gocassini/kokoro-onnx`).
Override the Python version if needed:

```bash
UV_PYTHON=3.12 ./bin/prepare-synthetic-meeting.sh
```

If you want to force a specific preinstalled interpreter instead:

```bash
PYTHON_BIN=/path/to/python3.12 ./bin/prepare-synthetic-meeting.sh
```

Generate the fixture only:

```bash
cd test
./bin/prepare-synthetic-meeting.sh
```

This writes a scenario-driven generated media set under
`test/media/processed/synthetic-pied-piper-v1/`, including:

- one media prefix per participant (`.mp4`, `.ivf`, `.ogg`)
- `manifest.json`
- `reference.txt`

The tracked inputs for this flow live in `test/scenarios/` plus the generator
scripts; the rendered media stays gitignored.

Publish it into a room:

```bash
cd test
CALL_URL="$(./bin/create-room.sh --name "Synthetic Pied Piper Review" | tail -n1)"
./bin/stream-synthetic-meeting.sh --call-url "$CALL_URL"
```

The current default scenario is a six-person Pied Piper review. The publisher
uses the scenario join delays both for room entry and for media timeline
alignment, so late-join playback lands on the intended absolute meeting time
instead of being delayed twice.

Run the full cloud/local roundtrip in one command:

```bash
cd test
./bin/roundtrip-synthetic-meeting.sh \
  --call-url "https://cloud.example.com/call/<ROOM_TOKEN>"
```

That flow will:

- generate or reuse the synthetic meeting media
- publish it into the real Talk room
- record the meeting into one MKV with the actual Go recorder
- run `cassini-transcriber/bin/process-meeting.sh --bundle-viewer`
- leave you with `meeting.webm`, `transcript.words.v1.json`, `captions.vtt`,
  `manifest.json`, and a static viewer bundle rooted at `index.html` with
  `catalog.json` plus `meetings/<meeting-id>/...` artifact files

If you want to test the plumbing without installing the TTS model yet:

```bash
./bin/prepare-synthetic-meeting.sh --backend mock --force
```

That mock path uses only the lightweight core requirements, so it stays fast
even when the full Kokoro stack is not installed yet.

## Three-Stream Sync Test (Go)

The 3-stream test uses full-length songs with alignment by start-delay padding (no trimming):

- `Giulia 2:09 == Vibrazioni 2:16`
- `Giulia 0:42 == Frankie 0:44`

`prepare-youtube-set.sh` pipeline:

1. download sources with `uvx yt-dlp` (skips download if file already exists)
2. render aligned full-length MP4 files
3. transcode aligned files to WebRTC-friendly VP8/Opus files (`.ivf` + `.ogg`)

Then `stream-three-songs.sh` starts 3 publishers with staggered joins and rotates audio
audibility every 5 seconds by muting at sender packet emission time, so muted tracks stop
sending RTP audio packets.

Default display names:

- `Le Vibrazioni - Giulia`
- `Elio e le Storie Tese - Spalman`
- `Frankie Hi-NRG MC - Chiedi Chiedi`

```bash
cd test
./bin/up.sh
CALL_URL="$(./bin/create-room.sh --name "3-song room" | tail -n1)"
./bin/stream-three-songs.sh --call-url "$CALL_URL"
```

Join delay override:

```bash
./bin/stream-three-songs.sh \
  --call-url "$CALL_URL" \
  --join-delay-giulia 4 \
  --join-delay-vibrazioni 6 \
  --join-delay-frankie 11
```

Audio-ready override (useful when aligned media includes initial silence padding):

```bash
./bin/stream-three-songs.sh \
  --call-url "$CALL_URL" \
  --audio-ready-after-giulia 7 \
  --audio-ready-after-vibrazioni 0 \
  --audio-ready-after-frankie 5
```

Custom labels:

```bash
./bin/stream-three-songs.sh \
  --call-url "$CALL_URL" \
  --name-spalman "Elio e le Storie Tese - Spalman"
```

Prep only:

```bash
cd test
./bin/prepare-youtube-set.sh
# force redownload/rebuild if needed
./bin/prepare-youtube-set.sh --force
```

Cloud room test:

```bash
cd test
./bin/stream-three-songs.sh \
  --call-url "https://cloud.example.com/call/<ROOM_TOKEN>" \
  --skip-prepare
```

Continuous retry loop (for on/off manual validation windows):

```bash
cd test
./bin/stream-three-songs-until.sh \
  --call-url "https://cloud.example.com/call/<ROOM_TOKEN>" \
  --until "08:00" \
  --skip-prepare
```

You can also pass an absolute stop time:

```bash
./bin/stream-three-songs-until.sh \
  --call-url "https://cloud.example.com/call/<ROOM_TOKEN>" \
  --until "2026-03-03 08:00" \
  --skip-prepare
```

Recorder + capture in one command:

```bash
cd test
CALL_URL="$(./bin/create-room.sh --name "3-song-recorded" | tail -n1)"
./bin/record-three-songs.sh \
  --call-url "$CALL_URL" \
  --duration 180 \
  --skip-prepare \
  --output /tmp/three-songs.mkv
```

This generates:

- raw recorder output MKV (`/tmp/three-songs.mkv`)
- session artifact directory under `/tmp/sessions/<meeting_id>/`
- sync validation output (`verify-sync-from-report.sh`, auto-run unless `--skip-sync-check`)
- recorder/publisher logs (`/tmp/three-songs.mkv.recorder.log`, `/tmp/three-songs.mkv.publisher.log`)

The MKV is now the primary meeting artifact and carries Cassini metadata inside
the container itself. Add `--write-report` to the recorder invocation if you
also want the legacy external JSON sidecar for debug/export workflows.
If you explicitly need a separate compatibility archive path, pass
`--archive-output /tmp/three-songs.csr`.

Run sync validation manually:

```bash
# requires the legacy sidecar; run recorder with --write-report first
./bin/verify-sync-from-report.sh \
  --recording /tmp/three-songs.mkv \
  --report /tmp/three-songs.mkv.json \
  --tolerance 0.35
```

## Compose Profiles

Default is `SPREED_PROFILE=full`.

```bash
# Full media path (default): Janus + signaling + coturn + nats
SPREED_PROFILE=full ./bin/up.sh

# Base services only: Nextcloud + Postgres
SPREED_PROFILE=base ./bin/up.sh
```

## CI integration smoke

Use `./bin/ci-e2e.sh` for the full Nextcloud + recorder + publisher run used by GitHub Actions.

```bash
./bin/ci-e2e.sh
```

### CI integration scenarios

The repository runs three local integration scripts in GitHub Actions:

- `./test/bin/ci-e2e.sh` (baseline single publisher)
- `./test/bin/ci-e2e-mute.sh` (mute-aware 3 publisher flow)
- `./test/bin/ci-e2e-rejoin.sh` (leave/rejoin flow with two publisher phases)

Scenario assertions are intentionally artifact-centric:

- `ci-e2e-mute.sh` requires only at least one final video/audio track, then validates
  multi-publisher capture via session artifact stream counts and publisher mute logs.
- `ci-e2e-rejoin.sh` validates both publisher phases plus recorder evidence of two
  distinct remote session subscriptions (instead of requiring `session_outputs >= 2`,
  which is brittle under merged artifact-remux output).
- `ci-e2e-rejoin.sh` does not fail solely on missing final video in a flaky run; it
  treats recorder/subscription evidence and session artifacts as the primary signal.

All scenarios use the local Compose stack in this repository:

- Nextcloud API at `http://127.0.0.1:28080`
- Signaling server at `http://127.0.0.1:28082`

You can run them locally the same way as CI:

```bash
./test/bin/ci-e2e.sh
./test/bin/ci-e2e-mute.sh
./test/bin/ci-e2e-rejoin.sh
```

Both CI entrypoints use bounded retry when creating the temporary Talk room to
handle transient OCS/API bootstrap races in freshly-started local stacks.
`bootstrap.sh` also auto-resolves a container-reachable signaling URL for local
Docker runs (gateway address instead of host-loopback).
`cassini-go-recorder/e2e_with_publisher.sh` also verifies that
`gocassini-remux` can rebuild an artifact-based MKV from `session.json`.

## Teardown

```bash
cd test
./bin/down.sh            # keep volumes
./bin/down.sh --volumes  # remove volumes
```
