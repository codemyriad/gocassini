---
shaping: true
---

# Automatic topic suggestions (D-847)

Date: 2026-10-14 · Status: Proposed

- [Implementation slices](slices.md)
- [Evaluation](evaluation.md) (Gate 1: passed)
- Prototype record: `docs/proposals/automatic-tagging-poc.md` on the `feat/automatic-tagging-poc` branch

## TL;DR

Cassini reads each published transcript with a small local embedding model. It compares the transcript with a library of described topics and suggests topics for the meeting. A person confirms or rejects each suggestion. A confirm writes an ordinary whole-meeting tag into the recording, credited to that person. A rejection is stored and is never undone by the machine. Unreviewed suggestions never reach the file, copies, search or the public viewer.

- **Model.** EmbeddingGemma 2, int8 ONNX export, CPU only. It runs in a `cassini topics` subprocess on the ONNX Runtime library the image already ships. It is an optional download, like the voice separation model.
- **Storage.** Human decisions (the topic library and the verdicts) and the review queue live in `annotations.sqlite3`, the operator's durable annotations database. A confirm writes the mark and the verdict in one transaction. Passage vectors live in a new disposable file, `topics.sqlite3`. No new table stores transcript text.
- **Who.** Anyone who can tag can edit the library and review suggestions. "Can tag" means "can read at least one meeting". Admins install the model, switch the feature on and start the check of past meetings.
- **New topics.** A per-room inbox proposes recurring themes that no topic covers yet. A person sees a proposal only when they can read every meeting it draws on.
- **Honesty.** Every list says how many of the meetings you can read were checked, how many were not, and why.
- **Order.** Runtime first, then an offline evaluation gate on int8 vectors, then the worker, the library, the review UI and the inbox. Each slice ships switched off. The slice that adds the schema (S3) is a one-way database step: an older image cannot open the upgraded annotations database.

## Terms

| Term | Meaning |
|---|---|
| Topic | A tag that has a written definition in the library. Its id is an ordinary annotation tag id. |
| Definition | Prose written by a person that describes a topic. The model embeds it. It never quotes a meeting. |
| Passage | A run of about 25 to 120 consecutive words of one transcript. The model embeds each passage. |
| Suggestion | The machine's claim that a meeting is about a topic. It is pending until a person decides. |
| Verdict | A person's decision on a (meeting, topic) pair: confirmed or rejected. Only people create verdicts. |
| Visible set | The meetings the caller can read now, from their current Nextcloud shares (`resolveVisibleMeetings`, `cassini-operator/internal/operator/search_endpoint.go:325`). |
| Proposal | A cluster of passages from one Talk room that recurs over several days and fits no topic. |

## Requirements (R)

"Must-have" rows come from the owner's decisions or the stated constraints. "Nice" rows are supporting behaviour that this design proposes.

| ID | Requirement | Status |
|---|---|---|
| R0 | A suggestion reaches the recording file, its copies, search and the public viewer only after a person confirms it. | Must-have |
| R1 | A confirm writes an ordinary whole-meeting tag through the existing annotations write path, credited to the confirming person. The annotations format does not change. | Must-have |
| R2 | A rejection is durable. Reruns, model upgrades and definition edits never suggest a rejected (meeting, topic) again. | Must-have |
| R3 | Anyone who can tag can create, describe, edit, retire and restore topics. These are USER routes, like today's Tag manager. | Must-have |
| R4 | A per-room inbox proposes recurring new topics. A proposal is shown only to a person who can read all of its evidence meetings. | Must-have |
| R5 | The model is EmbeddingGemma 2 text, int8 ONNX, run from Go through `yalue/onnxruntime_go` v1.31.0 on the shipped ONNX Runtime 1.27.1, with `eliben/go-sentencepiece`. It is a new optional catalogue model kind, CPU only. | Must-have |
| R6 | The feature is off until the model is installed and an admin switches it on, through the existing Settings conventions. | Must-have |
| R7 | Reruns are idempotent. Embedding is keyed on the transcript payload sha256, the model revision and the chunker version. | Must-have |
| R8 | Every read is limited to the caller's visible set. If the visible set cannot be worked out, the answer is an error, never an empty list. | Must-have |
| R9 | Coverage is stated honestly: which meetings were checked, which were not and why, and which are in languages nobody has validated. | Must-have |
| R10 | Thresholds are validated again on int8 vectors before any user sees a suggestion. | Must-have |
| R11 | Nothing is specific to one team: no built-in stop lists, aliases or names. Non-English transcripts work through the same code. | Must-have |
| R12 | Embedding never delays a recording and stays within CPU and memory limits on a host shared with Nextcloud and Talk. | Must-have |
| R13 | Retiring a meeting deletes every topic row and vector derived from it. | Must-have |
| R14 | People can review suggestions across meetings and confirm one topic on many meetings at once. | Nice |
| R15 | Verdicts feed an evaluation report and, later, per-topic thresholds. | Nice |
| R16 | Agents and the `cassini meetings` CLI read or decide suggestions. | Not in v1 |

## Owner decisions (binding)

1. **Suggestions live in a durable review queue in the operator's database, not in the `.opus` file.** Rejections are durable. Confirming writes an ordinary tag (a whole-meeting mark) through the existing write path, credited to the person who confirms. Unreviewed suggestions never reach the file, copies, search or the public viewer. The annotations format does not change.
2. **The topic library is editable by anyone who can tag.** It uses USER routes, the same level as today's Tag manager. It is not admin-only.
3. **The recurring-new-topic inbox is in v1.** It clusters within one Talk room only. A proposal is shown only to a person who can see all of its evidence meetings.
4. **Model:** EmbeddingGemma 2 text, int8 ONNX export (`onnx-community/embeddinggemma-2-ONNX`), run from Go through `yalue/onnxruntime_go` v1.31.0 against the shipped `libonnxruntime.so` 1.27.1, tokenizer through `eliben/go-sentencepiece`. It is a new optional catalogue model kind, like diarization, CPU only. Thresholds must be validated again on int8 vectors.

Other constraints: the repository is public; transcripts can be in any language; the feature is off until the model is installed and enabled; coverage is stated honestly; reruns are idempotent; every read is filtered by the visible set and fails closed.

## Current baseline

All paths are relative to the repository root.

- **Tags live in the recording.** A tag is `{id, label, color, icon}` inside the file's `cassini.annotations.v1` document. It has no definition and no status (`cassini-annotations/document.go:37-45`). A mark targets the whole meeting or a time range, and it carries an actor `{kind: person|agent, id}` and an `operationId` (`document.go:64-99`). A tag with no marks is dropped from the file (`cassini-annotations/ops.go:229-230`), so a topic that is on no meeting has no identity today.
- **There is no tag library.** The vocabulary is derived per caller from the meetings they can read (`annotations_tags.go:12-15`). The tag namespace is inferred from the files and is `""` on a fresh install (`annotations_store.go:367-399`).
- **Writes.** `commitDocument` (`annotations_mutations.go:24`) checks the caller's current share, takes the mutation lock, writes a durable snapshot in `annotations.sqlite3` and lets the outbox upload it. The normal path resolves labels to ids among the caller's visible meetings (`resolveVocabulary`, `annotations_meetings_request.go:136-183`). The trusted path (`Accepted: true`, used by tag jobs) skips that resolution and passes an empty namespace (`annotations_mutations.go:75-80`).
- **The annotations database is durable.** It runs in WAL mode with one connection (`SetMaxOpenConns(1)`, so reads queue behind writes too), `synchronous=FULL`, and an in-code migration ladder at schema version 7 (`annotations_store.go:17-25`, `annotations_documents.go:29-126`). An existing database is never discarded; the archive only seeds a replacement for a lost one.
- **Search is the template for shared derived data.** One index built by the service account; every query binds the visible set inside SQL (`search_query.go`). Disposable sidecars are deleted and rebuilt when their version changes (`search_store.go:183-238`).
- **Publish.** `runPublishJob` delivers, indexes search at `publish_runtime.go:154`, removes the staging site at `:170`, promotes `current/<job>.meeting` at `:177` and prunes the attempt bundle at `:181`. Search backfill reads `current/<job>.meeting` when it can trust it and downloads the `.opus` otherwise (`search_backfill.go:10-27,192-250`).
- **Models.** The operator never loads a model. It runs the `cassini` CLI (`models.go:116,446`; `speaker_refine.go:281`). One file lock, `.inference.lock`, serialises all inference (`cassini-go-recorder/internal/transcribe/model_admission.go:16-49`). The catalogue accepts the kinds speech, vad and diarization (`modelstore/catalogue.go:97-121`).
- **Prototype.** An offline Python experiment over 60 meetings of one room, and a throwaway UI that keeps review state in browser storage. The method: natural passages of 25 to 120 words, a support gate of cosine ≥ 0.85 and margin ≥ 0.05 over two control definitions, spans, and a meeting rule of 80 supporting words or one short strong span. The thresholds were picked by eye on fp32 vectors. Moving the gate from 0.83 to 0.85 halves the assignments (345 to 163).

## Architecture

```
publish indexed (:154) ──► enqueue topic_meeting (annotations.sqlite3)
promotion done (:177) ──────────────────────────────────────────────► wake
                                                                      │
 reconciler (startup, hourly, admin backfill) ────────────────────────┤
                                                                      ▼
                         topics worker (one at a time, yields to recordings)
                          1. resolve the transcript source (bundle or archive)
                          2. cassini topics embed --known-key …   (subprocess)
                               unchanged? → stop before loading the model
                               else: lock, chunk, embed → passages + vectors
                          3. write vectors to topics.sqlite3
                          4. score in-process against the library
                          5. upsert / withdraw suggestions (annotations.sqlite3)
                          6. mark the room for clustering
                                                                      │
  person ─► confirm ─► commitDocument(trusted, namespace) ─► mark + verdict, one tx
                                    └─► existing outbox uploads the .opus
```

### Components

**Recorder module (`cassini-go-recorder`)**

