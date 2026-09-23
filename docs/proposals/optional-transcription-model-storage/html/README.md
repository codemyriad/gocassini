# HTML edition

Open [index.html](../index.html) directly in a browser. It contains all six
documents, styles, navigation code, downloadable source text, and the rendered
breadboard SVG. Reading, navigation, diagram zoom, and printing need no server
or internet connection. Repository references link to the generation-time Git
commit on GitHub; external research links retain their original destinations.

The Markdown documents and original research text remain the source of truth.
After editing them, regenerate from the repository root:

```sh
node docs/proposals/optional-transcription-model-storage/build-html.mjs
```

The generator uses the workspace's `marked` package. If the Mermaid source
changes, it uses workspace `playwright` and its installed Chromium to render
the diagram, fetching Mermaid 11.4.1 from jsDelivr at build time. The rendered
SVG is cached in this directory with a source digest. For a fully offline
rebuild after a diagram change, set `CASSINI_MERMAID_BUNDLE` to a local copy of
that version's `mermaid.min.js`. Ordinary rebuilds reuse the SVG cache.

The HTML diagram uses a vertical layout for legibility; its nodes and wiring
are unchanged. The original Mermaid source remains available below it.

`style.css` and `reader.js` are authoring sources, embedded into `index.html`
during generation. Do not edit the generated HTML or SVG directly. Print all
documents with the toolbar button or the browser's print command.
