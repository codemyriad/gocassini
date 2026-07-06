# Viewer

The viewer is Cassini’s browser-facing meeting-reading UI.

It is responsible for:

- loading a published meeting library
- opening a selected meeting
- playing audio
- rendering transcripts
- seeking by transcript timing
- showing metadata and summary content when available

It is intentionally static-site-friendly.

## Boundary

The viewer is the final read-only layer.

It does **not**:

- create jobs
- stop jobs
- rerun jobs
- talk to the operator
- mutate published content

It only reads static files produced upstream.

## Where it fits

In the full system:

- the **operator** produces or promotes published output
- the **viewer** serves and reads that output
- the **control panel** is a separate application for job operations

See:

- [Operator stack](../operator-stack.md)
- [Control panel](./control-panel.md)

## Site-level input contract

At the site root the viewer expects:

- `index.html`
- `catalog.json`
- `assets/...`

`catalog.json` is the top-level meeting library contract.

Each meeting entry must provide at least one of:

- `artifactPath` — a published meeting directory
- `audioPath` — a portable `.opus` file

Current operator-managed publish output normally uses `artifactPath` entries because the operator currently publishes from transient `.meeting` bundles (build scratch, not a user-facing deliverable; the canonical, user-facing meeting format is the portable `.opus`).

## Two meeting input modes

### 1. Artifact-directory mode

In this mode, the viewer opens a published meeting directory.

Typical contents include:

- `meeting.webm`
- `transcript.words.v1.json`
- optional `transcript.display.v1.json`
- optional `transcript.readable.v1.json`
- optional `summary.md`
- optional `captions.vtt`
- optional `chapters.vtt`
- `manifest.json`

Runtime behavior:

- the canonical word transcript is always loaded
- optional transcript/summary sidecars are probed when present
- audio usually comes from `meeting.webm`

### 2. Portable `.opus` mode

In this mode, the viewer opens one portable Cassini meeting file directly.

Runtime behavior:

1. fetch the `.opus`
2. read embedded Cassini metadata
3. reconstruct transcript and metadata views in the browser
4. play the same `.opus` file as audio

This makes one-file meeting delivery possible without server-side unpacking.

## Meeting list ordering and filtering

The meeting library:

- sorts entries newest-first by parsing `dateLabel` as a timestamp; entries whose label is not a date sort after every dated entry, and ties break by `id`
- offers a filter box above the list (shown when there is more than one meeting) that matches meeting names and dates — both the stored label ("2026-03-12") and the format the cards render ("12 Mar 2026")
- auto-opens the meeting when the catalog contains exactly one entry

## Transcript layers

The viewer understands three transcript layers.

### Canonical transcript

`transcript.words.v1.json`

This is the timing source of truth.

### Readable transcript

`transcript.readable.v1.json`

This is an optional cleanup layer that improves readability while preserving mapping back to canonical content.

### Display transcript

`transcript.display.v1.json`

This is an optional viewer-oriented transcript layer that can carry richer display grouping and alignment.

If display transcript data is absent, the viewer falls back to readable or canonical transcript data.

## Summary behavior

### Directory artifacts

If `summary.md` exists in the published meeting directory, the viewer renders it.

### Portable `.opus`

Portable files may carry embedded summary metadata, but the current viewer runtime does not yet render embedded summary markdown from `.opus` files.

So today:

- published artifact directories can show summary content
- portable `.opus` meetings currently focus on transcript and metadata

## What the viewer is optimized for

The viewer is optimized for:

- static hosting
- no application server dependency
- transcript-first review
- accurate time-based seeking
- compatibility with both directory artifacts and portable `.opus` meetings

## Local development

Typical local viewer workflow:

```bash
cd cassini-viewer
npm install
npm run demo-data:pull
npm run dev
```

That pulls a hosted static viewer bundle into `exports/viewer-demo`, which the Vite dev server serves.

To remove the pulled demo bundle:

```bash
cd cassini-viewer
npm run demo-data:clean
```

Build validation:

```bash
cd cassini-viewer
npm test
npm run build
```

## Relationship to publish

The viewer is built once as a static app shell. Publish then places runtime data next to that shell:

- `catalog.json`
- `meetings/...`
- or portable `.opus` files referenced by `audioPath`

That split is what allows Cassini to add or replace published meetings without necessarily rebuilding the viewer JavaScript bundle each time.

## See also

- [Core pipeline](../core-pipeline.md)
- [Artifacts and filesystem](../reference/artifacts-and-filesystem.md)