| Path | Role |
|---|---|
| `internal/topics/chunk.go` | Port of the natural-passage chunker (`refine_daily.py:16-47`) with a token cap. Exports `ChunkerVersion = "natural-1"`. |
| `internal/topics/tokenizer.go` | `go-sentencepiece` over `tokenizer.model`. Adds BOS=2 and EOS=1. Prepends the prompt `task: clustering \| query: `. |
| `internal/topics/recipe.go` | Exports `EmbedRecipeVersion = "r1"`. It names everything outside the model and the chunker that changes a vector: the prompt prefix, the 512-token cap and the batching policy. Any change to one of them bumps it. |
| `internal/topics/embed.go` | `onnxruntime_go` v1.31.0 session over `$CASSINI_LIB_DIR/libonnxruntime.so`. Feeds empty `[0,512]` image, video and audio tensors and reads `sentence_embedding [B,768]`. |
| `internal/topics/language.go` | Text language identification over the passages: `abadojack/whatlanggo` (pure Go, MIT) on each passage of at least 12 words, then a vote weighted by words. The confidence is the winning language's share of those words; below 0.6 the answer is `unknown`. `DetectorVersion = "whatlanggo-1.0.1/vote-1"`. Known limits: Han-script text is always `zh`, so kanji-heavy Japanese can read as Chinese; the floors are untested on real multilingual meetings (Gate 2); the library has been unmaintained since 2019. |
| `internal/cassini/topics_source.go` | Reads the default `cassini.words.v1` transcript from a `.opus` (verifying `payloadRef.sha256`) or rebuilds the same body from a bundle with the pack code (`assembleTranscriptInputs`, `internal/cassini/portable_meeting_tags.go:31`; `EncodeTranscriptBody`, `internal/portable/manifest_transcripts.go:275`). It lives in package `cassini`, because `assembleTranscriptInputs`, `flattenPortableTranscriptItems` and `portableMeetingSource` are unexported there, and `internal/topics` must not import `cassini` (the CLI imports `topics`, so that would be an import cycle). It hands `internal/topics` a plain word list. |
| `internal/cassini/topics.go` | `cassini topics embed`, `cassini topics embed-texts`. Wired into the CLI dispatch. |
| `internal/cassini/models.go` | An embedding branch next to the diarizer one. `listModelView` (`:43-52`) computes the embedding model's readiness diarizer-style (see "Model catalogue"). The CLI refuses a non-CPU device (`:167`) and dispatches the probe to the new embedder probe, not the speech probe (`:206-210`). |
| `internal/modelstore` | `KindEmbedding`, `Validate` rules, catalogue entry, and an `InstalledEmbedder` helper that handles several files (`InstalledDiarizer` assumes `Files[0]`, `transcribe/model_store.go:82-87`). |
| `internal/transcribe/model_admission.go:51` | `probeMemoryFloor` entry of 1536 MiB for the embedding model. Without it the floor is 4608 MiB. |

**Operator (`cassini-operator/internal/operator/`, new `topics_*.go` files)**

| File | Role |
|---|---|
| `topics_store.go` | Durable tables (annotations schema 8) and the state transitions. |
| `topics_vectors.go` | The disposable `topics.sqlite3` sidecar, opened like `search.sqlite3` (WAL, busy timeout, rebuild on version change). |
| `topics_worker.go` | Claim, admission, preemption, subprocess, retry and blocked states. |
| `topics_reconcile.go` | Finds missing or stale work from durable state. |
| `topics_score.go` | Pure Go: support gate, spans, meeting rule, control margin. `ScorerVersion` and all constants. |
| `topics_rescore.go` | Per-topic and global rescoring that streams vectors meeting by meeting. |
| `topics_library.go` | Library routes, audit, caps, rate limit, and the tag-job interplay. |
| `topics_review.go` | Per-meeting and cross-meeting review routes, confirm and undo. |
| `topics_proposals.go`, `topics_names.go` | Per-room clustering, identity across passes, draft names. |
| `topics_coverage.go` | USER coverage (visible set) and ADMIN status. |
| `topics_settings.go` | Settings block, validation, the ADMIN routes. |
| `topics_eval_cmd.go`, `topics_eval_labels.go` | `cassini-operator topics-eval` (offline gates; `--labels` for Gate 2). |
| `topics_export_cmd.go` | `cassini-operator topics-export`: verdict rows with scores, no text. |
| `topics_gates.go` | Per-topic `T` and `M`, fitted from verdicts with leave-one-meeting-out validation. |

**Hooks into existing code**

- `publish_runtime.go`: next to `indexPublishedMeeting` (`:154`), while the attempt site still exists, read the join key with `deliveredOpusName(attemptSiteDir, jobID)` (`search_ingest.go:182-209`) and upsert a `queued` row that records `opus_name`, the delivered digest (`task.OpusSHA256`) and the attempt number. The site is removed at `:170`, before promotion at `:177`, so the key cannot be read after promotion, and the codebase forbids composing it from the job id (`search_ingest.go:30-37`). This is non-fatal, like search indexing. The worker is woken only after `promotePublishedPair` succeeds. If promotion fails or the process dies first, the row is still `queued`; the worker's trust rule sees that `current/<job>.meeting` is not the delivered build and reads the archive instead.
- `remote_retention_retire.go:56-117`: forget the sidecar rows under `meetingProjectionLocks`, next to `searchStore.ForgetMeeting`. Inside the existing annotations retire transaction, next to `deleteAnnotationRows`: delete the topic rows, expire or scrub `topic_receipt`, and put affected proposals into `recompute` (see "Retention").
- `run.go:969-977` `operatorAPIRoutes`: add `/settings/topics` and `/settings/topics/` before the `/settings/` prefix. Without them the LLM settings handler answers every topics settings path.
- `operator/models.go:343-355`: at startup, when `topic_suggestions.enabled` is set and the embedding model is installed, enqueue a background golden-vector re-check of it (see "Model catalogue"). Today only `settings.ActiveModel` is re-probed.
- `operator/device.go:85-101` `modelBuildPeakMB`: a case for the embedding model (1536 MiB). The default of 3584 MiB would otherwise gate its probe (`models.go:420`, `resource.go:83`).
- `cassini-app/src/operator/types.ts:290`: `kind?: "speech" | "diarization" | "embedding"`. `pipelineStatus.ts:59-61` `runningDownload` excludes the embedding kind as it excludes diarization, so Settings never reports its download as a transcription model download (`SettingsPanel.svelte:474`).
- `annotations_service.go:69`: add `case resource == "topics"` to `route`.
- `annotations_mutations.go`: two trusted fields on `annotateWriteRequest` (both `json:"-"`): `Namespace`, used when `Accepted` is true, and `InTx`, a hook that runs inside the mutation transaction.
- `annotations_meetings_request.go` `resolveVocabulary`: make it library-aware (see "Library").
- `annotations_tag_changes.go`: rename, merge and delete jobs update the library (see "Library").
- `recording_priority.go:41-58`: turn the single `priorityBuildCancel` slot into a set, so a recording start can cancel both a build and an embed.
- `operator/models.go:30-35,235`: list the embedding kind as an optional download and refuse a non-CPU install.

**Why a subprocess.** The operator never loads a model today, and its module has no cgo. The CLI already links sherpa-onnx; inside `cassini topics` only `onnxruntime_go` creates an ONNX Runtime environment, so the two never share one process environment. Model load takes about 60 ms, so one process per meeting costs almost nothing.

## Per-meeting pipeline

### 1. Triggers

- **Publish:** the hook above, priority 0.
- **Reconciler:** runs at startup, every hour and when the feature is switched on. It walks the same archive targets as `backfill-search` (`archiveBackfillTargets`, `search_backfill_cmd.go:184`). For each meeting that is not retired:
  - a `done` row whose `vectors_generation` differs from the sidecar's generation is queued again (sidecar lost or rebuilt);
  - a `done` row whose `embed_key` differs from `sha256(payload_sha256 ‖ current model_revision ‖ ChunkerVersion ‖ EmbedRecipeVersion)`, computed from the stored `payload_sha256`, is queued again at backfill priority (a model, chunker or recipe change);
  - a `done` or `unavailable` row whose recorded `published_attempt` differs from `artifact_availability.published_attempt`, or whose `delivered_digest` differs from the archive's delivered digest, is queued again (a rerun whose hook never ran, for example after a crash between `MarkPublishSucceeded` at `:132` and the hook);
  - a `running` row whose claim is older than the process start is queued again (crash recovery);
  - a meeting with no row is queued only if it was published after the feature was switched on, or falls inside an admin backfill.
- **Text vectors** are reconciler work too. At startup, every hour, after a sidecar generation change and after a model change, the reconciler embeds (`cassini topics embed-texts`) every active definition and both controls whose `(sha, model_revision, recipe)` vector is missing. Until both control vectors exist for the current model, no meeting is scored, and meetings count as `waiting`, not checked.
- **Admin backfill:** "Check past meetings" in Settings, or `cassini-operator backfill-topics [--since DATE] [--limit N]`. Priority 2. Enabling the feature does not start it by itself, because it costs about 10 s of CPU per meeting on a shared host. The coverage line shows what is not checked yet.
- **Rescore without embedding:** a topic is created, edited or restored, or `ScorerVersion` changes. See "Rescoring".

A meeting missing from the search index is **not** treated as deleted. Only retirement, or its absence from a complete archive listing (the rule `forgetVanishedMeetings` uses, `search_backfill.go:136`), removes topic rows.

### 2. Claim and admission

The worker processes one meeting at a time. Each "defer" below waits without using up a retry attempt.

1. The feature is on and the embedding model is Ready. Otherwise the worker sleeps.
2. Recording priority: `waitForRecordingIdle` (`recording_priority.go:67`). Then the worker registers its cancel function in the new cancel set. A recording that starts later cancels the embed. The process group is killed and the row goes back to `queued`.
3. Backfill-priority work does not start while any build is queued.
4. Memory: `resourceLimits.waitForMemory(floor + headroom)` (`resource.go:213`), as speaker refine does. On timeout the row gets `retry_at = now + 5 min`.
5. A conditional `UPDATE … SET state='running', claim=? WHERE opus_name=? AND state='queued'`.

