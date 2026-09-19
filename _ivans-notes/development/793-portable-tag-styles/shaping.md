# Implementation design

Status: execution-ready design, not implemented. Baseline: `feat/tag-queue` at `0ac233f2`, annotations schema 5. The user-authorized simplified scope in `plan.md` governs; this document resolves its implementation details. Routine helper names may change, behavior may not.

## 1. Shared representation and style operations

Use `Color *string` and `Icon *string`, each with `omitempty`, on `cassini-annotations.AnnotationTag`; recorder portable types remain aliases. Nil means absent. A non-nil empty icon serializes as `"icon":""` and survives clone, mutation, projection and snapshot writing. No additional format version, per-tag revision or provenance protocol is introduced.

| Input/state | Behavior |
|-------------|----------|
| Both fields absent | Legacy/unadorned definition; eligible for legacy fallback if one exists. |
| Either field present | Local appearance; never consult legacy/global style data for either field. |
| `icon: ""` | Explicit no icon; render none and preserve the empty member. |
| Missing colour on a local definition | Palette fallback, not legacy colour. |
| Unknown string colour/icon in a file | Preserve in storage/rewrites; render palette fallback/no icon. |
| New colour supplied by a write | Require one of the existing 12 palette names; empty is invalid. |
| New icon supplied by a write | Require an existing icon ID or empty for clearing. |
| Null/non-string style in an operation | Reject; absence is omission, not null. |

Document validation must allow unknown string tokens already present in a document. Palette checks apply at authoring inputs, not indiscriminately to the completed snapshot; otherwise an unrelated edit or archive snapshot command would reject imported unknown styles. The tolerant browser reader ignores non-string appearance members without losing the tag/marks. No generic arbitrary-JSON preservation project is included.

Offline wire examples:

```json
{"ops":[{"op":"mark","tag":{"label":"Decision"},"target":{"kind":"meeting"}}],"tagStyles":[{"label":"Decision","color":"blue","icon":"check"}]}
```

```json
{"ops":[{"op":"restyle","tagId":"tag_decision","color":"teal","icon":""}]}
```

`restyle` requires tag ID and at least one appearance field. Omitted fields remain unchanged. Unknown tag IDs join `notFound`, consistent with relabel. All operations in one request validate before persistence. Restyle is valid for unresolved audio because it does not change target ranges or bindings. An identical final definition is a no-op; do not increment the revision, attribute an edit, or rewrite media.

Keep creation hints separate from restyle. A hint names a new tag by folded label, requires valid colour, and defaults omitted icon to explicit empty. Conflicting duplicate hint labels are invalid; identical duplicates can be deduplicated. A hint for an existing identity cannot restyle it. An unused hint makes no change. When an offline batch creates multiple distinct IDs with the same folded label, require ID-based restyle instead of guessing which definition receives the hint.

Introduce a shared parsed batch carrying both operations and creation hints (e.g. `ParseBatch`, `ApplyBatch`, `MutateBatch`). Retain operations-only wrappers where useful for existing call sites/tests, but never let a wrapper silently discard a nonempty `tagStyles` envelope. Validate supplied styles once through shared palette helpers; remove the operator's duplicate palette authority or delegate to those helpers.

The operator supplies trusted initialization definitions keyed by ID to the pure batch mutator, separate from client creation hints. Use these only when an ID is introduced locally; never overwrite a definition already in that document. This avoids exposing arbitrary `tag.color` input that could bypass creation-only semantics. The same helper handles a merge destination absent locally. The CLI uses creation hints plus existing document definitions; new merge destinations without supplied appearance retain the old absent-field behavior.

Apply all style initialization and operations before computing `Outcome.Changed` and before final revision assignment/validation. Deep-copy optional fields and never mutate the current snapshot in place. Offline apply's early no-op path, JSON and human-readable show, `annotate snapshot`, and `meetings annotate` request forwarding all need the full batch semantics.

## 2. SQLite projection and migration

Add nullable `color`, `icon` and local audit columns `changed_by`, `changed_at_utc` to `annotation_tag`, plus `annotation_tag_by_id(tag_id, opus_name)`. Preserve `(opus_name, tag_id)` identity. SQL NULL means absent; empty icon is a real value. No global tag-definition table.

Use schema 6 if 5 is still current; otherwise allocate the next version after rechecking the branch. Fix the migration ladder so the existing batch-table creation is guarded by `version < 5` before appending `version < 6`. Current code falls through to unconditional batch-table creation for an upgrade; simply bumping the version would attempt to recreate those tables in a version-5 database.

Migration requirements:

1. Preserve generation, heads, immutable snapshot JSON, receipts, retention targets, jobs and retry state.
2. Add projection columns/index transactionally. Existing old rows get NULL styles.
3. Populate styles from each available desired snapshot's JSON, not confirmed or live files. This is a database-only projection migration; preserve labels/counts and do not mint snapshots or trigger workers. Version-2 rows without full documents retain existing import behavior.
4. Reopening the upgraded DB is a no-op. Future schema versions still fail closed without unlinking DB/WAL.
5. No downgrade conversion: an older binary may refuse schema 6. The tutorial must describe stopping writers and backing up the entire durable database consistently before upgrade, plus preserving the legacy JSON; do not promise lossless rollback after new writes.

