# Use transcription publication with optional source disposal

## Before starting

Run a build containing this feature with a configured recording backend and
publication destination. As an administrator, open **Operator → Publish
pipeline**, install/check a supported speech model, enable transcription and save.
The deletion option requires a prepared model. Installing this change does not
delete old media or change existing JSON publication settings.

For an installed ExApp, deploy through the normal versioned upgrade process so
AppAPI refreshes its ADMIN route registration, including the cleanup-retry URL.
An image replacement alone may leave an older route allowlist in place. See
[update constraints](../../exapp-update-constraints.md).

## Publish a transcript and retain source media

1. In **Recording media and publication**, choose **Full audio + video** or
   **Audio-only** under **Keep after each recording**. Storage policies determine
   how long those source recordings remain.
2. Select **Transcription only (.json)** under **Published meeting**. You can also
   choose **Include audio (.opus)** to publish playable audio; captured video
   stays on the server.
3. Save and start a new recording.
4. The published viewer has no player. The operator can rerun processing while
   the source exists and ordinary rerun requirements are met.

## Publish a transcript and delete source media

1. In **Recording media and publication**, choose **Nothing** under **Keep after
   each recording**. This removes recording media, including audio, video and raw
   packet logs, after the initial processing run; transcription artifacts remain.
2. **Transcription only (.json)** is selected automatically. Audio publication is
   disabled. Audio is captured temporarily for transcription, without video.
3. Read the notice and Save. No separate confirmation dialog is required.
4. Start a new recording. Existing recordings, including ones already accepted
   but waiting to process, retain their original policy.
5. After publication, open the job details and wait for **Media deleted**.
   Confirm the meeting JSON still opens and downloads without playback.
6. **Rerun** remains disabled. Tags, annotations and other text-only operations
   continue to use the published meeting.

    temporary audio -> transcription -> JSON publication -> media deletion
                              |
                           failure ----------------------> media deletion

If transcription permanently fails, source media is still deleted; there may be
no usable transcript. Transient retries during the initial processing run are
allowed. A failed publication can retain its sealed JSON locally, but this feature
does not add a publish-only retry workflow.

## If deletion fails

Job details show **Media deletion failed** and the error. Processing/publication
status remains separate. Fix the reported filesystem problem, then select
**Retry media deletion**. The worker checks it within 30 seconds when the job and
archive are available. Automatic retries also continue, with backoff capped at
16 minutes, and pending work resumes after restart.

Do not interpret a transcription-only viewer as proof of server deletion: use
the operator cleanup status. This policy removes Cassini-managed media; transcripts,
metadata and diagnostic logs remain under their retention rules, and external
backups/snapshots are outside its control. General orphan GC is separate work.
