# Validation plan

Status: planned checks only. No feature code or tests were run during this planning session. Baseline source was inspected at `0ac233f2`; earlier branch implementation notes contain prior results, not results for this feature.

## Scenarios and evidence

| ID | Scenario | Required evidence |
|----|----------|-------------------|
| V1 | Portable round-trip | Absent fields remain absent; known/unknown strings and explicit empty icon survive parse/clone/rename/snapshot. Public parser retains usable tags when appearance values are malformed. |
| V2 | Restyle semantics | ID-based partial updates; omitted fields unchanged; empty icon clears; null/invalid authored tokens rejected; missing ID recorded notFound; identical final definition is a no-op. |
| V3 | Combined mutation | Relabel+restyle produces one revision/snapshot per changed target; a relabel conflict rolls back style too; unrelated marks, actors and operation IDs remain unchanged. |
| V4 | Offline authoring | Real `.opus` creation-style apply, restyle, show (JSON and text), snapshot rewrite and public open; unknown existing values survive unrelated writes and unresolved audio remains bound correctly. Remote CLI forwards hints rather than dropping them. |
| V5 | Schema migration | Literal old-schema fixtures for versions 2–5 upgrade; version 5 retains generation, heads, snapshots, pending/blocked/in-flight states, jobs, receipts and batch targets. Reopen is idempotent; future schema refuses without deletion. No archive call or new snapshot during migration. |
| V6 | Projection correctness | Nullable styles populated from desired snapshots, not confirmed; imports preserve pending edits; explicit empty icon differs from NULL; audit retained for unchanged definitions and attributed only for actual edits. |
| V7 | New/existing identity | D-773 explicit-ID and mixed-case regressions stay green. An existing ID copied into a new meeting gets initial visible representative style; already-local definitions stay intact; client hints cannot restyle an existing identity. |
| V8 | Atomic batch | 1–100 meeting constraints unchanged; all-or-none snapshots/projections/receipts/styles; one vocabulary resolution before mutation; replay returns same styles after vocabulary/legacy input changes; conflicting reuse of request ID is 409. |
| V9 | Legacy compatibility | Read old unstyled document with legacy colour/icon without mutating DB/file; actual authorized edit embeds fallback once; partial restyle retains unedited appearance; cleared icon never resurrects. No-op ordinary edit does not trigger migration. Missing/corrupt legacy input is handled as specified. JSON checksum unchanged by all new write paths. |
| V10 | Scope and stale minority | Two users with overlapping but unequal visible sets: A's edit changes only A's carriers; B-only recording keeps local style in DB/file/UI. Representative style already matching the request does not hide a stale A-visible target. Busy returns before side effects. |
| V11 | Persisted jobs | Resume old bare-op rename/merge/delete jobs and new combined envelopes; stable target request IDs prevent double mutation after simulated crash between commit and progress persistence. Failed target remains retryable; removed tag is not re-created. |
| V12 | Vocabulary and list | One vote per visible meeting, deterministic pair tie-break, no hidden style/count influence; list uses per-meeting appearance rather than aggregate style. Explicit empty local object differs from absent old-server field. |
| V13 | Viewer/editor | Meeting, list, transcript and public embed use safe local appearance; no aggregate override. Unknown tokens cannot become CSS/path injection or silent name-only replacement. Editor scope text covers style changes, tracker follows restyle, and current meeting refreshes after a manager job. |
| V14 | Archive parity | Snapshot JSON equals parsed post-rewrite/upload annotation JSON, including cleared fields; audio digest unchanged for metadata edits; conditional upload, readback verification, coalescing and delayed/blocked retries retain behavior. Wait for saved before comparing downloaded file with app. |
| V15 | Performance boundary | Indexed carrier query; no full-file scan to find targets; ordinary reads add no media downloads/writes; a new styled mark and combined edit do not schedule separate style rewrites. Batch UI remains one POST and one publication. |
| V16 | Merge/delete/audit | Existing local destination keeps its style; missing destination receives resolved initialization. Delete cleans local definitions without changing legacy file. Rename-only preserves local styles. Visible attribution is useful and does not imply a global authoritative change. |

Prefer existing fixtures and fakes in `annotations_*_test.go`, recorder annotation tests and viewer tests; add focused cases instead of creating a second harness. Migration fixtures must actually represent old schemas: constructing a supposedly old DB from a modified current-schema constant alone does not prove the upgrade.

## Prerequisites

- Go matching module directives (currently 1.24.0 or compatible newer), a working native toolchain for recorder dependencies, and FFmpeg/ffprobe available for actual portable rewrite tests.
- Node 22 (current CI choice) and npm; install workspace dependencies from the root `package-lock.json` with `npm ci` if absent/stale. Do not regenerate nested lockfiles.
- Docker/Compose for local end-to-end work. On macOS follow `harness/README.md` setup, including Docker Desktop host networking. Use `./bin/cassini` as the CLI entry point; it builds the recorder and can require the same native runtime dependencies as recorder tests.
- Synthetic portable recordings and local test identities. Never use production meetings or commit real audio, secrets or deployment config.
- Read existing harness state first. Do not reset/reseed/delete volumes to get a clean result without explicit authorization.

