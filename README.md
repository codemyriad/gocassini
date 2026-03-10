# Gocassini

`gocassini` is a CLI meeting recorder focused on one deterministic contract:

- input: Nextcloud Talk room
- artifact: one `*.mkv` output per meeting
- diagnostics: per-run session artifact directory (`session.json`, `events.ndjson`, `streams/*.rtplog`) next to the MKV; optional legacy external JSON run report via `--write-report`

The repository is intentionally CLI-first and automation-friendly.

## Get Up And Running

Use two terminals from repo root.

Set your call URL in each terminal (or source it from `.envrc`):

```bash
export CALL_URL="https://cloud.example.com/call/<ROOM_TOKEN>"
```

Terminal 1: start recorder

```bash
./cassini-recorder/bin/record-talk.sh \
  --call-url "$CALL_URL" \
  --name "CassiniRecorder" \
  --output /tmp/meeting.mkv
```

In talk mode, `--output` can point directly at the final `.mkv`. Keep `--final-output` only if you need a separate compatibility path.

By default, talk mode auto-terminates when all remote participants leave (with a 30s grace). Add `--duration <seconds>` only if you also want a hard cap.
`Ctrl-C` is handled gracefully so cleanup/remux/report still run before exit.

Terminal 2: start the Player with the showcase meeting

```bash
./cassini-player/bin/stream-showcase-meeting.sh \
  --call-url "$CALL_URL"
```

By default, the showcase fixture will prepare its media on demand the first
time you run it.

## Repository layout

- `cassini-recorder/`: Recorder and suite-level capture wrappers.
- `cassini-go-recorder/`: Go recorder implementation and lower-level native diagnostics.
- `cassini-diagnostics/`: Diagnostics and recovery wrappers for artifacts.
- `cassini-transcriber/`: Transcriber and readable-transcript generation.
- `cassini-readable/`: Standalone readable transcript cleanup wrappers.
- `cassini-player/`: Player and suite-level room-streaming wrappers.
- `cassini-lab/`: Local stack, fixtures, and validation wrappers.
- `cassini-publisher/`: Publisher and suite-level orchestration wrappers.
- `cassini-viewer/`: Viewer runtime for published meeting artifacts.
- `test/`: Lab implementation, fixtures, and E2E internals.
- `.github/workflows/ci.yml`: unit + integration CI.
- `media-kit.txt` / `media-kit.md`: brand direction and design principles.

## Suite model

The repo is organized as a small opinionated tool suite:

- `Recorder`: capture one live room into one meeting `.mkv`.
- `Transcriber`: turn a meeting recording into timed transcript artifacts.
- `Publisher`: package artifacts into a static meeting library.
- `Viewer`: render that library in the browser.
- `Player`: join rooms and stream deterministic media for tests and demos.
- `Lab`: local stack, fixture generation, roundtrip scripts, and validation.
- `Diagnostics`: inspect, verify, remux, and compatibility-recovery tools.

`Diagnostics` is intentionally a supporting surface, not the main product
boundary.

## Showcase Meeting

The repository now has two synthetic meeting tracks:

- the default `synthetic-pied-piper` fixture, which is still useful for harness
  coverage,
- the `showcase-lantern-festival` fixture, which is written to sound more like
  a natural meeting and is the better demo/cleanup example.

Generate the showcase fixture:

```bash
./cassini-lab/bin/prepare-showcase-meeting.sh
```

If you already have a room and want to play it live:

```bash
./cassini-player/bin/stream-showcase-meeting.sh \
  --call-url "https://cloud.example.com/call/<ROOM_TOKEN>"
```

## Local testing

### Unit tests

```bash
cd cassini-go-recorder
go test ./...
```

```bash
python3 -m unittest discover -s cassini-transcriber/tests -t cassini-transcriber
```

For the local MKV-to-audio+transcript path, see
`cassini-transcriber/README.md`. That README now includes:

- automatic backend selection (`http` vs local GPU vs local CPU),
- a Docker-based one-command runner,
- current benchmark numbers for the test meeting.

### Recorder smoke test (no real call)

```bash
./cassini-recorder/bin/simulate.sh --output /tmp/gocassini.csr
./cassini-diagnostics/bin/inspect-artifact.sh /tmp/gocassini.csr
```

### Debugging and architecture notes

- `cassini-go-recorder/docs/formats.md`: packet truth format and planned migration to stream-session layout.
- `cassini-go-recorder/docs/timelines.md`: timing model and drift handling strategy.
- `cassini-go-recorder/docs/muxing.md`: remux design and back-end strategy.

### Full local integration test (real Nextcloud stack)

