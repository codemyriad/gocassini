# Implementation slices

Status: planned, no slice started. Execute only when instructed in the implementation session. Read `handoff.md` first. Commit the agreed plan, then create `progress.md` with all five slices marked ⬜; mark the active slice 🔄 and committed slices ✅. For each slice: implement, validate/fix, update progress and commit before proceeding. Use the branch's conventional commit style. All paths below are repository-relative. Full behavioral contracts live in `shaping.md`; validation IDs/commands are in `validation.md`.

## S1 — Portable model and offline authoring

Outcome: a standalone recording can receive and retain appearance, with no operator dependency.

- Change `cassini-annotations/{document,ops,mutate}.go`; add shared palette/batch helpers and focused model tests where appropriate. Use nullable fields, strict authoring and tolerant completed-document checks as specified.
- Change recorder `internal/cassini/{annotate,annotate_ops,annotate_snapshot,meetings_annotations}.go` and portable aliases/tests. Forward the full envelope through remote CLI requests; do not silently lose creation hints. Update text and JSON show behavior.
- Implement ID-based restyle, creation-only hints, trusted initial definitions and no-op semantics. Rename keeps appearance; absent merge destinations can receive initialization without changing existing destinations.
- Keep operations-only callers source-compatible until S2 moves them to full batch mutation. A wrapper must not silently ignore hints.

Gate: V1–V4, V14 and shared/recorder commands. Include an actual `.opus` round-trip, not only struct serialization. Proposed commit: `feat(annotations): carry portable tag styles and support offline restyling [D-793]`.

## S2 — Per-recording storage and query primitives

Outcome: the database can store/query portable appearance safely, with the migration and preparation helpers needed for the coordinated write cutover in S3.

- Change operator `annotations_documents.go` and `annotations_store.go`: guarded schema ladder, nullable projected styles/audit, carrier index, DB-only projection population from desired documents and visibility-scoped style aggregation.
- Add/test helpers for trusted initial appearance and conditional legacy materialization that S3 will wire into single/bulk mutations. Do not partially retire the old write model here: creation, tag edits and style-store cutover land together in S3.
- Prepare read-only legacy access and response types for separate `legacyTagStyles` metadata and local meeting-ref `appearance` objects. Test raw-vs-effective query helpers directly; switch production read assembly to these helpers in S3. Do not put display fallback into immutable documents or receipts.
- Preserve local audit across projection replacement and provide transaction helpers to stamp real definition changes. Production stamping and removal of post-commit style writes are part of S3's cutover.
- Expand document migration, store, batch and identity tests. No new API route is needed; verify the existing USER-gated manifest routes still cover the payload changes.

Gate: V5–V6, query/helper portions of V7/V9/V12/V15 plus existing operator regressions. Assert schema-5 upgrade does not recreate batch tables, mutate confirmed baselines, change generation or enqueue writes. Proposed commit: `feat(annotations): project per-recording tag appearance [D-793]`.

## S3 — Cut over ordinary writes and scoped style jobs

Outcome: creation, single/bulk marks and tag edits all own portable styles through snapshots; name and style edits share the existing caller-scoped job/archive lifecycle. Keep this one cohesive cutover so an intermediate commit cannot create local styles that the old global restyle route then fails to change.

- Change `annotations_meetings_request.go`, `annotations_mutations.go`, `annotations_batch.go` and `annotations_meetings.go`: prepare identity/styles once, then invoke shared batch mutation. Include eligible legacy fields before final revision assignment; preserve atomic snapshots/projections/receipts for selections. Hash original requests and replay their exact committed responses.
- Activate S2's recording-local read assembly and cached read-only legacy input, including `legacyTagStyles` and local meeting-ref appearance. Stamp actual definition edits in the same transaction.

