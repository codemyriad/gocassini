# Initial fit assessment

Historical assessment. The user's latest decision supersedes the central authority, global propagation and reconciliation proposals below. See `plan.md` and `slices.md`. Q1 (parity after archive saved) and Q2 (ID-based offline restyle) remain accepted; Q3 is superseded by matching existing rename scope.

Inspected 2026-09-18 on `feat/tag-queue`, HEAD `0ac233f2`, with a clean tracked worktree. Read D-793 (no comments), D-774, D-775, branch changes, bulk implementation notes, and the relevant mutation, job, archive, CLI and viewer code. This is static inspection, not runtime validation.

## Requirement

Store optional `color` and `icon` on each annotation tag, so app-authored and offline-authored recordings retain their appearance in the public viewer. Rendering precedence is installation style, then embedded style, then the existing dealt colour fallback. Old recordings and unknown style tokens remain readable. Preserve unknown tokens through unrelated rewrites; validate newly supplied styles against the application palette. No format-spec changes or archive-wide backfill.

## Fit with the branch

The branch already separates a durable application commit from archive synchronization. Extend the complete snapshots with styles; do not resolve live styles inside archive workers, because workers verify the file against the exact persisted snapshot.

```text
mark / bulk mark / tag edit
           |
           v
resolve tag identity + intended style
           |
           v
SQLite snapshot + projection + receipt ----> immediate app state
           |
           v
existing archive workers
           |
           v
annotate snapshot -> conditional upload -> readback verification
           |
           v
recording with styles -> public viewer
```

Ticket references need adaptation:

- The canonical Go tag model now lives in `cassini-annotations/document.go`; recorder portable types are aliases. Change the shared model so operator and CLI agree.
- `commitAndRecord` delegates to `commitDocument`; bulk writes call `mutateAnnotationDocument` directly inside one transaction. Put style materialization in the shared mutation path and preserve batch atomicity and request replay.
- New styles currently save through `styleNewTags` to `tag-styles.json` after the document transaction. Capturing styles only there is insufficient: they must enter the durable snapshot before its commit. Installation-style persistence and job acceptance also need a crash-consistent design; the precise storage choice belongs in the plan.
- Capture installation styles when applying existing tags, not just request styles for brand-new tags. Rename should carry the applicable style; merge should use the destination's style. Preserve D-773's ID semantics.
- Restyles should enqueue durable per-recording document changes through the existing job infrastructure; archive workers perform the actual rewrites. Persist the intended style payload for restart/retry. Combined rename and style changes should produce one document update per target. Enforce `busy` before changing installation style.
- Jobs are persisted/restored on this branch, despite a stale in-memory-only comment. A job finishes when document mutations finish; its failed list does not represent subsequent archive upload failures. Archive status is separate.
- The browser parser currently discards style fields, and `viewMarks` only uses vocabulary/new-tag colours or dealt defaults. Both need changes, including missing versus explicitly cleared icons and unrecognised tokens.
- Offline `ParseOps` currently rejects a top-level `tagStyles`; the CLI also exits early for unchanged ops. Style-only changes must be considered deliberately, and snapshot validation must preserve unknown existing tokens.
- This branch removes rewrites from request latency and can coalesce pending snapshots. It still stages, rewrites, uploads and verifies full recordings; it does not implement in-place Opus metadata patches.

## Proposed verification boundary

Cover shared-model round trips and unknown-token preservation; offline creation and style edits; single/bulk snapshot styles and request replay; restartable restyles, combined rename, merge and icon clearing; old/unknown-style browser fallbacks; and app edit -> archive saved -> downloaded file -> public viewer. Confirm no extra rewrite is introduced solely to add styles to a mark snapshot.

## Questions and decisions

Q1:
- **Question:** Does the download/public-viewer parity guarantee begin once archive sync reports saved?
- **Suggestion:** Yes. Keep immediate application commits and asynchronous archive sync, and make the distinction clear in job feedback and the tutorial.
- **Rationale:** This preserves the branch's flow; a completed tag job can precede the recording upload.
- **Alternatives:** Gate app downloads until sync finishes; add an explicit export that waits for the current snapshot.
- **Response:** Confirmed explicitly. Parity is guaranteed once archive sync reports saved.

