# V4 (D-242) — Followups

Items noticed during the V4 build that we deliberately deferred. None block D-242 acceptance; each is sized for a separate slice or ticket.

---

## 1. Manifest does not list summary.md or its LLM provenance

`internal/transcribe/format.go` writes `manifest.json` listing audio, transcript, readable transcript, and captions. It does not list `summary.md`, and `provenanceInfo` has no field for the summary LLM model. The viewer finds the summary file by filename convention today, but a self-describing manifest is the better contract — especially when the portable `.opus` packer (which reads the manifest) eventually needs to know about summaries.

**Suggested track:** V6 (self-host bundle) when packaging contracts get a polish pass.
**Effort:** small. Add `Summary string` to `artifactFiles` and `MeetingSummary *provStep` to `provenanceInfo`; populate from `cfg.SummaryLLM.Model` when step 9 ran.

## 2. Manifest.Summary fields exist but are unused

`internal/portable/manifest.go:40,70` declares `Manifest.Summary map[string]any` and `Meeting.Summary string` on the *portable* manifest (the `.opus`-embedded one, distinct from the artifact `manifest.json`). V4 deliberately did not populate them — the viewer doesn't read them, and locking in a schema before we know what summary metadata downstream needs would be premature.

**Suggested track:** V6 packaging. Decide the schema then or remove the fields.

## 3. Portable .opus packer doesn't reference summary.md

`internal/cassini/portable_meeting.go` embeds audio, transcript, and readable transcript into the `.opus` payload. It has no path for `summary.md`. For the portable file's "single file you can email" promise, the summary should probably be embedded too.

**Suggested track:** V6. Either embed the summary markdown as an attachment, or include it in the manifest's `Summary` map (#2 above).

## 4. No chunking for very long meetings

D3 in the build plan deferred this. A long meeting whose readable transcript exceeds the LLM's context window will hit a clean warn-and-skip but produce no summary. We have not measured how long "too long" is in practice. Acceptable for MVP; revisit only if real meetings break.

**Suggested track:** post-MVP. If we hit it, the fix is map-reduce: per-section drafts from chunks, then a final consolidation pass.

## 5. No way to disable summary independently of readable cleanup

Currently the only way to turn step 9 off is to leave `OPENROUTER_API_KEY` unset, which also disables step 8. An operator who wants readable cleanup but not summaries (cost, privacy, model availability) has no clean toggle.

**Suggested track:** small follow-up if anyone asks. Add `CASSINI_SUMMARY_DISABLED=1` env or `--no-summary` CLI flag that forces `SummaryLLM.IsConfigured()` to return false.

## 6. No strict mode for summary failures

Step 8 has `StrictReadableCleanup` for "fail the build if cleanup fails." Step 9 is always warn-and-skip. If we ever want builds to hard-fail when summaries fail, we'd add a parallel `StrictSummary` toggle.

**Suggested track:** only if/when needed. Today the V4 acceptance criteria explicitly want graceful skip.

## 7. Prompt quality is locked by structure, not by output

`TestSummarySystemPromptPinsTemplateAndRules` ensures the system prompt mentions every required heading and rule. It does not check the *output* quality of summaries — that requires running the LLM against real meetings. Once V0 demo data is reliably available (see #10), we should run the pipeline against a known meeting and lock the output via either a manual review checklist or a golden-output test.

**Suggested track:** part of the V4 manual smoke test now; promote to repeatable when D-250 stabilizes demo data.

## 8. Architecture overview is stale

`cassini-go-recorder/docs/architecture-overview.md:219` says the package does not own transcript generation or summarization. Both now live here. This is also flagged by [D-251](https://linear.app/code-myriad/issue/D-251) ("Review documentation alignment"). The new `transcription-pipeline.md` partly mitigates by adding a doc-alignment note, but the overview itself should grow a subsystem entry for `internal/transcribe`.

**Suggested track:** roll into D-251 when it's picked up.

## 9. The `cassini build` `provenanceInfo` should record the summary model when used

When step 9 runs, `manifest.json` should record which model produced the summary. This is a one-line change once #1 lands — track together.

**Suggested track:** with #1.

## 10. Demo data summary seeding is fragile (already tracked)

The demo-data-pull script reads a gitignored seed file at `harness/media/processed/showcase-lantern-festival-v1/summary.md` that does not exist on a fresh clone. `seedAlternatingMeetingSummaries` silently no-ops without it. Already tracked in [D-250](https://linear.app/code-myriad/issue/D-250) — dedicated R2 demo bucket. Listed here only because it affects how easy the V4 manual smoke test is to perform.

**Suggested track:** D-250.

## 11. Unrelated: `cassini build` integration tests are environment-dependent

`internal/cassini/cli_test.go` `TestBuild*` cases fail locally without `ffmpeg`/`ffprobe` on PATH because the doctor precheck blocks. Not new and not caused by V4. Worth flagging as a CI/doctor-mocking opportunity.

**Suggested track:** adjacent / DX. Either mark these tests as integration-tagged or stub the doctor check in tests.
