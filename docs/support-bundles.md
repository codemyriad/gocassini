# Download a support bundle

Open Cassini and press **Download support bundle** above the meeting browser.
The button is available even when your meeting list is empty or fails to load.
Administrators can also find it in **Operator → Doctor**.

Cassini downloads a ZIP named
`cassini-support-<original|scrubbed>-<UTC time>-<id>.zip`. Review it before
attaching it to your support request. Nothing is uploaded automatically.

**Original diagnostics are the default.** They retain log text, unfamiliar error
messages and diagnostic identifiers, which can contain names, URLs, credentials
or meeting text. You can send the ZIP as downloaded, or extract it, edit/remove
sensitive information and create a new ZIP yourself.

Select **Scrub sensitive information** before downloading if you want Cassini to
produce a filtered bundle instead. This replaces original logs and error messages
with diagnostic categories/status codes and pseudonymizes job identifiers. It
can remove information needed to diagnose an unfamiliar failure. You can download
both versions and decide which to share. The chosen mode is recorded in the
manifest and README. Neither option changes which users' data you can access.

An ordinary user's bundle contains the browser's basic connection/list status,
the recording-setup verdict and a fresh check of the user's received recording
shares in Nextcloud. An empty list and a failed request are reported separately.
The bundle contains no other users' jobs or installation logs. If your recordings
are missing, an administrator's bundle can provide the publication diagnostics
that your account cannot access.

An administrator's bundle additionally contains:

- App, commit, Go and platform versions; selected processing/model settings;
  cached model readiness and health findings, with their check times.
- Up to 50 jobs updated in the last seven days, with up to three recent attempts
  per job, processing outcomes and failures (with original error details unless
  scrubbing is selected).
- The last 256 KiB of each available stage log, and persistent operator,
  delivery and sharing events. HTTP and OCS status
  codes are kept separately: Nextcloud can refuse a share inside HTTP 200.

In scrubbed mode, logs are diagnostic projections: recognized failure categories
such as a group-sharing restriction, out of memory or disk full. Unrecognized
errors are classified without their original wording. Job references are
pseudonyms consistent within one ZIP. In original mode, log tails are copied as
captured and job/error diagnostics retain their identifiers and text. Archive
entry names use generated references in both modes.

Cassini does not collect media, transcript/summary documents, questions, tags,
speaker-edit files, raw configuration or database files. Sensitive information
from those sources can still appear in original logs and error messages.

`manifest.json` lists each source, capture time, limits and omissions. A source
can be unavailable, expired or truncated while the rest of the ZIP remains useful.
The bundle is a snapshot across independent sources; checks can be stale and
sharing policy may have changed since publication. Downloading it runs no repair,
recording test, model load or archive-wide scan. A received-share check can make
one bounded, read-only request to Nextcloud as the downloading user.

Collection has a 15-second budget and the ZIP contains at most 20 MiB of
uncompressed entries. Duplicate downloads for one user are refused while an
export is in progress; at most two exports run at once.

## Diagnostic storage and retention

Starting with this feature, Cassini keeps a private structured diagnostic journal
in `<work-root>/diagnostics/`, as two streams. `events.jsonl` holds job state,
delivery and share events. `operator.jsonl` holds operator log text, without the
per-request access lines, which still go to the container log. Each stream keeps an
active and a previous generation of at most 1 MiB each, so busy operator output can
never rotate share evidence away. Together they use at most 4 MiB, appear under
**Logs** in storage accounting and follow the Logs age policy. Rotation can
discard older events in a stream sooner. Diagnostic write failures are
reported in the bundle and do not fail recording jobs. The journal retains
original operator log text and share error/recipient details locally so users can
choose original or scrubbed exports later. Original detail
per event is limited to 16 KiB and oversized records are reported as capture
failures. This journal can therefore contain sensitive information. Existing
stderr and process-local share warnings from before this feature cannot be
recovered.

Stage logs still follow their existing retention policy. The exporter reads only
the four known stage files and never archives whole attempt directories, which
can also contain speaker edits. ZIPs use private temporary files, removed after
the request succeeds, fails or is cancelled.
Temporary ZIPs left by a process crash are removed on startup once 24 hours old.

## Installation and direct API use

The release must refresh the installed AppAPI route registration. New routes in
`appinfo/info.xml` are `POST support/bundle` at USER access and
`POST operator/support-bundle` at ADMIN access. Follow the usual manifest refresh
procedure in [ExApp update constraints](exapp-update-constraints.md).

A directly accessed standalone operator requires `CASSINI_OPERATOR_API_TOKEN`
and a matching bearer token for its administrator bundle, even if the other
standalone APIs are open. The self-diagnostics endpoint requires a signed-in
AppAPI user; a bearer token cannot impersonate one. The administrator endpoint
is mounted under the configured operator base path. Both endpoints accept a
small JSON object with `surface` (`browse` or `operator`),
`timezone_offset_minutes`, `online`, `catalog_state` (`not_requested`, `loading`,
`loaded` or `failed`) and `catalog_reason` (`none`, `network_error` or
`request_failed`), plus `privacy` (`original` or `scrubbed`, default `original`).
Unknown modes are rejected. They return `application/zip` with `Cache-Control: no-store`.

Nextcloud's ADMIN proxy route enforces administrator access; the AppAPI signature
authenticates the proxy and caller. No browser-supplied user or admin flag can
change the export's scope.
