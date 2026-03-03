# Gocassini

`gocassini` is a CLI meeting recorder focused on one deterministic contract:

- input: Nextcloud Talk room
- artifact: one `*.mkv` output per meeting
- additional: per-run `.csr` archive and JSON run report

The repository is intentionally CLI-first and automation-friendly.

## Repository layout

- `cassini-go-recorder/`: recorder implementation and binaries.
- `test/`: reproducible local Nextcloud Talk stack and publisher harness.
- `.github/workflows/ci.yml`: unit + integration CI.
- `media-kit.txt` / `media-kit.md`: brand direction and design principles.

## Local testing

### Unit tests

```bash
cd cassini-go-recorder
go test ./...
```

### Recorder smoke test (no real call)

```bash
cd cassini-go-recorder
go run ./cmd/gocassini --mode simulate --output /tmp/gocassini.csr
go run ./cmd/gocassini-inspect /tmp/gocassini.csr
```

### Debugging and architecture notes

- `cassini-go-recorder/docs/formats.md`: packet truth format and planned migration to stream-session layout.
- `cassini-go-recorder/docs/timelines.md`: timing model and drift handling strategy.
- `cassini-go-recorder/docs/muxing.md`: remux design and back-end strategy.

### Full local integration test (real Nextcloud stack)

```bash
cd /path/to/gocassini-repo-root
./test/bin/ci-e2e.sh
```

This uses `test/compose.yml` and starts a local Nextcloud Talk stack, creates a room, runs publishers, and validates recorder outputs.

### Render and inspect combined output

- Compose MP4 for full-length review (with mute-alternating audio when publisher logs are available):

```bash
cd /path/to/gocassini-repo-root
./test/bin/record-three-songs.sh \
  --duration 300 \
  --output /tmp/three-songs-full.mkv \
  --recorder-duration 360
```

- The script writes:
  - `<output>.json` (run report, includes session artifact paths),
  - `<output>.composed.mp4` (preview composition),
  - `<output>.csr` (archive),
  - session artifact directory at `<output_dir>/sessions/<id>/`.

- To verify session artifacts in CI-style mode:

```bash
./test/bin/verify-session-artifact.sh --final-output /tmp/three-songs-full.mkv
```

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
cd test
./bin/up.sh
CALL_URL="$(./bin/create-room.sh --name "Smoke room" | tail -n1)"
./bin/stream-video.sh --call-url "$CALL_URL" --duration 20
./bin/down.sh --volumes
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
  --duration 3600 \
  --output /var/lib/gocassini/recording.csr \
  --final-output /var/lib/gocassini/recording.mkv
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
- unit tests for `test/go-talk-rotator`
- integration test against local Nextcloud harness using `./test/bin/ci-e2e.sh`

### Migration status

- The repository now has new core packages (`pkg/core/...`) that define the long-term architecture:
  - `pkg/core/session`: immutable schema for session-level metadata
  - `pkg/core/store`: append-only stream packet log
  - `pkg/core/timeline`: timeline estimator API
  - `pkg/core/mux`: mux abstraction
- Current recorder command still writes `.csr` through the legacy archive path. A staged migration keeps this stable while we add deterministic remux validation against the new schema.

## Next steps

This project currently supports Nextcloud Talk as the only provider and keeps the extension path explicit in code and docs for future providers.
