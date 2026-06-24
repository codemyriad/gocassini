# Viewer

The viewer is Cassini's browser-facing meeting consumption UI.

It is responsible for:

- loading a published meeting library
- opening a selected meeting
- playing audio
- rendering transcript views
- seeking by transcript timing
- showing metadata and summary content when available

It is intentionally static-site-friendly.

## The viewer's place in the system

The viewer is the final read-only layer.

It does **not**:

- create jobs
- stop jobs
- rerun jobs
- talk to the operator
- mutate published content

It only reads static files that were produced upstream.

## Site-level input contract

At the site root the viewer expects:

- `index.html`
- `catalog.json`
- `assets/...`

`catalog.json` is the top-level routing contract for meetings.

Each meeting entry must provide at least one of:

- `artifactPath` — a published meeting directory
- `audioPath` — a portable `.opus` file

Current operator-managed publish output uses `artifactPath` entries because the operator currently publishes from transient `.meeting` bundles (build scratch, not a user-facing deliverable; the canonical, user-facing meeting format is the portable `.opus`).
Other export flows can produce `audioPath` entries or mixed catalogs.

Current catalog entry fields include:

- `id`
- `title`
- `dateLabel`
- `artifactPath` or `audioPath`
- optional `speakerCount`
- optional `segmentCount`
- optional `digestDurationMs`

## Catalog behavior

The viewer:

- loads `catalog.json`
- sorts entries newest-first by `dateLabel` and then `id`
- auto-opens the meeting when the catalog contains exactly one entry
- otherwise shows a meeting library and routes selected meetings with `?meeting=<id>`

Portable catalog entries may hydrate their counts lazily after first load by reading embedded metadata from the `.opus` file.

## Two meeting input modes

### 1. Artifact-directory mode

In this mode, `artifactPath` points at a published meeting directory.

Typical contents:

- `meeting.webm`
- `transcript.words.v1.json`
- optional `transcript.display.v1.json`
- optional `transcript.readable.v1.json`
- optional `summary.md`
- optional `captions.vtt`
- optional `chapters.vtt`
- `manifest.json`

### Runtime behavior in artifact mode

The viewer always loads:

- `transcript.words.v1.json`

It probes optional sidecars for:

- `transcript.display.v1.json`
- `transcript.readable.v1.json`
- `summary.md`
- `captions.vtt`
- `chapters.vtt`
- `manifest.json`

Audio comes from the transcript's declared media source, usually `meeting.webm`.

### Display-transcript behavior in artifact mode

If `transcript.display.v1.json` exists, the viewer uses it as the highest-level display contract.

If it does not exist:

- the viewer falls back to `transcript.readable.v1` when available
- otherwise it falls back to the canonical word transcript

Published sites usually include `transcript.display.v1.json` because the exporter materializes it if the source `.meeting` bundle did not already contain one.

### 2. Portable `.opus` mode

In this mode, `audioPath` points directly at a portable Cassini meeting file.

### Runtime behavior in portable mode

The viewer:

1. fetches the `.opus`
2. tries to read embedded Cassini metadata from a partial byte-range request first
3. falls back to fetching the full file if necessary
4. reconstructs transcript and metadata views in the browser
5. plays the same `.opus` file as audio

This lets the same viewer shell open one-file meetings without unpacking them server-side.

### Portable transcript behavior

Portable files may embed:

- canonical transcript data
- readable transcript data
- display transcript data
- metadata and provenance

If a display transcript is not embedded, the portable loader synthesizes one in-browser from canonical and readable transcript data.

### Important current limitation in portable mode

Portable files may embed summary metadata and summary attachment content, but the current viewer runtime does **not** render embedded summary markdown from `.opus` files.

So today:

- directory artifacts can show `summary.md`
- portable `.opus` meetings currently show transcript and metadata, but not embedded summary text

## Transcript contracts

The viewer understands three transcript layers.

### Canonical transcript: `transcript.words.v1`

This is the source of truth for:

- timing
- speakers
- seek behavior
- word-level indexing

### Readable transcript: `transcript.readable.v1`

This is an optional cleaned-up view that improves readability.

It preserves a mapping back to canonical content.

### Display transcript: `transcript.display.v1`

This is an optional UI-optimized transcript form that can carry:

- token-level timing
- token alignment metadata
- display-specific block grouping

It is the richest contract the viewer can consume.

## Timing precision model

The viewer does not assume every rendered word has exact timing.

It classifies the loaded artifact into one of three timing levels:

- `word`
- `mixed`
- `segment`

### `word`

Every displayed word is individually timed.

### `mixed`

Some displayed words are exact, while rewritten or aligned display text uses interpolated timing.

### `segment`

Only passages are reliably timed.

This precision label is important user-facing truth.
The viewer prefers honest seeking over invented precision.

## Metadata rendering

The viewer turns raw artifact data into structured metadata sections.

It surfaces information such as:

- meeting title or id
- recorded time
- processed time
- duration
- speaker list
- source identity
- audio integrity fields
- processing provenance

It also keeps a raw JSON inspection view for debugging.

## Summary handling

Current summary behavior is mode-dependent.

#### Directory artifacts

If `summary.md` exists next to the published meeting files, the viewer renders it as meeting summary content.

#### Portable `.opus`

The current runtime does not yet decode and render embedded summary markdown attachments.

## Captions and chapters

If present, the viewer attaches:

- `captions.vtt` as captions track
- `chapters.vtt` as chapters track

These tracks are optional.

## Relationship to publish

The viewer is built once as a static app shell.

Publish then places runtime data next to that shell:

- `catalog.json`
- `meetings/...`
- or `.opus` files referenced by `audioPath`

Because of that split:

- adding meetings does not inherently require rebuilding the viewer JavaScript bundle
- operator-managed publish can replace the live site by swapping exported static files

### What the viewer is optimized for

The current viewer is optimized for:

- static hosting
- no application server dependency
- transcript-first review
- accurate time-based seeking
- compatibility with both exported directories and portable single-file meetings

### What the viewer does not currently try to do

The viewer is not currently:

- an operator dashboard
- a backend-backed library/search service
- a multi-user application
- a direct editor of published artifacts

It is a static meeting reader.