**Concurrent writers.** Three rules keep one meeting's rows consistent:

- **Finish by claim.** The worker finishes with `UPDATE … WHERE opus_name=? AND claim=?`. If no row matches, its result is dropped.
- **Requeue a running row.** A publish hook that finds the row `running` does not overwrite it. It sets `requeue=1` and the new delivered digest and attempt. The finishing worker sees `requeue=1`, writes its result only if its `embed_key` still matches, and sets the row back to `queued` in the same transaction.
- **Rescorer check.** The rescorer remembers the `embed_key` of the `passage_set` it read. In its write transaction it compares that key with `topic_meeting.embed_key`. If they differ, it drops the write, because the worker has new passages for this meeting.

**Retirement.** Every writer respects the retirement tombstone, as the existing projections do (`search_store.go:325-334`, `meeting_metadata_store.go:47-55`, `annotations_mutations.go:115-118`):

- every `topics.sqlite3` write for a meeting takes `meetingProjectionLocks` for that `opus_name` and calls `meetingNotRetired` first;
- every `annotations.sqlite3` topic write (worker completion, rescorer, publish hook, direct verdicts, dirty-room marking, proposal recompute) calls `meetingNotRetired` inside its transaction and writes nothing for a retired meeting.

This matters in practice: backfill priority works through old meetings, and old meetings are the ones retention retires.

### 3. Transcript source

The worker uses the search-backfill trust rule:

- **Bundle:** `current/<job>.meeting` when it is the delivered build. For publish-triggered rows the hook stores the delivered digest (`task.OpusSHA256`), so the check is a cheap comparison with the bundle's sealed digest. For backfill rows the worker asks the archive for the delivered digest, exactly as `backfillOneMeeting` does (`search_backfill.go:192-250`).
- **Archive:** otherwise, download the delivered `.opus` as the service account into a private temporary folder (`downloadArchiveOpus`, `search_backfill_archive.go:118`), deleted in a `defer`.
- **Locks for the bundle.** The worker takes `artifactGate.RLock` and `lockArtifacts(job)` (as `backfillOneMeeting` does, `search_backfill.go:196-199`; `retention_operations.go:14-16`). Under both it checks the digest and copies the rebuilt default words body, with its sha256, to a private temporary file. Then it releases both locks and runs the CLI on that copy. Retention expiry and a rerun's promotion therefore cannot change the bundle between the check and the read. The per-job lock is not held for the whole embed, because `runPublishJob` also takes it.

The `.opus` carries only raw words and the bundle carries the producer's segments (`search_backfill.go:13`). Both give the same default words body: `internal/cassini/topics_source.go` rebuilds it from the bundle with the pack code, so its sha256 equals the `payloadRef.sha256` the delivered file carries. A unit test packs a bundle and checks that the two digests match. If they ever differ, the worker uses the archive.

### 4. Idempotency

`embed_key = sha256(payload_sha256 ‖ model_revision ‖ ChunkerVersion ‖ EmbedRecipeVersion)`, concretely the lowercase hex sha256 of the four strings joined by `\n` (`topics.EmbedKey`).

`EmbedRecipeVersion` covers the prompt prefix, the token cap and the batching policy, which all change the vectors. Definition and control vectors carry the same `model_revision` and recipe in their key, so passages and definitions are always compared in one vector space.

- `payload_sha256` is the default words transcript's `payloadRef.sha256` (`manifest_transcripts.go:67`). Marks rewrite OpusTags but not this body, so a confirm never forces a new embed.
- `model_revision` is `<model id>@<catalogue source_sha256>`.
- The worker passes the stored key as `--known-key`. If the CLI computes the same key and the sidecar still holds this meeting's vectors in the current generation, the CLI exits with `{"unchanged": true}` before it takes the inference lock or loads the model. The worker then only rescores.

### 5. Chunking (`natural-1`)

Ported exactly from `refine_daily.py:16-47`:

- Drop empty words. Sort by `(startMs, endMs, index)`.
- Close the passage at the first matching rule: end of meeting; ≥ 25 words and a gap to the next word of ≥ 2000 ms; ≥ 25 words and the next word has a different speaker; ≥ 40 words and the word ends a sentence (`[.!?]["'’”]*$`); 120 words.
- **Token cap.** Prefix plus passage must fit 512 tokens. The chunker closes the passage at the word before it would exceed the cap (boundary `token cap`). It splits; it never truncates, because truncation drops content silently. This rarely fires in English (120 words is about 160 to 250 tokens) but matters for some languages. A single word over the cap is split at rune boundaries into pieces that each count as one word (boundary `long word split`), so none can support a topic.
- **Equal times.** Words with the same start and end keep their transcript order. The prototype broke such ties by word id as a string (`w_10` before `w_2`); on the prototype corpus that reorders words in 3 of 60 meetings, so Gate 1 feeds the Go chunker words pre-sorted the prototype's way.
- Words are whitespace tokens. This holds for the languages the shipped speech models produce. A script without spaces would need a different counter (open question 3).
- Passages under 12 words are kept but never support a topic.

### 6. Embedding

- Passages are sorted by token count. Batching (at most 16 rows or 1024 padded tokens) ships only if a test shows that padded vectors equal single vectors (cosine ≥ 0.99999). Otherwise batch size is 1, which is what was measured (about 7 to 8 s per 30-minute meeting at 8 threads).
- **Guards.** A vector that is not finite, has the wrong dimension, or has a norm outside 1 ± 1e-3 makes the CLI exit with a distinct "model fault" code. The meeting becomes `blocked` with reason `model-fault`.
- **Output.** JSON lines `{ordinal, startMs, endMs, wordCount, tokenCount, boundary, vector}` plus a header `{format, embedKey, payloadSha256, modelRevision, chunkerVersion, recipeVersion, dimensions, count, language, languageConfidence, detectorVersion, recordedLanguage?, truncatedNone: true}`. No passage text leaves the CLI. The audio digest is not in the header: the operator already holds the delivered digest. Exit codes: 0, 1 runtime, 2 usage, 3 no usable model or runtime, 4 model fault, 5 no usable transcript (stderr names `no-transcript`, `transcript-has-no-words` or `transcript-integrity`). `--words <file>` reads a plain word list instead of a meeting, for evaluation.
- **Language.** Production builds record no transcript language: `WriteManifest` never sets one (`transcribe/format.go:346-362`), and the single-transcript pack path copies none (`portable_meeting_tags.go:36-51`). So the CLI detects the language itself from the passage text (`internal/topics/language.go`) and reports it in the header. A language recorded in the bundle, when one exists, is reported next to it but does not replace it. A meeting below the confidence floor is `unknown`. `unknown` counts as not validated, and coverage shows it as "language unknown", never as a named language.
- The operator writes the meeting's `passage` rows in one sidecar transaction, under the rules in "Concurrent writers" and "Retirement".

### 7. Scoring

`topics_score.go`, pure Go, in the operator. A meeting is scored only when its `passage_set.model_revision` and recipe are the current ones and both control vectors exist for them. Otherwise it is skipped and counts as `waiting`. For each active topic that has a definition vector for the current model and recipe:

1. If a verdict exists for (meeting, topic), skip it.
2. If the meeting's **desired** annotations snapshot already carries the topic (by tag id, by an alias id, or by folded label), record a `confirmed` verdict with `source='direct'`, withdraw any suggestion and skip it. The desired snapshot (`annotation_head.desired`) counts a mark that has not uploaded yet.
3. Otherwise compute spans and the meeting rule:
   - a passage supports the topic when it has ≥ 12 words, `cos ≥ T` and `cos − max(controls) ≥ M`;
   - a non-supporting passage closes the span; a gap over 2500 ms or `cos(previous, this) < 0.88` starts a new span;
   - the meeting qualifies on ≥ 80 words from spans of ≥ 25 words, or on one span of ≥ 40 words with min score ≥ 0.88 and min margin ≥ 0.08.
4. Qualifies: upsert `pending`, replace its evidence. Does not qualify: set `withdrawn`. A row in state `decided` (see "Confirm") is never touched by the scorer.

`T` and `M` start at 0.85 and 0.05 and are fixed by the evaluation gate. Every constant, the control definitions, the validated-language list and the expected `DetectorVersion` belong to one `ScorerVersion`. Changing any of them changes `scorer_key`, which rescores without embedding. A meeting whose recorded detector version is not the expected one counts as language `unknown`.

The two built-in control definitions are generic English prose (as in the prototype). They are not language-neutral. Their effect on other languages is an evaluation item (open question 3).

### 8. Rescoring

- **Per topic** (create, definition edit, restore): embed the definition with `cassini topics embed-texts`, then rescore only that topic. Until its vector exists, the topic shows "preparing" and its old suggestions stay as they are. If the embed fails, the reconciler's text-vector rule retries it.
- **Global** (a `ScorerVersion` change): every `done` meeting. A model or recipe change is different: the reconciler's `embed_key` rule queues every meeting for a new embed at backfill priority, and it embeds every definition and both controls again. Until a meeting is embedded again, the scorer skips it (its `passage_set.model_revision` is old) and coverage counts it as `waiting`. Old-space passages are never compared with new-space definitions.
- The rescorer reads one meeting's vectors at a time from the sidecar (about 280 KiB), outside any annotations transaction. It then writes one meeting per short annotations transaction, under the "Rescorer check". It never loads the archive's vectors into memory at once. 1,000 meetings take a few seconds of CPU.

### 9. Reruns

- A rerun that publishes queues the meeting again.
- Same `payload_sha256`: no embed, rescore only.
- New transcript (a re-transcription or a speaker edit): new key, new passages, evidence ranges move with it.
- Verdicts are keyed on `(opus_name, tag_id)` only. **A rejection survives every rerun, model upgrade and definition edit.**
- Confirmed tags are ordinary marks, so `cassini annotate carry` keeps them on the new file (`publish_sink_nextcloud.go:64-91`).
- When the audio changes, carry keeps old marks but leaves them unresolved (`internal/cassini/annotate_test.go:590-612`), and `MutateBatch` then refuses any new mark (`cassini-annotations/mutate.go:16-21`). See "Confirm" for how the review UI handles this.

