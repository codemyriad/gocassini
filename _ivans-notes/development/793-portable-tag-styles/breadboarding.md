# Surfaces and data flow

Companion to `shaping.md`. Identifiers here map to validation scenarios and slice responsibilities; they are not new public API names.

## B1 — Create/apply a tag

The tag picker submits the existing single-meeting or atomic selection request. The server resolves identity and initial appearance once, then commits complete local definitions before returning success. The UI uses returned snapshots immediately.

```text
Tag picker / selection bar
        |
        v
mark + optional creation styles
        |
        v
access checks -> visible vocabulary/identity resolution
        |
        v
single transaction (all targets for a selection)
  tag definitions + marks + projection + receipt
        |                              |
        v                              v
UI results                         archive pending
                                       |
                                       v
                           existing workers -> saved
```

| Surface | Input | Output/state |
|---------|-------|--------------|
| Picker | New label + colour or existing selected ID | Existing mark request; no new selection workflow |
| Mutation helper | Parsed batch + trusted initialization + current document | One finalized snapshot per changed recording |
| Selection result | Complete batch response | One UI publication; no partial rows |
| Archive indicator | Existing sync state | Pending/delayed/blocked until verified saved |

## B2 — Rename/restyle in the tag manager

The manager submits only edited fields. Style changes now have the same visible-meeting scope and asynchronous target job as rename. One combined request updates name and appearance atomically within each target; targets remain independent.

```text
Tag editor -> partial update -> stale visible carrier check
                                  |
                      +-----------+-----------+
                      |                       |
                   no changes              changed
                      |                       |
                   no job                persisted job
                                              |
                                   per-target DB mutation
                                              |
                              +---------------+--------------+
                              |                              |
                         job progress                   archive sync
                              |                              |
                         finished/failed                 saved/retry
```

The existing job tracker refreshes vocabulary and the open meeting. It must not treat finished as proof all files are saved. A busy caller cannot start another scoped rename/restyle job. Failure UI retains the submitted action for same-session retry.

## B3 — Read local definitions and legacy fallback

Normal viewing does not enqueue repair. New local appearance overrides all aggregate/legacy styles. Eligible old definitions may use a separate legacy fallback map for display until an authorized mutation embeds it.

```text
Authorized read -> raw desired document + eligible legacy map
                                   |
                                   v
                       local appearance present?
                          /                \
                        yes                no
                         |                  |
                         v                  v
                    render local     legacy map if available
                         |                  |
                         +---------+--------+
                                   |
                          safe palette/icon rendering

Public read -> embedded document -> safe palette/icon rendering
```

List responses carry local `appearance` objects so badges do not accidentally inherit picker styles. Existing aggregate label/search semantics remain intact. No global style data is requested by a public reader.

## B4 — Offline CLI

The same model and style operation run without the operator. A batch produces at most one rewrite and preserves audio binding, IDs and unrelated marks.

```text
ops JSON -> shared parser/mutator -> validate complete document
                                          |
                                 changed? +-- no --> unchanged/copy
                                          |
                                         yes
                                          |
                                          v
                                  one recording rewrite
                                          |
                                          v
                            show / public embed retain appearance
```

## Reflection checks

- Every style write reaches the same durable mutation/snapshot path as names; no post-commit JSON side effect remains.
- Every display surface has local style data or an explicit old-server compatibility fallback.
- Empty icon is a stored value, not a missing wire path.
- Jobs retain requested fields across restart; archive workers never consult mutable vocabulary.
- Legacy display and raw archived state remain distinguishable; legacy fallback is not silently persisted on GET.
