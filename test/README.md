# Local Talk Test Harness (`test/`)

## Purpose

This directory is the reproducible E2E harness for Nextcloud Talk/Spreed testing:

- start/stop a local Talk stack with Docker Compose
- create rooms quickly
- run publisher tests (single stream and 3-stream sync test)
- prepare deterministic media inputs for sync validation

All generated media/runtime artifacts stay outside git-tracked content.

## Structure

- `compose.yml`: local stack (Nextcloud + Postgres + optional full WebRTC services)
- `config/`: signaling, Janus, and Coturn config used by `compose.yml`
- `bin/`: operator scripts
  - `up.sh`, `down.sh`, `status.sh`: stack lifecycle
  - `bootstrap.sh`: Nextcloud/Talk bootstrap and app settings
  - `create-room.sh`: create Talk room and print call URL
  - `prepare-media.sh`: small local sample media assets (`.mp4` + `.ivf` + `.ogg` + `.ulaw`)
  - `prepare-youtube-set.sh`: YouTube download + alignment + WebRTC transcode
  - `stream-video.sh`: basic publisher flow (1-3 clients) using Go rotator + local sample assets
  - `stream-three-songs.sh`: 3-client synchronized publisher flow (Go rotator)
  - `stream-three-songs-until.sh`: retrying loop for continuous cloud/local soak until a wall-clock time
  - `record-three-songs.sh`: Go recorder + 3-client stream + composition in one command
  - `compose-recording.sh`: Rust/GStreamer compositor for recorder multi-stream MKV -> review MP4
  - `verify-sync-from-report.sh`: compare final MKV stream start offsets against recorder report expectations
  - `verify-audio-tail.sh`: fail if composed output audio ends too early or is silent in the last seconds
  - `ci-e2e-rotation.sh`: local-stack E2E with 3 rotating publishers and composed-tail-audio assertions
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

Recorder + composition in one command:

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
- raw recorder archive CSR (`/tmp/three-songs.csr`)
- recorder JSON report (`/tmp/three-songs.mkv.json`)
- composed review MP4 (`/tmp/three-songs.composed.mp4`)
- composed tail-audio validation (`verify-audio-tail.sh`, auto-run)
- sync validation output (`verify-sync-from-report.sh`, auto-run unless `--skip-sync-check`)
- recorder/publisher logs (`/tmp/three-songs.mkv.recorder.log`, `/tmp/three-songs.mkv.publisher.log`)

Run sync validation manually:

```bash
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
- `./test/bin/ci-e2e-rotation.sh` (3 rotating publishers with composed tail-audio checks)
- `./test/bin/ci-sync-composition.sh` (deterministic synthetic sync gate for multitrack -> composed output)

Scenario assertions are intentionally artifact-centric:

- `ci-e2e-mute.sh` requires only at least one final video/audio track, then validates
  multi-publisher capture via session artifact stream counts and publisher mute logs.
- `ci-e2e-rejoin.sh` validates both publisher phases plus recorder evidence of two
  distinct remote session subscriptions (instead of requiring `session_outputs >= 2`,
  which is brittle under merged artifact-remux output).
- `ci-e2e-rejoin.sh` does not fail solely on missing final video in a flaky run; it
  treats recorder/subscription evidence and session artifacts as the primary signal.
- `compose-recording.sh` is backed by `test/bin/compose-rs` (Rust) and defaults to
  low-res preview rendering (`640x360@6fps`) to keep turnaround fast during sync checks.
- The compositor uses bounded queues and process RSS guards to reduce memory-pressure
  risk on long sparse-track meetings.
- Known composition-stage failure mode (now fixed in `compose-rs`): branch-level
  `videorate` before `compositor` can stall when one participant video ends early.
  Framerate normalization is now applied only after the compositor stage.
- Audio sync path now uses FFmpeg `amix` in a post-compose mux step:
  GStreamer renders video-only (VAAPI), then FFmpeg mixes all source audio tracks
  and muxes audio+video. This removed the observed inter-track audio skew on the
  `daily-meeting--2026-03-04--12:36:53` fixture.

All scenarios use the local Compose stack in this repository:

- Nextcloud API at `http://127.0.0.1:28080`
- Signaling server at `http://127.0.0.1:28082`

You can run them locally the same way as CI:

```bash
./test/bin/ci-e2e.sh
./test/bin/ci-e2e-mute.sh
./test/bin/ci-e2e-rejoin.sh
./test/bin/ci-e2e-rotation.sh
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
