# Nextcloud audio and transcription retention

Operator → Storage → Retention has separate Nextcloud policies for **Recordings
(audio)** and **Transcriptions and meeting data**. Both default to forever,
including upgrades from settings version 3. Container recordings, attempt
artifacts, logs and independently saved insights have their own policies.

Audio expiry replaces the active Opus file with a portable
`*.cassini.transcription.json`. The transcript, speaker names, summary, supported
attachments, tags and annotations remain readable and editable. Play and audio
download are disabled. A recording without words still retains its metadata.
Transcription expiry removes the whole active meeting and Cassini's serving
indexes. Equal deadlines remove the meeting directly, without a conversion.

Transcription retention must equal or outlast audio retention. For example,
audio 30 days and transcription 90 days keeps text for the remaining 60 days.
Audio forever with finite transcription retention is invalid. Ages use the
original recording's UTC date, not the last annotation, publication or rename.
A restored file does not acquire a new age. Extending a policy does not restore
content already removed.

## Preview and execution

Choose policies and select **Preview Nextcloud retention** before saving. The
preview reads live managed inventory and reports due conversions/removals,
deadlines, known active-file counts/bytes and skip reasons. It does not save
settings, adopt files, create operations or mutate Nextcloud. Unknown ownership,
ambiguous identities, missing original dates and changed file locations are left
alone. Failed previews are not empty archives.

Saving uses the settings revision to detect another administrator's changes.
Saving does not delete files. The existing daily schedule, startup pass and
manual synchronous sweep execute the saved policies. Active/busy jobs are
protected. Operation status remains available after a proxy timeout; rerun the
sweep to recover incomplete operations rather than deleting files by hand.

The ADMIN routes are:

- `GET/PUT /operator/storage/retention` — version 4 settings; PUT needs `If-Match`.
- `POST /operator/storage/retention/preview` — proposed version 4 settings.
- `GET /operator/storage/retention/operations?offset=0` — pages of 100 outcomes.
- `POST /operator/storage/retention/sweep` — existing synchronous sweep, with
  Nextcloud conversion/removal counts and outstanding operations in its result.

## Supported storage and access

Retention is certified against the pinned Nextcloud 33.0.9, 34.0.0 and 35.0.0
AppAPI fixtures. Finite policies require an existing private Cassini archive in
local home storage. Enable Nextcloud's `serverinfo` app so Cassini can verify the
storage inventory. Missing evidence, other/object storage, external storage,
server-side or end-to-end encryption, file access control and automated tagging
apps block activation. Ordinary reading and container retention remain separate.
These configurations need their own compatibility proof before remote expiry can
be enabled; there is no force or unconditional-delete fallback.

Conversion conditionally overwrites the existing leaf and moves that same file
without overwriting another destination. File IDs, direct/group/Team shares,
downstream shares and public tokens/attributes remain attached to the file.
Current Nextcloud authorization still applies to every read and mutation.
Recipient rename and revocation are tested independently of file-ID equality.
Nextcloud defines what public-link download restrictions enforce; Cassini
preserves those attributes and checks that conversion does not widen their
observed access. A hidden Download button is not a secure-erasure boundary.

Existing Cassini meeting IDs and aliases continue to resolve. Raw external DAV
URLs containing the old filename can break after rename; Nextcloud file-ID links
and shares are the stable references. Downloads advertise JSON after conversion.

## Recovery and privacy boundaries

Cassini writes a durable intent and fsynced private staging before mutation. It
verifies the exact source ETag/identity and reads back hashes after PUT and MOVE.
An interrupted response is resolved from observed bytes, never by an
unconditional retry. Pending annotation edits are captured into JSON; edits
accepted during upload retain their own pending snapshot and survive restart.
Unknown annotations, unclassified binary attachments, malformed data and external
content conflicts leave the file unchanged or the operation incomplete, with an
explicit error. Resolve the conflict and retry; keep the durable jobs database
and annotation store together with their work root when recovering an install.

The immutable locator, age and retirement tombstone live in the main jobs
database, not the disposable metadata/search indexes. On final expiry, serving
is denied before deletion and while cleanup retries. Cassini removes the target
from search/metadata, annotation snapshots and projections, and scrubs the target
from receipts and tag jobs without removing unrelated targets. Audit rows retain
identifiers, timestamps and status, not transcript text. Generated context staging
is request-scoped and removed on completion; separately saved insights/downloads
follow their own policies.

Nextcloud retains any previous versions and Deleted files under **its own
configuration**. Cassini does not purge them or change that configuration. A
version rollback may put Opus bytes under a JSON filename: readers detect actual
content, and the next sweep re-evaluates the original age. A rollback with older
annotations conflicts with newer acknowledged edits instead of discarding them.
A restored retired file must still match its managed identity before re-eviction;
unexplained replacements are not automatically deleted. Tombstones continue to
deny Cassini publication and serving.

Logical active-file bytes are not physical disk reclamation. History/trash
capacity is unknown, not zero. This feature does not erase filesystem snapshots,
backups, independent downloads or storage-device remnants.

## Deployment and synthetic verification

Deploy the reader-compatible image and its matching versioned manifest together.
Version `0.2.0-beta.8` adds the preview/operations routes; the compatibility probe
verifies an AppAPI upgrade from the previous route set. Keep both policies forever
until the instance reports capability and the preview matches expectations.
Older releases cannot operate safely on retained JSON or version 4 policies;
returning to an older image is not a way to restore removed audio.

The following test creates its own pinned Compose projects, temporary users and
files, and removes those projects afterward. It does not reset an existing
harness or use real recordings:

```sh
IMAGE_REF=cassini-exapp:d803 harness/bin/validate-retention-matrix.py
```

Build that image from the current checkout first. The matrix exercises AppAPI
route upgrade, policy defaults/preview, conditional DAV mutations, shares/public
access, historical restore, concurrent annotations/restart, actual sweep,
retirement, same-audio refresh and the installed headless viewer. For the existing
development harness, use `validate-retention-dav.py` and
`validate-retention-lifecycle.sh`; they create only new synthetic leaves/users.
The browser seeder deliberately accepts only the matrix's disposable containers.

See [the storage contract](nextcloud-retention-dav.md),
[portable JSON](portable-transcription.md), [container retention](container-retention.md)
and [storage usage](storage-usage.md).
