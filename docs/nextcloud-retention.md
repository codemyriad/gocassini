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

Age uses the published meeting's optional `recordedAtLocal`, falling back to
mandatory `createdAtUtc` only when `recordedAtLocal` is absent. The local
recording timestamp supplies its own calendar date without a timezone shift;
creation timestamps are evaluated on UTC dates. Adoption still requires a
matching `<job-id>.opus` and successful publication. Invalid or unavailable
timestamps keep the file unchanged; job completion dates are never substituted.
Existing job-based lifecycle records migrate to these timestamps at the next
sweep. The chosen timestamps persist, so republish, reruns and annotations do
not reset an adopted meeting's age. Older catalog entries require downloading
the published Opus once during adoption to read its embedded timestamps.

Preview and imperative sweep are API-only inspection/development tools. The UI
provides policy settings and operation status. Preview evaluates proposed
settings without saving settings, lifecycle migrations or deletion intent.
Saving activates the
policy for startup and scheduled sweeps; it does not sweep immediately. The
shared daily schedule defaults to 02:00 UTC.

The ADMIN-only API is under `/operator/storage/retention`:

- `GET` reads settings and their ETag; `PUT` supplies the complete settings and
  matching `If-Match` revision. Stale revisions return 412.
- `POST /preview` evaluates complete proposed settings with the current revision.
  Its `meetings` array includes `name`, `createdAtUtc`, `recordedAtLocal`
  (empty string if absent), `age` (integer calendar days, negative for future
  dates, null when unknown), and `decision` (`evict` or `keep`). Existing
  action/reason/deadline and aggregate diagnostics remain available. A skipped,
  busy or unverifiable file reports `keep` with a reason.
- `POST /sweep` executes due work and reports retirements and incomplete intents.
  A partial failure returns an error even when unrelated meetings were removed.
- `GET /operations?offset=0` returns up to 100 journal statuses and `nextOffset`.

```text
successful managed publication + published meeting timestamps
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

After timestamp adoption, deletion does not require downloading the Opus. A
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

Example preview row (inside `meetings`):

```json
{
  "name": "meeting.opus",
  "createdAtUtc": "2026-09-29T12:00:00Z",
  "recordedAtLocal": "2026-03-05T23:38:29",
  "age": 215,
  "decision": "evict"
}
```

Fetch settings with `GET /operator/storage/retention`, then post that complete
JSON document (optionally adjusting the policy) to
`POST /operator/storage/retention/preview`. The revision must still match.
The response's `capability` and `reason` indicate whether remote execution is
available; decisions describe policy eligibility subject to that gate.
