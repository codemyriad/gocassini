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

If `doctor` reports an unwritable transcriber cache or Whisper lock directory,
fix that path or point Cassini at a writable cache root before building:

```bash
export CASSINI_CACHE_ROOT="$PWD/.cache/cassini-transcriber"
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

## Harness Commands

The local stack and showcase/demo flows now live under `cassini dev`:

```bash
./bin/cassini dev stack up
./bin/cassini dev room create --name "Local room"
./bin/cassini dev smoke
./bin/cassini dev fixture prepare-showcase
./bin/cassini dev player showcase --call-url "$CALL_URL"
```

## Current Constraints

- `cassini build` currently uses the existing transcriber runner under
  `cassini-transcriber/bin/docker-run-local.sh`.
- `cassini publish` currently uses the existing static exporter under
  `cassini-publisher/bin/export-static-meetings.sh`.
- `cassini doctor` should be run before `record --out ...opus` or `build --out ...opus`;
  it catches cache and runtime issues early.
- In this checkout, the current doctor output is expected to fail if the
  Whisper cache lock directory is not writable.

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

## Docs In This Branch

- [DX_REARCHITECTURE.md](DX_REARCHITECTURE.md): replacement product boundary
- [DX_EXECUTION_PLAN.md](DX_EXECUTION_PLAN.md): staged execution plan
- [USER_REVIEW_NOTES.md](USER_REVIEW_NOTES.md): original DX review findings