```bash
cd /path/to/gocassini-repo-root
./cassini-lab/bin/ci-e2e.sh
```

This uses `test/compose.yml` and starts a local Nextcloud Talk stack, creates a room, runs player clients, and validates recorder outputs.

To validate A/V drift on a produced recording:

```bash
./cassini-diagnostics/bin/verify-av-drift.sh \
  --input /tmp/meeting.mkv
```

### Showcase roundtrip

Use the showcase meeting when you want a demo-quality end-to-end artifact:

```bash
./cassini-lab/bin/roundtrip-showcase-meeting.sh \
  --call-url "https://cloud.example.com/call/<ROOM_TOKEN>"
```

That flow generates the fixture if needed, plays it into the room, records the
meeting, and runs the publisher pipeline to produce the final artifact bundle.

### Local private config (`.envrc`)

For private values (for example a cloud `CALL_URL`), keep them outside git:

```bash
cd /path/to/gocassini-repo-root
cp .envrc.example .envrc
# edit .envrc with your real values
```

`.envrc` is gitignored.

### Live smoke in local stack

```bash
./cassini-lab/bin/up.sh
CALL_URL="$(./cassini-lab/bin/create-room.sh --name "Smoke room" | tail -n1)"
./cassini-player/bin/stream-video.sh --call-url "$CALL_URL" --duration 20
./cassini-lab/bin/down.sh --volumes
```

## Deploying the recorder service to a server

### 1) DNS and external dependencies

Create DNS records first:

- `A` record for the target host, for example `gocassini.example.com`.
- Ensure TLS termination is in place if you will expose HTTPS endpoints.
- Ensure recorder host can resolve and reach Nextcloud Talk URLs and TURN/Signaling endpoints.

### 2) Build and install on the server

```bash
ssh deployer@<server>
sudo mkdir -p /opt/gocassini
sudo chown "$USER":"$USER" /opt/gocassini
git clone <repo-url> /opt/gocassini

cd /opt/gocassini/cassini-go-recorder
go build -o /usr/local/bin/gocassini ./cmd/gocassini
go build -o /usr/local/bin/gocassini-inspect ./cmd/gocassini-inspect
```

### 3) Run in production

Create a service file (`/etc/systemd/system/gocassini.service`) with one recorder job per conference policy (or run it manually for each capture event).

```bash
cat >/tmp/gocassini.service <<'EOF'
[Unit]
Description=Gocassini Meeting Recorder
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/gocassini \
  --mode talk \
  --call-url https://cloud.example.com/call/<ROOM_TOKEN> \
  --name GocassiniObserver \
  --output /var/lib/gocassini/recording.mkv
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF

sudo mv /tmp/gocassini.service /etc/systemd/system/gocassini.service
sudo mkdir -p /var/lib/gocassini
sudo systemctl daemon-reload
sudo systemctl enable --now gocassini
```

Replace placeholders with your actual room tokens / orchestration wrapper.
Add `--duration <seconds>` to `ExecStart` only if you also want a hard cap in addition to auto-stop-on-empty-room.

### 4) If your operator is Salt

The same deployment should be applied from Salt instead of manual `scp`+`systemctl`.

- Put repository checkout, binaries, and unit file under your Salt file roots.
- Add a Salt state to install dependencies (`golang`, `ffmpeg`, `docker` if using local test stack).
- Add a Salt state to copy `/etc/systemd/system/gocassini.service`.
- Add a Salt state to enable and start the service.
- Apply by targeting the recorder nodes:

```bash
salt '*' state.apply gocassini.recorder
```

`gocassini.recorder` is your state ID; replace with your actual environment state name.

## CI

GitHub Actions runs:

- unit tests for `cassini-go-recorder`
- unit tests for `cassini-transcriber`
- unit tests for `test/go-talk-rotator`
- integration tests against local Nextcloud harness:
  - `./cassini-lab/bin/ci-e2e.sh`

Additional harness-specific CI variants remain under `test/bin/`.

### Migration status

- The repository now has new core packages (`pkg/core/...`) that define the long-term architecture:
  - `pkg/core/session`: immutable schema for session-level metadata
  - `pkg/core/store`: append-only stream packet log
  - `pkg/core/timeline`: timeline estimator API
  - `pkg/core/mux`: mux abstraction
- Talk capture is now MKV-first and records session artifacts under `sessions/<id>/`.
- Legacy `.csr` archives remain only for simulate-mode and compatibility-oriented tooling.

## Next steps

This project currently supports Nextcloud Talk as the only provider and keeps the extension path explicit in code and docs for future providers.
