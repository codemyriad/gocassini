# Portable tag styles using existing rename semantics

Status: planning complete, implementation not started. Read `handoff.md` first in a fresh session. This is the behavioral overview; `shaping.md` resolves the exact contracts, `breadboarding.md` maps the flows, `slices.md` orders implementation, and `validation.md` defines the gates.

## Decision

Apply the existing tag-name model to colour and icon. Each recording's annotation document owns its tag definitions. SQLite stores durable desired documents and query projections; vocabulary is aggregated from accessible recordings, not an authoritative global dictionary. Extend the existing rename job and archive flow to styles.

This supersedes central authority, privileged global propagation and reconciliation on access. Keep ID-based offline restyle and the archive-saved parity guarantee. No implementation has started and Linear has not been edited.

## Representation and rendering

Add optional `color` and `icon` to `annotations.tags[]` in the shared Go model and browser reader. Items reference `tagId`; no styles repeat per mark. Use existing document revisions and desired/confirmed snapshots, with no per-tag version protocol.

```text
Recording annotation document
  tags: ID -> label, colour, icon
              ^
              |
  items: mark -> tagId + target
```

Render the recording's own desired definition in the app and embedded definition in the public viewer. Aggregate vocabulary must not override explicit local styles, including meeting-list badges. Unknown colours use the existing palette fallback; unknown icons draw nothing. Preserve unknown tokens during unrelated rewrites. Explicit empty icon means no icon, not permission to recover an old icon.

Project styles into per-recording `annotation_tag` rows. Keep existing name aggregation unchanged. For pickers/managers, derive a representative colour/icon pair by visible-meeting frequency with a stable tie-break, following the existing name aggregation approach. Select the pair together. It is picker metadata, not an override of individual recordings.

## Writes and propagation

```text
Rename / restyle by tag ID
             |
             v
Validation + caller-visible carrier lookup in SQLite
             |
             v
Persist job, targets and requested changes
             |
             v
Per-recording transaction:
  definition -> snapshot + document revision + projection
             |
             v
Existing workers -> .opus rewrite -> upload -> verify
```

- Style-only edits use rename-style persisted jobs, progress/failures, restart handling and busy responses. Updates are atomic per recording, not across all targets.
- Combined label/colour/icon edits produce one document update per target. Persist partial-field semantics: omitted fields unchanged, empty icon clears it.
- Target only caller-accessible recordings. Other recordings retain their own definitions; opening them does not enqueue repair. Keep the existing service-account archive writer for already accepted work.
- Find carriers in SQLite, not by scanning files. Add `(tag_id, opus_name)` for efficient lookup.
- New marks include styles in their original snapshot. Creation `tagStyles` does not restyle existing tags. Existing local definitions stay intact; when a selected ID first enters another recording, copy its resolved visible vocabulary definition as the initial value, analogously to the label. Resolve once for bulk requests, preserving shared IDs and atomic commits.
- Offline apply accepts creation styles and an ID-based `restyle` operation. Style-only changes count as mutations; unchanged values do not create revisions.
- Rename preserves local styles. Merge retains an existing destination's style; when creating that destination locally, carry the resolved destination definition. Delete uses existing cleanup behavior. Do not broaden authorization scope.
- Reuse existing per-recording mutation ordering for overlapping jobs, rather than introducing global version ordering.

## Compatibility with existing styles

Do not discard `tag-styles.json` data or introduce an archive-wide migration rewrite.

Implementation decision: stop new writes to that file and preserve it as read-only legacy input. Only definitions with both appearance fields absent can use it as a transitional fallback. On the next actual authorized mutation, copy applicable legacy styles into the recording's desired document in the same transaction and archive write. Pure reads and ordinary no-op edits do not cause migration. Use presence-preserving fields so old icons cannot return after a clear. Never delete the legacy file automatically. See `shaping.md` for partial edits and response metadata.

```text
Legacy style JSON (read-only compatibility input)
                   |
                   v
Next authorized recording mutation -> self-contained definition
                                             |
                                             v
                                    normal archive flow
```

This is a compatibility bridge, not a live global authority. Untouched legacy files retain their existing portability limitation. They may still need legacy fallback in the app until migrated; retiring that fallback is a follow-up, not a hidden archive sweep.

Replace `settleTagStyles` writes, including rename attribution. Preserve useful existing attribution as legacy data, and persist future attribution with document/projection updates where the API needs it; do not retain a writable global style dictionary for attribution alone.

## Completion and performance

Job completion means target DB mutations were attempted; inspect failures separately. Download parity begins after that recording reports archive saved. Keep existing pending/delayed/blocked states, retries and exact-snapshot verification.

Ordinary reads add no media access, version reconciliation or repair jobs. Reuse bounded archive workers and coalescing. Marks and combined edits need no extra rewrite just for styles.

## Acceptance

- Single/bulk app marks capture styles durably; downloaded/public appearance matches after saved.
- Offline creation/restyle works; old files open; absent fields and unknown tokens survive appropriately.
- Restyle and combined rename/style affect only caller-visible carriers and preserve unrelated marks.
- Retry reaches failed/stale targets even when others already match; jobs survive restart.
- Distinct local styles for one ID are displayed faithfully; vocabulary aggregation does not mask them.
- Cleared icons stay cleared, including with legacy input and subsequent rename/merge.
- Upgrade preserves old styles and pending snapshots without an archive sweep; an authorized mutation materializes legacy styles once.
- Batch rollback/replay and D-773 identity remain intact; archive annotations equal the committed snapshot.

## Excluded

Central authoritative names/styles; live global style overrides; privileged global fan-out; per-tag revisions; reconciliation on reads; global search/name redesign; archive-wide backfill; format-spec changes; new merge/delete lifecycle protocols; in-place Opus patching.
