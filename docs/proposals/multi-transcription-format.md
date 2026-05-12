# Proposal: Multi-Transcription Support in the Portable Meeting Format

Date: 2026-05-12
Status: Draft for discussion
Author: format research

## TL;DR

The current Cassini Portable Meeting format (v1, `org.cassini.portable-meeting/1`) carries exactly one transcript per file. We propose a backward-compatible extension that lets a single `.opus` file hold **multiple transcriptions of the same audio** — e.g. Parakeet 0.6B and Canary 1B side by side — so the viewer can switch, compare, and run regressions without producing duplicate files.

The recommended design splits each transcript body into its **own OpusTag payload chunk set** with its own SHA-256 and gzip envelope. The main manifest keeps only a small *index* of available transcriptions plus their provenance. This avoids paying parse cost for transcripts the viewer doesn't display, keeps each body independently hash-verifiable, and falls back cleanly for v1-aware consumers.

## Motivation

### Why now

[D-277 — Swap STT model to state-of-the-art for accuracy (canary-1b-v2)](https://linear.app/code-myriad/issue/D-277) explicitly calls for publishing a Canary 1B transcript "next to the parakeet one for side-by-side inspection" as part of its Definition of Done. Today the only way to do that is to ship two `.opus` files (or a sidecar JSON) — neither matches the format's "one file, plays anywhere" promise. A multi-transcription extension is the natural home for the comparison artifact D-277 needs.

### Adjacent use cases

- **Model A/B and regression**: when we change STT models, keep the old transcript embedded so the change can be audited later. Today nothing forces us to retain the previous engine's output.
- **Multi-language transcription**: one Italian-speaking meeting, two transcripts (one transcribed in Italian, one machine-translated to English).
- **Cleanup variants**: multiple readable-cleanup runs from different LLMs, all tied to the same raw ASR.
- **Human + machine**: a human-corrected transcript alongside the raw ASR with full provenance.
- **Reviewer mode in viewer**: D-268 wants golden-test–style locking on summary outputs. Once we ship multi-transcript, the same mechanism gives us golden-test locking on transcripts.

### What the current format prevents

From `spec/cassini-portable-meeting-manifest-v1.schema.json`:

- `transcript` is a single object (line 161), required at the top level.
- `provenance.speechToText` is a single `processingStep`.
- `readableTranscript` is a single object paired with one `readableCleanup`.

So today: one raw ASR + one cleanup, tied to one engine. A second engine's output has nowhere to go.

## Constraints to preserve

These are non-negotiable; they come from the v1 design principles in `docs/portable-meeting-format.md`:

1. **Progressive enhancement.** File MUST play as plain `.opus` in any audio player.
2. **Self-describing.** A curious observer with `ffprobe` + standard tools must be able to discover and decode the metadata.
3. **Integrity.** All transcripts are about the *same* PCM audio. The existing `pcmSha256` guard still applies.
4. **Backward compatibility.** A v1-aware consumer reading a v2 file must still get a working single-transcript view (or a clear "newer format" message), not a hard error.

## Performance: why splitting matters

The user's question that triggered this proposal: *is JSON parsing expensive, and if we only need one transcription do we still parse them all?*

### Today

The manifest is one gzipped JSON blob. `JSON.parse` is a whole-document operation; it cannot skip keys. So if multiple transcripts lived inside the same top-level blob, every load would parse every one.

### Empirical envelope

A word-timed transcript item is ~80 bytes uncompressed (`speaker`, `startMs`, `endMs`, `text`). For a 1-hour meeting at ~150 wpm:

| # of transcripts | JSON bytes (uncomp.) | After gzip | V8 `JSON.parse` |
|---|---|---|---|
| 1 | ~720 KB | ~220 KB | ~5–8 ms |
| 2 | ~1.4 MB | ~440 KB | ~15 ms |
| 5 | ~3.6 MB | ~1.1 MB | ~40 ms |

For 1–2 engines the cost is negligible. It only starts to matter at 5+ or on low-power devices, and only if all bodies live in one blob.

### Implication

Parse cost alone wouldn't force splitting — but **hash verification, independent extensibility, and clean per-transcript integrity** do. Once we split, the parse-cost-on-demand property comes for free.

## Design space

Four options considered.

### Option A: array inside the manifest (do everything in one blob)

```json
{
  "transcripts": [
    { "id": "parakeet", "format": "cassini.words.v1", "items": [...], "provenanceRef": "stt#0" },
    { "id": "canary",   "format": "cassini.words.v1", "items": [...], "provenanceRef": "stt#1" }
  ],
  "provenance": {
    "speechToText": [
      { "id": "stt#0", "engine": "sherpa-onnx-go", "model": "parakeet-tdt-0.6b-v2-int8" },
      { "id": "stt#1", "engine": "sherpa-onnx-go", "model": "canary-1b-v2" }
    ]
  }
}
```

- **Pros**: Simplest schema change. One payload to decode. Existing inspect tooling still works on the same chunk set.
- **Cons**: Pay parse + memory cost for everything every time. Cannot hash-verify individual transcripts. A corrupt or oversized body taints the whole manifest. Hits OpusTag size limits sooner.

### Option B: separate OpusTag payload chunk sets per transcript (recommended)

Each transcript gets its own chunk set, descriptor, and SHA-256. The main manifest becomes an *index*:

```
CASSINI_FORMAT=org.cassini.portable-meeting/2
CASSINI_PAYLOAD_*               # main manifest (small)
CASSINI_TX_PARAKEET_PAYLOAD_*   # parakeet transcript body
CASSINI_TX_CANARY_PAYLOAD_*     # canary transcript body
CASSINI_TX_READABLE_A_PAYLOAD_* # readable cleanup body
```

Manifest body:

```json
{
  "kind": "cassini-portable-meeting",
  "version": 2,
  "transcripts": [
    {
      "id": "parakeet",
      "role": "raw-asr",
      "default": false,
      "format": "cassini.words.v1",
      "language": "en",
      "wordCount": 9183,
      "provenanceRef": "stt#parakeet",
      "payloadRef": { "prefix": "CASSINI_TX_PARAKEET_PAYLOAD_", "sha256": "..." }
    },
    {
      "id": "canary",
      "role": "raw-asr",
      "default": true,
      "format": "cassini.words.v1",
      "language": "en",
      "wordCount": 9224,
      "provenanceRef": "stt#canary",
      "payloadRef": { "prefix": "CASSINI_TX_CANARY_PAYLOAD_", "sha256": "..." }
    }
  ],
  "readableTranscripts": [
    {
      "id": "readable-qwen",
      "role": "readable-cleanup",
      "default": true,
      "sourceTranscriptId": "canary",
      "provenanceRef": "clean#qwen35",
      "payloadRef": { "prefix": "CASSINI_TX_READABLE_QWEN_PAYLOAD_", "sha256": "..." }
    }
  ],
  "provenance": {
    "speechToText": {
      "parakeet": { "engine": "sherpa-onnx-go", "model": "parakeet-tdt-0.6b-v2-int8", "device": "cuda" },
      "canary":   { "engine": "sherpa-onnx-go", "model": "canary-1b-v2",            "device": "cuda" }
    },
    "readableCleanup": {
      "qwen35": { "backend": "openai-compatible", "model": "cassini-cleanup-qwen35-0.8b-v35-q4km" }
    }
  }
}
```

- **Pros**: Pay parse cost only for the transcript you display. Each body has its own SHA-256 and is independently verifiable. Can add/remove transcripts without rewriting the main manifest hash. Bodies can be of different `format` versions side by side (e.g. legacy `cassini.words.v1` + future `cassini.words.v2`). Naturally handles the multi-readable case too.
- **Cons**: More OpusTag namespaces. Decode plumbing is a little more involved (extract index → resolve `payloadRef` → fetch chunk set → decode). Self-describing-ness needs a clear naming rule for the new tag prefixes.

### Option C: embed each transcript as a base64 attachment

Lean on the existing `attachments` slot — each transcript becomes an attachment with MIME `application/vnd.cassini.transcript+json`.

- **Pros**: Zero schema churn; the attachment mechanism already handles arbitrary payloads.
- **Cons**: Attachments are *opaque* to the format spec; they don't have first-class provenance, default-selection, or `format` discriminators. Loses the structural meaning of "this is a transcription you can switch to."

### Option D: external sidecar files

Keep `.opus` single-transcript; ship `.transcript.<id>.json` files next to it.

- **Pros**: No format change at all.
- **Cons**: Breaks the entire promise of the format ("one file, portable"). Already explicitly rejected in v1.

## Recommendation

**Option B**, with three guardrails:

1. **Always have exactly one `default: true` transcript.** v1-aware consumers and dumb players that read only the legacy single-transcript path should still get the canonical "best available" transcript via a v1 compatibility shim (see Compatibility).
2. **Use a fixed naming rule for per-transcript tag prefixes**: `CASSINI_TX_<UPPER_SNAKE_ID>_PAYLOAD_NNN`. The id must match `^[a-z0-9][a-z0-9_-]{0,31}$`; we uppercase + transliterate `-` to `_` for the tag name. Each per-transcript chunk set has its own `_CHUNK_COUNT`, `_SHA256`, `_RAW_BYTES`, `_GZIP_BYTES`, and `_MIME` descriptor tags. Same self-describing discipline as v1.
3. **Bump the format URI**: `org.cassini.portable-meeting/2`. v2 producers MAY also emit a v1-style top-level `transcript` object pointing to the default transcript (see Compatibility), so v1 consumers degrade to single-transcript view rather than failing.

## Schema sketch (v2 manifest)

Notable changes from v1:

| v1 | v2 |
|---|---|
| `transcript: object` | `transcripts: array` (1..N), plus optional v1-compat `transcript` mirror of the default |
| `readableTranscript: object` | `readableTranscripts: array` (0..N) |
| `provenance.speechToText: ProcessingStep` | `provenance.speechToText: map<id, ProcessingStep>` |
| `provenance.readableCleanup: ProcessingStep` | `provenance.readableCleanup: map<id, ProcessingStep>` |
| `kind: "cassini-portable-meeting"` (unchanged) | same |
| `version: 1` | `version: 2` |
| `profile: "ogg-opus"` | same |

New per-transcript entry:

```jsonc
{
  "id":   "canary",                      // unique within file, kebab/snake
  "role": "raw-asr",                     // raw-asr | readable-cleanup | display | human-corrected | translation
  "default": true,                       // exactly one default per role
  "format": "cassini.words.v1",          // body format version
  "language": "en",                      // BCP-47 of the body
  "wordCount": 9224,
  "sourceTranscriptId": "canary",        // optional: derived-from pointer
  "provenanceRef": "stt#canary",         // points into provenance.speechToText.canary
  "createdAtUtc": "2026-05-12T...",
  "payloadRef": {
    "prefix": "CASSINI_TX_CANARY_PAYLOAD_",
    "chunkCount": 14,
    "sha256":   "...",                   // sha256 of decompressed body JSON
    "rawBytes": 718432,
    "gzipBytes": 221110,
    "mime":     "application/vnd.cassini.transcript-words+json",
    "encoding": "base64url+gzip+utf8json"
  },
  "summary": {                            // optional fast-render stats
    "speakerCount": 4,
    "averageConfidence": 0.92
  }
}
```

The body referenced by `payloadRef` is exactly the same JSON shape today's `transcript` carries — `{ format, language, wordCount, items[] }`. Migrating an existing v1 transcript to a v2 entry is a copy + descriptor rewrite, no body change.

## OpusTag layout

Per file, in addition to the v1 tags:

```
# bumped descriptor
CASSINI_FORMAT=org.cassini.portable-meeting/2
CASSINI_PAYLOAD_SCHEMA=https://cassini.local/spec/cassini-portable-meeting-manifest-v2.schema.json

# main manifest payload (small; index + provenance + meeting)
CASSINI_PAYLOAD_CHUNK_COUNT=<N>
CASSINI_PAYLOAD_SHA256=<hex>
CASSINI_PAYLOAD_RAW_BYTES=...
CASSINI_PAYLOAD_GZIP_BYTES=...
CASSINI_PAYLOAD_000..N=<chunks>

# discoverable list (so a casual observer sees "two transcripts" from ffprobe alone)
CASSINI_TRANSCRIPT_IDS=parakeet,canary
CASSINI_TRANSCRIPT_DEFAULT=canary

# per-transcript payloads (one chunk set per transcript)
CASSINI_TX_PARAKEET_PAYLOAD_MIME=application/vnd.cassini.transcript-words+json
CASSINI_TX_PARAKEET_PAYLOAD_ENCODING=base64url+gzip+utf8json
CASSINI_TX_PARAKEET_PAYLOAD_CHUNK_COUNT=14
CASSINI_TX_PARAKEET_PAYLOAD_SHA256=<hex>
CASSINI_TX_PARAKEET_PAYLOAD_RAW_BYTES=...
CASSINI_TX_PARAKEET_PAYLOAD_GZIP_BYTES=...
CASSINI_TX_PARAKEET_PAYLOAD_000..13=<chunks>

CASSINI_TX_CANARY_PAYLOAD_MIME=application/vnd.cassini.transcript-words+json
CASSINI_TX_CANARY_PAYLOAD_ENCODING=base64url+gzip+utf8json
CASSINI_TX_CANARY_PAYLOAD_CHUNK_COUNT=14
...

# decode hint covers both layers
CASSINI_DECODE_HINT=Concatenate CASSINI_PAYLOAD_000..N for the manifest; for a transcript body concatenate CASSINI_TX_<ID>_PAYLOAD_000..N. Each chunk set: base64url -> gzip -> UTF-8 JSON.
```

## Compatibility

### v1-aware consumers reading a v2 file

Two flavors of compatibility, producer's choice via a profile flag:

- **Compat profile (default for the foreseeable future).** v2 producer additionally emits a top-level `transcript` field mirroring the *default* transcript body — i.e. inlines the default transcript into the main manifest just like v1 did. v1 readers see a working file with one transcript; v2 readers ignore the inlined copy and resolve `transcripts[].payloadRef` for the real bodies.
- **Strict profile.** Main manifest omits the v1 mirror. v1 readers see `"version": 2` and SHOULD report "newer format; cannot fully render" rather than guess.

The compat profile costs us one inlined-transcript-worth of bytes in the main payload but gives every existing tool (viewer, inspect command, ffprobe-based dumps) a working fallback during the rollout.

### v2-aware consumers reading a v1 file

`version: 1` files keep working unchanged: the v2 loader treats the top-level `transcript`/`readableTranscript` as a single-entry virtual array with synthesized ids (`raw-asr`, `readable`). No producer changes required for the v1 corpus.

## Producer and consumer impact

Code areas that need to change (with a v1→v2 audit pass):

**Producer (`cassini-go-recorder`):**

- `internal/portable/manifest.go`: `Manifest.Transcript` → `Manifest.Transcripts []TranscriptEntry`; add `PayloadRef`; rewrite `EncodeManifest` to return the main payload plus a list of per-transcript encoded payloads. `BuildOpusTags` learns to emit per-transcript chunk sets and descriptor tags.
- `internal/cassini/portable_meeting.go`: the artifact loader's `Files.Transcript` becomes `Files.Transcripts []FileTranscript` so the bundle can carry multiple word-transcript files. Existing single-transcript bundles continue to produce a v2 file with one entry.
- `internal/inspect/portable_audio.go`: surface a list rather than a single line in `cassini inspect`.

**Consumer (`cassini-viewer`):**

- `src/viewer/portable.ts`: `extractPortableManifestFromArrayBuffer` extracts the main manifest only; add `loadPortableTranscriptBody(manifest, id)` that resolves a `payloadRef` to a chunk set, decodes, and parses. Cache per-body promises.
- `src/viewer/loadArtifact.ts`: `LoadedArtifact` gets `availableTranscripts: TranscriptDescriptor[]` and `currentTranscriptId`; switch happens by calling a new `loadPortableTranscriptBody(...)`.
- `src/viewer/portable.test.ts`: existing v1 fixtures stay green via the v1-shim path; new v2 fixtures cover the multi-transcript case.
- New viewer UI affordance — out of scope for the format proposal, but the loader API needs to support **comparison mode** (load two bodies into memory and diff them). The format already supports this; only the UI is missing.

**Spec & docs:**

- `spec/cassini-portable-meeting-manifest-v2.schema.json` (new).
- `docs/portable-meeting-format.md`: append a v2 section, mark v1 sections "v1 only" where they diverge.

## Open questions

1. **Should `readableTranscripts[].sourceTranscriptId` be required?** Probably yes — a cleanup transcript without a known source-ASR is ambiguous about which words to align against. But it's awkward for human-corrected transcripts that don't strictly derive from one ASR. Leaning: required for `role: readable-cleanup` and `role: display`, optional otherwise.
2. **Default-selection rule when there are multiple `raw-asr` entries.** One default per role, or one global default? Likely **one default per role**, so the viewer can pick a default raw-ASR and a default readable independently.
3. **Inlined vs split for very small transcripts.** A short lecture's transcript might be 30 KB. Forcing it through its own chunk set is overhead. We could allow `payloadRef.inline: true` with the body inline in the main manifest. Probably not worth it for v2.0 — keep one transport, optimize later if a real file needs it.
4. **Should the compat profile *also* mirror the default `readableTranscript`?** Yes for parity, at the cost of more inlined bytes. Worth re-checking once we measure a real v2 file.
5. **OpusTag aggregate size limits.** Ogg has no hard limit, but some tools handle very large comment headers poorly. Worth running `ffprobe`, `mediainfo`, `metaflac`-style tools against a 5-transcript file before declaring v2 stable.
6. **MIME type for the per-transcript body.** We propose `application/vnd.cassini.transcript-words+json` for `cassini.words.v1` bodies, `application/vnd.cassini.transcript-readable+json` for cleanup bodies, etc. — same `vnd.cassini` namespace as v1's manifest MIME.

## Rollout plan

Five steps, each independently shippable.

1. **Spec & schema.** Write `v2.schema.json`, append a v2 section to `docs/portable-meeting-format.md`, add a v2 page to this proposal's discussion. No code changes yet. *Reviewable in one PR.*
2. **Producer compat profile.** Teach `cassini-go-recorder` to emit v2 files with **one** transcript (so we exercise the new tag layout end-to-end without changing user-visible behavior). Compat profile is on by default. Re-run all integration tests; ensure v1 viewer still opens the file. *One PR.*
3. **Consumer.** Teach `cassini-viewer` to detect `version: 2` and use the new loader path. Until step 4 lands, the viewer just shows the one transcript like before. *One PR.*
4. **Producer multi-transcript.** Bundle reader accepts multiple word-transcript inputs (e.g. `transcript.parakeet.words.v1.json` + `transcript.canary.words.v1.json`). Wire up D-277's side-by-side artifact. *One PR.*
5. **Viewer compare/switch UI.** Add a transcript switcher and (later) a diff view. *One PR, design-led.*

## Success criteria

- A `.opus` file containing two raw-ASR transcripts (parakeet + canary) plays in `mpv`/Quod Libet/Apple Music with no warnings.
- `cassini inspect file.opus` lists both transcripts with their provenance.
- The viewer loads the file, displays the default transcript, and switching to the other transcript fetches and parses only that transcript's payload (verified by a debug log of cache hits + a perf trace).
- A v1-only viewer build (last release pre-v2) opens the same v2 file in compat profile and shows the default transcript.
- The PCM-hash integrity check still passes; per-transcript SHA-256 checks also pass.

## References

- v1 spec: `docs/portable-meeting-format.md`
- v1 schema: `spec/cassini-portable-meeting-manifest-v1.schema.json`
- Research brief on format direction: `file-format-report.md`
- Linear D-277 (driving motivation): https://linear.app/code-myriad/issue/D-277
- Linear D-257 (V4 manifest followups, sets the pattern for adding fields safely): https://linear.app/code-myriad/issue/D-257
- Linear D-267 (capacity-limit conversation; future-shape compatible): https://linear.app/code-myriad/issue/D-267
- Producer codepath: `cassini-go-recorder/internal/portable/manifest.go`, `cassini-go-recorder/internal/cassini/portable_meeting.go`
- Consumer codepath: `cassini-viewer/src/viewer/portable.ts`, `cassini-viewer/src/viewer/loadArtifact.ts`
