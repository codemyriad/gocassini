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

## Contract with `cassini-transcriber`

The viewer does not infer transcript structure from loose text.
It expects the canonical `transcript.words.v1` JSON contract and treats that as the source of truth for transcript rendering and click-to-seek behavior.

The schema snapshot used for this bootstrap lives at [schema/transcript-words-v1.schema.json](schema/transcript-words-v1.schema.json).

## Development

```bash
cd cassini-viewer
npm install
npm run dev
```

## Validation

```bash
cd cassini-viewer
npm test
npm run build
```

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

This writes one static app plus a runtime meeting catalog:

```text
exports/static-meetings/
  index.html
  catalog.json
  assets/...
  meetings/
    daily-meeting--2026-03-04--12-36-53/
      meeting.opus
      transcript.words.v1.json
      transcript.readable.v1.json
      captions.vtt (optional)
      ...
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
      "digestDurationMs": 1830000
    }
  ]
}
```

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