- Change `annotations_tag_changes.go` and `annotations_tag_jobs_store.go`: per-target stale-field checks, `restyle` kind, combined operation envelope, old bare-op restore compatibility and stable receipts across restart.
- Validate requested fields before scheduling; no-op needs no job. Reuse one busy guard, one persisted target list and one mutation per target. Keep scope, label conflicts, merge/delete and accepted-work behavior.
- Remove all production writes to `tag-styles.json`, including `settleTagStyles`; preserve it only as compatibility input. Adjust existing tests that currently assert style-file mutations.
- Keep `annotations_workers.go`'s architecture unchanged. Add regression coverage proving the exact style-bearing snapshot is written and verified with no live style lookup.
- Document interim commits as not deployment-ready until the new viewer in S4 is included; do not run old/new operators concurrently as a compatibility strategy.

Gate: V2–V3, V5–V12, V14–V16 at backend level and focused race tests. Test saved old jobs as well as new ones, partial rename+style rollback, retry of a stale minority and an inaccessible carrier unchanged in both DB and archive. Proposed commit: `feat(annotations): commit portable styles through annotation writes and scoped jobs [D-793]`.

## S4 — Recording-local appearance throughout the viewer

Outcome: meeting/list/public surfaces faithfully render each recording's appearance and explain scoped asynchronous edits.

- Change viewer `viewer/{portable,annotations,tagPalette,bulkTags,tagManager}.ts`, `components/marking/session.ts` and relevant components/tests. Preserve raw string values and narrow safely at rendering boundaries.
- Update `tagsByMeeting` and `withAnnotationBatch` to preserve local appearance refs; use one appearance resolver/dealer across list and meeting rendering. Keep old-server absent-ref fallback distinct from an explicit empty local appearance object.
- Thread separate legacy metadata through providers, including `cassini-app/src/appDataProvider.ts` if mapping is explicit. Static/public providers rely solely on embedded fields.
- Update `TagEditor.svelte`, `TagManager.svelte` and `App.svelte` refresh wiring: style scope confirmation, new job kind, safe draft handling for unknown styles, and refresh of the open meeting when a manager job updates it.
- Keep the selection bar's one-response publication and public viewer's read-only mode. Do not restore aggregate precedence or add access-time jobs.

Gate: V7–V9, V12–V14 and complete viewer/app suites and builds. Proposed commit: `feat(viewer): render recording-local tag appearance [D-793]`.

## S5 — End-to-end verification and handoff

Outcome: verified upgrade, portable parity and a usable handoff to the user.

- Complete `validation.md` in a local harness using synthetic/disposable fixtures, retaining existing volumes. Exercise new tag, bulk tag, style-only and combined edits, inaccessible carriers, legacy migration, restart/retry and public playback of a downloaded saved artifact.
- Run full relevant suites/builds once after all slices; broaden only for failures/new changes. Record commands, pass/fail and known unrelated warnings without claiming unrun checks passed.
- Record normal-read network counts, combined-edit snapshot/rewrite counts and carrier query plan. No new benchmark framework or performance target is required; demonstrate no new media scans or duplicate style pass.
- Update recorder/operator documentation and obsolete comments (including in-memory job/style-only assumptions). Add `changelog.d/d-793.portable-tag-styles.md` using allowed headings; do not edit `CHANGELOG.md`, release numbers or generated microsite assets unless explicitly required by an existing build check.
- Create `implementation.md`, `tutorial.md` and `followups.md` only now, with actual outcomes, setup, commands, saved-state caveat and legacy retention limits. Keep private recordings/credentials out of commits.

Gate: all V1–V16 scenarios covered, no unresolved feature failures, API paths still match `appinfo/info.xml`, and planning progress lists actual commits. Proposed commit: `docs(annotations): document and verify portable tag styles [D-793]` (include feature regression tests/fixes with accurate commit type if necessary).

## Dependency order

```text
S1 shared model/CLI -> S2 storage/query primitives -> S3 writes/jobs cutover
                                                        |
                                                        v
                                               S4 viewer surfaces
                                                        |
                                                        v
                                               S5 end-to-end/docs
```

Do not stop after a successful intermediate slice once execution is authorized; continue through S5 unless a blocker needs user input. Do not infer authorization to deploy, push or modify Linear from this plan.
