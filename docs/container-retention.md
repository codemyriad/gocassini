# Container-local retention

Administrators configure retention under **Operator → Storage**. Every category
starts at **Keep forever**. Choose **7, 30, 60, 90, or Custom days**. Custom
retention accepts a whole number from 1 to 9999 days. Saving changes does not delete files immediately: existing
artefacts are evaluated at startup and daily at the configured time (default
**02:00 UTC**), using their original
lifecycle dates. There is no byte/count cap or manual delete-now button.

| Category | Contents | Age starts at |
|---|---|---|
| Recordings | Successful `.run`: captured audio and video | Original recording completion |
| Attempt history | Failed `.run`; failed build/seal `.meeting`/`.opus`; superseded successful seals; failed `.site` staging | Attempt termination; superseded output uses the replacing successful publication date |
| Current output archive | Current `.meeting` and `.opus`, including its retained seal alias | Publication of that version |
| Logs | Attempt stage logs, successful and failed | Attempt termination |

Recordings use one policy for the entire source `.run` bundle: captured audio,
video, raw packets and supporting files expire together. There is no separate
audio or video policy. Processed `.meeting`/`.opus` output uses the independent
current-output policy.

```text
Recordings policy -> delete entire source .run -> rerun unavailable
Current policy    -> delete current .meeting + .opus and retained seal alias
```

Attempt history uses either one group policy or fine-grained policies. The first
fine-grained split copies the group value; subsequent toggles preserve inactive
values. Only the selected mode applies.

UTC dates determine expiry: 30 days after January 31, 2026 is March 2, 2026.
There are no week or month units. Artefacts are
eligible on their expiry date. A busy job delays cleanup without resetting age.

Set **Sweep time** and **Timezone** in Storage to choose the daily cleanup
schedule. Time is entered in 24-hour format; timezone names such as
`Europe/Zagreb` follow daylight-saving changes automatically. Save activates the
new schedule without a restart. Startup still performs an expiry sweep, and
retention ages continue to use UTC dates regardless of the sweep timezone.
If a clock change skips the selected time, cleanup runs at the first available
local time afterward. If a time repeats, only its first occurrence is used.

```text
Save time + timezone -> reset timer -> next local scheduled time -> expiry sweep
Operator startup    -> expiry sweep -> next local scheduled time -> expiry sweep
```

## Lifecycles and safety

Successful duplicate cleanup is independent of age policies. Capture is promoted
and its durable reference stored before its attempt copy is removed. A build or
seal does not replace the published archive: only successful publication does.

```text
capture -> promote current .run -> remove attempt duplicate
                 |
                 v
attempt .meeting -> attempt .opus -> publish successfully
                                         |
                                         v
                             promote current .meeting/.opus
                                         |
                              remove duplicate .meeting/.site
                              keep the immutable attempt seal
```

Expiring a recording removes the entire run, including video and raw packet
captures. The UI explains why rerun is unavailable; the API also refuses it.
Cleanup does not inspect or remux media, so embedded recorder report attachments
do not affect expiry. Symlinks and unsafe paths are refused and logged.

Cleanup skips active, queued and blocked jobs. Each job is reserved against
pipeline/rerun use; archive-wide local backfill reads are protected too. A second
operator cannot own the same work root. Stop the operator before running the
standalone search-backfill maintenance command against its local archive.

```text
eligible -> reserve job -> journal exact paths -> rename -> commit availability
                              |                               |
                              +-- restart recovery            v
                                                     remove old bytes -> log
```

The SQLite pending-operation journal is removed when each operation finishes;
it is not a permanent eviction audit. Jobs and attempts remain, with original
provenance paths and separate file availability. Unknown legacy dates or lineage
are logged/skipped. Identifiable legacy output is adopted conservatively; an
already-lost older `.meeting` cannot be recreated from its seal.

## Configuration, deployment and diagnostics

Settings live in `retention_settings.json` beside the configured operator SQLite
database. Settings version 3 uses days for all policies. When loading a
version 1 file, the previous group policy (or the active captured-audio policy in
fine-grained mode) becomes the recordings policy. A previous video-only deadline
no longer applies. In version 1 and 2 files, weeks convert to 7 days each and
months to 31 days each, including inactive attempt-history policies. This
conversion never brings a deletion deadline forward. Values exceeding 9999 days
after conversion disable expiry until the configuration is repaired; they are
never capped to an earlier deadline. The next
Save persists the new format. Saves use atomic replacement and a revision precondition, so a stale
admin form cannot overwrite another save. Invalid startup configuration disables
expiry and is shown as an error in Storage; repair the file and restart. The old
`--artifact-retention` / `CASSINI_ARTIFACT_RETENTION` values are deprecated, ignored
and warned about, not translated into finite expiry policies.

The `schedule` setting stores `time` (`HH:MM`) and `timezone` (for example `UTC`).
Older files without this setting default to 02:00 UTC. Invalid schedule values
are rejected. Timezone data is embedded in the operator for minimal containers.

The admin API is `GET/PUT /operator/storage/retention` (or the configured base
prefix). PUT requires the quoted revision in `If-Match` and in the JSON body.
It is ADMIN-only in the AppAPI manifest. Installed AppAPI registrations must pick
up the new route through the normal versioned update workflow; a new image alone
does not refresh a stale route allowlist. See
[update constraints](./exapp-update-constraints.md#5a-routes-are-not-creation-time--an-in-place-update-rewrites-them-provided-the-release-bumps-version).

Look for `artifact operation completed`, `retention failed`, `retention skipped`
and `archive reconciliation skipped` in operator service logs. Completion logs
include job, attempt, category, policy revision and deadline. Logical sizes are
not presented as reclaimed disk bytes: hard links can retain blocks elsewhere.

Not managed here: published Nextcloud files, legacy published sites, model caches,
generic abandoned temp files (D-835), operator service-log rotation, annotations,
and job/attempt metadata. No production cleanup is part of installing this code
unless finite policies are explicitly saved.

## Verification

From the repository root, with Go, npm dependencies and Playwright
Chromium available:

```sh
cd cassini-operator
go test ./internal/operator -run 'TestRetention|TestPublishedPair|TestArtifactOperation'
cd ..
npm test --workspace cassini-app
npm run test:retention-browser --workspace cassini-app
npm run build:all --workspace cassini-app
```

The browser check uses a synthetic API, never real recordings. Retention tests
verify whole-bundle deletion, independent output retention, settings migration
and unavailable-source rerun protection.
