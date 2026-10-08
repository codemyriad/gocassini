# Source media disposal

Capture, publication and media retention remain independent. `retain_video`
controls audio-only versus audio/video capture. `meeting_format` controls Opus
versus transcription JSON. The new `source_retention` defaults to
`storage-policy`; `delete-after-processing` is explicit, requires JSON,
audio-only capture and enabled prepared transcription.

## Admission and processing

`prepareRecordJob` takes one trusted settings snapshot. `request_json` contains
`processing_policy` with publication format and retention, alongside the existing
capture provenance. Disposal jobs also save the transcription settings required
to complete that initial run. Caller recording parameters cannot override them.
Changing settings applies to subsequently admitted recordings and never converts
existing recordings. Jobs without a processing snapshot retain legacy behavior.

    administrator settings -> recording admission -> immutable job policy
              |
              +-- later Save -> subsequent recordings only

JSON with retained source still permits source-based reruns while source media
exists. Disposal jobs cannot rerun, even when deletion has failed. Temporary
resource deferrals within their initial attempt are still supported. Exhausted
or permanent resource failures terminate instead of remaining blocked; native
transcription failures and skipped/failed transcript manifests cannot silently
publish an audio-only fallback for these jobs.

The public recording policy is embedded in `provenance.recording`. This declares
the selected policy, not proof of completed deletion. Recording time is preserved
by the base branch's existing export path. Operator metadata remains authoritative
for actual cleanup progress.

## Durable lifecycle cleanup

Migration 0015 adds `media_cleanup`. An obligation is inserted in the same
transaction as an opted-in job. Its terminal job state activates cleanup, so the
success/failure transaction itself makes the obligation eligible; there is no
crash-prone enqueue handoff after termination.

    admitted (waiting) -> processing terminal -> pending cleanup
                                                   |
                   startup/worker retry <--- error  |
                                                   v
                              journal -> rename -> remove old bytes
                                                   |
                                                completed

Stage finalizers attempt cleanup under their job lock. A worker checks due
obligations at startup and every 30 seconds, skips busy jobs/archive readers,
and retries errors with exponential delays from 30 seconds to a 16-minute cap.
Queued/running jobs are protected. Interrupted jobs are terminal for this policy.
The worker processes up to 100 eligible obligations per pass; general orphan GC
and retrospective conversion are not included.

Deletion reuses the retention journal and target validation. It removes canonical
and attempt `.run` / `.meeting` media, Opus aliases, attempt `.site` staging,
job-owned `.scratch` directories, source promotion staging/backups, and seal
contents other than the intended JSON. Record/build/seal/publish subprocesses get
an attempt-owned `TMPDIR`. The JSON packer's temporary Opus remains inside the
seal directory and is explicitly covered. Published JSON, the local JSON archive,
immutable JSON seals and stage logs remain under their existing policies.

Successful disposal publication promotes only JSON, never the audio intermediate.
Indexing runs before intermediate cleanup. Search backfill can read the published
JSON after the local source is gone. Failed publication may retain a completed
JSON seal but does not enable source-based reruns or add publish-only recovery.

The operation directory is removed and its parent synced before the deletion
journal is cleared. Only after all operations finish does the final transaction
mark source availability `deleted` and cleanup `completed`. Cleanup errors do not
change publication success into failure. Existing age retention waits for disposal
to finish before acting on that job's remaining output/log files.

## Operator surface

The Recording media and publication section first offers Nothing, Full audio +
video, or Audio-only, followed by the publication format. Nothing selects JSON
publication, disables Opus publication and turns video capture off. Choosing either
retained-media option allows both publication formats. Model
readiness is validated server-side; the form also prevents saving without a
selected enabled model. The settings update is atomic.

Job lists, details and live events expose cleanup state. Both SSE and polling
continue reporting cleanup after processing is terminal. Rerun is disabled with
a policy-specific explanation, independently of whether bytes still exist.
`POST /operator/jobs/<id>/cleanup` resets the retry deadline; it does not rerun
processing. It is covered by the ADMIN AppAPI route registration.

## Verification

Validated locally with the complete operator package test suite, focused operator
race tests, the complete portable-format suite, recorder transcription/packing/
recording-time tests, all 395 app tests (38 files), the viewer transcription browser
test, and normal plus embedded app production builds. Existing Svelte accessibility
and unused-CSS warnings remain.

Tests cover settings independence/validation/persistence, trusted admission,
terminal stage failures, interrupted cleanup recovery, symlink refusal, hard-link
aliases, archive-reader/job locks, JSON-only promotion, local sink delivery,
Nextcloud JSON delivery and shares, search after deletion, policy/date roundtrips,
and browser settings, rerun blocking and cleanup polling/retry.

A live Nextcloud Talk call and production deployment are not part of the local
verification. Existing source-capture tests remain applicable because video/audio
negotiation and packet capture are reused unchanged.
