# Cassini

`cassini` is the product CLI for recording Nextcloud Talk meetings into one
portable meeting file that can later be loaded in the web app.

The normal user flow is:

1. point Cassini at a meeting
2. let Cassini record and process it
3. end up with one portable `.opus` file

Cassini still uses `.run`, `.meeting`, and `.site` artifacts internally, but
those are now implementation detail and debugging surfaces rather than the main
product story.

## Repo Entry Point

From this source checkout, use:

```bash
./bin/cassini
```

That wrapper builds the current `cassini` CLI from the Go module and runs it
from your current working directory.

## First Commands

See what the product exposes:

```bash
./bin/cassini --help
```

Validate the environment before expensive work starts:

```bash
./bin/cassini doctor
```

If `doctor` reports an unwritable Cassini cache or model directory,
fix that path or point Cassini at a writable cache root before building:

```bash
export CASSINI_CACHE_ROOT="$PWD/.cache/cassini"
./bin/cassini doctor
```

## Main Flow

Set your Talk room URL:

```bash
export CALL_URL="https://cloud.example.com/call/<ROOM_TOKEN>"
```

Record a meeting and let Cassini finish with one portable file:

```bash
./bin/cassini record --call "$CALL_URL" --out "./My Meetings/2026-03-11 Weekly Sync.opus"
```

If Cassini fails after capture or during processing, fix the issue and rerun the
same command with the same `--out` path. Cassini now keeps resumable state in a
hidden `.cassini-work/` directory next to the target file and will reuse the
finished recording or finished meeting artifact when possible.

Inspect the resulting file:

```bash
./bin/cassini inspect "./My Meetings/2026-03-11 Weekly Sync.opus"
```

If you already have an existing recording, build the portable file from that:

```bash
./bin/cassini build /path/to/meeting.mkv --out "./My Meetings/Imported Meeting.opus"
```

## Try It Without A Real Call

The user-facing portable-file path currently needs a real meeting.

For local smoke and diagnostics, simulate mode still writes a debug `.run`
bundle:

```bash
./bin/cassini record --simulate --out ./runs/demo.run
./bin/cassini inspect ./runs/demo.run
```

Browse an already-generated sample site from this checkout:

```bash
./bin/cassini serve ./cassini-viewer/exports/static-meetings
```

If you have processed `.opus` recordings, build a browser view of them in one step:

```bash
cd cassini-viewer
npm install
npm run build
node ./scripts/export-static-meetings.mjs \
  --source-dir /path/to/your/processed-opus \
  --output-dir /tmp/cassini-opus-view
cd ..
./bin/cassini serve /tmp/cassini-opus-view
```

## Advanced Flow

If you want to keep the internal working artifacts visible, you can still use
the explicit pipeline:

```bash
./bin/cassini record --call "$CALL_URL" --out ./runs/weekly-sync.run
./bin/cassini build ./runs/weekly-sync.run --out ./meetings/weekly-sync.meeting
./bin/cassini publish ./meetings --out ./site
./bin/cassini serve ./site
```

Inspect any primary Cassini artifact:

```bash
./bin/cassini inspect "./My Meetings/2026-03-11 Weekly Sync.opus"
./bin/cassini inspect ./runs/weekly-sync.run
./bin/cassini inspect ./meetings/weekly-sync.meeting
./bin/cassini inspect ./site
```

## Deployment Bundle

For the repo-root Docker Compose deployment bundle, see:

- [deployment/README.md](deployment/README.md)

From there you can bring up the packaged operator, control panel, and viewer with `docker compose up --build`.

## Harness Commands

The local stack and showcase/demo flows now live under `cassini dev`.

Important local harness notes:

- prefer `./bin/cassini dev stack up` over raw harness `docker compose`, because the wrapper runs the harness scripts and additional setup after Compose starts
- use `127.0.0.1`, not `localhost`, for local harness URLs, including in the browser
- the local harness currently does not work on macOS because of networking issues in the harness stack

Main commands:

```bash
./bin/cassini dev stack up
./bin/cassini dev room create --name "Local room"
cp .envrc.example .envrc
direnv allow
./bin/cassini dev smoke
./bin/cassini dev fixture prepare-showcase
./bin/cassini dev player showcase --call-url "$CALL_URL"
```

For local viewer development, pull demo data directly into the viewer dev
server root. Set `DEMO_DATA_URL` in a gitignored `.envrc` or export it in your
shell, then run:

```bash
cd cassini-viewer
npm install
npm run build
npm run demo-data:pull
npm run dev
```

Use `npm run demo-data:clean` to remove the pulled bundle.

The demo-data pull downloads `index.html`, referenced `assets/*`, `catalog.json`, and each meeting directory into
`cassini-viewer/exports/viewer-demo`, which is where the Vite dev server already
serves `/catalog.json` and `/meetings/*` from. Meeting file names are read from
each meeting's `manifest.json`, so `DEMO_DATA_URL` remains the only required
setting.

## Current Constraints

- `cassini build` now uses the native Go transcription pipeline in
  `cassini-go-recorder/internal/transcribe`.
- `cassini publish` currently uses the static exporter under
  `cassini-publisher/bin/export-static-meetings.sh`.
- `cassini doctor` should be run before `record --out ...opus` or `build --out ...opus`;
  it catches cache, media-tool, and runtime issues early.
- In this checkout, the current doctor output is expected to fail if the
  Cassini cache or model directories are not writable, or if required media
  tools like `ffmpeg`/`ffprobe` are unavailable.

## Product Commands

```text
./bin/cassini doctor
./bin/cassini record
./bin/cassini build
./bin/cassini publish
./bin/cassini serve
./bin/cassini inspect
```

## Legacy Surface

Older wrappers under directories such as:

- `cassini-recorder/`
- `cassini-diagnostics/`
- `cassini-publisher/`
- `cassini-lab/`
- `cassini-player/`

are now legacy shims or implementation surfaces. They are being deprecated in
favor of `./bin/cassini`.

The local harness implementation now lives under `harness/`, and it is now
being surfaced through `./bin/cassini dev ...` rather than taught as a peer
product.

## Docs

- Documentation workflow: [docs/README.md](docs/README.md) — source of truth, audience structures, and generated outputs
- Canonical source of truth: [docs/source-of-truth/](docs/source-of-truth/)
- System overview: [docs/architecture.md](docs/architecture.md)
- Cross-cutting reference: [docs/portable-meeting-format.md](docs/portable-meeting-format.md), [docs/audio-glossary.md](docs/audio-glossary.md)
- Component deep-dives: [cassini-go-recorder/docs/](cassini-go-recorder/docs/), [cassini-transcriber/docs/](cassini-transcriber/docs/), [cassini-viewer/docs/](cassini-viewer/docs/)
- Planning and review notes: [planning/](planning/) — DX rearchitecture, execution plan, user review findings, MVP initiative