### 10. Failures

They follow the annotation outbox (`annotations_workers.go`): attempts, `retry_at` backoff from 1 min to 6 h, then `blocked` after 5 attempts with a message. Waiting for `.inference.lock` is not a failure: a build holds the lock for the whole transcription, including additional transcripts and the summary call (`transcribe.go:182-185`, `:326`). The CLI prints `{"locked":true}` once it holds the lock, and the operator arms the 10-minute deadline only then. A cancel or timeout before that line is a deferral that uses no attempt. Status reports lock wait separately. An admin can retry blocked rows. These go straight to `unavailable` with a reason: `no-transcript`, `transcription-disabled`, `transcript-has-no-words`, `transcript-integrity`. Reasons reuse the search ingest names where they exist (`search_ingest.go:54-64`). Coverage counts these meetings as "not checked", never as "no topics".

### 11. Retention

Retirement sets the tombstone first (`remote_retention_retire.go:56`). From then on every topic writer refuses the meeting (see "Retirement" under "Claim and admission"). Then, inside the existing retire steps:

- the sidecar `passage_set` and `passage` rows are deleted under `meetingProjectionLocks`, next to `searchStore.ForgetMeeting`;
- in the annotations retire transaction:
  - delete `topic_meeting`, `topic_suggestion` (cascading to evidence), `topic_verdict` and `topic_proposal_member`;
  - set `topic_receipt.response` to `{"expired":true}` where `opus_name` is the meeting, and scrub the meeting's entries from batch receipts found through `topic_receipt_target`, with the same `scrubRetiredResult` the annotation receipts use (`:83-117`);
  - for every proposal that had a member in the meeting: set `state='recompute'`, clear `draft_name`, `hints_json` and `centroid`, and mark its room in `topic_room_dirty`. A proposal in `recompute` is never served.

The background recompute is the only thing that can take a proposal out of `recompute`. It rebuilds the centroid and the draft name from live members only. A proposal left with fewer than 3 meetings becomes `stale` and is hidden. A dismissed proposal returns to `dismissed` with its recomputed centroid, so the dismissal still suppresses it. A proposal with no members left is deleted. If the recompute crashes, or naming waits for search, the proposal stays in `recompute` and stays hidden.

## Data model

### Durable: `annotations.sqlite3`, schema 7 → 8

The upgrade is a `version < 8` step in the existing ladder (`annotations_documents.go`). It runs whether or not the feature is on. It is one-way: an older image refuses a database with a newer schema (`annotations_documents.go:37-39`), and that takes all tagging down, not only topics. The release notes state the rollback limit, and the upgrade procedure takes a backup of `annotations.sqlite3` first.

**Why this database.** A confirm must write the mark and the verdict together. `commitDocument` already writes the mark in one transaction on this database's single connection (`annotations_mutations.go:115`, `inTx`). With the verdict in the same transaction, no meeting can carry the tag while its suggestion still shows as pending, and a concurrent reject cannot land after another person's confirm. `jobs.sqlite3` would need a two-phase reconciler. Both are operator databases and both must be backed up (open question 1).

```sql
-- Human truth: the library. tag_id is an annotation tag id.
CREATE TABLE topic_library (
  tag_id         TEXT PRIMARY KEY,
  label          TEXT NOT NULL,
  label_folded   TEXT NOT NULL,           -- foldTagLabel
  definition     TEXT NOT NULL,           -- 1..1000 runes, written by a person
  definition_sha TEXT NOT NULL,           -- sha256(NFC(definition))
  color TEXT, icon TEXT,
  status         TEXT NOT NULL,           -- active | retired
  merged_into    TEXT,                    -- set when retired by a merge
  origin         TEXT NOT NULL,           -- manual | tag | proposal
  proposal_id    TEXT,
  revision       INTEGER NOT NULL,        -- optimistic concurrency
  created_by TEXT NOT NULL, created_at INTEGER NOT NULL,
  updated_by TEXT NOT NULL, updated_at INTEGER NOT NULL);
CREATE UNIQUE INDEX topic_library_active_label
  ON topic_library(label_folded) WHERE status = 'active';

CREATE TABLE topic_library_event (        -- audit and restore
  id INTEGER PRIMARY KEY, tag_id TEXT NOT NULL, revision INTEGER NOT NULL,
  actor TEXT NOT NULL, at INTEGER NOT NULL, change_json BLOB NOT NULL,
  redacted_by TEXT, redacted_at INTEGER);  -- set: change_json holds a tombstone, not the text

CREATE TABLE topic_tag_alias (            -- another tag id that is the same topic
  alias_tag_id TEXT PRIMARY KEY, tag_id TEXT NOT NULL, created_at INTEGER NOT NULL);

-- Human truth: verdicts. The scorer only ever adds source='direct'.
CREATE TABLE topic_verdict (
  opus_name TEXT NOT NULL, tag_id TEXT NOT NULL,
  verdict   TEXT NOT NULL,                -- confirmed | rejected
  source    TEXT NOT NULL,                -- review | direct (meeting already carried the tag)
  actor_id  TEXT,                         -- NULL for source='direct'
  at INTEGER NOT NULL,
  operation_id TEXT,                      -- the confirm's annotation operation
  local_tag_id TEXT,                      -- set when the mark went on a same-label local tag
  payload_sha256 TEXT, definition_sha TEXT, scorer_key TEXT,  -- for evaluation
  rule TEXT, support_words INTEGER, peak REAL, min_margin REAL, -- server-only
  PRIMARY KEY (opus_name, tag_id));

CREATE TABLE topic_receipt (              -- idempotency for writes that touch no file
  caller TEXT NOT NULL, request_id TEXT NOT NULL, request_hash TEXT NOT NULL,
  opus_name TEXT,                         -- single-meeting requests; scrubbed on retire
  response BLOB NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY (caller, request_id));
CREATE INDEX topic_receipt_by_meeting ON topic_receipt(opus_name);
CREATE TABLE topic_receipt_target (       -- batch and proposal requests: every meeting named
  caller TEXT NOT NULL, request_id TEXT NOT NULL, opus_name TEXT NOT NULL,
  PRIMARY KEY (caller, request_id, opus_name));
CREATE INDEX topic_receipt_target_by_meeting ON topic_receipt_target(opus_name);

-- Definition and control vectors. Small (200 topics x 3 KiB), so they live here
-- and survive a sidecar rebuild. Derived data: the reconciler refills missing rows.
CREATE TABLE topic_text_vector (
  kind TEXT NOT NULL,                     -- definition | control
  sha TEXT NOT NULL, model_revision TEXT NOT NULL, recipe_version TEXT NOT NULL,
  vector BLOB NOT NULL,                   -- 768 x float32 LE, L2-normalised
  PRIMARY KEY (kind, sha, model_revision, recipe_version));

-- Work state: the outbox and the coverage record.
CREATE TABLE topic_meeting (
  opus_name TEXT PRIMARY KEY,
  job_id    TEXT NOT NULL,
  state     TEXT NOT NULL,                -- queued | running | done | unavailable | blocked
  priority  INTEGER NOT NULL,             -- 0 publish, 1 rescore, 2 backfill
  reason    TEXT NOT NULL DEFAULT '',
  delivered_digest TEXT,                  -- from the publish hook or the archive
  published_attempt INTEGER,              -- compared with artifact_availability
  requeue INTEGER NOT NULL DEFAULT 0,     -- a publish arrived while running
  claim TEXT, claimed_at INTEGER,
  payload_sha256 TEXT, audio_sha256 TEXT,
  language TEXT, language_confidence REAL, detector_version TEXT,  -- detected by the CLI
  embed_key TEXT, vectors_generation TEXT, scorer_key TEXT,
  passages INTEGER, tokens INTEGER,
  attempts INTEGER NOT NULL DEFAULT 0, retry_at INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  queued_at INTEGER NOT NULL, finished_at INTEGER);

-- The review queue: machine output, never shown once a verdict exists.
CREATE TABLE topic_suggestion (
  opus_name TEXT NOT NULL, tag_id TEXT NOT NULL,
  state     TEXT NOT NULL,                -- pending | withdrawn | decided (hidden while a verdict exists)
  rule      TEXT NOT NULL,                -- supporting-words | short-strong
  support_words INTEGER NOT NULL,
  peak REAL, min_margin REAL,             -- server-only, never served on USER routes
  definition_sha TEXT NOT NULL, scorer_key TEXT NOT NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  PRIMARY KEY (opus_name, tag_id));
CREATE INDEX topic_suggestion_by_tag ON topic_suggestion(tag_id, state);

CREATE TABLE topic_evidence (             -- time ranges only, no text
  opus_name TEXT NOT NULL, tag_id TEXT NOT NULL, ordinal INTEGER NOT NULL,
  start_ms INTEGER NOT NULL, end_ms INTEGER NOT NULL,
  word_count INTEGER NOT NULL, counted INTEGER NOT NULL,  -- 1: counted toward the rule
  PRIMARY KEY (opus_name, tag_id, ordinal),
  FOREIGN KEY (opus_name, tag_id) REFERENCES topic_suggestion ON DELETE CASCADE);

-- Proposals.
CREATE TABLE topic_proposal (
  id TEXT PRIMARY KEY, room_id TEXT NOT NULL,
  state TEXT NOT NULL,                    -- open | approved | linked | dismissed | stale | recompute
  prior_state TEXT,                       -- the state to return to after a recompute
  centroid BLOB,                          -- 768 x float32, from live member passages only; NULL in recompute
  model_revision TEXT NOT NULL, recipe_version TEXT NOT NULL, chunker_version TEXT NOT NULL,
  draft_name TEXT, hints_json TEXT, flags_json TEXT NOT NULL,  -- NULL in recompute
  nearest_tag_id TEXT,
  meeting_count INTEGER NOT NULL, day_count INTEGER NOT NULL, passage_count INTEGER NOT NULL,
  linked_tag_id TEXT, decided_by TEXT, decided_at INTEGER,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE topic_proposal_member (      -- every passage that contributed
  proposal_id TEXT NOT NULL REFERENCES topic_proposal(id) ON DELETE CASCADE,
  opus_name TEXT NOT NULL, start_ms INTEGER NOT NULL, end_ms INTEGER NOT NULL, sim REAL NOT NULL,
  PRIMARY KEY (proposal_id, opus_name, start_ms));
CREATE INDEX topic_proposal_member_by_meeting ON topic_proposal_member(opus_name);

CREATE TABLE topic_room_dirty (room_id TEXT PRIMARY KEY, dirty_at INTEGER NOT NULL);
CREATE TABLE topic_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);  -- enabled_at, backfill_since
```

