# Automatic topic suggestions: implementation slices

Date: 2026-10-14 · Status: Proposed · Parent: [README](README.md)

Each slice can merge on its own. Each ships switched off: no user sees anything new until an admin installs the model and switches the feature on, and until the slice that adds a surface has landed. "Switched off" does not mean "can be reverted": S3's schema step runs whether or not the feature is on, and after it an older image cannot open the annotations database (see S3). Each slice carries its own `changelog.d/` fragment and the docs it changes. A slice that changes `appinfo/info.xml` needs a versioned app upgrade, because an image swap keeps the old route allowlist.

All paths are relative to the repository root. `op/` means `cassini-operator/internal/operator/`. `rec/` means `cassini-go-recorder/internal/`.

| # | Slice | Depends on | First visible to |
|---|---|---|---|
| S1 | Embedding runtime and catalogue | — | Admins (optional download) |
| S2 | Chunker, scorer and the int8 evaluation gate | S1 | Nobody (offline report) |
| S3 | Durable schema, worker, admin enablement | S2 | Admins (Settings row, status) |
| S4 | Topic library | S3 | Everyone who can tag |
| S5 | Review on the meeting page | S4 | Everyone who can tag |
| S6 | Cross-meeting review | S5 | Everyone who can tag |
| S7 | Recurring-topic proposals | S5 | Everyone who can tag |
| S8 | Labelled review set and per-topic thresholds | S5 | Admins (report) |

S6 and S7 are independent of each other.

---

## S1. Embedding runtime and catalogue

**Scope.** Run EmbeddingGemma 2 int8 from the CLI. Distribute it as an optional catalogue model. No feature behaviour.

**Files touched**

- `cassini-go-recorder/go.mod`: add `github.com/yalue/onnxruntime_go v1.31.0` and `github.com/eliben/go-sentencepiece`.
- `rec/topics/tokenizer.go`, `rec/topics/embed.go`, `rec/topics/recipe.go` (`EmbedRecipeVersion`) (new).
- `rec/cassini/topics_source.go` (new, package `cassini`, next to the pack code): the `.opus` read and the bundle-to-body rebuild. It must sit in `cassini` because `assembleTranscriptInputs` (`portable_meeting_tags.go:31`), `flattenPortableTranscriptItems` (`portable_meeting.go:588`) and `portableMeetingSource` are unexported there, and `rec/topics` cannot import `cassini` without an import cycle. It hands `rec/topics` a plain word list.
- `rec/cassini/topics.go` (new) and the CLI dispatch: `cassini topics embed-texts --in <jsonl> --out <file>`. `cassini topics embed` lands in S2, with the chunker.
- `rec/cassini/models.go`: an embedding branch next to the diarizer one. `listModelView` (`:43-52`) reports Ready from installed files, a runtime check and a probe receipt that is not bound to the boot or the image; the CLI refuses a non-CPU device (`:167`) and dispatches the probe to the embedder probe, not `probeInstalledModelFn` (`:206-210`).
- `rec/modelstore/catalogue.go`, `catalogue.json`: `KindEmbedding`, `Validate` rules for the three files, the entry `embeddinggemma-2-text-int8`.
- `rec/modelstore` or `rec/transcribe/model_store.go`: `InstalledEmbedder` for several files.
- `rec/transcribe/model_admission.go:51`: probe floor 1536 MiB; probe embeds 4 pinned strings and compares with committed golden vectors.
- `op/models.go:30-35,235`: list the kind as optional, refuse non-CPU.
- `op/device.go:85-101` `modelBuildPeakMB`: a case for the embedding model (1536 MiB), so its probe does not wait for the 3584 MiB default.
- `cassini-app/src/operator/types.ts:290`: add `"embedding"` to `kind`. `cassini-app/src/operator/pipelineStatus.ts:59-61`: `runningDownload` excludes the embedding kind as it excludes diarization.
- `cassini-app/src/TopicSuggestionsModel.svelte` (new, modelled on `VoiceSeparationModel.svelte`): install and probe state only.
- `NOTICE.txt` section; `docs/reference/configuration.md` for `CASSINI_TOPICS_THREADS`; `appinfo/info.xml` variable declaration.