## Automated commands

From the repository root; these commands are for the implementation session, not claimed results:

```sh
(cd cassini-annotations && go test ./...)
(cd cassini-go-recorder && go test ./internal/cassini ./internal/portable -run 'Annotat|MeetingsAnnotate')
(cd cassini-operator && go test ./internal/operator -run 'Annotat|Tag')
```

Use the focused commands while iterating. After S4, run full relevant coverage and builds:

```sh
(cd cassini-annotations && go test ./...)
(cd cassini-go-recorder && go test ./internal/cassini ./internal/portable)
(cd cassini-operator && go test ./internal/operator)
(cd cassini-operator && go test -race ./internal/operator -run 'AnnotationBatch|AnnotationD773|TagJob|Restyle|Annotation.*Style')
npm test --workspace cassini-viewer
npm test --workspace cassini-app
npm run build:all --workspace cassini-viewer
npm run build:all --workspace cassini-app
```

Confirm selected race-test names match real tests, including newly added tests; a passing command with no matching tests is not evidence. Run formatting/lint checks appropriate to changed code, and inspect `git diff --check`. If a native dependency blocks recorder tests, use the repo's documented supported development environment; report the exact limitation rather than treating skipped media tests as passing.

## Local harness flow

First inspect state with `./bin/cassini dev stack status`. For an existing seeded harness, the prior branch workflow was:

```sh
./bin/cassini dev stack down
./bin/cassini dev stack up --resume --storage-mode acl-enabled --build
```

This restarts the local stack while preserving volumes. Only do so in the authorized implementation/validation session and after checking it is the intended local stack. If no seed exists, follow the current harness README to create a local fixture rather than assuming the commands populate meetings.

Open `http://127.0.0.1:28080/apps/app_api/embedded/gocassini/viewer` when using the standard local profile; use the actual resolved URL for other profiles. The prior seeded harness used public test credentials `admin` / `admin`; do not assume a different installation uses these. Wait for initial annotation import.

1. Use two accessible synthetic meetings and a third accessible only to a second test user. Seed the same tag ID where scope differences matter; do not rely on same labels minting the same ID offline.
2. Create a coloured tag on a whole meeting and on a stretch; apply an existing ID and a new tag to multiple selected meetings. Capture request counts and snapshot values.
3. Restyle, clear icon, then combine rename/style. Check the job, exact per-target document changes, current meeting/list refresh and inaccessible target invariance.
4. Poll each target until sync is saved. Download the archived `.opus` from the local installation, not an original sealed source file that predates tagging.
5. Use the public demo below to compare appearance. Capture DB response JSON, parsed file annotations and screenshots without private data.
6. Test legacy input in a disposable local data fixture or copied store; do not overwrite the user's existing JSON merely to create a test. Compare its checksum before/after writes.
7. Use fakes for deterministic lost-response/crash/blocked-archive tests; supplement with a controlled local restart if needed. Never claim job completion is archive completion.

```text
App edit -> desired snapshot -> archive saved -> download
                                                 |
                                                 v
                               standalone show + public embed
```

## Offline and public-viewer exercise

Prepare JSON ops files as disposable test artifacts, using the examples in `shaping.md`. Replace paths and tag IDs with actual synthetic fixture values. Use a distinct output to keep the input recoverable.

```sh
./bin/cassini annotate show /absolute/path/to/input.opus --json
./bin/cassini annotate apply /absolute/path/to/input.opus --ops /absolute/path/to/create.json --actor-id local-test --out /absolute/path/to/styled.opus --json
./bin/cassini annotate apply /absolute/path/to/styled.opus --ops /absolute/path/to/restyle.json --actor-id local-test --out /absolute/path/to/restyled.opus --json
./bin/cassini annotate show /absolute/path/to/restyled.opus --json
npm run build:public --workspace cassini-viewer
node cassini-viewer/scripts/serve-embed-demo.mjs --recording /absolute/path/to/restyled.opus
```

Use the host URL printed by the demo (default host port 4179; assets/recording on 4178). It deliberately uses separate origins and Range-capable file serving. Follow installed browser/computer skill instructions if using those tools for visual validation. Capture both whole-meeting and stretch appearance.

## Performance evidence

Use existing test spies/counters and the browser Network panel rather than adding instrumentation to product flows. Run `EXPLAIN QUERY PLAN` against a disposable migrated SQLite fixture for tag-carrier lookup and inspect actual execution if a different plan is selected. Compare a mark with/without supplied style and a combined edit: one changed target should have one new desired snapshot, and should require no extra archive pass solely because appearance was added. Retries, superseding edits and existing readback verification may legitimately add IO; record them separately.

Normal warm reads may still perform the branch's existing access HEAD and catalog work. The requirement is no additional media download or archive write for checking/rendering styles, not zero network traffic.

## Completion record

During S5 write actual test/build commands, results, environment limitations, commit IDs and local evidence paths into `implementation.md`. Write user-facing setup and usage into `tutorial.md`. Record deferred legacy retirement and existing broader job-recovery limitations in `followups.md`. None of these should claim results before execution.
