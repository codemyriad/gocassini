# Container-local retention and eviction

Retention is the saved rule for how long to keep an artifact. Eviction is the
deletion performed when that rule becomes due. These policies apply to the
operator's local working artifacts, under its configured **work root**.
They do not delete recordings published to Nextcloud Files or the live site.

The same page shows [storage usage by retention category and date](storage-usage.md).

## Initial setup

When an administrator opens Cassini before retention has ever been saved,
**Choose what Cassini keeps** presents the current Nextcloud recording-access
panel and the complete retention editor on one scrolling screen. A missing
recordings account can be created there using the administrator's Nextcloud
session. Account setup and retention saving are independent: an account problem
does not discard retention choices, and account setup remains available in
**Operator → Publish pipeline**.

The editor is the same component used by **Operator → Storage**, including
categories, grouped/fine-grained history, day presets, custom days, schedule,
validation, reload and conflict handling. Future retention fields belong in that
shared panel so both places receive them together.

```text
Initial setup --------+--> shared retention panel --> existing GET/PUT API
Operator -> Storage -+                                    |
                                               saved policies + revision
Initial setup --> shared recording-access panel          |
                                                  future opens skip setup
```

**Save and continue** persists the displayed policies, even if unchanged, and
closes the review. The existing settings revision records completion; it works
across browsers and container restarts. Installations with any previously saved
retention settings skip the review. Existing installations that have never saved
retention settings also receive it.

The review is a modal over the Cassini scaffold. Its body scrolls inside the
screen bounds while the action footer stays visible. **Save and continue**
unlocks after the bottom is reached (or immediately if all content fits), and
native form validation still applies. Escape and backdrop clicks do not close
the review; a successful save is required. Reloading saved settings warns before
discarding edits. Errors remain visible in the footer.

```text
Cassini scaffold (inert beneath modal)
  +-- Review header
  +-- Scrollable access + retention settings
  +-- Fixed actions -> bottom reached -> Save -> close modal
```

The review does not gate recording. A failed initial read offers a link to
Storage for recovery rather than inventing settings or blocking the rest of
Cassini. Non-admins are never asked to configure retention.

This setup change preserves the existing keep-forever defaults. It does not add
an audio-only capture switch or expiry of published Nextcloud files. Those are
separate policies; a container-local retention choice is not a promise to erase
every copy of a recording.

## Configuring policies

Administrators configure retention under **Operator → Storage**. Every category
starts at **Keep forever**. Choose **7, 30, 60, 90, or Custom days**. Custom
retention accepts a whole number from 1 to 9999 days. Saving changes does not delete files immediately: existing
artefacts are evaluated at startup and daily at the configured time (default
**02:00 UTC**), using their original
lifecycle dates. There is no byte/count cap. A manual API sweep evaluates the
same deadlines; it does not force unexpired artifacts to be deleted.

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

## Exact deletion targets

In the following paths:

- `W` is the configured operator work root, for example
  `/var/lib/cassini-operator/jobs`. Check the deployment's actual configuration.
- `J` is the logical job ID.
- `A` is the attempt number, padded to at least three digits (`001`, `002`, ...).
- A trailing `/` means **the entire directory and all its contents**, including
  nested files and directories. Deletion is not limited to the example filenames.

The worker derives these paths from job and attempt IDs. It does not search the
disk for every file with a matching extension, or use file modification times.
Only existing targets are included in a deletion operation.

