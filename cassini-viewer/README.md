# Cassini Viewer

`cassini-viewer` is the browser-facing side of the meeting artifact flow.

First pass goals:

- load one final audio artifact,
- load one `transcript.words.v1.json` transcript,
- render a readable transcript in the browser,
- seek audio by clicking segments or words,
- highlight the currently active segment,
- support simple transcript search.

This package is intentionally static-build friendly. `vite build` produces an `index.html` entry point that can be shipped with companion files such as:

```text
meeting-library/
  index.html
  catalog.json
  assets/...
  meetings/
    <meeting-id>/
      meeting.webm
      transcript.words.v1.json
      transcript.readable.v1.json
      captions.vtt
      chapters.vtt
      manifest.json
```

At runtime the app loads `catalog.json`, then fetches the selected meeting artifact from the
directory listed in that catalog entry. If exactly one meeting is listed, the app opens it
automatically. If several meetings are listed, the root `index.html` acts as the landing page and
the per-meeting view is `index.html?meeting=<meeting-id>`.

This publish-safe branch intentionally does not commit real meeting artifacts under `public/demo/`.
To preview the runtime-catalog UI, serve the built viewer next to a generated `catalog.json` and
artifact directories, or run the static export flow below.

## Contract with Cassini meeting artifacts

The viewer does not infer transcript structure from loose text.
It expects the canonical `transcript.words.v1` JSON contract produced by the
Cassini build pipeline and treats that as the source of truth for transcript
rendering and click-to-seek behavior.

The schema snapshot used for this bootstrap lives at [schema/transcript-words-v1.schema.json](schema/transcript-words-v1.schema.json).

## Development

```bash
cd cassini-viewer
npm install
npm run demo-data:pull
npm run dev
```

Before pulling demo data, check `../.envrc.example` and set `DEMO_DATA_URL` in
your local gitignored `.envrc` or shell.

`npm run demo-data:pull` downloads a hosted static viewer bundle into
`exports/viewer-demo`, which is where the Vite dev server serves `/catalog.json`
and `/meetings/*` from.

To clear the pulled bundle:

```bash
cd cassini-viewer
npm run demo-data:clean
```

Current demo-data behavior and limitations:

- `npm run demo-data:pull` refreshes `exports/viewer-demo` from `DEMO_DATA_URL`.
- `npm run demo-data:clean` removes the pulled viewer demo bundle.
- Audio currently does not work on the Vite dev server. Treat that as a known
  local-dev limitation for now.

## Validation

```bash
cd cassini-viewer
npm test
npm run build
```

## Mechanical Timing Audit

When click-to-seek timing changes, do not rely only on JSON inspection or the UI highlight.
Double-check the final behavior mechanically by auditing the clicked token against the audio clip at its assigned timestamp.

Quick entry point:

```bash
fish -lc 'cd cassini-viewer && npm run audit:portable-token -- \
  --audio ./exports/viewer-demo/meetings/daily-meeting-2026-03-18--12:30.opus \
  --snippet "two or two or three days of work a month" \
  --word three'
```

Use this against the exact `.opus` file the UI serves before considering a timing fix done.

## Metadata Contract

Portable meetings are expected to carry enough metadata for the viewer to answer the basic user questions without showing raw JSON:

- when the meeting was recorded
- how long it is
- who is speaking
- when the file was processed
- which speech-to-text and cleanup pipeline produced the transcript

For current Cassini files, the important fields are:

- `meeting.recordedAtLocal`: local wall-clock recording time shown as `Recorded`
- `meeting.processedAtUtc`: processing/export time shown as `Processed`
- `meeting.createdAtUtc`: portable file creation time
- `speakers`: rendered as readable speaker chips, not raw JSON
- `provenance.*`: rendered in the `Processing` section

The recorder now writes `source.recordedAtLocal` into `manifest.json`, and portable packing copies it into the embedded portable manifest. If an older artifact is missing that field, the portable repacker infers it from the standard Cassini timestamped filename so existing meetings can be upgraded without rerunning ASR.

## Static export

The preferred suite-level entry point for static publishing lives in
`cassini-publisher`. Build self-contained browser packages from:

- directories that already contain `manifest.json`-style meeting artifacts, or
- processed `.opus` recordings produced by `cassini` (`.opus` files that contain
  embedded Cassini metadata)

From either source, export a ready-to-serve catalog with:

```bash
./cassini-publisher/bin/export-static-meetings.sh \
  --source-dir /path/to/meeting-artifacts \
  --output-dir /tmp/static-meetings
```

By default the export is **lightweight** (D-531): it writes the runtime meeting
catalog and per-meeting artifacts only — the viewer shell (`index.html` +
`assets/`) is served separately (from the Docker image in a deployment), so no
viewer `dist` is needed:

```text
exports/static-meetings/
  catalog.json
  meetings/
    daily-meeting--2026-03-04--12-36-53/
      meeting.opus
      transcript.words.v1.json
      transcript.readable.v1.json
      captions.vtt (optional)
      ...
```

Add `--rebuild-viewer` to also embed the viewer shell for a self-contained,
standalone-servable static site (this path requires the built viewer `dist`, and
builds it on demand):

```text
exports/static-meetings/
  index.html      # from the viewer build
  assets/...      # from the viewer build
  catalog.json
  meetings/...
```

Portable `.opus` inputs are decoded in the exporter and turned into the same
`transcript.words.v1` contract that the viewer expects, so no extra preprocessing
is required.

The generated `catalog.json` looks like:

```json
{
  "version": "cassini.viewer.catalog.v1",
  "meetings": [
    {
      "id": "daily-meeting--2026-03-04--12-36-53",
      "artifactPath": "./meetings/daily-meeting--2026-03-04--12-36-53",
      "title": "Daily Meeting",
      "dateLabel": "2026-03-04 12:36",
      "speakerCount": 6,
      "segmentCount": 42,
      "digestDurationMs": 1830000,
      "roomId": "a7bc3k9x",
      "roomName": "Daily Meeting"
    }
  ]
}
```

`roomId` and `roomName` name the conversation the meeting was recorded in — for
a Nextcloud Talk recording, the room's token and its display name. Both are
optional and often absent: a meeting recorded before Cassini kept the room, or
one whose room lookup failed, simply has no room, and no consumer may require
one. They are separate from `title` because a title is free text that may have
been overridden or derived from a file name, while `roomName` is a claim about
which room this is — only the second is safe to group by.

After the app has been built once, adding a new meeting to a deployed static site does not require
rebuilding JavaScript. Copy a new artifact directory under `meetings/<meeting-id>/` and append a new
entry to `catalog.json`.

If `public/demo/` exists in your working tree, `--source-dir` is optional and defaults to that directory.

For viewer-only development, the lower-level export implementation is still
available from this package:

```bash
cd cassini-viewer
npm run export:meetings -- --source-dir /path/to/meeting-artifacts
```
