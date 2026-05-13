# Build Plan: Portable Meeting Format v2 (Multi-Transcription)

Date: 2026-05-12
Status: Draft
Pairs with: [`multi-transcription-format.md`](./multi-transcription-format.md) — the design proposal.

## Locked decisions

| Decision | Value |
|---|---|
| Compat profile | **Strict.** v2 producers do not write a v1 `transcript` mirror. v1 readers reject. |
| Release ordering | Consumer (step 2) ships to production before producer (step 3) starts emitting v2. |
| Producer v2 emission | Feature-flagged off in production until the v2-capable viewer is live. |
| Reuse | `EncodedPayload`, `ChunkString`, `ProcessingStep`, range-fetch fallback — all reused. |

## Dependency graph

```
              ┌──────────────────────┐
              │ Step 1: Spec & schema │     (docs + JSON schema)
              └──────────┬───────────┘
                         │
              ┌──────────▼───────────┐
              │ Step 2: v2 viewer    │  ← MUST ship to prod before step 3 enables
              │ (TS — read v2, shim) │
              └──────────┬───────────┘
                         │ (production deploy)
              ┌──────────▼───────────┐
              │ Step 3: v2 producer  │  (Go — flag-gated, single-transcript)
              │ (Go — emit v2)       │
              └──────────┬───────────┘
                         │
              ┌──────────▼───────────┐
              │ Step 4: multi-tx     │  (Go — bundle reads N transcripts)
              │ wire D-277 artifact  │
              └──────────┬───────────┘
                         │
              ┌──────────▼───────────┐
              │ Step 5: viewer UI    │  (TS — switch/compare; design-led)
              └──────────────────────┘
```

## Step-by-step file plan

### Step 1 — Spec & schema (one PR, ~1 day)

**New files:**
- `spec/cassini-portable-meeting-manifest-v2.schema.json` — v2 schema with `transcripts: array (1..N)`, `readableTranscripts: array (0..N)`, `provenance.speechToText: map<id, ProcessingStep>`, `provenance.readableCleanup: map<id, ProcessingStep>`. `additionalProperties: false` at every level the v1 schema sets it.

**Modified files:**
- `docs/portable-meeting-format.md` — append a "v2 (Multi-Transcription)" section after the existing v1 content; mark v1-only sections explicitly where they diverge.

**Tests:** Schema self-validation (compile against the JSON Schema 2020-12 metaschema). Hand-built example v2 manifest in `spec/examples/v2-two-transcripts.json` validated by CI.

**Done when:** Schema lints clean; example manifest validates; docs render.

### Step 2 — v2-capable viewer (one PR, ~3 days)

**Modified files:**
- `cassini-viewer/src/viewer/portable.ts`:
  - `PortableMeetingManifest` interface gains `version`, `transcripts?`, `readableTranscripts?`, keeps v1 fields as optional for the shim path.
  - `extractPortableManifestFromArrayBuffer` returns the index manifest only (no `transcript.items` decoded yet).
  - New `loadPortableTranscriptBody(audioUrl, manifest, transcriptId)`: resolves the `payloadRef` to its chunk set in the OpusTags, decodes (base64url → gzip → JSON), verifies SHA-256, caches the parsed body by `(audioUrl, transcriptId)`.
  - New `synthesizeV2FromV1(manifest)`: takes a v1 manifest and produces a virtual v2 shape with one transcript entry, ids `raw-asr` and `readable`. Strict path: also surface "newer format" UI hook when `version > 2` is encountered.
- `cassini-viewer/src/viewer/loadArtifact.ts`:
  - `LoadedArtifact` gains `availableTranscripts: TranscriptDescriptor[]` and `currentTranscriptId: string`.
  - On initial load, eagerly resolve the default transcript so the existing UI keeps rendering on first paint with no code changes downstream.
  - Add `switchTranscript(id): Promise<LoadedArtifact>` that lazy-loads alternate bodies via `loadPortableTranscriptBody`.
- `cassini-viewer/src/viewer/portable.test.ts`: keep v1 fixtures green via the synthesized-array path; add v2 fixtures (single-transcript and dual-transcript).

**Performance:**
- Widen `PORTABLE_METADATA_RANGE_END` from 256 KB to 1 MB. Cheap; covers ~3-5 transcripts of typical size before the full-fetch fallback engages.

