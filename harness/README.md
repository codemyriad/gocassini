# Local Talk Test Harness (`harness/`)

If you are using Cassini as a product from this repo checkout, start from the
repo root and use the harness through `./bin/cassini dev ...` rather than
calling harness scripts first.

## Purpose

This directory is the reproducible E2E lab for Nextcloud Talk/Spreed testing:

- start/stop a local Talk stack with Docker Compose
- create rooms quickly
- run player scenarios and recorder roundtrips
- prepare deterministic media inputs for sync validation
- generate meeting-like spoken fixtures for transcription and player validation

All generated media/runtime artifacts stay outside git-tracked content.

Unless noted otherwise, command snippets below assume you are running from
`harness/`.

Preferred product-facing entry points:

- `../bin/cassini dev stack ...`
- `../bin/cassini dev room create`
- `../bin/cassini dev fixture prepare-showcase`
- `../bin/cassini dev player ...`
- `../bin/cassini dev smoke`

The scripts in `harness/bin/` remain the underlying lab implementation and local
stack internals.

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
  - `stream-synthetic-meeting.sh`: play the synthetic meeting fixture with realistic names and join delays
  - `stream-video.sh`: basic player flow (one or more clients) using Go rotator + local sample assets
  - `roundtrip-synthetic-meeting.sh`: record a real Talk meeting MKV, then build the transcriber + publisher + viewer artifact bundle
  - `stream-three-songs.sh`: 3-client synchronized player flow (Go rotator)
  - `stream-three-songs-until.sh`: retrying loop for continuous cloud/local soak until a wall-clock time
  - `record-three-songs.sh`: Go recorder + 3-client stream capture in one command
  - `verify-sync-from-report.sh`: compare final MKV stream start offsets against recorder report expectations
  - `verify-av-drift.sh`: compare paired audio/video elapsed time in a final MKV
  - `verify-session-artifact.sh`: validate session artifact structure beside a final MKV
  - `smoke.sh`: end-to-end smoke run
- `go-talk-rotator/`: Go room player used by the streaming scenarios
- `media/`: test inputs (`raw/`, `aligned/`, `webrtc/`) (gitignored except placeholders)
- `runtime/`: runtime state (`last_call_url`, `last_room_token`, logs)

## Quickstart

```bash
../bin/cassini dev stack plan
../bin/cassini dev stack up
CALL_URL="$(../bin/cassini dev room create --name "Local smoke room" | tail -n1)"
../bin/cassini dev player video --call-url "$CALL_URL" --duration 20
```

`stack up` is non-destructive by default: if harness containers, volumes, or
networks already exist, it fails with the next safe command to run. Use
`../bin/cassini dev stack up --resume` for matching stopped resources,
`../bin/cassini dev stack up --reset` to recreate the resolved stack, and
`../bin/cassini dev stack stop --full` for complete harness cleanup.

One-command smoke test:

```bash
../bin/cassini dev smoke
```

## Stack configuration modes

The product-facing setup surface is `cassini dev stack`:

```bash
../bin/cassini dev stack plan   # print resolved config only
../bin/cassini dev stack up     # start/bootstrap the resolved config
../bin/cassini dev stack status
../bin/cassini dev stack stop   # stop current resolved stack
../bin/cassini dev stack stop --full
```

Important flags:

- `--services legacy-default|core|appapi|full|full-remote`
- `--cassini none|installed-exapp` (default: `none`)
- `--recording-backend legacy|direct-operator|installed-exapp|none`
- `--exapp-image-mode build|reuse-local|pull` or `--build`
- `--patch=auto|none|force`

Installed ExApp setup remains opt-in. A production-shaped local Talk run looks
like:

```bash
../bin/cassini dev stack up \
  --services full \
  --cassini installed-exapp \
  --recording-backend installed-exapp \
  --build
```

For direct recorder/operator debugging without an installed ExApp:

```bash
../bin/cassini dev stack up \
  --services full \
  --cassini none \
  --recording-backend direct-operator
```

## Remote HTTPS / Tailscale browser harness