**No transcript text is stored in any new table.** Evidence is millisecond ranges. Quotes are read at request time (see "API").

**Write load.** This database runs in WAL mode (`search_store.go:253-257`) but has a single connection (`annotations_documents.go:29`). So every request on it, reads included (vocabulary, coverage, the USER GET routes), waits behind whatever transaction holds that connection. The worker and the rescorer therefore read vectors from the sidecar and compute scores outside any annotations transaction. Each annotations transaction only writes one meeting's precomputed rows and commits, and they yield between meetings. A person's request then waits at most one very short write.

### Disposable: `topics.sqlite3`

Opened like `search.sqlite3` (WAL, busy timeout). A `user_version` mismatch deletes and rebuilds it. It holds no titles, rooms, user ids or text.

```sql
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);   -- 'generation': random per creation
CREATE TABLE passage_set (
  opus_name TEXT PRIMARY KEY, embed_key TEXT NOT NULL, model_revision TEXT NOT NULL,
  chunker_version TEXT NOT NULL, recipe_version TEXT NOT NULL, built_at INTEGER NOT NULL);
CREATE TABLE passage (
  opus_name TEXT NOT NULL REFERENCES passage_set ON DELETE CASCADE,
  ordinal INTEGER NOT NULL, start_ms INTEGER NOT NULL, end_ms INTEGER NOT NULL,
  word_count INTEGER NOT NULL, token_count INTEGER NOT NULL, boundary TEXT NOT NULL,
  vector BLOB NOT NULL,                   -- 768 x float32 LE, L2-normalised
  PRIMARY KEY (opus_name, ordinal));
```

Definition and control vectors are not in this file. They are in `topic_text_vector` in `annotations.sqlite3`, so a sidecar rebuild never leaves the library without vectors.

**Size.** About 91 passages per meeting × 3 KiB is about 280 KiB per meeting, so about 280 MiB per 1,000 meetings. The storage usage panel lists this file.

**Recovery.** When the sidecar is rebuilt, its generation changes. The reconciler queues every `done` meeting again at backfill priority. Suggestions and verdicts stay. Definition and control vectors stay too, because they are in `annotations.sqlite3`; if any are missing, the reconciler's text-vector rule embeds them before any meeting is scored. Passage vectors are gone, so this costs a full re-embed of the archive. Until a meeting is embedded again it counts as `waiting`, not checked.

## Embedding runtime

| Concern | Decision |
|---|---|
| Process | One `cassini topics embed` subprocess per meeting, started with `Setpgid` and killed as a group on timeout or preemption (the pattern at `speaker_refine.go`, `models.go:466`). Timeout 10 min, armed only when the CLI reports `{"locked":true}`. |
| Lock | The CLI takes `transcribe.LockModelRuntime` (`model_admission.go:19`) only after the key check, and holds it for the embed. Builds, probes and diarization serialise with it. A build waits at most one meeting (10 to 40 s). An embed may wait much longer for a build; that wait uses no attempt (see "Failures"). |
| Threads | `min(4, governor CPU budget)` (`operator/resource.go:15-34`). `CASSINI_TOPICS_THREADS` overrides it; it is declared in `info.xml`. |
| Memory | Probe floor 1536 MiB (measured peak RSS 0.58 GB with batches of at most 1024 padded tokens; 1.78 GB at 4096). Embed runs wait for `floor + headroom` through `waitForMemory`. |
| Preemption | A recording start cancels a running embed through the generalised cancel set. The row returns to `queued` with no attempt used. |
| Token cap | 512 tokens including the prefix; split, never truncate. |
| Batching | Only after the padding parity test passes; otherwise batch size 1. Measured: parity holds (min cosine 1.0000000 over 600 natural passages), 1.7× faster at 4 threads; at most 16 rows and 1024 padded tokens per batch. |
| Guards | Finite values, 768 dimensions, unit norm; otherwise exit code "model fault". |
| Platforms | amd64 CPU image only in v1. The CLI checks the ONNX Runtime API version at start and reports `unsupported-runtime` on an image where it is too old (open question 12). |

## Model catalogue

- **Entry.** `embeddinggemma-2-text-int8`, kind `embedding`, device `cpu` only.
- **Files.** `model_quantized.onnx`, `model_quantized.onnx_data` (this exact name, because the graph refers to it) and `tokenizer.model`. `Validate` requires all three for this kind and keeps the existing URL and flat-name rules (`catalogue.go:132,149-151`).
- **Sources.** Pinned by commit: `onnx-community/embeddinggemma-2-ONNX@daa72c51` for the graph and `google/embeddinggemma-2@914f7f89` for the tokenizer. Re-hosted under `https://dist.gocassini.com/models/files/<sha>/` since 2026-10-14 ([model-distribution.md](model-distribution.md)). The download is about 289 MB with zstd.
- **Licence.** A `license` field and a `NOTICE.txt` section. The model card links to Google's "Gemma 4 license" page, which carries the plain Apache License 2.0 text (read 2026-10-14). The catalogue entry says `Apache-2.0`.
- **Readiness: a probe receipt not bound to the boot.** The runtime fingerprint changes on every reboot and every image upgrade: it includes `/proc/sys/kernel/random/boot_id`, the executable's size and mtime, and the `libonnxruntime*` stats (`transcribe/model_store.go:124-151`). The operator re-probes only the active speech model at startup (`operator/models.go:343-355`). A receipt bound to that fingerprint would turn the feature off after every restart. So the embedding model is Ready when its files are installed (`s.Complete(m) == nil`), a cheap runtime check passes (the ONNX Runtime library loads and its API version is new enough; checked on every listing, like the diarizer's), and its probe passed. The probe's receipt is bound to a constant (`topics.ReadyFingerprint`), never to the boot or the image: a reboot or an upgrade keeps it, a failing probe removes it, and a probe that only waited (memory, the lock) leaves it as it was.
- **Probe.** At install, embed 4 pinned public strings in several languages and compare with golden int8 vectors committed next to the catalogue (cosine ≥ 0.9995). A failure fails the install. At operator startup, when `topic_suggestions.enabled` is set, the operator queues the same probe in the background (next to `models.go:343`). A failing probe pauses the worker with reason `model-fault` and shows in Settings; a passing one changes nothing. The feature keeps running across a restart while the probe waits for memory or the lock.
- **Install.** Through the existing ADMIN `operator/settings/models/install` route, the Settings installer, or `cassini models install|pack|import` for air-gapped hosts.

## Topic library

### Identity

A topic is a `topic_library` row. Its `tag_id` is an annotation tag id, so it exists before any file carries it.

**Create** `{label, definition}`:

1. If `fromTagId` is given, the caller must see that tag (`TagVisible(id, visible)`). The topic adopts it (origin `tag`), so existing marks join the topic.
2. Else, if the folded label matches a tag in the caller's visible vocabulary (`ResolveLabel`, `annotations_store.go:342-369`), adopt that id, unless that id is already a `topic_library` row:
   - a `retired` row: answer 409 `topic-retired` with the row's id, and the UI offers **Restore** instead. A retired topic's marks stay on files, so its id stays in the vocabulary, and inserting it again would break the `tag_id` primary key. If the person declines Restore and confirms "create a separate topic", skip adoption and mint a new id;
   - an `active` row: the unique label index already refuses this (409 `label-taken`).
3. Else mint a new id (`ann.NewAnnotationTagID`).

It never adopts an id that exists only on meetings the caller cannot see. The cost: the same label can end up under two ids across access groups (open question 9). Existing per-file vocabularies are never imported in bulk, because that would publish labels that only exist on meetings some users cannot read. Creation answers 503 while the tag index is building (`errAnnotationIndexBuilding`).

### `resolveVocabulary` becomes library-aware

Today a manual add from the TagPicker resolves a tag id the caller cannot see by its label, and mints a new id if the label is unknown (`annotations_meetings_request.go:155-176`). That would split a library topic into two ids. The change:

- an id counts as known when `TagVisible(id) || library.Active(id)`;
- a typed label that matches no visible tag resolves to the active library id with that folded label before a new id is minted;
- `prepareInitialTagAppearance` uses the library colour and icon for a library id.

This leaks nothing: library labels are already visible to every caller who can tag. Bulk tagging (`annotations_batch.go`) goes through the same function, so it is covered too.

### Edits

- Every edit carries `expectRevision`; a mismatch answers 409.
- Every edit appends a `topic_library_event`. People can view a topic's history and restore an earlier label or definition.
- **Redact a revision.** An admin, or the person who wrote that revision, can redact a past revision. Redaction replaces its label and definition in `change_json` with a tombstone and keeps the actor and the time. It is for text that should never have been saved, such as words pasted from a meeting or a private-room term. A redacted revision cannot be restored. The current revision cannot be redacted; edit it first.
- A definition edit changes `definition_sha`, embeds the new definition and rescores that topic.
- **Caps.** At most 200 active topics (the per-file tag limit). Each user may make at most 30 definition edits or creates per hour; more answers 429. Each edit triggers an archive-wide rescore of one topic, so this stops an easy CPU drain.
- **Retire.** No new suggestions; pending ones become `withdrawn`; verdicts and marks stay. **Restore** rescores the topic; rejections still hold.