`projectedDocument`, `projectedTag`, `projectAnnotations` and `replaceAnnotationProjection` must carry nullable styles. Do not run palette validation during projection. Rebuild/import must project embedded values and retain the branch's protection of pending desired edits.

For attribution, use the per-recording columns, not portable format additions. `replaceAnnotationProjection` currently deletes and reinserts all rows: preserve the previous audit values when the local definition is unchanged, stamp actual name/style changes and merge destinations in the mutation transaction, and clear unknown attribution when an external import actually changes a definition. New marks alone do not claim a user renamed/restyled the tag. Legacy attribution can fill an otherwise absent display value without writing the JSON. A fresh reindex may lose local-only audit history, as it is not portable; document this limit.

## 3. Legacy compatibility without a live global dictionary

Load `tag-styles.json` once for a running service as immutable legacy input (or equivalent request-independent read-only cache). Missing file means none; unreadable/malformed input is logged and treated as unavailable for display, never deleted or overwritten. Do not perform filesystem reads per tag inside SQL loops. Deploy the completed slices together; do not run old and new operators concurrently against this data.

Only definitions where both appearance fields are absent are eligible. On an actual successful authorized recording mutation, materialize surviving legacy definitions into the resulting document; supplied restyles win over legacy values. A partial restyle first preserves the effective old legacy appearance, then applies requested fields, so icon-only edits do not unexpectedly change the colour. Apply this in the shared mutation preparation, not in archive workers or after commit.

An ordinary no-op mark or rename does not create a migration write just because legacy data exists. An explicit restyle that pins the current legacy appearance does materialize it and is a real document change. Materialize other surviving legacy definitions only when this request is already changing the document. Imported documents and pure reads remain untouched.

Implement that distinction explicitly: prepare legacy appearance for tags directly restyled, apply the requested batch and check it against the original raw document; only if a real mutation exists, materialize remaining eligible surviving definitions before final revision assignment. Do not prefill every legacy tag before the ordinary no-op check, which would turn harmless retries into migration writes.

```text
Raw desired snapshot + immutable legacy fallback + authorized edit
                                |
                                v
                  final local definitions (one revision)
                                |
                                v
                  immutable snapshot -> exact archive write
```

If legacy data is unavailable, leave unrelated absent styles absent rather than inventing or clearing data. Never override explicit file fields, including unknown tokens. Retain the legacy file after migration: untouched recordings still need it for their former in-app appearance. Removal/backfill remains a follow-up.

Do not mix display fallback into stored/confirmed annotation JSON. Add an optional `legacyTagStyles` map to single-meeting read responses, containing only eligible IDs from that authorized meeting. Static/public providers omit it. The map is presentation metadata, not a mutation request and not a new snapshot. Desired/confirmed equality continues to describe the archived raw document; untouched legacy display remains the documented portability exception.

For writes, receipt-backed `annotations` always contains the exact raw committed document. Successful mutations embed applicable legacy values, so they need no synthetic receipt annotation overlay. If a no-op reply still needs fallback, the viewer retains/refreshes its separate legacy map rather than changing receipt contents on replay.

## 4. Vocabulary, list data and new tag writes

Names keep their existing visible-meeting aggregation and matching rules. For styles, vote once per visible meeting for its effective raw `(color, icon)` pair, applying eligible legacy fallback before voting. Pick the highest count, then lexicographic nullable pair (absent before present; empty before nonempty). Keep pair members together; do not combine a colour from one recording with an icon from another. Unknown strings remain data and are sanitized only when rendered.

Aggregate `changedBy/changedAtUtc` from the most recent known actual definition change among visible carriers, with deterministic tie-breaking; do not count hidden meetings or claim this proves every carrier is synchronized. Use the existing API field names. No new global audit record.

`GET /annotations/tags` currently returns ID/count references for meetings, and `tagsByMeeting` paints them through aggregate vocabulary. Add `appearance: {color?: string, icon?: string}` to each meeting-tag reference; emit the object even when empty. Fill it from that meeting's definition/eligible legacy fallback. The new client uses its presence to avoid aggregate overrides. Its absence means an older-server response and may retain the old compatibility behavior. Keep existing label and search behavior in this task; do not introduce canonical names or alter name matching.

Use one shared browser appearance resolver/dealer for meeting and list rendering so fallbacks agree for the same tag set. Count all tags in the meeting, including stretch-only tags, when dealing colours; actual stored fields always beat optimistic `newColors` and aggregate metadata. Explicit no-icon remains none.

