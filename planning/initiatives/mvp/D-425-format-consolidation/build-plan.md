# D-425 — Collapse to one canonical meeting format (`.opus`)

Build plan for the format-consolidation epic. The Linear issues (D-425 master; D-426…D-431 children) stay lean and point here for design + sequencing.

## Problem

We ship two representations of a meeting and the deployed pipeline depends on the wrong one:

- **`.opus`** — one sealed Ogg/Opus file; the `org.cassini.portable-meeting/2` manifest is embedded in OpusTags; integrity-sealed by exact-PCM SHA-256. The intended user-facing deliverable (`cli.go:355` calls it "the normal user path").
- **`.meeting`** — a *directory* (`cassini.json`, `kind:"meeting"`, `cassini.meeting.v1`) of loose `meeting.webm` + `transcript.words.v1.json` + `manifest.json`. A resumable working/job envelope.

Intent was always one format — `.meeting` is "the extracted working form of one `*.opus` file" (`planning/usability-enhancements-plan.md:69,201`). The collapse was scaffolded (`packMeetingBundle` exists) but never finished, so today `publish` `copyDir`s the bundle into the site (`publish.go:168`) and the published viewer reads loose files. Net: two audio encodings, three manifest schemas, and durable imports that survive only as `.meeting` dirs in `current/` (the "publish overwrites imports" problem).

## Target

`.opus` is the **single durable, user-facing artifact**. `.meeting` becomes a **transient build-scratch directory** — created during record/build/resume, packed into `.opus`, then discarded. It is never an output, a publish input, or a viewer input.

We are not deleting the directory *shape*: a sealed atomic `.opus` cannot represent an in-progress, resumable job, so the working dir stays — but only ephemerally, never as a durable contract.

## Decisions

### D-426 (S1) — catalog `.opus` entry contract

- A `.opus` meeting is a catalog entry with **`audioPath`** pointing at the `.opus` file (relative to `catalog.json`), and **no** `artifactPath`. The viewer loads it via `loadPortableArtifactFromAudioPath`.
- **No catalog schema change.** `catalog.ts` already accepts both fields and requires at least one (`catalog.ts:68-69`). The change is purely *which field `publish` emits*.
- **Multi-transcript:** carried by the embedded v2 manifest's `transcripts[]`; the viewer's `switchPortableTranscript` selects from it. No new catalog field.
- **Transition:** `publish` MAY dual-emit `audioPath` + `artifactPath` behind a flag so an older viewer build still resolves; target steady state is `audioPath` only. The viewer already dispatches on whichever field is present, so dual-emit fully decouples B (D-429) and C (D-430).

### D-427 (S2) — `.opus`↔bundle primitive

- **Forward-only. No general `.opus` → `.meeting` reverse verb.** `packMeetingBundle` (bundle → `.opus`) stays the one primitive.
- `publish` accepts **two inputs**: a ready `.meeting` bundle (which it packs to `.opus` in a temp dir) **or** a `.opus` file (pass-through after `verifyPortableMeetingFile`). Imports (D-428) land as `.opus`, so they flow through the pass-through path — no reconstruction needed.
- If a future flow genuinely needs loose files re-derived from a `.opus` (none today), add a minimal internal `extractMeetingBundle(opus) -> tempDir` used as a cache only — explicitly *not* a durable format. Out of scope until a consumer needs it.

## Per-ticket implementation notes

- **D-429 (B) publish-packs-opus** — in `stagePublishInput`/publish flow: for each ready bundle, call `packMeetingBundle` into a temp `.opus` and stage that single file; emit catalog `audioPath` entries. Add a `.opus` pass-through input branch (per S2). Keep ready/partial skip logic. Replace the `copyDir` of the bundle.
- **D-430 (C) viewer-opus-primary** — route the published-site path through `loadPortableArtifactFromAudioPath`; keep `loadArtifactFromDirectory` as a dev affordance. Harden: HTTP Range manifest fetch, `transcripts[]` switching, large v2 manifests. Own the parity test with B.
- **D-428 (A) import-safety** — make the operator import path produce/store `.opus` in `current/` (or pack on first read) so imports survive once `.meeting` is no longer a publish input. **Deploy before B's prod cutover.**
- **D-431 (D) manifest-retirement** — once nothing durable reads them, drop `cassini.meeting.v1` (`cassini.json`) and `cassini.meeting-artifact.v1` (`manifest.json`) from the durable surface; keep them only in the build temp dir. Standardize on portable v2, v1 read-only. Cleanup sweep + docs. **Last.**

## Parity test (the B≡C gate)

A single meeting loaded two ways — legacy loose-file path vs packed `.opus` — must render identically: transcript words/segments, display timing, readable text, speaker set, and metadata. Guards the `segments[]` → portable `items[]` reprojection in `packMeetingBundle`/`flattenPortableTranscriptItems`. Blocks D-431.

## Sequencing

```
D-426 ─┬→ D-429 ─┐
       └→ D-430 ─┤
D-427 ─┬→ D-428  ├→ D-431 (last)
       └→ D-429  │
   parity (B≡C) ─┘
```

A/B/C parallel; B+C cut over together (dual-emit optional); **A deploys before B drops `.meeting` as a publish input**; D last.

## Risks

- Deployed `publish → site → viewer` currently depends on `.meeting` loose files — this re-plumbs publish *and* the viewer's primary load path, not a struct deletion.
- Durable imports live as `.meeting` in `current/`; sequence A before B (above) or imported meetings vanish on the next native recording.
- Audio differs by design (WebM working vs sealed Opus); consolidation pays a transcode and must preserve exact-PCM integrity.
- Static hosting (george) must honor HTTP Range so the viewer reads the embedded manifest from the `.opus` header without a full download.