### Interplay with the existing tag jobs (`annotations_tag_changes.go`)

| Change | Library | Files |
|---|---|---|
| Rename or restyle a library tag | The job updates the library row first, so later confirms write the new label and colour. | The existing per-caller job rewrites the caller's visible files. Other files keep the old label, as today. The UI shows the library label. |
| Merge A into B | A is retired with `merged_into = B`. A's pending suggestions are withdrawn. If B has no definition, B is added and takes A's. A's rejections are copied to B where B has no verdict, because a merge says the two tags mean the same thing (open question 4). B is rescored. | Existing merge job. |
| Delete a library tag | The row stays. The dialog offers "Also retire this topic". | Existing delete job. |

### Who sees the library

- The routes answer 404 to a caller whose visible set is empty, and 502 when the visible set cannot be worked out. "Anyone who can tag" therefore means "anyone who can read at least one meeting".
- Labels, definitions, colours, icons, status and the time of the last edit are visible to all of these callers.
- **User ids are not served** on library routes. The audit log, with actor ids, is visible to admins only (open question 8).
- Usage and pending counts are computed over the caller's visible set only. No global counts, first-seen or last-used dates are ever served.
- The editor says: "Everyone who can tag will see this name and definition. Do not paste text from a meeting."
- Adopting a tag that so far appears only in a private room publishes its name deployment-wide. The editor warns about this whenever `fromTagId` is used, and whenever the caller's visible meetings that carry the tag come from fewer than 2 rooms (catalog entries carry `roomId`, `search_endpoint.go:389-394`). The warning names those rooms.

## Review: states and API

### State machine for one (meeting, topic)

```
                  qualifies, no verdict, tag absent
  (none) ───────────────────────────────────────────► pending
  pending ──stops qualifying / topic retired or merged──► withdrawn ──qualifies again──► pending
  pending ──person: confirm──► CONFIRMED   (mark and verdict in one transaction; suggestion → decided)
  pending ──person: reject───► REJECTED    (suggestion → decided)
  meeting already carries the tag ──scorer──► CONFIRMED (source=direct)
  CONFIRMED ──person: undo (toast)──► undo-operation + verdict deleted + decided → pending, one tx
  REJECTED  ──person: undo or reopen──► verdict deleted + decided → pending, one tx
```

Upper case marks a verdict. The scorer never changes a verdict, and never touches a `decided` row. A decision keeps the suggestion row and its evidence (state `decided`, never served), so Undo and Reopen bring the same suggestion back at once. No rescore is needed. If the row was never a suggestion (a `direct` verdict), Reopen runs a synchronous rescore of that one (meeting, topic) from the sidecar. A tag that a person removes by hand later stays `confirmed`, so it is not suggested again; Reopen is the way back.

### Confirm

1. `visibleRecording` resolves the meeting against the caller's current share (uncached `currentRecordingPath`).
2. **Replay first.** Derive `RequestID = sha256("topic-confirm" ‖ opus ‖ tag ‖ caller ‖ client requestId)`, cut to the 64-character id pattern (`annotations_meetings_request.go:32`), and derive `OperationID` from `RequestID` the same way. If `annotation_receipt` already holds `(caller, RequestID)`, return that receipt with the stored verdict, marked as replayed. This check runs before step 3, because after the first success the suggestion is no longer `pending`.
3. Check: the feature is on, the topic is active, the suggestion is `pending`, and there is no verdict.
4. Pick the tag to mark from the desired snapshot:
   - the file has the library id: mark it;
   - the file has a different id with the same folded label: mark that local id, and record `topic_tag_alias` and `local_tag_id`, so one file never shows two chips with one name (`findAnnotationTag` honours an explicit id and would otherwise add a duplicate label, `cassini-annotations/ops.go:514-531`);
   - otherwise mark the library id with the library label.
5. Call `commitDocument` with:
   - `Accepted: true`, `ActorKind: "person"`, one op `{op:"mark", tag:{id,label}, target:{kind:"meeting"}}`;
   - `TagStyles: [{label, color, icon}]` from the library (`annotations_meetings_request.go:46-47`);
   - `OperationID` and `RequestID` from step 2. `OperationID` is kept in the verdict for Undo.
   - `commitDocument` hashes the whole request, including `operationId`, `tagStyles` and the label in the op (`annotations_mutations.go:52-57`), and answers 409 when a reused `requestId` has a different hash (`:59-66`, `:119-125`). With derived ids the hash changes only if the library label or style changed between two attempts. If `commitDocument` answers that 409 on the derived id, the confirm re-reads the receipt and replays it, as in step 2, instead of failing;
   - **`Namespace: index.Namespace(ctx)`**. The trusted path passes `""` today, and `MutateBatch` then mints a new random namespace for a file that has no annotations (`cassini-annotations/mutate.go:38-43`). That is the most common case for a confirm. While the index is building, the confirm answers 503 "tag index preparing";
   - `InTx`: insert the verdict and set the suggestion to `decided` inside the same transaction.
6. If `MutateBatch` refuses because old marks are bound to different audio (`mutate.go:16-21`), answer 409 `marks-unresolved`. The UI explains that the meeting's audio changed and its old marks need attention first. Reject still works, because it writes no file.

Steps 2 and 5 make a retried confirm return the first answer.

**Reject, undo and reopen** first re-check the caller's share with `readDocument`. A person who lost the share cannot change the queue. They use `topic_receipt` for idempotency, with `opus_name` set, so retirement can expire them. Batch and proposal requests record every meeting they name in `topic_receipt_target`.

### Routes

One new USER entry in `info.xml`: `^annotations\/topics(\/.*)?$`, verbs `GET,POST`. All GETs are fetched with `cache:'no-store'`. A new route needs a versioned app upgrade, because an image swap keeps the old route allowlist.

| Route | Request | Response |
|---|---|---|
| `GET annotations/topics` | | `{enabled, ready, reason?, library:[{tagId,label,definition,color,icon,status,mergedInto,revision,updatedAt,preparing,broad,usage:{meetings,pending}}]}` |
| `GET annotations/topics/coverage` | `?room=` | `{visible, checked, waiting, notChecked, unavailable:{reason:n}, blocked, checkedSince, unvalidatedLanguage, unknownLanguage}` |
| `POST annotations/topics/library` | `{requestId,label,definition,color?,icon?,fromTagId?}` | `{topic}` |
| `POST annotations/topics/library/<tagId>` | `{requestId,expectRevision,label?,definition?,color?,icon?,status?}` | `{topic, job?}` (a rename starts the existing tag job) |
| `GET annotations/topics/library/<tagId>/history` | | `{revisions:[{revision,label,definition,at,redacted,canRedact}]}` (a redacted revision has no label or definition) |
| `POST annotations/topics/library/<tagId>/history/<revision>` | `{requestId, action:"redact"}` | `{revision}`; 403 unless the caller is an admin or wrote that revision |
| `GET annotations/topics/meetings/<meetingId>` | | `{state, reason?, language:"<code>"\|"unknown", validatedLanguage, confirmable, blockedReason?, suggestions:[{tagId,label,definition,color,icon,rule,supportWords,evidence:[{startMs,endMs,wordCount,counted}]}], decided:[{tagId,label,verdict,source,by,at}]}` |
| `POST annotations/topics/meetings/<meetingId>/<tagId>` | `{requestId, decision:"confirm"\|"reject"\|"undo"\|"reopen", stateToken?}` | `{verdict?, annotations?: annotateResult}` |
| `GET annotations/topics/review` | `?tag=&room=&cursor=&limit≤50` | `{groups:[{tagId,label,items:[{meetingId,title,date,rule,supportWords,quote?}]}], next, coverage}` |
| `POST annotations/topics/review/batch` | `{requestId, tagId, meetingIds[≤100], decision:"confirm"\|"reject"}` | `{results:[{meetingId,status,error?}]}` |
| `GET annotations/topics/proposals` | `?room=` | `{proposals:[{id,roomId,draftName,hints,flags,nearest?,meetings,days,evidence:[{meetingId,startMs,endMs,quote}]}]}` |
| `POST annotations/topics/proposals/<id>` | `{requestId, action:"approve"\|"link"\|"dismiss"\|"restore", label?, definition?, color?, tagId?}` | `{proposal, topic?}` |

Rules for every route:

- **Visible set.** Every read binds the caller's visible set inside SQL with `json_each`, as `search_query.go` does. If the visible set cannot be worked out, the answer is 502, never an empty list.
- **Counts.** Every count (usage, pending, "broad", coverage, "checking N meetings") is over the caller's visible set.
- **Quotes.** The meeting page renders quotes from the transcript it already loaded, by time range. The review queue and the inbox read one short quote per item from the search index's `segment_ref` rows at request time, for visible meetings only. When search does not hold the meeting, the item shows no quote.
- **Scores.** Raw cosines and margins are never served on USER routes. People see the rule in words and the number of supporting words.
- **Batch confirm** runs each meeting through the same trusted confirm path. It does not use `applyAnnotationBatch`.
- **Off.** When the feature is off, GETs answer 200 `{enabled:false}` and POSTs answer 409 `disabled`. Stored rows are kept and not served.
- **Actors.** v1 accepts `person` only on these routes (R16).

### ADMIN routes

| Route | Manifest |
|---|---|
| `GET operator/settings/topics`: settings plus content-free operational status: `topic_meeting` counts by state and reason, oldest waiting age, blocked list (meeting names, reasons), tokens/s, lock wait, preemptions, memory deferrals, model revision, probe state, `ScorerVersion`, sidecar size, library audit with actor ids. **No** suggestion or verdict counts, per topic or in total. | existing `^operator\/settings(\/.*)?$` GET,PUT |
| `PUT operator/settings/topics` `{enabled, threads?}`: enabling answers 409 unless the embedding model is Ready | existing |
| `POST operator/settings/topics/backfill` `{since?}` → `{queued, estimateSeconds}` | **new** `^operator\/settings\/topics\/(backfill\|retry)$` POST ADMIN, following `operator/settings/models/install` |
| `POST operator/settings/topics/retry` `{meetings?}` (none means every blocked row) | same new entry |