**Tests (see coverage diagram below).** **Done when:** v1 fixtures still load; v2 fixtures load with default transcript; switching fetches the alternate body once and serves from cache thereafter; PCM-hash integrity check still runs.

### Step 3 — v2 producer, single-transcript, flag-gated (one PR, ~3 days)

**Modified files:**
- `cassini-go-recorder/internal/portable/manifest.go`:
  - Add `TranscriptEntry`, `PayloadRef`, `ReadableTranscriptEntry` types.
  - `Manifest.Transcripts []TranscriptEntry`, `Manifest.ReadableTranscripts []ReadableTranscriptEntry`. Keep `Manifest.Transcript` / `ReadableTranscript` for the v1-read path (Go inspect command reads existing v1 files).
  - New `EncodeTranscriptBody(body, chunkSize) → EncodedPayload`: re-uses `ChunkString` + the existing gzip+base64url pipeline.
  - `EncodeManifest` returns `(MainPayload, []NamedPayload{prefix, payload})` instead of one `EncodedPayload`. Main payload contains the index; named payloads are per-transcript bodies.
  - `BuildOpusTags` learns to emit per-transcript chunk sets (`CASSINI_TX_<ID>_PAYLOAD_*`), descriptors, and `CASSINI_TRANSCRIPT_IDS` / `CASSINI_TRANSCRIPT_DEFAULT`.
  - Constant `FormatV2 = "org.cassini.portable-meeting/2"`; constant `PayloadSchemaV2`.
- `cassini-go-recorder/internal/cassini/portable_meeting.go`:
  - Bundle artifact JSON: `files.transcript` (singular) stays as a v1-only path. Add `files.transcripts []FileTranscript` for v2 bundles.
  - Loader for v2 bundles wires multiple transcript JSON files into `Manifest.Transcripts`.
- `cassini-go-recorder/internal/inspect/portable_audio.go`:
  - When manifest is v2, print one block per transcript with provenance.
- Feature flag: env var `CASSINI_FORMAT_V2=1` to enable v2 emission. Off by default. Producer code path is fully present; just behind a guard at the entry point.

**Validation:** Reserved transcript ids (`payload`, `format`, `audio`, etc.) rejected at the producer with a clear error.

**Done when:** With the flag on, packer produces a v2 single-transcript `.opus` file; `ffprobe` shows the per-transcript chunk sets; the v2 viewer (already in production) reads it correctly; v1 viewer surfaces "newer format" message. Round-trip integration test green.

### Step 4 — Producer multi-transcript + D-277 wiring (one PR, ~2 days)

