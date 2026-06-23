# V4 (D-242) — Summary generation in core pipeline

## Linkage

- **Ticket:** https://linear.app/code-myriad/issue/D-242 (status: Todo → In Progress, assignee: Chris De Sousa)
- **Slice doc:** `planning/initiatives/mvp/slices.md` (search "V4")
- **Per-slice shaping doc:** none — D-242 ticket carries full scope, AC, demo checklist, code areas, and implementation notes. No additional shaping needed (see retro for whether this judgment held).
- **Contract owner:** V0 (D-240, closed). The summary template is `cassini-go-recorder/internal/transcribe/templates/summary.v0.md` and is the only authority on summary structure. This plan does not redefine it.
- **Consumer:** V3 (D-241, closed). The viewer reads an optional `summary.md` sidecar per meeting (`cassini-viewer/scripts/demo-data-pull.mjs:144`). What V4 emits is what V3 will render.

## Pre-flight assumptions

- [x] D-240 closed; V0 template is at `cassini-go-recorder/internal/transcribe/templates/summary.v0.md` with six fixed sections (Overview, Key Points, Decisions, Action Items, Open Questions, Next Step).
- [x] D-241 closed; the viewer reads `summary.md` next to the other meeting artifacts as an *optional* sidecar (404 tolerated).
- [x] `internal/transcribe/llm.go` exposes `chatCompletion(cfg, system, user) (string, error)` and is reusable as-is.
- [x] `internal/transcribe/transcribe.go:117` step 8 (LLM readable cleanup) is the structural model for step 9: gate on `cfg.LLM.IsConfigured()`, warn-and-skip on error, pipeline succeeds either way.
- [x] `internal/portable/manifest.go:40,70` declare `Manifest.Summary map[string]any` and `Meeting.Summary string` fields that are currently unused. We will not populate them in V4. Risk: premature schema lock-in.
- [x] LLM env wiring (`OPENROUTER_API_KEY`, `OPENROUTER_BASE_URL`/`LLM_BASE_URL`, `LLM_MODEL`) already populates `cfg.LLM`. We only add an optional `SUMMARY_MODEL` override.
- [x] `internal/transcribe/transcribe_test.go` already uses a `readableCleanupFn` package-var mocking pattern — the new `buildMeetingSummaryFn` mirrors it.
- [x] `internal/transcribe/transcribe.go` does not currently expose post-cleanup segments to its caller; `writeReadableArtifacts` returns only `(hasReadable, err)`. Plan needs a small refactor of that signature so step 9 can use cleaned text without re-reading the file.

## Open technical decisions

### D1. Prompt strategy — single-shot vs. section-by-section
- **Recommendation:** single-shot. Six small sections, atomic output, one round-trip.
- **Alternative:** per-section, only if quality is bad in evaluation.
- **Blast radius:** low.
- **Decided:** ☑ accepted.

### D2. Input source — readable transcript vs. raw words
- **Recommendation:** use cleaned (readable) segments when present; fall back to raw segments otherwise. Implement by changing `writeReadableArtifacts` to also return its applied segments, and let step 9 receive whichever is appropriate.
- **Blast radius:** low (helper signature change, three existing tests adjust).
- **Decided:** ☑ accepted.

### D3. Long-meeting handling — chunk or fail?
- **Recommendation:** no chunking in v1. If the LLM rejects on size, the warn-and-skip gate produces no summary and the pipeline succeeds. Revisit if real meetings break.
- **Blast radius:** low; chunking is additive.
- **Decided:** ☑ accepted.

### D4. Model selection — reuse `LLM_MODEL` or new `SUMMARY_MODEL`?
- **Recommendation:** new `SUMMARY_MODEL` env var. Defaults to `LLM_MODEL`, which itself defaults to `openai/gpt-4o-mini`. Operators can pick a stronger frontier model for summaries without changing the cleanup model.
- **Blast radius:** low.
- **Decided:** ☑ accepted.