The existing settings wildcard allows only GET and PUT (`info.xml:271-273`), so the two actions need the new POST entry. In the operator, `/settings/topics` and `/settings/topics/` must be registered in `operatorAPIRoutes` (`run.go:969-977`); otherwise the `/settings/` prefix sends them to the LLM settings handler. The ADMIN `operator/status` JSON also gains a short `topics` block with the same content-free fields.

**Why no suggestion or verdict counts on ADMIN routes.** Admin gives no extra meeting visibility in this codebase, and today no ADMIN route serves tag or content aggregates (job JSON carries room names and timestamps only, `run.go:1500-1560`; no admin or status handler reads `annotation_tag`). Anyone who can tag can write a definition. So an admin who can read one meeting could write a probe definition, wait for the archive-wide rescore, and read from a deployment-wide count how many meetings match, including meetings they cannot read. Per-topic quality numbers are therefore only in the offline `cassini-operator topics-export`, which needs shell access on the server, and in the visible-set counts on USER routes.

## Recurring-topic proposals

**When.** A scored meeting marks its room dirty. A room is reclustered at most every 6 hours, only while recording is idle, and every room once a night. It is pure Go over the sidecar.

**Room.** From the catalog entry's `roomId` in `meetings.sqlite3` (`meeting_metadata_store.go:19-25`). A meeting with no room is not clustered. The window is the room's last 90 days (open question 6).

**Algorithm** (deterministic for a fixed input):

1. **Candidates:** passages with ≥ 40 words from `done` meetings in the room whose best active-topic score fails the **same** gate as suggestions (`T`, `M`). This fixes the prototype's hard-coded 0.83/0.04, which did not match its 0.85/0.05 gate.
2. **Order** by meeting start, then `start_ms`.
3. **Group:** join the nearest centroid (normalised running sum) at cosine ≥ 0.90, else start a new group. Then one reassignment pass against the final centroids, to reduce order effects.
4. **Keep** groups with ≥ 3 distinct meetings on ≥ 3 distinct days. At most 12 open proposals per room.
5. **Flags** (no word lists): `routine` is computed at read time, when the group's member meetings are more than 50% of the caller's visible `done` meetings in the room in the group's date range. It is never stored, because a count over the whole room would include meetings the viewer cannot read and reveal that they exist. `edges` (stored) when most of its passages sit in the first or last 5% of their meeting (greetings and wrap-ups). Flagged groups sort last and start collapsed.
6. **Draft name** (no built-in stop list, any language):
   - tokens are runs of Unicode letters and digits of ≥ 3 runes, case-folded;
   - document frequency is computed over all passages of the **member meetings only**, never the whole room, so no hidden meeting shapes the name;
   - terms in more than 50% of those passages are dropped (a stop list learned from the data);
   - the members' own speaker labels are dropped;
   - score = in-group frequency × log(N / df), keeping terms seen in ≥ 2 member meetings;
   - the top 5 that are not substrings of each other become hints; the draft name is the top 2 joined by " / ". The UI always labels it a draft.
   - Passage text for naming is read from the search index's `segment_ref` rows of the member meetings by time range. If search does not hold a member, naming waits, and the proposal is not served until it has a name.
7. **Nearest topic:** cosine of the centroid to each definition. Shown as a hint only at ≥ 0.90; the prototype's 0.83 flagged 11 of 12 groups. To be calibrated.
8. **Identity across passes:** a new group matches a stored proposal in the room by member-passage overlap ≥ 50%, else centroid cosine ≥ 0.95. The centroid test is used only when the stored proposal has the current `model_revision`, `recipe_version` and `chunker_version`; centroids from different vector spaces are never compared. A proposal from an older revision first has its centroid recomputed from its re-embedded members. Until then it matches only by member meeting and time overlap (a member passage of the same meeting whose time range overlaps by ≥ 50%), which also covers a chunker change, where `start_ms` values move. A match keeps its id and state. A match with a `dismissed`, `approved` or `linked` row stays suppressed. An unmatched `open` row becomes `stale`.

**Visibility.** A proposal is listed or actionable only if its `state` is `open` (or `dismissed`, for Restore), it has at least one member, and **every** `topic_proposal_member.opus_name` is in the caller's visible set: `EXISTS` a member `AND NOT EXISTS` a member outside the visible set. Without the `EXISTS` test, a proposal whose members were all deleted would pass the `NOT EXISTS` test for everyone. A proposal in `recompute` is never served. This uses all contributing meetings, not just the quoted ones, because the name, hints and centroid come from all of them. Share sets are fixed at first publish (`publish_sink_direct_shares.go:121`), so two meetings of one room can have different audiences; the strict rule covers that. What remains is an absence signal: a visible passage may be missing from a group because a hidden meeting pulled the centroid. This is weaker than search's accepted ranking leak (`search_query.go:35-43`) and is documented next to it.

**Actions** (anyone who can see the proposal):

- **Create topic:** opens the editor with **empty** label and definition fields. The draft name and hints are shown next to the fields, labelled "Words from meetings in <room>". The library is visible to everyone who can tag, while the draft name comes from transcripts that only this proposal's audience can read; it can hold names of people or projects, and the room may be a private 1:1 call (room type is not stored, open question 5). If the label or definition the person types shares a term with the draft name or the hints, saving needs a blocking confirmation: "These words come from meetings in <room>. Everyone who can tag will see them." A human-written label and definition are required. The topic gets origin `proposal`. Its suggestions then go through review; nothing is confirmed automatically.
- **Link to topic:** records `linked_tag_id`, rescores that topic on the member meetings, and offers to edit its definition. The same term-overlap confirmation applies to that edit.
- **Dismiss:** durable; suppresses future matches. **Restore** undoes it.

## UI

All components use Svelte in legacy mode, DaisyUI tokens and both themes, with Vitest browser tests. The prototype UI is not reused; only its semantics are.

### DataProvider (`cassini-viewer/src/viewer/dataProvider.ts:124-141`)

New optional methods: `loadTopicStatus`, `loadTopicLibrary`, `saveTopic`, `loadTopicHistory`, `loadMeetingSuggestions`, `decideSuggestion`, `loadReviewQueue`, `decideSuggestionBatch`, `loadTopicCoverage`, `loadTopicProposals`, `decideTopicProposal`. A missing method hides its feature. `StaticCatalogProvider` implements none of them, so the public viewer never shows a suggestion. `cassini-app/src/appDataProvider.ts` attaches them only when `GET annotations/topics` says `enabled`.

### Meeting page

- `MeetingTags.svelte`: a **Suggested** group after the confirmed chips. A new `TagChip` variant `suggested` (dashed outline).
- `components/topics/SuggestionPopover.svelte`:
  - the topic's definition;
  - why, in words: "Discussed for about 2 minutes across 3 passages" or "One short, focused discussion";
  - 1 to 4 evidence quotes from the loaded transcript; a timestamp click calls the existing `seek()` and highlights the stretch;
  - **Confirm** and **Not this meeting** buttons;
  - the line "Suggested from the transcript. Not saved to the recording until someone confirms it.";
  - when `validatedLanguage` is false: "Suggestions have not been checked for this meeting's language.";
  - when `confirmable` is false: the `marks-unresolved` explanation, with Confirm disabled.
- Confirm turns the chip into an ordinary tag credited to the person and shows an **Undo** toast (sends `undo`).
- A collapsed "Decided for this meeting (n)" list shows rejections and confirmations with **Reopen**.
- `TranscriptFrame` and `MarkingRail` draw the open suggestion's evidence in a dotted, read-only style; `SectionsNav` steps through it.
- A coverage note when the meeting is waiting, not checked, or unavailable, with the reason.

### Browse

- `RoomsRail`: a **Needs review (n)** item above Tags. It opens `components/topics/NeedsReview.svelte`: the review queue grouped by topic, keyboard confirm and reject, select several meetings and confirm one topic on all of them (at most 100).
- `MeetingList` rows: a dashed "2 suggested" badge.
- Tag filters and search never match suggestions.

### Tag manager (`components/tags/TagManager.svelte`)

- A **Topics** tab: the library list with usage and pending counts (visible set), "Describe this tag" on existing tags, `TopicEditor` (name, definition, colour, icon, the visibility warning), retire and restore, history and restore, "preparing" state, and a **Very common** warning when a topic is pending or confirmed on more than 60% of the caller's checked meetings.
- A **New topics** tab: `TopicInbox.svelte` with a room selector. Each card shows the draft name marked "draft", hints, quotes from 3 or more meetings with links, flags, the nearest-topic hint and the three actions.
- The delete and merge dialogs gain the retire option.

### Coverage text

The review queue and an "About suggestions" popover show one line, always over the caller's visible set, for example:

> Checked 112 of the 130 meetings you can see · 12 waiting · 6 could not be checked (no transcript) · meetings before 3 Oct not checked yet · 9 in languages not yet validated · 3 language unknown.

And the limits, in plain words: a missing suggestion does not mean the topic was absent; short mentions are missed; speech-recognition errors carry through.

### Admin Settings (`cassini-app`)

Under Settings → *After each meeting* (`SettingsPanel.svelte:623+`), a **Topic suggestions** row, in a new `TopicSuggestionsSettings.svelte` modelled on `VoiceSeparationModel.svelte`:

- the model card (download size, licence, install, probe state);
- the enable switch, disabled until the probe passes;
- **Check past meetings**, with an optional start date, a time estimate and progress;
- queue and coverage counts, the blocked list with **Retry**, the last error.

`OperatorClient` gains `getTopicSettings`, `putTopicSettings`, `startTopicBackfill`, `retryTopicMeetings`.

## Settings and enablement

