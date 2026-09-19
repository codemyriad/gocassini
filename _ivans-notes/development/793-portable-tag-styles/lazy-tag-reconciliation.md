# Exploration: central tags with reconciliation on authorized access

Superseded: the user dropped this approach in favor of existing rename semantics for styles. Retained as history only; see `plan.md`.

User proposal: store authoritative tag names and styles centrally with per-tag revisions; immediately propagate edits only to the editing user's accessible recordings; serve current central metadata to other users and reconcile their stale recordings when they access them. This is a proposed replacement for Q3's privileged global fan-out, under evaluation rather than an approved implementation plan.

## Fit and flow

`readDocument` already checks access with a DAV HEAD and then loads the desired annotation snapshot from SQLite. No media download is necessary for an indexed recording. `annotation_tag` already maps meetings to tags. Extend that projection with the tag definition version captured in the desired document, and add a central tag table containing ID, canonical label, styles and revision.

```text
Tag edit -> central definition revision + durable visible-target intent
                    |                            |
                    v                            v
          UI and retrieval joins        reconcile authorized targets
                                                 |
Authorized meeting read -> DB stale check --------+
                                                 |
                                                 v
                                new complete desired snapshot
                                                 |
                                                 v
                           existing archive sync -> verified file
```

The central table owns names as well as appearance. Vocabulary, label resolution, tag filters and search-result labels must join it while still restricting membership/counts to visible recordings. Current code derives vocabulary names from the most common per-recording label and filters by per-recording labels, so a frontend-only overlay is insufficient.

## Performance proposal

- Central edit: update one tag definition and persist an intent for the caller-authorized targets; do per-recording work outside the request transaction.
- Read: check only the meeting's tags with one indexed join to central definitions; preserve the existing HEAD access check. Do not fetch files or scan the archive.
- Index membership in both directions: existing `(opus_name, tag_id)` and new `(tag_id, opus_name)`; index central label lookup too.
- If stale, durably upsert one reconciliation task per meeting after authorization. No-op reads should not write. The UI can return a central metadata overlay without waiting for media work.
- Reconciliation re-reads the latest desired document and central definitions under the mutation transaction, updates all stale tag definitions together, advances the document revision once, and leaves marks intact. It must not replace the latest document with an old queued copy.
- Reuse desired/confirmed archive heads, bounded workers and retries. Repeated opens must not create duplicate snapshots or writes. Rapid central changes can coalesce before physical archive work; edits during an upload may require a later pass.
- Batch metadata lookups for lists/retrieval to avoid per-result SQL/DAV requests. Initial implementation can reconcile on meeting open/edit and use the existing immediate authorized fan-out; list-driven catch-up is optional broader coverage.

## Correctness boundaries

1. Convergence is conditional: untouched recordings can remain stale indefinitely. Raw Files downloads/public readers cannot trigger this app's reconciliation. Eventual convergence requires an authorized trigger, successful processing and a quiet interval between edits.
2. There are three versions: central tag definition; tag definitions in desired meeting snapshot; tag definitions in confirmed archive snapshot. A read must not claim up-to-date archive content merely because desired equals confirmed when central definitions are newer. Overlay metadata must not be persisted over the confirmed baseline; workers need that exact baseline to detect external changes.
3. A central tag change can alter an effective read without changing the meeting state token. Include a separate tag-definition version/fingerprint in responses/cache validation, or reconcile before issuing a token whose scope promises to cover that effective view. Stale UI style edits need central revision preconditions.
4. Central revisions are scoped to their authority. Independent installations/offline writers cannot safely compare bare revision numbers. Preserve file definitions, distinguish imported/offline versions, and use provenance and/or content comparison; never adopt a larger file number automatically. A local revision must not remain falsely current after an offline change to label/style.
5. Authorized scheduling preserves the current file access boundary; workers already use `cassini` after acceptance. Decide explicitly if access must be rechecked at execution (current accepted work generally proceeds after acceptance). A global canonical label still intentionally affects other users' views of the same tag. Filter vocabulary responses and do not leak hidden carriers or label-conflict ownership.
6. Limit the first design to names/styles. Global delete/merge needs retained tombstones/redirects, or a dormant file could reintroduce an obsolete identity.
7. Migration must seed one canonical definition per existing tag deterministically, preserve current style data, and preserve pending annotation snapshots. Canonical label collision policy must account for existing duplicate labels and visibility rules.

## Assessment

Viable and a natural extension of the branch. Adds small indexed database work to reads; full file rewrite/upload remains the dominant cost. No performance claim is benchmarked yet. The principal scope expansion beyond D-793 is canonical names and backend retrieval semantics, plus reliable reconciliation and version provenance.