| UI policy / API key | Exact paths under `W` | Selection and age anchor |
|---|---|---|
| Recordings / `recordings` | `current/J.run/`; `runs/J--attempt-A.run/` if its original capture copy remains | Job source must point to canonical `current/J.run`. Uses the recording attempt's `record_finished_at`. |
| Attempt history: Failed recordings / `history.fine.failed_capture` | `runs/J--attempt-A.run/` | Failed or interrupted attempt, only when the job has no canonical source reference. Uses attempt termination. |
| Attempt history: Failed build / seal output / `history.fine.failed_build` | `runs/J--attempt-A.meeting/`; `runs/J--attempt-A.seal/` | Failed or interrupted attempt. Uses attempt termination. Also covers these files when the attempt failed later, during publication. |
| Attempt history: Superseded successful output / `history.fine.superseded` | `runs/J--attempt-A.meeting/`; `runs/J--attempt-A.seal/` | Successfully published attempt with a later successful publication. Uses the **next successful attempt's** `publish_finished_at`. |
| Attempt history: Failed publish staging / `history.fine.failed_publish` | `runs/J--attempt-A.site/` | Failed or interrupted attempt. Uses attempt termination. |
| Current output archive / `current` | `current/J.meeting/`; `current/J.opus`; `runs/J--attempt-A.seal/`; `runs/J--attempt-A.meeting/` | `A` is the published attempt recorded in `artifact_availability`. It must have succeeded and have `publish_finished_at`, which is the age anchor. |
| Logs / `logs` | `runs/J--attempt-A.logs/` | Each finished attempt of an eligible job. Uses attempt termination. |

“Attempt termination” means `completed_at`, falling back to `interrupted_at`.
Missing or unparseable lifecycle dates prevent deletion for that category.
History rows use `history.policy` when the history group is in group mode;
the `history.fine.*` values only apply in fine-grained mode.

### What is inside those targets?

The directory boundary is the exact deletion contract; contents vary with the
recorder, build options, and how far an attempt progressed. Typical contents are:

```text
W/
  current/
    J.run/                         Recordings
      cassini.json                 source bundle manifest
      recording.mkv                captured audio/video container
      session/                     optional capture/session data
    J.meeting/                     Current output archive
      cassini.json
      meeting.webm
      transcript.words.v1.json
      manifest.json
      captions.vtt                 optional
      summary.md                   optional
    J.opus                         Current output archive
  runs/
    J--attempt-A.run/              failed source or leftover capture copy
    J--attempt-A.meeting/          intermediate build output
    J--attempt-A.seal/
      J.opus                       sealed output for this attempt
    J--attempt-A.site/             publication staging
      catalog.json
      meetings/...
    J--attempt-A.logs/
      record.log
      build.log
      seal.log
      publish.log
```

Any additional regular files inside a selected bundle are deleted with it.
Raw packet captures and supporting session files inside `.run` are included.
There is no inspection of individual audio/video tracks and no remux step.
The `.opus` file includes processed audio and embedded meeting metadata; its
local copies belong to output policies, not the source recordings policy.
Files outside these targets are not swept merely because they belong to a job.

### Which policy wins?

Classification follows the artifact's lifecycle:

- A successfully promoted capture remains a **recording** even if build, seal,
  or publish later fails. A short failed-recordings policy does not delete it.
- The latest successful published version belongs to **current output archive**,
  including its attempt seal copy/hard link. Attempt-history retention does not
  independently expire that latest successful seal.
- After a later attempt publishes successfully, the older successful seal belongs
  to **superseded successful output**. An unsuccessful rerun does not supersede it.
- A failed publication can leave `.meeting`, `.seal`, and `.site` directories.
  The first two use failed-build retention; `.site` uses failed-publish retention.
- Logs have their own policy regardless of which artifact policies apply.

For example, if attempts 1, 3, and 5 publish successfully, attempt 1's superseded
age starts at attempt 3's publication, and attempt 3's at attempt 5's publication.
Attempt 5 remains current. Failed attempts between them do not reset those dates.
An older `.meeting` already removed as a duplicate is not recreated for history.

## Expiry dates and sweep timing

UTC dates determine expiry: 30 days after January 31, 2026 is March 2, 2026.
There are no week or month units. Artefacts are
eligible on their expiry date. A busy job delays cleanup without resetting age.

Precisely, the deadline is **the UTC date of the lifecycle timestamp plus N
days**, at 00:00 UTC. This is a calendar-date rule, not N times 24 hours after
the exact timestamp. A capture completed on September 1 at 23:50 UTC with a
7-day policy becomes eligible on September 8 at 00:00 UTC. The next sweep that
can reserve the job performs deletion. With the default schedule that would
normally be September 8 at 02:00 UTC; a manual or startup sweep can act earlier
that day.