**Acceptance criteria**

- The model installs through Settings and through `cassini models install|pack|import`, probes Ready on the amd64 CPU image, and survives an app upgrade without a new download. It is still Ready, with no new probe, after an app upgrade and after a reboot (a changed `boot_id`).
- The embedding model's download is not shown as a transcription model download in Settings, and its probe is admitted at 1536 MiB, not 3584 MiB.
- `topics_source.go` on a fixture `.opus` and on its source bundle gives the same payload sha256, equal to the file's `payloadRef.sha256`.
- `embed-texts` output vectors are finite, 768-dimensional and unit-norm; otherwise the CLI exits with the model-fault code.
- The batching cost on natural passages is measured and written in the PR. Batching is on only if the padding parity test passes.
- The `license` field (`Apache-2.0`) and the `NOTICE.txt` section agree with the licence page the model card links to.

**Tests**

- Tokenizer ids against HF `tokenizers` output on public multilingual text plus synthetic edge cases.
- Golden-vector test behind the model-cache environment variables `CASSINI_TOPICS_MODEL_DIR` and `CASSINI_LIB_DIR` (cosine ≥ 0.9995).
- Padded batch equals batch size 1 (cosine ≥ 0.99999).
- Bundle-rebuilt payload sha equals the packed one.
- `Validate` accepts the new kind and still rejects bad URLs and nested names.
- `models list` Ready for the embedding kind after its probe passed, still Ready with a changed runtime fingerprint (`boot_id`, image), and not Ready after a failing probe; `models install|probe` for the embedding kind calls the embedder probe and refuses `--device cuda`.
- `modelBuildPeakMB` case for the embedding model.
- Lock contention: a second `embed-texts` waits for the first.
- Vitest: the model card; `runningDownload` ignores an embedding job.

---

## S2. Chunker, scorer and the int8 evaluation gate

**Scope.** Port the method to Go. Fix the thresholds on int8 vectors before any user surface exists.

**Files touched**

- `rec/topics/chunk.go` (new): `natural-1` with the 512-token split rule, `ChunkerVersion`.
- `rec/topics/language.go` (new): text language identification (`github.com/abadojack/whatlanggo` v1.0.1, MIT, added to `cassini-go-recorder/go.mod`): a per-passage vote weighted by words, floor 0.6; `DetectorVersion`.
- `rec/cassini/topics_embed.go` (new): `cassini topics embed --input <bundle|file.opus> [--known-key K] [--threads N] --out <file>`, and `--words <file>` instead of `--input` for evaluation. It chunks and embeds a whole meeting, detects its language, and prints `{"locked":true}` once it holds `.inference.lock`.
- `op/topics_score.go` (new): support gate, spans, meeting rule, controls, `ScorerVersion = 1`, validated-language list.
- `op/topics_eval_cmd.go` (new): `cassini-operator topics-eval --vectors <dir> --definitions <file> [--reference <file>] [--sweep] [--out <file>]` (`--labels` comes with S8).
- `docs/proposals/automatic-tagging/evaluation.md` (new): aggregate numbers only.

**Acceptance criteria**

- On synthetic fixtures, Go passages equal the Python passages exactly.
- `cassini topics embed` on a fixture `.opus` and on its source bundle reports the same `payloadSha256` and `embedKey`; `embedKey = sha256(payload ‖ model_revision ‖ ChunkerVersion ‖ EmbedRecipeVersion)`.
- With `--known-key` equal to the computed key, the CLI exits with `{"unchanged":true}` and never takes `.inference.lock`.
- Per-passage output is finite, 768-dimensional and unit-norm; otherwise the CLI exits with the model-fault code.
- The header carries a detected language, its confidence and `detectorVersion`; text below the floor gives `unknown`.
- On the private corpus (run locally, not committed), Go passages equal the Python passages, and the int8 decision agreement with the 163 fp32 assignments is ≥ 97% at the chosen gate. Every flip is inspected and summarised in `evaluation.md`. If the target fails, `T` and `M` are chosen again on int8 and the reason is written down.
- `ScorerVersion = 1` holds the final constants and the two control definitions.
- No production wiring: the operator binary gains only the dev command.