Q2:
- **Question:** Should offline `tagStyles` also restyle existing tags without adding or removing marks?
- **Suggestion:** Yes: support `ops: []` with styles for existing labels, rejecting ambiguous label matches.
- **Rationale:** This lets a downloaded recording acquire or change appearance without an operator; copying the app's new-tags-only rule would prevent that.
- **Alternatives:** Restrict it to newly created tags; introduce a separate ID-based restyle operation for existing tags.
- **Response:** User chose the ID-based alternative after discussion. Keep `tagStyles` for creation and add an explicit operation targeting an existing tag ID for offline restyles; it can also serve operator jobs. Exact operation schema remains to be planned.

## Central lookup and portable copies: proposed direction

Style belongs to a tag identity, not to each mark. Each recording already has a local tag dictionary (`annotations.tags`) and marks (`annotations.items`) that reference it through `tagId`. Add colour/icon to the local dictionary entry once per tag; do not duplicate them on every item.

```text
Installation tag styles (one row per tag ID; proposed SQLite storage)
        |                                    |
        v                                    v
App vocabulary override             Durable restyle job
                                             |
                                             v
                              Per-recording annotation snapshot
                                             |
                                             v
                              Archive worker -> .opus manifest
                                             |
                                             v
                              Local tags[] dictionary -> items[]
```

Recommendation, not yet an approved implementation plan: move the installation style lookup from `tag-styles.json` into the existing durable SQLite database, with migration preserving current values. Persist an installation style change and its fan-out intent together. Jobs materialize intended styles into complete per-recording snapshots; existing workers archive those exact snapshots. New marks capture the applicable style in their original snapshot.

The app treats an explicit installation style as authoritative; a standalone reader uses the file's local dictionary. Files therefore contain portable snapshots of centrally managed styles. Temporary disagreement while synchronization is pending is intentional and covered by the accepted saved-state guarantee. Already downloaded copies remain independent snapshots.

An imported file must not silently overwrite an existing installation style. Without an installation override, its embedded style remains usable as a fallback. Reconcile any policy for adopting imported styles centrally during planning.

Existing tag jobs touch only the caller's readable recordings, while installation styles apply globally. The user subsequently selected privileged installation-wide style propagation (Q3 below). Concurrent edits must not let an older fan-out job overwrite newer style intent; settle ordering/versioning in the plan.

The user accepted the central lookup plus portable per-recording dictionary architecture. No implementation has been authorized yet. Detailed implementation slices and validation criteria remain to be written.

Q3:
- **Question:** Should restyle propagation retain the existing boundary of updating only recordings the caller can read?
- **Suggestion:** Yes for D-793. Central appearance updates immediately; archive parity is guaranteed for the job's successfully synchronized targets. Other recordings retain their embedded styles until an authorized update reaches them.
- **Rationale:** This extends the branch's existing flow without introducing authority to modify inaccessible recordings. The app may display the new central style over an older embedded style in an untargeted recording.
- **Alternatives:** Permit installation-wide propagation through a privileged role; make central styles and propagation explicitly scoped to an ownership or sharing domain (a broader redesign).
- **Response:** User selected privileged installation-wide propagation, probably through the Cassini service account. This decision concerns style propagation; do not silently broaden rename/merge/delete scope.

## Target lookup and privileged propagation

Subsequent sidebar: the user proposed replacing privileged global target selection with central definitions plus reconciliation on authorized access. See `lazy-tag-reconciliation.md` for the current exploration; do not treat the earlier global propagation choice as the final plan while this alternative is under discussion.

Verified in code: `annotation_tag` already stores `(opus_name, tag_id, label, label_folded)` with primary key `(opus_name, tag_id)`. `tagCarriers` selects carriers from this table, currently joining the caller's visible meeting set. Files are not scanned to discover carriers on each edit. The schema lacks a tag-first index; add `(tag_id, opus_name)` for efficient global lookup.

The archive worker already performs DAV reads and writes as `ncRecordingsOwner`, whose value is `cassini`. Installation-wide restyles therefore primarily require global target selection and durable scheduling, rather than a new file access identity. Retain user attribution and existing authorization to edit the tag; privileged execution does not imply exposing inaccessible meeting identities through job status.

```text
Authorized style edit -> central style + durable propagation intent
                                           |
                                           v
                         DB lookup: tag_id -> affected opus_names
                                           |
                                           v
                         Update affected annotation snapshots
                                           |
                                           v
                         Existing archive workers as cassini
```

Discovery uses the DB; physical archive work still reads/rewrites each affected file, with two existing workers. Initial import/rebuild must populate the mapping. New marks use the current central style, and late imports need reconciliation so a recording missed by an earlier target enumeration eventually receives the installation override. Incomplete index coverage must not be presented as full installation convergence.