For a browser on another machine, serve Nextcloud and signaling through a
trusted HTTPS origin (for example Tailscale Serve) and make the harness render
remote-safe signaling, Janus, and TURN config:

```bash
TS_FQDN="$(tailscale status --self --json | jq -r '.Self.DNSName | sub("\\.$"; "")')"
TS_IP="$(tailscale ip -4)"

sudo tailscale serve --bg --https=443 http://127.0.0.1:28080
sudo tailscale serve --bg --https=8443 http://127.0.0.1:28082

../bin/cassini dev stack up \
  --public-mode remote-https \
  --public-url "https://${TS_FQDN}" \
  --public-host "${TS_FQDN}" \
  --media-host "${TS_IP}" \
  --services full-remote
```

Remote mode is explicit. Setting `CASSINI_HARNESS_PUBLIC_URL`,
`CASSINI_HARNESS_PUBLIC_HOST`, `CASSINI_HARNESS_MEDIA_HOST`, or
`CASSINI_HARNESS_SIGNALING_PUBLIC_URL` while public mode is still `local-http`
fails validation instead of silently switching modes. Explicit remote mode also
starts an internal Docker-network HTTPS helper for Nextcloud's server-side
signaling notifications on hardened hosts where containers cannot hairpin to
host ports; the Mac browser still connects to the real host signaling service
through Tailscale Serve.

## D-283 internal HPB proof

The harness now carries a standalone signaling `internalsecret` in:

- `config/signaling.conf`
- `bin/common.sh` as `SIGNALING_INTERNAL_SECRET`

If you change `config/signaling.conf`, restart the `signaling` service before
running the proof path.

Minimal proof flow from the repo root:

```bash
source harness/bin/common.sh

docker compose -p spreedtest -f harness/compose.yml up -d nextcloud signaling
# Optional: export HARNESS_SIGNALING_HOST=signaling.localhost if your harness
# needs a shared host/container alias instead of the default gateway URL.
./harness/bin/bootstrap.sh
CALL_URL="$(./harness/bin/create-room.sh --name "D-283 internal proof" | tail -n1)"
./bin/cassini dev player video --call-url "$CALL_URL" --duration 20 &
PLAYER_PID=$!

CASSINI_TALK_RECORDING_SECRET="$CASSINI_TALK_RECORDING_SECRET" \
CASSINI_TALK_SIGNALING_INTERNAL_SECRET="$SIGNALING_INTERNAL_SECRET" \
./bin/cassini record --call "$CALL_URL" --duration 15 --out /tmp/d283-internal.run

wait "$PLAYER_PID"
```

Notes:

- `cassini record` now defaults to `hpb-internal` mode for Talk recordings.
- use `--talk-auth-mode guest-participant` only when you intentionally want the legacy fallback path.
- if the run ends with `no remuxable streams`, treat that as a media-routing / runtime acceptance issue, not necessarily an auth/bootstrap failure.

Focused debug checklist for internal-mode failures:

1. confirm `CASSINI_TALK_RECORDING_SECRET` is set
2. confirm `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` matches signaling `internalsecret`
3. confirm Talk signaling settings advertise a reachable standalone signaling URL for both Nextcloud and the recorder
4. if `hello` fails with `invalid_client_type`, the signaling server did not pick up `internalsecret`
5. if `hello` fails with `invalid_token` / `auth_failed`, the internal secret does not match
6. if join succeeds but no streams arrive, inspect the room `join` / participants events, subscriber creation, and `requestoffer` seam

## Showcase Meeting

The preferred demo and cleanup-evaluation sample is the showcase meeting:

```bash
../bin/cassini dev fixture prepare-showcase
CALL_URL="$(../bin/cassini dev room create --name "Lantern Festival Demo" | tail -n1)"
../bin/cassini dev player showcase --call-url "$CALL_URL"
```

Roundtrip it end to end with:

```bash
../cassini-lab/bin/roundtrip-showcase-meeting.sh \
  --call-url "https://cloud.example.com/call/<ROOM_TOKEN>"
```