- `settings.json` gains `topic_suggestions: {enabled: false, threads: 0}`, saved through ADMIN `PUT operator/settings/topics`.
- Enabling answers 409 unless an embedding model is installed and Ready. Ready does not depend on the boot or the image (see "Model catalogue"), so a reboot or an upgrade does not switch the feature off. Uninstalling the model, or a runtime check that stops passing, behaves as off with `reason: "model-unavailable"`. A failing startup probe pauses the worker with `reason: "model-fault"`.
- The existing `CASSINI_<STEP>_DISABLED` variables only seed a step's default (`llm_settings.go:216`). This step's default is already off, so it adds no such variable. It adds only `CASSINI_TOPICS_THREADS`, declared in `info.xml`.
- The worker reads settings at each claim. No recording job is involved, so there is no admission snapshot. Switching off stops new claims; a running embed finishes.
- `topic_meta.enabled_at` records when the feature was first switched on. Coverage uses it for "meetings before … not checked yet".

## Privacy

New rows for `docs/privacy.md:17-26`:

| Step | Data | Leaves the server? | Stored | Who sees it | Deleted |
|---|---|---|---|---|---|
| Topic suggestions | Transcript words | No; local CPU model | Passage vectors and time ranges (no text) in `topics.sqlite3` | Nobody directly | With the meeting, on a sidecar rebuild, or on uninstall |
| Review decisions | Who confirmed or rejected which topic on which meeting | No | `annotations.sqlite3` | People who can read that meeting | With the meeting |
| Topic library | Names and definitions people write | No | `annotations.sqlite3` | Everyone who can read at least one meeting; edit history with user ids for admins only | A past revision can be redacted; whole retired topics: open question |
| Topic proposals | Clusters of passages within one room | No | Time ranges and a centroid in `annotations.sqlite3` | Only people who can read every source meeting | Hidden at once and recomputed when a source meeting is retired |

Suggestions never reach the `.opus`, copies, search, the static viewer, or agent and CLI reads in v1.

## Evaluation plan

### Gate 1: int8 re-validation (offline, before any user surface)

- Run the Go chunker on the private 60-meeting corpus and compare passages with the Python ones. They must match exactly (word ranges, boundaries, counts).
- Embed with the Go int8 runtime. Report the per-passage cosine to fp32 and the per-passage score delta distribution.
- Run the Go scorer at 0.83/0.04, 0.85/0.05 and 0.87/0.06. Compare (meeting, topic) decisions with the 163 fp32 assignments at 0.85/0.05.
- **Accept** at ≥ 97% decision agreement at the chosen gate, with every flip inspected and written down. Otherwise choose `T` and `M` again on int8 and record why.
- The corpus and per-meeting outputs stay private. The repository gets the harness (`cassini topics embed` over a local folder, then `cassini-operator topics-eval --vectors <dir> --definitions <file> --reference <file>`) and aggregate numbers in `docs/proposals/automatic-tagging/evaluation.md`.

### Gate 2: labelled review set (before the feature leaves preview)

- About 300 (meeting, topic) pairs, stratified by score band above and below the gate, including near misses.
- At least 2 rooms and at least 1 room in a language other than English.
- Two people label each pair against the transcript.
- Report precision at the gate and estimated recall from the below-gate bands, per detected language (the language the CLI records, not one from the build, which records none). Target precision about 0.8.
- The languages that pass go into the validated-language list of the `ScorerVersion`. Others are shown as "not yet validated".

### In production

- Verdicts are labels. Each stores the definition sha, the scorer key and the scores.
- Per-topic confirm and reject counts are not served on any route (see "Why no suggestion or verdict counts on ADMIN routes"). `cassini-operator topics-export` writes verdict rows with scores and no text, for offline analysis by someone with shell access on the server.
- Recall needs an audit: periodically sample meetings with no suggestion and ask people which topics they see.
- Per-topic thresholds: once a topic has ≥ 20 review verdicts with ≥ 5 rejections on its current definition, `T` and `M` are fitted with leave-one-meeting-out validation (`topics_gates.go`; evaluation.md, "Per-topic gates").

## Tests

- **Recorder.** Tokenizer ids against HF `tokenizers` output on **public** multilingual text and synthetic edge cases (whitespace runs, emoji, combining marks, numbers); no meeting text is committed. Chunker cases ported from `test_refine_daily.py:6-18`, plus properties: every word exactly once, deterministic, token cap respected. Bundle-rebuilt payload sha equals the packed `payloadRef.sha256`. `--known-key` exits before taking the lock. Padded equals single batch. Non-finite output exits with the model-fault code. Golden vectors behind the model-cache environment variables. `Validate` accepts the embedding kind and still rejects bad URLs. `models list` reports the embedding model Ready from installed files, the runtime check and its probe receipt, still Ready after `boot_id` changes, and not Ready after a failing probe. `models install|probe` for the embedding kind calls the embedder probe, never the speech probe, and refuses a non-CPU device. Language detection on public multilingual text, and `unknown` below the confidence floor.
- **Operator.** Migration 7 → 8 on a copy of a real-shaped database. A table test of every state transition. Same key means no CLI call (a fake `cassini` counts calls). A rejection survives a rerun, a model bump and a definition edit. Confirm and verdict commit together; a crash between them is impossible by construction. A retried confirm, after the first succeeded, returns the first receipt (not 409, not "not pending"), also when the library colour changed in between. Confirm on a file with no annotations writes the deployment namespace. Confirm on a same-label local tag marks that tag and records the alias. Confirm on unresolved marks answers 409 `marks-unresolved`. Undo and Reopen bring the suggestion back as `pending` at once. Preemption, memory deferral and a timeout before the CLI holds the lock use no attempts. Crash recovery requeues `running` rows. The publish hook records the `opus_name` from the attempt catalog before the site is removed; a promotion failure leaves a `queued` row that the worker reads from the archive. A `done` row whose published attempt is behind `artifact_availability` is requeued. A model or recipe change requeues every `done` row, and the scorer skips meetings embedded under the old revision. A publish during a running embed sets `requeue` and the meeting is embedded again. A rescorer write whose `embed_key` no longer matches is dropped. Retire scrubs every table, `topic_receipt` and the sidecar. Retirement racing a running embed leaves no rows in either store. Sidecar rebuild requeues `done` meetings, and the definitions and controls still have vectors (none are lost; a missing one is re-embedded before scoring). After an operator restart with a changed `boot_id`, the feature still runs. A manual TagPicker add of a library label keeps the library id. Merge copies rejections and withdraws pending rows. `/settings/topics` routes are not answered by the LLM settings handler. ADMIN status contains no suggestion or verdict counts.
- **Permissions.** No suggestion, evidence quote or count from a hidden meeting appears in any response body (copy the search test pattern). A proposal with one hidden member is invisible and not actionable. Retiring a hidden member and killing the recompute leaves the proposal invisible to everyone; a proposal with no members is never served. The `routine` flag does not change with meetings the caller cannot read. "Create topic" with an unedited draft-name term needs the explicit confirmation. Creating a topic with a retired topic's label offers Restore and never violates the primary key. A redacted revision serves no label or definition. Library routes answer 404 for an empty visible set and 502 when resolution fails. Library responses contain no user ids.
- **Viewer and app.** Browser tests for the suggested chip, the popover (confirm, reject, undo, seek), `NeedsReview` with batch confirm, `TopicEditor`, `TopicInbox`, the coverage text, and the Settings row. A static-provider test proves every topic surface is absent.
- **Harness.** One end-to-end run with a deterministic fake embedder behind a build tag: record, publish, suggestion appears, confirm, the mark is in the delivered file credited to the person.

## Open questions

1. **Which database.** The design puts the queue and verdicts in `annotations.sqlite3` so a confirm is one transaction. Does that match "the operator's database"? If that database is lost, the archive seeds a replacement and the library and rejections are gone. Backups must include it.
2. **Model licence.** Resolved 2026-10-14: the linked licence page is the plain Apache License 2.0. Google's prohibited-use policy is a separate page; the docs should link it as guidance.
3. **Languages.** The control definitions are English prose, and the chunker counts whitespace words. Production records no transcript language, so this design detects it in the CLI; S2 chose `whatlanggo` with a per-passage vote and a 0.6 floor (see `language.go`). Should meetings in unvalidated or unknown languages get suggestions with a warning (this design), or only appear in coverage? Should the build record the language too? Should the margin use the other library topics instead of fixed controls?
4. **Merge.** This design copies A's rejections to B on a merge. Confirm, or keep them as A's history only?
5. **Private 1:1 calls.** The room type is not stored per meeting (`talk_backend.go:132-136`). Should proposals leave them out? That needs a stored room-type field.
6. **Proposal window.** A rolling 90 days per room, or the whole history?
7. **Backfill start.** Admin-started (this design) or automatic on enable?
8. **Library authorship.** User ids are hidden on USER routes and the audit is admin-only. Should people see who edited a topic?
9. **Duplicate ids.** The library never adopts an id that exists only on hidden meetings, so one label can have two ids. Is a later service-account tool to unify them needed?
10. **New-topic flood.** A new topic can create pending suggestions on hundreds of past meetings. Is the grouped review queue enough, or should a new topic only score recent meetings by default?
11. **Agent access.** Should agents (D-813) read or decide suggestions in a later version?
12. **Platforms.** The CUDA image's ONNX Runtime version and arm64 are unchecked. v1 is amd64 CPU only.
13. **Lock starvation.** Can a long build hold `.inference.lock` long enough to starve the queue? Not measured. Lock wait uses no attempts and no timeout budget, and it is exposed in status.
14. **Vector size.** float32 (about 280 MiB per 1,000 meetings) or float16? The thresholds are sensitive, so float16 needs its own parity check.
15. **Preview label.** Should the feature ship labelled "Preview" (as diarization shipped as "Proof of concept") until Gate 2 passes? *Not decided; for the maintainer. Until then the feature ships without a Preview label, and every suggestion says its language is not yet validated (Gate 2 is pending, see evaluation.md).*
16. **Retired topics.** Should retired library entries ever be purged as a whole? (Single past revisions can already be redacted.)