**Tests**

- Chunker cases ported from `test_refine_daily.py:6-18`; properties: every word once, deterministic, token cap respected, a single over-long run splits at word boundaries.
- Scorer golden cases on synthetic vectors: both qualifying rules, the margin, span breaks on gap and on similarity, the 12-word floor.
- `--known-key` exits before the lock; `{"locked":true}` is printed only after the lock is held.
- Language detection on public multilingual text; `unknown` below the floor.

---

## S3. Durable schema, worker and admin enablement

**Scope.** Every published meeting is embedded and scored once the feature is on. With an empty library this creates no suggestions. Admins see coverage and can backfill and retry.

**Files touched**

- `op/annotations_documents.go`: `version < 8` step that creates every `topic_*` table (including `topic_receipt_target` and `topic_text_vector`); `annotationsSchemaVersion = 8`. **This is a one-way step.** It runs whether or not the feature is on, and after it an older image refuses the database (`annotations_documents.go:37-39`), which takes all tagging down. The release notes state the rollback limit, and the upgrade procedure backs up `annotations.sqlite3` first. If a release that can be held is wanted, the empty-table migration can land alone first, before any behaviour depends on it.
- `op/topics_store.go`, `op/topics_vectors.go`, `op/topics_worker.go`, `op/topics_reconcile.go`, `op/topics_coverage.go`, `op/topics_settings.go` (new).
- `op/topics_backfill_cmd.go` (new) and the dispatch in `op/run.go`: `cassini-operator backfill-topics [--since] [--limit]`.
- `op/run.go:969-977` `operatorAPIRoutes`: register `/settings/topics` and `/settings/topics/` before the `/settings/` prefix, which otherwise sends them to `llmSettingsHandler`.
- `op/publish_runtime.go`: next to `indexPublishedMeeting` (`:154`), before the site is removed at `:170`, resolve `deliveredOpusName` and upsert a `queued` row with `opus_name`, delivered digest and attempt; wake the worker after `promotePublishedPair` succeeds. Non-fatal.
- `op/models.go:343-355`: at startup, when the feature is on, queue a background golden-vector probe of the installed embedding model.
- `op/recording_priority.go`, `op/processing_policy.go`, `op/run.go:137`: the single `priorityBuildCancel` slot becomes a set; builds and embeds both register.
- `op/remote_retention_retire.go`: sidecar forget under `meetingProjectionLocks`, next to `searchStore.ForgetMeeting`; inside the annotations retire transaction, topic rows deleted and `topic_receipt` expired or scrubbed (single receipts by `opus_name`, batch receipts through `topic_receipt_target`), as `annotation_receipt` is (`:83-117`).
- Topic writers (worker, rescorer, publish hook): `meetingProjectionLocks` plus `meetingNotRetired` for sidecar writes; `meetingNotRetired` inside every annotations transaction; finish by `claim`; the `requeue` flag; the bundle copied under `artifactGate.RLock` and `lockArtifacts(job)`.
- `op/settings.go`: `topic_suggestions {enabled, threads}`.
- `appinfo/info.xml`: new ADMIN entry `^operator\/settings\/topics\/(backfill|retry)$` POST.
- `cassini-app/src/TopicSuggestionsSettings.svelte` (new), `SettingsPanel.svelte` "After each meeting" row, `operator/client.ts` methods.
- Storage usage panel: list `topics.sqlite3`.
- `docs/topic-suggestions.md` (new, operator part), `docs/privacy.md` first row, `docs/reference/api.md` ADMIN routes.

**Acceptance criteria**

