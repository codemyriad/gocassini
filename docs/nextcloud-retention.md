# Whole-meeting retention in Nextcloud Files

Administrators configure **Nextcloud Files > Whole meetings** in Operator >
Storage. The default is **Keep forever**. A finite policy deletes the published
`.opus`, including audio, transcript and meeting notes, and removes the meeting
from active Files, Cassini lists, search and annotations. Container retention
remains independent; see [container retention](container-retention.md).

Settings version 4 adds `nextcloud: { "meetings": { "forever": true } }`.
For expiry, use `{"forever":false,"count":30,"unit":"days"}`. Counts must be
integers from 1 to 9999. Unsupported policies are rejected. Existing container
settings upgrade with their deadlines preserved and Nextcloud set to forever.

The original age is the earliest valid `record_finished_at` in the job and its
attempts. Adoption requires a matching `<job-id>.opus` in Cassini's private
archive and a successful `done`/`succeeded` attempt. Unknown provenance or missing
recording dates appear as skipped and leave files unchanged. Republish, reruns
and annotations cannot reset a persisted age. Deadlines use UTC calendar dates.

Preview evaluates unsaved settings without deleting files or saving intent. It
shows keep/retire/skip decisions, deadlines, verified managed active bytes and
counts, busy jobs, capability failures and recovery state. Saving activates the
policy for startup and scheduled sweeps; it does not sweep immediately. The
shared daily schedule defaults to 02:00 UTC.

The ADMIN-only API is under `/operator/storage/retention`:

- `GET` reads settings and their ETag; `PUT` supplies the complete settings and
  matching `If-Match` revision. Stale revisions return 412.
- `POST /preview` evaluates complete proposed settings with the current revision.
- `POST /sweep` executes due work and reports retirements and incomplete intents.
  A partial failure returns an error even when unrelated meetings were removed.
- `GET /operations?offset=0` returns up to 100 journal statuses and `nextOffset`.

```text
successful managed publication + original recording completion
    -> due policy + idle job + verified file ID/path/strong ETag
    -> durable deletion intent + retiring tombstone
    -> owner DELETE with If-Match
    -> verify absence + scrub application projections
    -> retired tombstone + minimal audit
```

Preparation shares the artifact lock with rerun admission and rechecks the job.
Pending intent blocks new reruns and cannot be replaced by a new sweep. Recovery
checks recorded intent, file identity and ETag; a missing file without a recorded
deletion attempt is a conflict. Conflicts retain their intent while unrelated
due meetings continue. Journal, capability and inventory failures stop new work.
Restored content is removed again only when its original managed identity can be
verified. Changed IDs, paths or ETags are never silently accepted.

The worker uses conditional deletion without downloading the whole Opus. A
strong ETag guards the mutation; the file ID and exact DAV response path guard
identity. Retention DAV requests refuse redirects with AppAPI credentials.
Tombstones prevent serving, publishing and projection rebuilds during retirement.
Job history and minimal operation audit remain.

The capability gate retains the existing conservative range (NC 33.0.9 through
35), private local home storage and serverinfo evidence. Encryption, external
storage and access-control/automated-tagging configurations remain unsupported.
These are runtime gates, not a claim that this split was validated on a deployed
matrix. Installed validation must exercise settings, preview and sweep on a
fresh deployment with disposable fixtures; see [validation](nextcloud-retention-dav.md).

Nextcloud versions and Deleted files, independent exports and backups are
separate. Active-file deletion does not guarantee physical disk reclamation.