The single/bulk mutation preparation resolves the visible vocabulary once per request under the existing mutation serialization, before beginning a transaction. Resolve identity using the D-773 logic; never consult hidden tags for style inheritance. For an existing selected ID missing locally, copy the selected visible representative appearance as initialization only. Client creation hints apply only to identities genuinely new to the visible vocabulary; do not use a global count to expose hidden identities.

Batch preparation must happen before any target projection changes, so every new local occurrence receives the same selected definition. Preserve one transaction for all batch targets and receipt; include styles in result snapshots and derive response metadata from those results rather than reading the DB through a second connection inside the transaction (`SetMaxOpenConns(1)`). Receipt hashing remains based on the original client request, not mutable derived vocabulary. Replays return the original committed snapshots.

## 5. Tag-manager jobs

`POST /annotations/tags/<id>` keeps `label?`, `color?`, `icon?`. Validate before scheduling; no JSON store writes. Test requested fields against each visible carrier, not only the representative vocabulary, so retry can fix a stale minority. Preserve current label conflict rules, 404 behavior and one running job per caller. An entirely current request returns no job.

Use kind `restyle` for style-only jobs and existing `rename` for edits including a real label change. Combined edits contain ordered `[relabel, restyle]` operations in one mutation transaction per target. If the label is already correct everywhere, omit relabel but still run needed style changes. Failure of any operation rolls back that target; other targets continue. A tag removed after target selection remains a not-found/no-op, never re-created merely by restyle.

Persist a complete operation envelope in the existing `annotation_tag_job.op` column: `{"ops":[...]}` plus resolved destination initialization where needed. Restore must also accept old rows containing one bare operation object and normalize to the same old one-element `Ops` request. Do not change old operation IDs/request IDs or invalidate prior per-target receipts. Reject malformed saved envelopes explicitly, never reinterpret them as empty jobs.

Use the existing deterministic per-job/per-recording request ID, per-target commit and progress persistence. No full recording IO while holding the mutation transaction. Keep current accepted-work authorization and archive owner; targets remain the editing caller's accessible set. No access-triggered reconciliation.

Keep same-session retry actions for rename/restyle and existing restart resume. General failed-job recovery after browser reload is a known prior limitation, not a new job-management redesign here. The tutorial must show reapplying requested values through the API if UI draft equality hides a partially failed minority.

Remove `styleNewTags`, writable style update paths and `settleTagStyles` writes once the ordinary mutations/jobs own appearance and audit. Merge retains local destination appearance, or uses the resolved visible destination initialization if absent; delete removes definitions and corresponding audit with existing cleanup. No global merge/delete behavior.

## 6. UI and archive boundary

Portable parsing/types preserve style strings; `TagIcon`/palette only render recognized tokens. Widen data types that incorrectly claim all stored tokens are known, and narrow at rendering/editor boundaries. The editor uses a safe default for an unknown token but must not silently submit its replacement when the user edits only the name: send only explicitly changed fields.

Update tag-manager confirmation text for style-only edits to state the same meeting scope as rename. Poll restyle jobs through the existing tracker; remove assumptions that styles start no job or jobs only live in memory. Refresh vocabulary and the currently open meeting after job progress/completion as needed so a previously saved meeting session does not stay stale. Reuse existing refresh events and poll cadence, not new continuous synchronization.

Archive workers remain unaware of legacy/vocabulary style resolution. `annotate snapshot` writes the exact durable document; verification, conditional upload, lost-response handling, retries and coalescing stay intact.

```text
Job target commit -> UI can read new local definition
       |
       v
archive pending -> upload + verification -> saved
       |
       +--> delayed/blocked -> existing retry path
```

## 7. Source map and pitfalls

| Area | Existing files |
|------|----------------|
| Shared model/mutation | `cassini-annotations/{document,ops,mutate}.go` |
| CLI/portable | `cassini-go-recorder/internal/portable/annotations.go`, `internal/cassini/{annotate,annotate_ops,annotate_snapshot,meetings_annotations}.go` |
| DB/migration | `cassini-operator/internal/operator/{annotations_store,annotations_documents}.go` |
| Single/bulk writes | `annotations_meetings_request.go`, `annotations_mutations.go`, `annotations_meetings.go`, `annotations_batch.go` |
| Jobs/legacy/reads | `annotations_tag_changes.go`, `annotations_tag_jobs_store.go`, `annotations_styles.go`, `annotations_tags.go`, `annotations_reads.go`, `annotations_service.go` |
| Portable/viewer state | `cassini-viewer/src/viewer/{portable,annotations,dataProvider,bulkTags,tagManager,tagPalette}.ts`, `components/marking/session.ts` |
| App/provider/manager | `cassini-app/src/appDataProvider.ts`, viewer `App.svelte`, `components/tags/{TagManager.svelte,manager/TagEditor.svelte}` |

Do not assume label lookup is a global canonical dictionary. Do not apply display fallback to confirmed baselines. Do not erase explicit empty fields with `omitempty` on plain strings. Do not skip style-only changes via the CLI's current early no-op path. Do not change replica authority/versioning, identity namespaces, access policies, or release versions to implement this feature.
