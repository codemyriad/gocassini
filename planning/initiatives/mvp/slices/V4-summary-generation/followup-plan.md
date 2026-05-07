# V4 Summary — Followup Bundle Plan

> Wraps up the loose ends identified in `followups.md` that genuinely belong to the V4 summary slice. Items routed to V6 packaging or other slices remain there. See [`D-257`](https://linear.app/code-myriad/issue/D-257) for full triage.

## Linkage

- **Parent slice:** V4 summary generation ([D-242](https://linear.app/code-myriad/issue/D-242), closed)
- **Triage ticket:** [D-257](https://linear.app/code-myriad/issue/D-257)
- **Source list:** [`followups.md`](./followups.md) — items #1, #3, #5, #9, plus the *map half* of #2.
- **Branch:** continues from `feat/d-242-summary-generation` (or new `feat/d-257-summary-followups`, TBD by dev).
- **Contract owner:** V4 (this slice). The summary template at `cassini-go-recorder/internal/transcribe/templates/summary.v0.md` is still the only authority on summary structure.
- **Consumer:** the artifact `manifest.json` is read by the viewer and by `internal/cassini/portable_meeting.go` (the `.opus` packer). The portable manifest is read by future `.opus` decoders — no live consumer today.

## Items in scope (and why)

| # | Item | Why this slice |
|---|---|---|
| 1 | `manifest.json` lists `summary.md` + records summary model | Self-describing artifact contract; viewer should not rely on filename convention. |
| 9 | `provenanceInfo` records summary model | One-liner that ships with #1. |
| 5 | `CASSINI_SUMMARY_DISABLED=1` env toggle | Closes a real ergonomics gap: today, only way to disable summary is to also disable readable cleanup. |
| 3 | `.opus` packer embeds `summary.md` | Completes the "single file you can email" promise for portable bundles. |
| 2a | `Manifest.Summary` map populated (top-level on portable manifest) | Free metadata (`model`, `templateVersion`, `format`) using values we already have. Bundles naturally with #3. |

## Items deliberately **out of scope**

| # | Item | Why not now |
|---|---|---|
| 2b | `Meeting.Summary string` (on portable manifest's Meeting struct) | Semantic is unsettled — TL;DR? full markdown? first heading? No consumer has asked. Populating it commits us to one. Wait for V6. |
| 4 | Chunking for very long meetings | Watch-and-see; revisit only if a real meeting breaks. |
| 6 | `StrictSummary` mode | Acceptance explicitly wants warm skip; revive when a consumer asks. |
| 7 | Output-quality regression test | Blocked on D-250 demo data. |
| 8, 10 | Already routed to D-251 / D-250 | — |
| 11 | `cassini build` integration tests need ffmpeg | Not V4-caused; spin out as DX/CI ticket. |

## Manifest landscape — keep this distinction visible

The codebase has **two manifests with different roles**. We keep mixing them up; a second orientation pass should not have to re-derive this.

### `manifest.json` — the artifact directory's manifest

- **Where:** written by `internal/transcribe/format.go::WriteManifest` into the meeting output directory.
- **Reads:** the web viewer (via `cassini-viewer/scripts/demo-data-pull.mjs`), and `internal/cassini/portable_meeting.go::loadPortableMeetingSource` when packing a `.opus`.
- **Role:** describes the directory of files (audio, transcripts, captions, **and now summary.md**) plus how each was produced (`provenanceInfo`).
- **Touched by this plan:** add `Summary` to `artifactFiles`, add `MeetingSummary *provStep` to `provenanceInfo`.

### Portable `Manifest` — embedded inside `.opus`

- **Where:** declared in `internal/portable/manifest.go`; encoded gzip+base64 into Vorbis comment tags by `BuildOpusTags`.
- **Reads:** any future `.opus` decoder (`cassini` re-import path, third-party tooling). **No live consumer today.**
- **Has two summary-related fields** that have been unused since they were declared:
  - `Manifest.Summary map[string]any` — *metadata about the summary artifact* (model, format, template version). The `map[string]any` type is a deliberate "no committed schema" escape hatch — populating it adds keys without locking anything.
  - `Meeting.Summary string` — sits alongside `Title`, `DurationMs`, `Language`. Semantic role: *summary content surfaced as a meeting attribute* (a TL;DR, or maybe full markdown). Picking a meaning commits the schema. **No consumer has asked for one yet, so we do not pick.**
- **Touched by this plan:** populate the `Manifest.Summary` map; embed `summary.md` via the existing `Attachments []map[string]any` field; leave `Meeting.Summary` empty.

> **In-code reminder:** commit 3 (or 4) lands a doc-comment block above these two fields in `internal/portable/manifest.go` capturing the map-vs-string distinction, so the next person doesn't re-litigate it from scratch.

## Pre-flight assumptions

- [ ] `internal/transcribe/format.go::WriteManifest` has exactly one caller — `transcribe.go:140`. Signature changes are contained.
- [ ] `writeSummaryArtifact` (`transcribe.go:210`) currently returns only `error`. To make `manifest.json` truthful, it must signal whether `summary.md` was actually written. Refactor to `(hasSummary bool, err error)` mirrors `writeReadableArtifacts`.
- [ ] `cfg.SummaryLLM.Model` is the source of truth for the summary model. Already present in `BuildConfig` (`transcribe.go:18`).
- [ ] The portable `Manifest` already has `Attachments []map[string]any` (`internal/portable/manifest.go:41`). Embedding `summary.md` does not require schema additions — only conventions on the attachment entry.
- [ ] `loadPortableMeetingSource` (`internal/cassini/portable_meeting.go:217`) already parses `manifest.json` into `portableMeetingArtifact`. Extending that struct with a summary entry is the natural place to learn about it.
- [ ] `DefaultBuildConfig` (`transcribe.go:150`) is the single env-wiring site for summary configuration. `CASSINI_SUMMARY_DISABLED` belongs there.
- [ ] `summary_test.go` already exercises configured / unconfigured / failure paths. Toggle behavior is testable through the same surface.

If any of these turns out to be wrong on first inspection, stop and revise the plan rather than coding around it.

## Open technical decisions

### D1. Toggle surface — env only, or env + CLI flag?

- **Recommendation:** env only (`CASSINI_SUMMARY_DISABLED=1`) for v1. Symmetric with the existing `CASSINI_READABLE_STRICT_BATCHES` precedent. CLI flag (`--no-summary`) becomes its own tiny ticket if anyone asks.
- **Alternative:** ship both at once. Cheap but adds CLI surface area before there's demand.
- **Blast radius:** low. Env-only forecloses nothing.
- **Decided:** ☑ accepted

### D2. `Manifest.Summary` map shape

- **Recommendation:** `{"model": cfg.SummaryLLM.Model, "templateVersion": "v0", "format": "markdown"}`. All three are values we already have at write-time.
- **Why not `wordCount`:** would be a new computation (`len(strings.Fields(body))`). Not free. Add only if a consumer asks.
- **Why this is safe:** `map[string]any` is schema-less by design. New fields can be added later without breaking existing decoders.
- **Blast radius:** low.
- **Decided:** ☑ accepted

### D3. Embedding `summary.md` in `.opus`

- **Recommendation:** add an entry to `Manifest.Attachments []map[string]any` of shape `{"name": "summary.md", "mime": "text/markdown", "contentBase64": "<…>"}`. Reuses the existing escape hatch for "additional files in the bundle" — same justification as #2a (no schema commitment).
- **Alternative:** introduce a typed `Manifest.SummaryAttachment` struct. Premature; we have one attachment kind today.
- **Blast radius:** low. Worst case is renaming the keys later; decoders haven't formed.
- **Decided:** ☑ accepted

### D4. `Meeting.Summary string` — leave empty or remove?

- **Recommendation:** **leave empty** (already the default). Removing it would be backwards-incompatible to any future decoder that's already reading the field as `omitempty`. Leaving it documented (see "In-code reminder" above) preserves the option.
- **Decided:** ☑ accepted

### D5. Where to document the two-manifest distinction permanently

- **Recommendation:** doc-comment block in `internal/portable/manifest.go` directly above the two `Summary` fields. Closest to the code, hardest to miss. The longer rationale lives here in this plan; the in-code version is a 6–8 line orientation note pointing back here.
- **Alternative:** new ticket. Worse — tickets close, files persist.
- **Decided:** ☑ accepted

## Commit sequence

Each commit independently builds and `go test ./...` green.

1. **Refactor `writeSummaryArtifact` to return `(bool, error)`.** Mirrors `writeReadableArtifacts`. Update the single caller in `BuildMeetingArtifact`. No behavior change. Tests in `summary_test.go` adjust for the new signature; assertions stay.

2. **Artifact manifest learns about summary (#1 + #9).** In `format.go`:
   - Add `Summary string` to `artifactFiles`.
   - Add `MeetingSummary *provStep` to `provenanceInfo`.
   - Extend `WriteManifest` signature: add `summaryModel string, hasSummary bool` params (or a small `summaryInfo` struct if the param list gets unwieldy).
   - In `BuildMeetingArtifact`, pass `cfg.SummaryLLM.Model` and the new `hasSummary` flag from step 9.
   - Add a unit test that round-trips a manifest with summary present and asserts both fields land in the JSON.

3. **`CASSINI_SUMMARY_DISABLED` env toggle (#5).** In `DefaultBuildConfig`, after `summaryLLM` is constructed, if `envBool("CASSINI_SUMMARY_DISABLED")` is true, force `summaryLLM.APIKey = ""` (so `IsConfigured()` returns false). Add a unit test in `summary_test.go` confirming step 9 no-ops when the toggle is set even though `OPENROUTER_API_KEY` is present.

4. **Portable manifest carries summary metadata + attachment (#3 + #2a).** In `internal/cassini/portable_meeting.go`:
   - Extend `portableMeetingArtifact` to include the new summary file and model fields from `manifest.json`.
   - In `loadPortableMeetingSource`, read `summary.md` from disk if `manifest.json` lists it.
   - In `buildPortableMeetingManifest`, populate `manifest.Summary = map[string]any{...}` (model/templateVersion/format) and append a `summary.md` entry to `manifest.Attachments`.
   - Add a doc-comment block in `internal/portable/manifest.go` above the two `Summary` fields per D5.
   - Update `portable_meeting_test.go` to verify both manifest fields and the attachment entry appear when a summary exists, and that none of the three appear when it doesn't.

5. **Smoke test (manual, no commit).**
   - With `OPENROUTER_API_KEY` set: run `cassini build` against a meeting → confirm `summary.md`, `manifest.json` lists summary + provenance, `.opus` pack contains the attachment + populated `Manifest.Summary` map.
   - Re-run with `CASSINI_SUMMARY_DISABLED=1` → confirm `summary.md` not produced, manifest fields absent, `.opus` packs without attachment.
   - Re-run with `OPENROUTER_API_KEY` unset → same as the disabled-toggle case (sanity check, ensures we didn't regress the existing key-toggle path).

## Verification map

| Followup item | Made true by | Verified by |
|---|---|---|
| #1 Manifest lists `summary.md` | Commit 2 | New unit test in `format_test.go` (or `transcribe_test.go`); smoke step 5 |
| #9 Manifest records summary model | Commit 2 | Same as #1 |
| #5 Toggle disables summary independently | Commit 3 | New unit test in `summary_test.go`; smoke step 5 |
| #3 `.opus` embeds `summary.md` | Commit 4 | Updated `portable_meeting_test.go`; smoke step 5 |
| #2a `Manifest.Summary` map populated | Commit 4 | Same as #3 |

## Process retro

> Fill in *during* the build, not at the end.

### What was unclear coming out of triage?

- _(filled during work)_

### What did this plan miss?

- _(filled during/after work)_

### Suggestion for build-plan template

- _(filled during/after work)_
