# The cassini CLI from a checkout

This page is for people working in the repo. If you want to run Cassini on a
Nextcloud, install the app instead — see
[Installing Cassini as a Nextcloud ExApp](./exapp-install.md). If you want to
read published recordings from outside Nextcloud, see
[Agent access to meeting recordings](./agent-meeting-access.md); that path needs
no checkout.

From a source checkout, use the wrapper:

```bash
./bin/cassini
```

It builds the current `cassini` CLI from the Go module and runs it from your
current working directory. Output paths are resolved from that directory.

## First commands

See what the CLI exposes:

```bash
./bin/cassini --help
```

Validate the environment before expensive work starts:

```bash
./bin/cassini doctor
```

If `doctor` reports an unwritable Cassini cache or model directory, fix that
path or point Cassini at a writable cache root before building:

```bash
export CASSINI_CACHE_ROOT="$PWD/.cache/cassini"
./bin/cassini doctor
```

## The main flow

Set your Talk room URL:

```bash
export CALL_URL="https://cloud.example.com/call/<ROOM_TOKEN>"
```

Record a meeting and finish with one portable file:

```bash
./bin/cassini record --call "$CALL_URL" --out "./My Meetings/2026-03-11 Weekly Sync.opus"
```

If Cassini fails after capture or during processing, fix the issue and rerun the
same command with the same `--out` path. Cassini keeps resumable state in a
hidden `.cassini-work/` directory next to the target file and reuses the
finished recording or finished meeting artifact where it can.

Inspect the resulting file:

```bash
./bin/cassini inspect "./My Meetings/2026-03-11 Weekly Sync.opus"
```

If you already have a recording, build the portable file from that:

```bash
./bin/cassini build /path/to/meeting.mkv --out "./My Meetings/Imported Meeting.opus"
```

## Trying it without a real call

The portable-file path needs a real meeting. For local smoke tests and
diagnostics, simulate mode writes a debug `.run` bundle:

```bash
./bin/cassini record --simulate --out ./runs/demo.run
./bin/cassini inspect ./runs/demo.run
```

Browse an already-generated sample site from this checkout:

```bash
./bin/cassini serve ./cassini-viewer/exports/static-meetings
```

If you have processed `.opus` recordings, build a browser view of them in one
step:

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

## The explicit pipeline

To keep the internal working artifacts visible, run the stages yourself:

```bash
./bin/cassini record --call "$CALL_URL" --out ./runs/weekly-sync.run
./bin/cassini build ./runs/weekly-sync.run --out ./meetings/weekly-sync.meeting
./bin/cassini publish ./meetings --out ./site
./bin/cassini serve ./site
```

`inspect` reads any primary Cassini artifact:

```bash
./bin/cassini inspect "./My Meetings/2026-03-11 Weekly Sync.opus"
./bin/cassini inspect ./runs/weekly-sync.run
./bin/cassini inspect ./meetings/weekly-sync.meeting
./bin/cassini inspect ./site
```

See [Artifacts and filesystem](./reference/artifacts-and-filesystem.md) for what
each artifact holds.

## The harness

The local Talk lab and the showcase/demo flows live under `cassini dev`:

```bash
./bin/cassini dev stack up
./bin/cassini dev room create --name "Local room"
cp .envrc.example .envrc
direnv allow
./bin/cassini dev smoke
./bin/cassini dev fixture prepare-showcase
./bin/cassini dev player showcase --call-url "$CALL_URL"
```

The harness implementation is under [`harness/`](../harness/README.md).

## Viewer demo data

For local viewer development, pull demo data directly into the viewer dev server
root. Set `DEMO_DATA_URL` in a gitignored `.envrc` or export it in your shell,
then run:

```bash
cd cassini-viewer
npm install
npm run build
npm run demo-data:pull
npm run dev
```

Use `npm run demo-data:clean` to remove the pulled bundle.

The pull downloads `index.html`, referenced `assets/*`, `catalog.json` and each
meeting directory into `cassini-viewer/exports/viewer-demo`, which is where the
Vite dev server already serves `/catalog.json` and `/meetings/*` from. Meeting
file names are read from each meeting's `manifest.json`, so `DEMO_DATA_URL`
stays the only required setting.

## The commands

```text
cassini annotate   Read and write the tags and marks a packed .opus file carries
cassini build      Build a browser-ready meeting artifact from a recording
cassini dev        Access the local harness namespace
cassini doctor     Validate the local environment before expensive work starts
cassini insight    Ask one question of several meetings and keep the answer
cassini inspect    Inspect a run, meeting, site, or lower-level Cassini artifact
cassini meetings   Read the meeting recordings your Nextcloud account may access
cassini operator   Launch the separate cassini-operator binary
cassini pack       Pack a built .meeting bundle into a portable .opus file
cassini publish    Publish one or more meeting bundles as a static site
cassini record     Record a meeting into one portable artifact
cassini retag      Rewrite a packed .opus file's room and job fields without re-encoding it
cassini serve      Serve a static Cassini site locally
```

`cassini meetings` reads published recordings out of Nextcloud as a given
Nextcloud user — see
[Agent access to meeting recordings](./agent-meeting-access.md).