That showcase scenario is still synthetic, but it is written more like a real
meeting and is the better sample for judging transcript cleanup quality.

## Harness Fixture

The original synthetic fixture remains useful for harness coverage. It gives us:

- spoken language instead of synthetic sine audio
- stable participant names and join delays
- overlaps, abbreviations, dates, filenames, and code terms
- a repeatable reference transcript for transcriber tuning

Recommended local setup:

```bash
uv run --python 3.12 --with-requirements harness/requirements-tts.txt python --version
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
./bin/prepare-synthetic-meeting.sh
```

This writes a scenario-driven generated media set under
`harness/media/processed/synthetic-pied-piper-v1/`, including:

- one media prefix per participant (`.mp4`, `.ivf`, `.ogg`)
- `manifest.json`
- `reference.txt`

The tracked inputs for this flow live in `harness/scenarios/` plus the generator
scripts; the rendered media stays gitignored.

The current default scenario is intentionally still harness-oriented. It is
good for repeatable timing, joins, and transcript plumbing, but it is not the
best demo or cleanup-evaluation sample.

Play it into a room:

```bash
CALL_URL="$(./bin/create-room.sh --name "Synthetic Pied Piper Review" | tail -n1)"
./bin/stream-synthetic-meeting.sh --call-url "$CALL_URL"
```

The current default scenario is a six-person Pied Piper review. The player
uses the scenario join delays both for room entry and for media timeline
alignment, so late-join playback lands on the intended absolute meeting time
instead of being delayed twice.

Run the full cloud/local roundtrip in one command:

```bash
./bin/roundtrip-synthetic-meeting.sh \
  --call-url "https://cloud.example.com/call/<ROOM_TOKEN>"
```

That flow will:

- generate or reuse the synthetic meeting media
- publish it into the real Talk room
- record the meeting into one MKV with the actual Go recorder
- run `cassini-publisher/bin/process-meeting.sh --bundle-viewer`
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

Then `stream-three-songs.sh` starts 3 players with staggered joins and rotates audio
audibility every 5 seconds by muting at sender packet emission time, so muted tracks stop
sending RTP audio packets.

Default display names:

- `Le Vibrazioni - Giulia`
- `Elio e le Storie Tese - Spalman`
- `Frankie Hi-NRG MC - Chiedi Chiedi`

```bash
../cassini-lab/bin/up.sh
CALL_URL="$(../cassini-lab/bin/create-room.sh --name "3-song room" | tail -n1)"
../cassini-player/bin/stream-three-songs.sh --call-url "$CALL_URL"
```

Join delay override:

```bash
../cassini-player/bin/stream-three-songs.sh \
  --call-url "$CALL_URL" \
  --join-delay-giulia 4 \
  --join-delay-vibrazioni 6 \
  --join-delay-frankie 11
```

Audio-ready override (useful when aligned media includes initial silence padding):

```bash
../cassini-player/bin/stream-three-songs.sh \
  --call-url "$CALL_URL" \
  --audio-ready-after-giulia 7 \
  --audio-ready-after-vibrazioni 0 \
  --audio-ready-after-frankie 5
```

Custom labels:

```bash
../cassini-player/bin/stream-three-songs.sh \
  --call-url "$CALL_URL" \
  --name-spalman "Elio e le Storie Tese - Spalman"
```

Prep only:

```bash
./bin/prepare-youtube-set.sh
# force redownload/rebuild if needed
./bin/prepare-youtube-set.sh --force
```

Cloud room test:

```bash
cd test
../cassini-player/bin/stream-three-songs.sh \
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
- recorder/player logs (`/tmp/three-songs.mkv.recorder.log`, `/tmp/three-songs.mkv.publisher.log`)

The MKV is now the primary meeting artifact and carries Cassini metadata inside
the container itself. Add `--write-report` to the recorder invocation if you
also want the legacy external JSON sidecar for debug/export workflows.
If you explicitly need a separate compatibility archive path, pass
`--archive-output /tmp/three-songs.csr`.

Run sync validation manually:

```bash
# requires the legacy sidecar; run recorder with --write-report first
../cassini-diagnostics/bin/verify-sync-from-report.sh \
  --recording /tmp/three-songs.mkv \
  --report /tmp/three-songs.mkv.json \
  --tolerance 0.35