### D5. Manifest population — sidecar only, or also `Manifest.Summary` fields?
- **Recommendation:** sidecar only for v1. Viewer doesn't read manifest summary fields. Filling them now risks premature schema lock-in. Track for V6 in Followups.
- **Blast radius:** medium if wrong (schema sticks).
- **Decided:** ☑ accepted.

### D6. Failure semantics — warn-and-skip vs. fail
- **Recommendation:** warn-and-skip, mirroring step 8. Preserves AC "disabling summary generation does not break the pipeline."
- **Blast radius:** low.
- **Decided:** ☑ accepted.

### D7. Prompt content — embed template via `go:embed` vs. inline string
- **Recommendation:** `//go:embed templates/summary.v0.md`. Template stays single source of truth.
- **Blast radius:** low.
- **Decided:** ☑ accepted.

### D8. Tests — unit only or integration?
- **Recommendation:** unit only. Add `buildMeetingSummaryFn` package var (mirrors `readableCleanupFn`) so the test substitutes an in-memory fake. No live LLM in CI.
- **Blast radius:** low.
- **Decided:** ☑ accepted.

## Commit sequence

1. **Add `summary.go`** in `internal/transcribe`. Defines `BuildMeetingSummary(cfg LLMConfig, streams []AudioStream, segments []Segment) (string, error)`. Embeds the V0 template via `go:embed`. Builds a system prompt that pins headings and section formats; user prompt is the transcript flattened to `Label: text` lines. Calls `chatCompletion`. Returns the markdown body.
2. **Refactor `writeReadableArtifacts`** to also return the applied (cleaned) segments. Update existing tests to the new signature.
3. **Add step 9 in `BuildMeetingArtifact`** in `transcribe.go`. After step 8: pick cleaned segments when available, otherwise raw; call new `writeSummaryArtifact` helper; gate on `cfg.SummaryLLM.IsConfigured()`; warn-and-skip on error; write to `summary.md` next to other artifacts. Add `buildMeetingSummaryFn` package var.
4. **Add `SUMMARY_MODEL` env override** in `DefaultBuildConfig` and a `SummaryLLM LLMConfig` field on `BuildConfig`. Defaults: clone of `LLM`, override Model from `SUMMARY_MODEL` if set.
5. **Add tests** in `summary_test.go`: skip-if-not-configured (no error, no file), warn-and-skip on LLM error (no error returned, warning in stdout, no file written), success path (mock fn returns markdown, file appears with that exact content). Snapshot the system prompt to lock the contract with the template.
6. **Smoke test (manual, no commit).** Pull demo data → set `OPENROUTER_API_KEY` and `SUMMARY_MODEL` → run `cassini build` against a meeting → confirm `summary.md` appears next to `transcript.readable.v1.json` → open the meeting in the viewer → confirm rendering. Then unset `OPENROUTER_API_KEY` → re-run → confirm pipeline still succeeds and viewer falls back gracefully.

## Verification map

| AC bullet from D-242 | Made true by | Verified by |
|---|---|---|
| Pipeline produces summary in V0 template format | Commits 1, 3 | `summary_test.go` (system prompt snapshot test); smoke step 6 |
| Generated summary renders correctly in V3 viewer | Commit 3 (sidecar path matches `cassini-viewer/scripts/demo-data-pull.mjs:144`) | Smoke step 6 (open in viewer) |
| Disabling summary generation does not break the pipeline | Commits 3, 4 (gate on `cfg.SummaryLLM.IsConfigured()`) | `summary_test.go` skip-if-not-configured; smoke step 6 second pass |
| Summary written alongside transcript artifacts | Commit 3 (writes `<outputDir>/summary.md`) | Smoke step 6 directory listing |

## Process retro

### What was unclear coming out of shaping/ticket?

- _(filled during work)_

### What did this plan miss?

- _(filled during/after work)_

### What about the codebase wasn't in the ticket's "likely code areas"?

- _(filled during work)_

### Suggestion for shaping skill / ticket template

- _(filled during/after work)_

### Suggestion for build-plan template

- _(filled during/after work)_
