# Proposal: Multi-Transcription Support in the Portable Meeting Format

Date: 2026-05-12
Status: Draft for discussion

## TL;DR

The Cassini Portable Meeting format (v1, `org.cassini.portable-meeting/1`) carries exactly one transcript per file. We propose a backward-compatible v2 that lets a single `.opus` file hold **multiple transcriptions of the same audio** — e.g. Parakeet 0.6B and Canary 1B side by side — so the viewer can switch or compare without producing duplicate files.

Recommended shape: each transcript body lives in its **own OpusTag chunk set** with its own SHA-256 and gzip envelope. The main manifest carries an *index* of available transcriptions plus their provenance. The viewer decompresses and parses only the body it displays; each body is independently hash-verifiable; adding or removing a transcript leaves the others untouched.

## Motivation

[D-277 — Swap STT model to canary-1b-v2](https://linear.app/code-myriad/issue/D-277) requires publishing the new transcript "next to the parakeet one for side-by-side inspection" as part of its Definition of Done. Today the only ways to do that are two `.opus` files or a sidecar JSON — neither matches the format's "one file, plays anywhere" promise.

The same shape covers four other cases that have come up: keeping the previous engine's transcript when we swap STT models (audit + regression review for free); multiple cleanup-LLM runs against the same raw ASR; a human-corrected transcript alongside raw ASR; and multilingual recordings (e.g. an Italian transcript + a machine-translated English transcript).

The v1 schema closes off all of these: `transcript`, `provenance.speechToText`, and `readableTranscript` are each required to be a single object.

## Constraints to preserve

From the v1 design principles in `docs/portable-meeting-format.md`:

1. **Progressive enhancement.** File plays as plain `.opus` in any audio player.
2. **Self-describing.** `ffprobe` + standard tools can discover and decode the metadata.
3. **Integrity.** All transcripts describe the *same* PCM audio; the v1 `pcmSha256` guard still applies.
4. **Backward compatibility.** A v1-aware consumer reading a v2 file gets a working single-transcript view or a clear "newer format" message — never a hard error.

## Parse cost: what splitting actually saves

The question that started this proposal: if we put N transcriptions in one JSON blob, do we parse them all?

Yes — `JSON.parse` is a whole-document operation. With one blob, every load decompresses and parses every transcript. Empirically that's affordable for small N: a 1-hour word-timed transcript is ~720 KB uncompressed JSON (~80 bytes per item × ~9000 words), gzip ~220 KB, V8 `JSON.parse` ~5–8 ms. Two transcripts ~15 ms, five ~40 ms.

So parse cost alone doesn't force a split for our current sizes. What splitting actually buys is structural: each body has its own SHA-256 and is independently verifiable; a corrupt or oversized body doesn't taint the others; bodies of different `format` versions can coexist (e.g. legacy `cassini.words.v1` next to a future `cassini.words.v2`); and we can add or remove a transcript without rewriting the main manifest hash. Skipping `gzip + JSON.parse` for the bodies you don't display comes along for the ride. (Tag extraction from the Ogg comment header is byte slicing and stays cheap regardless.)

## Design options

**A — one blob with an array inside.** Add `transcripts: []` to the existing manifest payload. Pros: simplest schema change, no new tag namespaces. Cons: pay decompress + parse for everything every time; can't hash-verify individual transcripts; one bad body taints the manifest; pushes harder on OpusTag size.

**B — separate OpusTag chunk set per transcript (recommended).** Main manifest stays small and carries an index; each transcript body has its own `CASSINI_TX_<ID>_PAYLOAD_*` chunk set with its own descriptor and SHA-256. Pros: independent integrity per body, lazy-decompress on demand, free room for different body `format` versions, naturally handles the multi-readable case. Cons: more tag namespaces; consumer plumbing is a step longer (resolve descriptor → fetch chunks → decompress → parse).

**C — embed each transcript as a base64 `attachments` entry.** Pros: zero schema churn. Cons: attachments are *opaque* to the spec; they don't have first-class provenance, default-selection, or `format` discriminators. Loses the structural meaning of "this is a transcription you can switch to."

**D — external sidecar files.** Already rejected in v1; breaks the one-file portability promise.

## Recommendation

**Option B**, with three guardrails:

1. **Exactly one `default: true` transcript per role** (one default `raw-asr`, one default `readable-cleanup`, etc.). v1-aware tools that read only the legacy single-transcript slot can resolve to the default via the compat profile (below).
2. **Tag naming rule**: `CASSINI_TX_<UPPER_SNAKE_ID>_PAYLOAD_NNN`. Transcript ids match `^[a-z0-9][a-z0-9_-]{0,31}$`; the tag form uppercases and turns `-` into `_`. Each chunk set carries its own `_CHUNK_COUNT`, `_SHA256`, `_RAW_BYTES`, `_GZIP_BYTES`, `_ENCODING`, `_MIME` descriptors — same self-describing discipline as v1's `CASSINI_PAYLOAD_*`.
3. **Bump the format URI** to `org.cassini.portable-meeting/2`. v2 producers MAY additionally emit a v1-style top-level `transcript` mirror of the default so v1 readers degrade to a single-transcript view rather than failing.

## v2 manifest sketch

Changes from v1:

| v1 | v2 |
|---|---|
| `transcript: object` | `transcripts: array` (1..N), plus optional v1-compat `transcript` mirror of the default |
| `readableTranscript: object` | `readableTranscripts: array` (0..N) |
| `provenance.speechToText: ProcessingStep` | `provenance.speechToText: map<transcriptId, ProcessingStep>` |
| `provenance.readableCleanup: ProcessingStep` | `provenance.readableCleanup: map<transcriptId, ProcessingStep>` |
| `version: 1`, schema URL `/v1.schema.json` | `version: 2`, schema URL `/v2.schema.json` |

The transcript `id` doubles as the provenance key — no separate `provenanceRef` field. If a transcript entry has `id: "canary"`, look up `provenance.speechToText.canary`.

A per-transcript entry:

```jsonc
{
  "id":       "canary",                  // unique within file
  "role":     "raw-asr",                 // raw-asr | readable-cleanup | display | human-corrected | translation
  "default":  true,                      // exactly one default per role
  "format":   "cassini.words.v1",        // body format version
  "language": "en",                      // BCP-47 of the body
  "wordCount": 9224,
  "sourceTranscriptId": "canary",        // for derived roles (cleanup/display); optional otherwise
  "createdAtUtc": "2026-05-12T...",
  "payloadRef": {
    "prefix":     "CASSINI_TX_CANARY_PAYLOAD_",
    "chunkCount": 14,
    "sha256":     "...",                 // sha256 of the decompressed body JSON
    "rawBytes":   718432,
    "gzipBytes":  221110,
    "mime":       "application/vnd.cassini.transcript-words+json",
    "encoding":   "base64url+gzip+utf8json"
  }
}
```

The body referenced by `payloadRef` is exactly the shape today's `transcript` carries — `{ format, language, wordCount, items[] }`. Migrating a v1 transcript to a v2 entry is a copy plus a descriptor rewrite; the body itself doesn't change.

## OpusTag layout

In addition to v1's tags:

```
CASSINI_FORMAT=org.cassini.portable-meeting/2
CASSINI_PAYLOAD_SCHEMA=https://cassini.local/spec/cassini-portable-meeting-manifest-v2.schema.json

# main manifest (small: index + provenance + meeting metadata)
CASSINI_PAYLOAD_CHUNK_COUNT=<N>
CASSINI_PAYLOAD_SHA256=<hex>
CASSINI_PAYLOAD_RAW_BYTES=...
CASSINI_PAYLOAD_GZIP_BYTES=...
CASSINI_PAYLOAD_000..N=<chunks>

# discoverable from ffprobe alone
CASSINI_TRANSCRIPT_IDS=parakeet,canary
CASSINI_TRANSCRIPT_DEFAULT=canary

# per-transcript chunk set (one block per transcript)
CASSINI_TX_PARAKEET_PAYLOAD_MIME=application/vnd.cassini.transcript-words+json
CASSINI_TX_PARAKEET_PAYLOAD_ENCODING=base64url+gzip+utf8json
CASSINI_TX_PARAKEET_PAYLOAD_CHUNK_COUNT=14
CASSINI_TX_PARAKEET_PAYLOAD_SHA256=<hex>
CASSINI_TX_PARAKEET_PAYLOAD_RAW_BYTES=...
CASSINI_TX_PARAKEET_PAYLOAD_GZIP_BYTES=...
CASSINI_TX_PARAKEET_PAYLOAD_000..13=<chunks>

CASSINI_TX_CANARY_PAYLOAD_...
```

The v1 `CASSINI_DECODE_HINT` extends to mention the two-layer decode: concatenate `CASSINI_PAYLOAD_*` for the manifest, concatenate `CASSINI_TX_<ID>_PAYLOAD_*` for a body, each chunk set decodes as base64url → gzip → UTF-8 JSON.

## Compatibility

**v1-aware consumers reading a v2 file: strict profile.** v2 producers do not write a top-level `transcript` mirror. v1 readers see `version: 2` and surface "newer format; cannot fully render." This means the viewer (step 3 in the rollout) must ship to production before producer step 2 enables v2 emission — otherwise users with the older viewer cached see a broken file. The producer's v2-emission path is feature-flagged off until the v2-capable viewer is live.

(Considered and rejected: a compat profile that mirrored the default transcript inline so v1 viewers still rendered. Cost was tolerable, but it complicated the schema and the consumer logic, and we control both ends of the pipeline today — no third-party v1 reader is in the wild that we need to keep happy.)

**v2-aware consumers reading a v1 file.** `version: 1` keeps working unchanged: the v2 loader treats the v1 top-level `transcript` / `readableTranscript` as a single-entry virtual array with synthesized ids. This shim is the only backward direction we maintain.

## Code impact

**Producer — `cassini-go-recorder`:**
- `internal/portable/manifest.go`: `Manifest.Transcript` → `Manifest.Transcripts []TranscriptEntry`; add `PayloadRef`; `EncodeManifest` returns the main payload plus one encoded payload per transcript; `BuildOpusTags` emits the per-transcript chunk sets.
- `internal/cassini/portable_meeting.go`: artifact bundle's `Files.Transcript` becomes `Files.Transcripts []FileTranscript`. Single-transcript bundles produce a v2 file with one entry.
- `internal/inspect/portable_audio.go`: list transcripts in `cassini inspect` output.

**Consumer — `cassini-viewer`:**
- `src/viewer/portable.ts`: `extractPortableManifestFromArrayBuffer` extracts the main manifest only; add `loadPortableTranscriptBody(manifest, id)` that resolves a `payloadRef` to its chunk set, decompresses, parses, caches.
- `src/viewer/loadArtifact.ts`: `LoadedArtifact` gains `availableTranscripts` and `currentTranscriptId`; switching swaps which body is materialized.
- `PORTABLE_METADATA_RANGE_END` (currently 256 KB) frames the initial range request that pulls the OpusTags. With split bodies the tag header is larger; the existing full-file fallback still works, but we should widen the initial range or measure how often the fallback triggers — otherwise the optimization quietly stops paying off.
- `src/viewer/portable.test.ts`: v1 fixtures stay green via the synthesized-array path; add v2 fixtures.

**Spec & docs:**
- `spec/cassini-portable-meeting-manifest-v2.schema.json` (new).
- `docs/portable-meeting-format.md`: append v2 section; mark v1 sections where they diverge.

## Open questions

1. **Required `sourceTranscriptId`?** Required for `role: readable-cleanup` and `role: display` (a cleanup without a known source ASR is ambiguous to align). Optional for `human-corrected` and `translation`.
2. **Default selection across roles.** One default per role (so the viewer picks a default raw-ASR and a default readable independently), or one global default? Leaning per role.
3. **Inline tiny bodies?** A 30 KB transcript through its own chunk set is overhead. We could allow `payloadRef.inline: true` later; not worth it for v2.0.
4. **OpusTag aggregate size with many transcripts.** Ogg has no hard limit, but some tools handle very large comment headers poorly. Validate against `ffprobe`, `mediainfo`, and other tag tooling with a 5-transcript file before declaring v2 stable.
5. **Viewer initial-range size.** Decide whether to widen `PORTABLE_METADATA_RANGE_END`, or just rely on the full-file fallback when the index is past 256 KB.
6. **Reserved transcript ids.** Block ids that collide with descriptor tag names (`payload`, `format`, etc.) at the producer level rather than discovering it at decode time.

## Rollout

Order matters under the strict profile: v2-capable viewer ships before producers start writing v2. Each step independently shippable:

1. **Spec & schema.** v2 schema JSON, v2 section in `docs/portable-meeting-format.md`, no code yet.
2. **Consumer.** Viewer detects `version: 2` and uses the new loader path. Ships to production before any producer emits v2. v1 files still work via the synthesized-array shim.
3. **Producer v2 emission (feature-flagged off by default).** Recorder can emit v2 files with one transcript; flag stays off in production until the v2 viewer is live.
4. **Producer multi-transcript.** Bundle reader accepts multiple word-transcript inputs; wire to D-277's side-by-side artifact. Flip the feature flag.
5. **Viewer switch/compare UI.** Transcript switcher first, diff view later. Design-led.

See the companion build plan in [`docs/proposals/multi-transcription-format-plan.md`](./multi-transcription-format-plan.md) for file-level changes, test coverage, and parallelization.

## Success criteria

- A `.opus` with two raw-ASR transcripts (parakeet + canary) plays in `mpv` / Quod Libet / Apple Music with no warnings.
- `cassini inspect` lists both transcripts and their provenance.
- The viewer displays the default transcript on load and, on switch, decompresses + parses only the requested body (verifiable in a perf trace).
- A pre-v2 viewer build opens the same file in compat profile and shows the default transcript.
- PCM-hash integrity check still passes; per-transcript SHA-256 checks also pass.

## References

- v1 spec: `docs/portable-meeting-format.md`
- v1 schema: `spec/cassini-portable-meeting-manifest-v1.schema.json`
- Research brief on format direction: `file-format-report.md`
- [D-277](https://linear.app/code-myriad/issue/D-277) — driving motivation
- [D-257](https://linear.app/code-myriad/issue/D-257) — pattern for adding manifest fields safely
- [D-267](https://linear.app/code-myriad/issue/D-267) — capacity discussion, future-shape compatible
- Producer code: `cassini-go-recorder/internal/portable/manifest.go`, `internal/cassini/portable_meeting.go`
- Consumer code: `cassini-viewer/src/viewer/portable.ts`, `src/viewer/loadArtifact.ts`