```

## Service modes

The legacy default still maps to the historical full-media harness behavior.
For new flows prefer `cassini dev stack --services ...`:

```bash
# Full media path: Janus + signaling + coturn + nats
../bin/cassini dev stack up --services full

# Base services only: Nextcloud + Postgres
../bin/cassini dev stack up --services core
```

## CI integration smoke

Use `../cassini-lab/bin/ci-e2e.sh` for the baseline full Nextcloud + recorder + player run used by GitHub Actions.

```bash
../cassini-lab/bin/ci-e2e.sh
```

## D-263 Nextcloud Recording Lifecycle

Use `harness/bin/d263-nextcloud-lifecycle.sh` to test the native Talk
recording-backend lifecycle against a real local Nextcloud/Talk stack without
requiring real browser media capture.

The script:

- starts a temporary `cassini-operator` on port `14000`
- configures Talk's recording backend to call that operator
- creates and activates a Talk room
- starts recording through Nextcloud's Talk recording API
- uses a fake `cassini` executable only for the media worker
- stops recording through Nextcloud's Talk recording API
- waits for the operator job to reach `done/succeeded`

Run it after the harness stack is up:

```bash
harness/bin/up.sh
harness/bin/d263-nextcloud-lifecycle.sh
```

Useful overrides:

```bash
OPERATOR_PORT=14001 harness/bin/d263-nextcloud-lifecycle.sh
KEEP_RUNTIME=1 harness/bin/d263-nextcloud-lifecycle.sh
CASSINI_OPERATOR_BIN=/abs/path/to/cassini-operator harness/bin/d263-nextcloud-lifecycle.sh
```

This is not the full media acceptance test. It validates the Nextcloud/Talk
start/stop/backend lifecycle and operator pipeline handoff. Browser media,
HPS/WebRTC packet capture, remux, and Cassini viewer playback still belong to
the manual full-media path.

### CI integration scenarios

The repository runs one curated suite-level CI entry point plus two additional
harness-specific variants:

- `../cassini-lab/bin/ci-e2e.sh` (baseline single-player flow)
- `./bin/ci-e2e-mute.sh` (mute-aware 3-player flow)
- `./bin/ci-e2e-rejoin.sh` (leave/rejoin flow with two player phases)

Scenario assertions are intentionally artifact-centric:

- `ci-e2e-mute.sh` requires only at least one final video/audio track, then validates
  multi-player capture via session artifact stream counts and player mute logs.
- `ci-e2e-rejoin.sh` validates both player phases plus recorder evidence of two
  distinct remote session subscriptions (instead of requiring `session_outputs >= 2`,
  which is brittle under merged artifact-remux output).
- `ci-e2e-rejoin.sh` does not fail solely on missing final video in a flaky run; it
  treats recorder/subscription evidence and session artifacts as the primary signal.

All scenarios use the local Compose stack in this repository:

- Nextcloud API at `http://127.0.0.1:28080`
- Signaling server at `http://127.0.0.1:28082`

You can run them locally the same way as CI:

```bash
../cassini-lab/bin/ci-e2e.sh
./bin/ci-e2e-mute.sh
./bin/ci-e2e-rejoin.sh
```

Both CI entrypoints use bounded retry when creating the temporary Talk room to
handle transient OCS/API bootstrap races in freshly-started local stacks.
`bootstrap.sh` also auto-resolves a container-reachable signaling URL for local
Docker runs (gateway address instead of host-loopback).
`cassini-go-recorder/e2e_with_publisher.sh` also verifies that
`gocassini-remux` can rebuild an artifact-based MKV from `session.json`.

## Teardown

```bash
../cassini-lab/bin/down.sh            # keep volumes
../cassini-lab/bin/down.sh --volumes  # remove volumes
```
