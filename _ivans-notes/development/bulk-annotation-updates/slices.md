# Implementation plan

Updated 2026-09-18 before implementation. This records the accepted implementation approach; no shaping workflow is being started.

1. D-773: reproduce and fix explicit tag identity being lost to a case-insensitive local label fallback. Align the operator vocabulary and visible label lookup with ID-based writes across legacy namespaces. Preserve existing document namespaces; do not migrate recording bytes or redo the viewer guard. Test multiple distinct tags, case variants, cross-namespace counts, visibility and archive readback. Commit separately as `fix(annotations): preserve selected tag identity across meetings [D-773]`.
2. Atomic bulk API: extract reusable transaction-level mutation logic; add a bounded bulk route with a required batch request ID, validated meeting IDs, one visible-catalog resolution and bounded per-file access checks outside the transaction. Persist all document/projection changes and batch replay data atomically. Resolve new tag identity once for the batch. Preserve pending-snapshot retention and schema migrations. Wake both workers after commit. Test rollback, replay/collision/restart, denied or unprepared targets, shared tag identity and individual archive processing. Commit after validation.
3. List UI: expose the bulk provider capability, replace sequential selection POSTs with one request, and publish all affected rows and new vocabulary entries together. Preserve the batch request ID for safe retry after an ambiguous failure. Show pending/failure/retry feedback for the whole selection. Test one request, one state publication, fresh labels, retries and failure without partial rows; build the app. Commit after validation.
4. Finish implementation/tutorial/followup documents, run the relevant regression suites and build checks, review the diff, commit documentation, and push `feat/tag-queue`.

The API commit makes all selected desired documents visible together; archive completion remains per recording.

```text
Selection --> bulk POST --> access checks --> SQLite transaction --> response
                                                   |                  |
                                                   |                  v
                                                   |            one UI update
                                                   v
                                            pending A, B, C
                                             /          \
                                         worker 1     worker 2
                                             A            B
                                             C
```

Acceptance: no selected meeting changes if any target is rejected; retrying a committed batch does not repeat mutations; every selected row appears in one UI publication; existing tags remain when another tag is added; duplicate tag IDs across document namespaces form one vocabulary entry; workers continue to verify each recording independently.

Bounds and exclusions: keep batch sizes bounded and network calls outside the SQLite transaction. Keep tag styles in their existing separate store. No distributed operators, continuous multi-user subscriptions, or unrelated review followups.