**Modified files:**
- `cassini-go-recorder/internal/cassini/portable_meeting.go`: bundle reader accepts multiple word-transcript files (`transcript.parakeet.words.v1.json` + `transcript.canary.words.v1.json`) plus per-transcript provenance.
- `cassini-go-recorder/internal/transcribe/...`: produce parakeet + canary transcripts in the same pipeline run (D-277's actual work; touched here only for the bundle hand-off).
- Flag flip: producer emits v2 by default in CI for D-277's demo meetings.

**Done when:** A real meeting from the D-277 evaluation set has both engines' transcripts in one `.opus` file; the viewer loads and lets you switch.

### Step 5 — Viewer switch/compare UI (separate PR, design-led)

Out of scope for this plan in detail. Design pass before code. Transcript switcher first; diff view as a follow-up.

## Test coverage diagram

```
CODE PATHS                                                          USER FLOWS
[+] internal/portable/manifest.go                                   [+] v2 producer → v2 file → v2 viewer
  ├── NormalizeManifest                                               ├── [GAP] single-transcript happy path
  │   ├── [GAP] v2 normalization (transcripts[] default selection)    ├── [GAP] dual-transcript happy path, switch
  │   ├── [GAP] reject more than one default per role                 └── [GAP] [→E2E] ffprobe sees both chunk sets
  │   └── [GAP] reject reserved transcript ids                      [+] v2 producer → v2 file → v1 viewer
  ├── EncodeManifest                                                  └── [GAP] surfaces "newer format" cleanly
  │   ├── [GAP] returns main payload + per-transcript payloads      [+] v2 viewer → v1 file (shim)
  │   └── [GAP] determinism: same input → byte-identical output       └── [GAP] synthesized single-entry array
  ├── BuildOpusTags                                                 [+] Switch + cache
  │   ├── [GAP] emits CASSINI_TRANSCRIPT_IDS, _DEFAULT                ├── [GAP] switch to alternate fetches body once
  │   ├── [GAP] emits per-transcript chunk sets                       └── [GAP] switch back hits cache, no refetch
  │   └── [GAP] id → tag-name encoding (upper, _ instead of -)      [+] Failure paths
  └── (new) EncodeTranscriptBody                                      ├── [GAP] per-transcript SHA-256 mismatch surfaces
      └── [GAP] sha + size + chunk count agree with descriptor       ├── [GAP] missing payload chunk surfaces
                                                                      ├── [GAP] PCM-hash mismatch on v2 file
[+] internal/cassini/portable_meeting.go                              └── [GAP] body integrity ok but main manifest
  ├── [GAP] bundle: files.transcripts loads N entries                          sha mismatch surfaces
  └── [GAP] bundle: v1 files.transcript still works              [+] inspect CLI
                                                                      └── [GAP] cassini inspect lists both transcripts
[+] internal/inspect/portable_audio.go                                          with their provenance
  └── [GAP] v2 manifest prints per-transcript blocks

[+] cassini-viewer/src/viewer/portable.ts
  ├── extractPortableManifestFromArrayBuffer
  │   ├── [GAP] v2: returns index, no body items
  │   └── [GAP] v1: returns full manifest including transcript.items
  ├── synthesizeV2FromV1
  │   └── [GAP] one-entry array, correct ids
  ├── loadPortableTranscriptBody
  │   ├── [GAP] resolves payloadRef, decompresses, parses
  │   ├── [GAP] caches by (audioUrl, transcriptId)
  │   └── [GAP] sha-256 mismatch throws, doesn't poison cache

[+] cassini-viewer/src/viewer/loadArtifact.ts
  ├── [GAP] LoadedArtifact eagerly resolves default
  └── [GAP] switchTranscript returns updated LoadedArtifact

COVERAGE TARGET: every GAP listed gets a test before step lands.
LEGEND: [→E2E] = end-to-end ffprobe-or-real-browser test required.
```

**E2E tests required:**
- `ffprobe` against a produced v2 file must show the new tags discoverable.
- v1 file in v2 viewer must render in a real browser test (existing playwright/jsdom harness).
- D-277 demo file (parakeet + canary) loads and switches in the viewer in a real browser.

**Eval tests:** None — this is a format change, not a model change.

## Failure modes

| Failure | In test plan? | Error handling? | User-visible? |
|---|---|---|---|
| Per-transcript SHA-256 mismatch | yes (GAP) | yes — surface "transcript corrupt" in viewer; refuse to render that body, allow switch to others | yes, clear message |
| Missing `CASSINI_TX_<ID>_PAYLOAD_NNN` chunk | yes (GAP) | yes — same as v1's missing-chunk error | yes, clear message |
| Main manifest sha mismatch | yes (GAP) | yes — same as v1 path | yes |
| PCM hash mismatch (audio edited) | yes (GAP) | yes — existing v1 path applies unchanged | yes |
| OpusTag header too large for some tag tool | **flagged**, not blocking | n/a — third-party tool problem | the file still plays |
| Reserved transcript id used | yes (GAP) | yes — producer refuses with explicit error | producer-time only |
| More than one default per role | yes (GAP) | yes — producer refuses | producer-time only |
| v1 viewer reading v2 file | yes (GAP) | yes — "newer format" message | yes — this is the strict-profile contract |
| Range fetch shorter than tag header | yes — existing fallback path | yes — full-fetch fallback kicks in | no |

**No silent failures identified.** Every failure either throws at producer time, surfaces an error in the viewer, or is structurally impossible.

## Worktree parallelization

Lanes after step 1 (spec) lands:

| Lane | Step | Module touched | Depends on |
|---|---|---|---|
| A | 1 — Spec & schema | `spec/`, `docs/` | — |
| B | 2 — v2 viewer | `cassini-viewer/src/viewer/` | A |
| C | 3 — v2 producer (flag off) | `cassini-go-recorder/internal/portable,cassini,inspect/` | A |
| D | 4 — multi-transcript | `cassini-go-recorder/internal/transcribe,cassini/` | C |
| E | 5 — switch/compare UI | `cassini-viewer/src/` | B + D (in production) |

**Execution:** Launch B and C in parallel worktrees once A merges. D follows C in the same worktree. E follows B+D being live in production.

**Conflict flags:** Lanes B and C touch disjoint module directories — safe. Lane D shares directories with C and must wait. Lane E shares with B but on different concerns (loader vs UI components) — low conflict risk, sequential is safer.

## What already exists (do not rebuild)

- Base64url+gzip+chunk pipeline: `internal/portable/manifest.go` — `EncodeManifest`, `ChunkString`. Reuse as-is, just call N+1 times.
- `ProcessingStep` type for provenance — reuse, key by transcript id.
- `fetchPortableManifest` range-fetch with full-file fallback in `loadArtifact.ts` — reuse, only widen the range constant.
- v1 transcript body JSON shape (`cassini.words.v1`) — bytes unchanged, just moved from `manifest.transcript` to `payloadRef`.
- PCM-hash integrity check — unchanged, runs against the same audio bytes.

## NOT in scope

- **Viewer cache retention policy** — step 5 ships with module-level `portableManifestCache` + `portableBodyCache` that grow unbounded across a session. Acceptable for current one-or-two-meetings-per-session usage. A future control-panel surface will let users decide retention policy (e.g. "keep last N meetings", "prune on tab idle", "clear on demand") and prune accordingly. Owner: control-panel work, not this PR.
- **Viewer compare/diff UI** — step 5; design-led; separate plan.
- **Backfilling existing v1 files to v2** — v1 → v2 shim covers reading; no rewrite of historical files. If/when we need it, separate effort.
- **Confidence scores, alternative hypotheses, phonetic data** — proposal flags as future. Format already accommodates per-body `format` versioning so a `cassini.words.v2` body type can be added later without rebuilding any of this.
- **Audio source maps** — `file-format-report.md` lists this as a separate gap. Independent of multi-transcription; separate proposal.
- **Multiple `displayTranscript` entries** — v2 schema allows it structurally (same array shape) but no producer pipeline plans to emit more than one. Defer the UI question until someone needs it.
- **Cross-file diff** — comparing transcripts across different meeting files. Different problem entirely.

## Decisions still open (resolve during step 1 schema work)

1. **`sourceTranscriptId` requirement.** Required for `role: readable-cleanup` and `role: display` (cleanup without a source ASR is ambiguous to align). Optional for `human-corrected` and `translation`. Schema authors confirm during step 1.
2. **One default per role vs global default.** Lean: per role. Schema authors confirm during step 1.
3. **OpusTag aggregate size sanity check.** Before step 2 viewer lands: build a 5-transcript test fixture; run `ffprobe`, `mediainfo`, `metaflac` against it; record results in step 1 PR. Cheap; one afternoon.
4. **Viewer initial-range size.** Plan widens to 1 MB; revisit if the empirical fixture above shows trouble.
5. **Reserved transcript ids list.** Step 3 producer rejects ids matching v1 descriptor names (`payload`, `format`, `audio`, `meeting`, `integrity`, `transcript`, `provenance`). Final list locked during step 3.

## Effort summary

| Step | Human estimate | CC + gstack estimate |
|---|---|---|
| 1 — Spec & schema | ~1 day | ~30 min |
| 2 — v2 viewer | ~3 days | ~2 hours |
| 3 — v2 producer (flag off) | ~3 days | ~2 hours |
| 4 — Multi-transcript + D-277 | ~2 days | ~1 hour |
| 5 — Switch/compare UI | ~5 days (design-led) | ~3 hours |
| **Total** | ~2 weeks engineer-time | ~8-9 hours of AI-assisted work |

## Done criteria

- v2 schema validates the worked example.
- v2 viewer reads v1 files unchanged and reads single-transcript v2 files.
- v2 producer (flag on) writes a single-transcript file that the v2 viewer reads, that `ffprobe` inspects, and that v1 viewer surfaces a "newer format" message for.
- D-277 demo file (parakeet + canary in one `.opus`) loads in the viewer and switches.
- Every GAP in the coverage diagram has a test.
- PCM-hash integrity check still passes; per-transcript SHA-256 checks pass.

## References

- Design proposal: [`docs/proposals/multi-transcription-format.md`](./multi-transcription-format.md)
- v1 spec: [`docs/portable-meeting-format.md`](../portable-meeting-format.md)
- v1 schema: [`spec/cassini-portable-meeting-manifest-v1.schema.json`](../../spec/cassini-portable-meeting-manifest-v1.schema.json)
- Linear: [D-277](https://linear.app/code-myriad/issue/D-277)
