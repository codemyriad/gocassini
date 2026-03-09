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
meeting-artifact/
  index.html
  assets/...
  meeting.webm
  transcript.words.v1.json
  captions.vtt
  chapters.vtt
```

This publish-safe branch intentionally does not commit real meeting artifacts under `public/demo/`.
To preview the UI, serve the built viewer from inside a meeting artifact directory or copy the artifact files next to the built `index.html`.

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