Changing a policy applies to existing artifacts using their original dates.
Shortening it can make old artifacts eligible at the next sweep. Extending it
or choosing Keep forever cannot restore deleted files. A rerun does not reset
the source recording's age; a newly published output has its own publication date.

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

There are three triggers for the same expiry evaluator: operator startup, the
daily timer, and the manual API. Publication does not trigger timed expiry.
Saving settings resets the schedule timer but does not start an immediate sweep.
Startup runs regardless of the configured daily time; missed daily slots are not
replayed individually. Only one sweep runs at a time within the operator.

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

The exact job gate is `stage == done` and `state` in `succeeded`, `failed`, or
`interrupted`. Attempts must also have `stage == done`. The sweep enumerates
database jobs in pages of 100, then evaluates each job's own attempts and paths.
It covers multiple jobs because it enforces the saved global policies across
the local archive. It does not delete another job's files while handling one job.
Busy artifact locks cause a job to be skipped for this pass, without resetting
its deadline or scheduling an immediate retry.

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

Immediately before journaling a deletion, the worker rechecks the settings
revision. A concurrent save makes work prepared under the older revision stop
and wait for a later pass. Once a deletion is journaled, startup recovery finishes
it; changing the policy afterward does not roll back that operation. Recovery
runs before startup duplicate reconciliation and the startup expiry sweep.

### Duplicate cleanup is separate

`pruneArtifactsForJob` removes proven successful duplicates as part of lifecycle
cleanup. It runs after successful publication and during startup reconciliation.
The successful capture duplicate is also removed after capture promotion and
durable reference storage. This cleanup applies even with every policy set to
Keep forever:

| Successful lifecycle event | Duplicate removed | Retained artifact |
|---|---|---|
| Source promoted and referenced | Attempt `.run/` | Canonical `current/J.run/` |
| Publication succeeded | Attempt `.site/` | Sink's published recording/site |
| Attempt is the current published version | Attempt `.meeting/` | Canonical `current/J.meeting/` |

The immutable attempt `.seal/J.opus` remains until the applicable current-output
or superseded-history deadline. Scheduled and manual expiry do **not** call
`pruneArtifactsForJob`, adopt legacy archives, or perform a general orphan scan.

### User-visible effects

| Evicted category | Effect |
|---|---|
| Recordings | Job list shows **Recording deleted**; global Record bar is empty and labeled **Record (deleted)**; details explain that retention deleted the source and rerun is unavailable. Both UI and API prevent reruns. |
| Current output archive | Local current output is unavailable. Source-based rerun remains possible if the `.run` still exists and normal rerun requirements hold. Published copies remain available through their sink. |
| Attempt history | The selected old/failed attempt artifacts are gone; job and attempt records remain. |
| Logs | Stage log files are gone; this does not delete job/attempt rows or rotate operator service logs. |

The API distinguishes `present`, `missing`, and explicitly `expired` source/output
availability. Retained provenance paths describe what an attempt produced; their
presence in the database is not proof that the files still exist. The deleted
recording list badge uses recorded source expiry, not every missing-file condition.

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

### Run an expiry sweep manually

`POST /operator/storage/retention/sweep` runs the same expiry pass as startup and
scheduled runs, synchronously. It uses the current server time and saved policies;
no body is required. Active/reserved jobs and artifacts that are not yet due
remain protected. It does not run duplicate cleanup or change the daily schedule.

For a standalone operator, set its base URL and configured API token:

```sh
export OPERATOR_URL=http://localhost:8080/operator
read -s OPERATOR_TOKEN
curl --fail-with-body --request POST \
  --header "Authorization: Bearer $OPERATOR_TOKEN" \
  "$OPERATOR_URL/storage/retention/sweep"
```

