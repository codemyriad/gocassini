# Cleaned Transcript Alignment Plan

## Goal

Support cleaned transcripts that are word-accurate where alignment is trustworthy, while avoiding fake precision when cleanup rewrites diverge too far from ASR output.

The product target is:

- `Exact` transcript view: fully authoritative ASR word timing.
- `Cleaned` transcript view: word timing when the cleaned token can be matched to source ASR words.
- Unmatched cleaned tokens remain visible but untimed and unclickable.

This is intentionally:

- more honest than fabricating timings for rewritten words
- lighter for the browser
- compatible with a small cleanup model that should not be forced to solve cleanup and alignment in one pass

## Current Findings

### What exists today

1. STT produces canonical `transcript.words.v1` with real word timestamps.
2. Cleanup rewrites segment text.
3. A display transcript is derived by aligning cleaned tokens back onto ASR words.

Relevant code:

- recorder cleanup and transcript writing:
  - `cassini-go-recorder/internal/transcribe/transcribe.go`
  - `cassini-go-recorder/internal/transcribe/llm.go`
  - `cassini-go-recorder/internal/transcribe/format.go`
- viewer/runtime alignment:
  - `cassini-viewer/src/viewer/portable.ts`
  - `cassini-viewer/src/viewer/loadArtifact.ts`
- static export alignment:
  - `cassini-viewer/scripts/export-static-meetings.mjs`

### What we learned

- The existing LCS-style aligner already works well for monotonic local edits:
  - filler removal
  - repeated word/phrase removal
  - restart/repair collapse
  - punctuation/casing normalization
  - duplicated-word deletion
- It is weak on:
  - synonym rewrites
  - contraction expansion/collapse
  - tokenization changes like `11 30` -> `11:30`

### Real-data check

On the current 9 local `.opus` meetings:

- cleaned words inspected: `27,958`
- unmatched under the current aligner: `2`
- effective match rate: about `99.993%`

Important caveat:

- the current portable meetings mostly do not contain aggressively cleaned text yet
- many selected review passages are still essentially raw ASR text

## Decisions Already Applied

These changes are implemented but not yet committed:

1. Unmatched cleaned words no longer receive fabricated timings.
2. `timedWordCount` and `timingCoverage` count only source-aligned words.
3. Regression tests cover rewrite and contraction-expansion cases.
4. A repeatable extraction script now generates review samples from local meetings.

Files changed:

- `cassini-viewer/src/viewer/portable.ts`
- `cassini-viewer/scripts/export-static-meetings.mjs`
- `cassini-viewer/scripts/export-static-meetings.test.ts`
- `cassini-viewer/scripts/extract-cleanup-review-samples.mjs`

Generated review corpus:

- `cassini-viewer/exports/cleanup-review-samples.md`
- `cassini-viewer/exports/cleanup-review-samples.json`

## Recommended Product Contract

### Exact view

- source: `transcript.words.v1`
- word-level seek and highlight
- authoritative timing

### Cleaned view

- source: canonical precomputed `transcript.display.v1`
- cleaned tokens are clickable only if aligned to source ASR words
- untimed cleaned tokens render as plain text
- segment-level linkage remains via `sourceSegmentIds`

## Cleanup Rules To Optimize For

The cleanup model should be trained or prompted for constrained edits:

- remove fillers
- remove repetitions
- collapse speech repairs
- normalize punctuation and casing
- fix obvious local ASR mistakes when token identity stays close

The cleanup model should avoid or minimize:

- synonym replacement
- paraphrase
- contraction expansion/collapse
- aggressive number/date reformatting

These can be revisited later, but they should not be assumed word-alignable by default.

## Implementation Plan

### Phase 1: Canonicalize display output

1. Precompute `transcript.display.v1` during preprocessing/export for all meeting flows.
2. Treat the browser as a renderer only.
3. Stop recomputing cleaned-token alignment in the browser for playback behavior.

Target result:

- one canonical cleaned display artifact
- no repeated alignment logic at playback time

### Phase 2: Golden test corpus

1. Use extracted real meeting samples as review material.
2. For reviewed samples, store:
   - source ASR text
   - desired cleaned text
   - expected aligned/untimed tokens
3. Build golden tests for:
   - filler removal
   - repetition removal
   - restart collapse
   - punctuation-only cleanup
   - contraction cases
   - numeric formatting cases
   - synonym/rewrite cases that must remain untimed

### Phase 3: Tighten aligner

Keep the aligner deterministic and monotonic.

Likely improvements:

1. Better token normalization for:
   - contractions
   - punctuation
   - common numeric/time formatting
2. Confidence/caveat classification:
   - `source` for exact matched tokens
   - `none` for visible but untimed tokens
3. Optional later addition:
   - limited multi-token transforms such as `11 30` <-> `11:30`

### Phase 4: Viewer simplification

1. Prefer loading precomputed `transcript.display.v1`.
2. In cleaned view:
   - timed tokens are buttons
   - untimed tokens are plain text
3. Keep `Exact words` mode available for forensic accuracy.

## Immediate Next Steps

1. User reviews and edits examples from:
   - `cassini-viewer/exports/cleanup-review-samples.md`
2. Replace current samples with improved ASR/cleanup data from existing meetings.
3. Convert reviewed examples into golden fixtures.
4. Remove remaining browser-side dependence on recomputing cleaned-token alignment when precomputed display data is available.

## Useful Commands

Generate review samples:

```bash
cd cassini-viewer
node ./scripts/extract-cleanup-review-samples.mjs --limit 20
```

Run tests:

```bash
cd cassini-viewer
npm test
```

## Open Questions

1. Should `transcript.display.v1` remain the long-term cleaned-token artifact name?
2. Do we want an explicit alignment-confidence field beyond `alignment: "source" | "none"`?
3. Should limited token transforms such as time formatting be supported in the deterministic aligner, or intentionally left untimed?