- `PUT operator/settings/topics {enabled:true}` answers 409 until the model is Ready, and succeeds after.
- After an operator restart with a changed `boot_id` (and after an image upgrade), the feature still runs with no admin action.
- With the feature on, a new publish reaches `done` within one worker cycle, with zero suggestions and the passage count in the log line.
- A rerun with the same transcript does not run an embed (the fake CLI's call count stays the same).
- A rerun whose promotion fails, or whose hook never ran, is still processed against the delivered transcript (archive fallback, or the `published_attempt` reconciler rule).
- A model or recipe change requeues every `done` meeting; until a meeting is embedded again it counts as `waiting` and is not scored.
- A recording that starts during an embed cancels it; the row returns to `queued` with no attempt used.
- Low memory defers a claim without using an attempt. Waiting for `.inference.lock` behind a long build uses no attempt and does not time out.
- "Check past meetings" queues the archive (or meetings since a date), shows an estimate and progress, and yields to builds.
- A blocked meeting shows in the Settings row and Retry requeues it.
- Retiring a meeting removes its rows from both databases, and expires or scrubs its `topic_receipt` responses.
- Deleting `topics.sqlite3` requeues every `done` meeting; verdict and suggestion rows are untouched; definition and control vectors are still present (they live in `annotations.sqlite3`), and a missing one is embedded again before any meeting is scored.
- Disabling stops new claims; stored rows remain.
- ADMIN status serves only content-free counts: no suggestion or verdict counts.

**Tests**

- Migration 7 → 9 and 8 → 9, including a database that already holds marks and receipts.
- Worker state machine table test; crash recovery of `running` rows; backoff and `blocked` after 5 attempts.
- Fake `cassini` script that emits deterministic vectors and counts calls.
- Publish hook: `opus_name` read from the attempt catalog before the site is removed; a promotion failure leaves a `queued` row.
- A publish during a running embed sets `requeue`; the finishing worker writes by `claim` and requeues.
- Retire scrub test across both stores, including `topic_receipt` and `topic_receipt_target`.
- Retirement racing a running embed: no rows in either store afterwards.
- Preemption, memory deferral and lock-wait deferral tests.
- `/settings/topics` and its subpaths are not answered by the LLM settings handler.
- Browser test for the Settings row (switch disabled until Ready, backfill dialog, Retry).

---

## S4. Topic library

**Scope.** Anyone who can tag can describe, edit, retire and restore topics. Each save embeds the definition and rescores that topic, so pending rows exist in the database. They are not served to users yet, and no ADMIN route counts them (README, "Why no suggestion or verdict counts on ADMIN routes"); `topics-export` reads them offline.

**Files touched**

- `op/topics_library.go`, `op/topics_rescore.go` (new).
- `op/annotations_service.go`: `case resource == "topics"`.
- `op/annotations_meetings_request.go` `resolveVocabulary`, and `prepareInitialTagAppearance`: library-aware.
- `op/annotations_tag_changes.go`: rename and restyle update the library row; merge retires A, copies A's definition and rejections to B, withdraws A's pending rows; delete keeps the row.
- `appinfo/info.xml`: USER entry `^annotations\/topics(\/.*)?$` GET,POST.
- `cassini-viewer/src/viewer/dataProvider.ts`: library methods; `cassini-app/src/appDataProvider.ts` implementations with `cache:'no-store'`.
- `cassini-viewer/src/components/tags/TagManager.svelte`: Topics tab; `components/topics/TopicEditor.svelte` (new); retire option in the delete and merge dialogs.
- `docs/topic-suggestions.md` (library part), `docs/reference/api.md`, `docs/privacy.md` library row.

**Acceptance criteria**

- A caller with at least one visible meeting can create, edit (with `expectRevision`), retire, restore and view the history of a topic. A caller with an empty visible set gets 404. A failed visibility lookup gives 502.
- Labels are unique among active topics. Creating a topic with a retired topic's label, when the retired id is still in the caller's vocabulary, answers 409 `topic-retired` and the UI offers Restore; "create a separate topic" mints a new id. Neither path violates the `topic_library` primary key.
- "Describe this tag" adopts the existing id; existing marks count as the topic's usage. The visibility warning appears whenever `fromTagId` is used or the tag's visible meetings come from fewer than 2 rooms, and names the rooms.
- An admin, or the author of a past revision, can redact it; the history then serves no label or definition for it, and it cannot be restored.
- A manual TagPicker add of a library label on a meeting where no visible file carries it uses the library id and colour.
- The 31st definition edit in an hour by one user answers 429. The 201st active topic is refused.
- Library responses contain no user ids; the audit with actor ids is visible on the ADMIN status only.
- Usage, pending and "very common" counts are over the caller's visible set.
- Everything is hidden while the feature is off.

**Tests**

- Route tests for each rule above, including the retired-label case and redaction.
- Rescore streams one meeting at a time (a memory-bounded test with many fake meetings), reads vectors outside the annotations transaction, and writes one meeting per short transaction. A rescorer write whose `embed_key` no longer matches `topic_meeting.embed_key` is dropped.
- A failed definition embed is retried by the reconciler.
- Tag-job interplay: rename, merge and delete, each with and without a library row.
- Browser tests for the Topics tab and `TopicEditor`, including the visibility warning.

---

## S5. Review on the meeting page (first user value)

**Scope.** People see suggested topics on a meeting, with evidence, and confirm or reject them.

**Files touched**

- `op/topics_review.go` (new): `GET annotations/topics/meetings/<id>`, `POST …/<tagId>`, `GET annotations/topics/coverage`.
- `op/annotations_mutations.go`: trusted `Namespace` and `InTx` fields on `annotateWriteRequest`; `commitDocument` uses them when set.
- `cassini-viewer/src/components/marking/MeetingTags.svelte`; `components/tags/TagChip.svelte` (`suggested` variant); `components/topics/SuggestionPopover.svelte`, `CoverageNote.svelte` (new); evidence style in `TranscriptFrame.svelte`, `MarkingRail.svelte`, `SectionsNav.svelte`.
- `dataProvider.ts` and `appDataProvider.ts`: suggestion methods.
- `docs/topic-suggestions.md` (user part with screenshots), privacy decisions row.

**Acceptance criteria**

- Confirm writes one whole-meeting mark credited to the confirming person (`actor.kind = person`, `actor.id` = caller), in the deployment's tag namespace, through the normal outbox. The delivered file, its copies and the public viewer then show a plain tag.
- On a file with no annotations, the confirm writes the deployment namespace, not a new one.
- On a file that has the same label under another id, the confirm marks that id and records the alias; the file shows one chip.
- On a file whose marks are bound to different audio, confirm answers 409 `marks-unresolved` and the popover explains it; reject still works.
- Undo within the toast removes the mark with `undo-operation`, deletes the verdict, and shows the suggestion as pending again at once (its `decided` row returns to `pending` in the same transaction).
- A retried confirm (same client `requestId`) after the first succeeded returns the first receipt, also when the library colour changed in between. `OperationID` is derived from the derived `RequestID`.
- A rejected (meeting, topic) never comes back after a rerun, a model bump or a definition edit. Reopen brings it back as pending at once.
- A meeting whose language is unknown or not validated shows the warning line; coverage shows "language unknown" separately from named unvalidated languages.
- Nothing appears in the file, search or the static provider before a confirm.
- The coverage line matches the caller's visible set and names the reasons for unchecked meetings and unvalidated languages.

**Tests**

- Confirm and verdict commit in one transaction. A retried confirm returns the receipt: the replay lookup runs before the pending check, and a 409 request collision on the derived id is answered by replaying the receipt.
- Undo and Reopen restore the `decided` suggestion to `pending` with its evidence.
- Concurrent confirm and reject by two users: exactly one verdict wins, and it matches the file.
- No suggestion, evidence or count of a hidden meeting in any response body.
- A share removed between page load and confirm gives 404, not a write.
- Browser tests: chip, popover (confirm, reject, undo, seek), unresolved-marks state, unvalidated-language line, coverage note. A static-provider test proves the Suggested group is absent.
- Harness end-to-end with the fake embedder: publish, suggestion, confirm, mark in the delivered file.

---

## S6. Cross-meeting review

**Scope.** A review queue across the meetings a person can read, grouped by topic, with batch confirm.

**Files touched**

- `op/topics_review.go`: `GET annotations/topics/review`, `POST annotations/topics/review/batch`.
- `cassini-viewer/src/components/topics/NeedsReview.svelte` (new); `RoomsRail.svelte` "Needs review (n)"; `MeetingList.svelte` row badge; `App.svelte` filter wiring.
- `dataProvider.ts` and `appDataProvider.ts`: queue and batch methods.

**Acceptance criteria**

- Queue counts and items match the caller's visible set; 502 when the visible set cannot be worked out.
- Each item has at most one short quote, read from the search index for a visible meeting, or none.
- Batch confirm of one topic on up to 100 meetings runs each through the trusted confirm path and reports a result per meeting (including `marks-unresolved`).
- Tag filters and search never match suggestions.

**Tests**

- Route tests for visibility, paging and the batch limit.
- Batch confirm keeps the library id on every file (no split ids).
- Browser tests for the rail item, the badge, keyboard review and batch confirm.

---

## S7. Recurring-topic proposals

**Scope.** A per-room inbox of recurring themes that no topic covers.

**Files touched**

- `op/topics_proposals.go`, `op/topics_names.go` (new); dirty-room marking in the worker; nightly pass.
- `GET annotations/topics/proposals`, `POST annotations/topics/proposals/<id>`.
- Retention: inside the annotations retire transaction, set every affected proposal to `recompute` and clear its draft name, hints and centroid; the background recompute is the only way out of `recompute`.
- `topic_proposal` stores `model_revision`, `recipe_version` and `chunker_version`; centroids are never compared across revisions.
- `cassini-viewer/src/components/topics/TopicInbox.svelte` (new), New topics tab in `TagManager.svelte`.
- `docs/topic-suggestions.md` (inbox part), privacy proposals row.

**Acceptance criteria**

- Clustering is deterministic for a fixed input.
- A proposal is served and actionable only when its state is `open` (or `dismissed`, for Restore), it has at least one member, and the caller can read every member meeting.
- Draft names use no built-in word list and no data from meetings outside the group's members; members' speaker labels never appear in names.
- The `routine` flag is computed at read time over the caller's visible meetings in the room, never over the whole room.
- Create topic opens the editor with empty fields and the draft name beside them. Saving a label or definition that shares a term with the draft name or hints needs the blocking confirmation. A human label and definition are required, and no verdicts are created.
- A dismissed proposal does not come back on later passes, including after a model or chunker change; Restore undoes the dismissal.
- Retiring a member hides the proposal at once (`recompute`); the recompute rebuilds the centroid and name from live members; under 3 meetings the proposal turns `stale`. A crash or a naming wait leaves it hidden.

**Tests**

- Clustering and identity-matching fixtures (synthetic vectors).
- Name drafting on synthetic text in three languages with no stop list.
- Visibility: one hidden member hides the proposal and refuses its actions. A proposal with no members is never served.
- Retire a hidden member, kill the recompute: the proposal stays invisible to everyone.
- Dismissal suppression across a model revision change (matched by member meeting and time overlap only).
- "Create topic" with an unedited draft-name term needs the explicit confirmation.
- Browser tests for the inbox cards and the three actions.

---

## S8. Labelled review set and per-topic thresholds (Nice)

**Scope.** Measure precision and recall properly, extend the validated-language list, and later fit per-topic thresholds.

**Files touched**

- `op/topics_eval_cmd.go`: `--labels` support and `cassini-operator topics-export`.
- `op/topics_score.go`: validated languages from the report; per-topic `T` and `M` once a topic has ≥ 20 verdicts with ≥ 5 rejections.
- `docs/proposals/automatic-tagging/evaluation.md`: Gate 2 aggregates.

**Acceptance criteria**

- About 300 labelled pairs over ≥ 2 rooms and ≥ 1 non-English room, two labellers, precision and estimated recall per detected language in `evaluation.md` (aggregates only).
- Per-topic thresholds change `scorer_key` and rescore without embedding.
- The decision on dropping the "Preview" label (README open question 15) is recorded.

**Tests**

- Export contains no text and no data from retired meetings.
- Per-topic threshold fitting with leave-one-meeting-out on synthetic verdicts.