Success returns HTTP 200 with `{"status":"completed"}` after the pass finishes.
This includes passes where nothing was due or busy jobs were skipped. HTTP 409
means another manual or scheduled sweep is running. HTTP 503 means retention is
unavailable or disabled. HTTP 500 reports a sweep failure; other jobs may already
have been processed. Request cancellation or operator shutdown stops further
iteration; completed deletions remain committed. A long pass may exceed a proxy's
request timeout, so use a suitable client/proxy timeout when testing large archives.

Installed Nextcloud deployments use the ADMIN-only AppAPI proxy route
`/index.php/apps/app_api/proxy/gocassini/operator/storage/retention/sweep` with
normal authenticated admin requests. The manifest route must be registered via
the same versioned update workflow described above.

Authentication uses the normal operator middleware. In an installed ExApp the
AppAPI route requires admin access. Standalone bearer authentication depends on
`CASSINI_OPERATOR_API_TOKEN` being configured; it is optional and off by default.
The sweep handler does not add a separate token or confirmation step. There is
no dry-run, per-job selector, force-delete option, or deletion-count response.

Look for `artifact operation completed`, `retention failed`, `retention skipped`
and `archive reconciliation skipped` in operator service logs. Completion logs
include job, attempt, category, policy revision and deadline. Logical sizes are
not presented as reclaimed disk bytes: hard links can retain blocks elsewhere.

Not managed here: published Nextcloud files, legacy published sites, model caches,
generic abandoned temp files (D-835), operator service-log rotation, annotations,
and job/attempt metadata. No production cleanup is part of installing this code
unless finite policies are explicitly saved.

More explicitly, these policies leave the following outside their scope:

- Published Nextcloud Files, normally
  `cassini/CassiniRecordings/meetings/J.opus`, including their shares.
- The live local sink site, normally `/srv/cassini-site/published/`, its
  `catalog.json`, and its exported `meetings/` tree. The attempt `.site/` in the
  target table is separate staging.
- Model caches, arbitrary orphan files, abandoned generic temporary directories,
  and unrecognized legacy paths. There is no disk-pressure eviction or storage quota.
- The operator database, job/attempt metadata, annotations, saved settings, and
  operator service logs. The Logs policy only addresses attempt `.logs/` directories.

Deletion permanently removes the selected local files; the operation staging
directory is not a user-accessible trash or restore facility. Reclaimed disk
space can differ from summed file sizes because other hard links or external
filesystem snapshots can retain the data.

## Implementation map

Paths below are relative to the repository root:

| Code | Responsibility |
|---|---|
| [retention_settings.go](../cassini-operator/internal/operator/retention_settings.go) | Defaults, validation, UTC deadlines, migration, atomic settings saves and revisions. |
| [retention_worker.go](../cassini-operator/internal/operator/retention_worker.go) | Scheduling, job gates, category selection, age anchors and exact expiry targets. |
| [attempt_paths.go](../cassini-operator/internal/operator/attempt_paths.go) | Canonical and attempt path construction. |
| [retention_operations.go](../cassini-operator/internal/operator/retention_operations.go) | Locks, path validation, operation journal, deletion, availability updates and recovery. |
| [retention.go](../cassini-operator/internal/operator/retention.go) | Successful duplicate cleanup and startup reconciliation. |
| [retention_sweep_handler.go](../cassini-operator/internal/operator/retention_sweep_handler.go) | Manual sweep HTTP handler. |
| [retention_availability.go](../cassini-operator/internal/operator/retention_availability.go) | Source readiness and source/output availability. |
| [run.go](../cassini-operator/internal/operator/run.go) | Runtime startup, route registration and job-list source-expiry flag. |
| [Operator.svelte](../cassini-app/src/Operator.svelte) | Job list badge, recording bar and rerun explanation. |

## Verification

From the repository root, with Go, npm dependencies and Playwright
Chromium available:

```sh
cd cassini-operator
go test ./internal/operator -run 'TestRetention|TestPublishedPair|TestArtifactOperation'
cd ..
npm test --workspace cassini-app
npm run build:all --workspace cassini-app
```

The browser tests use synthetic APIs, never real recordings. Retention tests
verify whole-bundle deletion, independent output retention, settings migration
and unavailable-source rerun protection.
