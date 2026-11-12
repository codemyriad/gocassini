# Lifecycle events (webhooks)

Cassini can POST signed JSON events to one HTTP receiver when a recording moves
through its lifecycle. This is the integration surface for automation: post a
summary to a channel, notify an incident tool, update a wiki, or start a
downstream process without polling the operator or parsing the `.opus`
portable meeting.

Events are opt-in and configured in the operator's persisted settings
(`settings.json`), not in AppAPI's creation-time deploy environment. Enabling
events, rotating the secret or changing the allow-list therefore does not
require recreating the ExApp.

## Configuration

The four values are part of `PUT /settings` (and `GET /settings`), under these
JSON names:

| Field | Required | Meaning |
|---|---|---|
| `event_webhook_url` | To enable events | Absolute `http` or `https` receiver URL. Empty disables delivery. |
| `event_webhook_secret` | With the URL | HMAC-SHA256 secret used to sign every request. Write-only: responses never return it and report `event_webhook_secret_set` instead. A `PUT` that omits it keeps the stored secret. |
| `event_webhook_events` | No | Array allow-list, e.g. `["summary.completed", "recording.failed"]`. Empty delivers the whole catalog. Unknown names and blank entries are rejected. |
| `event_webhook_timeout` | No | Go duration per request, e.g. `10s`. Default `10s`; at most one minute. |

There is no dedicated webhook card in the Operator UI yet; use the settings API
or edit `settings.json` while the operator is stopped. The startup log names
only the receiver's scheme and host, never its path or query string, so a token
embedded in the URL is not logged.

A URL without a secret is refused: every event must be verifiable. `PUT
/settings` answers `400` for any invalid webhook value. Invalid values found in
`settings.json` turn delivery off and are logged; every other setting in the
file still applies. The values stay in the file as written, `GET /settings`
reports the reason as `event_webhook_error`, and saving other settings does
not touch them. Events already queued are kept: fix the values, in the file or
with a `PUT` that sets them, and delivery resumes.

Events are bound to the receiver they were queued for. Saving a different
`event_webhook_url`, or clearing it, closes whatever is still undelivered for
the old receiver
(`last_error` = `receiver changed`); nothing queued for one URL is ever sent to
another. Changing only the secret, allow-list or timeout keeps the queue.

## Event catalog

| Event | When it is emitted | Event-specific fields |
|---|---|---|
| `recording.started` | Talk's recording request was accepted and the recorder started. Sent before Talk has answered with the room's name, so `title` and `room_name` may be missing; every later event carries them. | `started_at` |
| `recording.stopped` | The recording stopped and the capture was safely stored. | `stopped_at`, `stop_reason` |
| `build.completed` | Transcription/build of a recording completed and the meeting was handed to seal. | `meeting_title` |
| `summary.completed` | A summary was written: a normal build's summary, one written or regenerated on request, or a speaker-edit rewrite. A speaker edit that keeps the existing summary emits nothing. | `summary_markdown`, `summary_model` (not on a speaker-edit rewrite) |
| `summary.failed` | A summary could not be written: the summary step of a build failed, or an attempt that only writes a summary failed at any stage. The recording itself is unaffected. Yielding for a recording or resource pressure is not a failure. | `phase`, `error`, `summary_reason` (a build's summary step only), `interrupted` |
| `recording.published` | A recording's `.opus` was delivered and the job succeeded. | `published_location` |
| `recording.failed` | Recording, build, seal or publish of a recording stopped without publishing: a failure, a build parked for lack of resources, or an operator restart that interrupted it. | `phase`, `error`, `interrupted` (restarts and shutdowns only) |
| `shares.completed` | A recording's publication finished and a share outcome is recorded; emitted again when a later Re-share changes that outcome. | `sharing`, `reconciled` (re-shares only) |

`shares.completed` includes the same outcome the Operator shows:

```json
{
  "status": "complete",
  "audience_count": 4,
  "skipped": [],
  "deferred": []
}
```

`recording.failed` carries `phase` equal to `record`, `build`, `seal` or
`publish`. A build deferred for temporary RAM/VRAM pressure is retried on its
own and emits nothing. A build that is still short of resources after its last
retry, or that can never run on this host, is parked: `recording.failed`
arrives with `state` equal to `blocked`. Nothing was lost — the capture is
kept, and an administrator can rerun the job, which then emits
`recording.published` like any other. Until someone does, nothing is
published, which is why a receiver hears about it.

`build.completed`, `recording.published` and `shares.completed` announce a
recording: its first build, or a full rerun. Regenerating a summary or editing
speakers rebuilds and republishes a meeting that is already out, and emits
none of the three, so a "new meeting" automation does not fire again for every
edit. A summary written that way still emits `summary.completed`.

`summary.failed` carries `phase` equal to `summary`, or to `seal` or `publish`
when an attempt that only writes a summary got that far. Such an attempt, and a
speaker-edit attempt, work on a copy of a meeting that is already published, so
neither ever emits `recording.failed`; a failed speaker-edit attempt emits
nothing.

A failure event carries `interrupted: true` when the stage did not fail on its
own but was cut short by the operator: stopped for an upgrade while the stage
was running, or found unfinished at the next start after a crash. The job needs
a rerun either way; the flag tells a restart from a fault in the recording.

Secondary features — speaker edits, annotations/tags, insights, imports and
model installs — are not in this catalog yet. The transport and payload shape
are designed so the catalog can grow without breaking receivers.

## Envelope

Every POST has the same envelope:

```json
{
  "id": "evt_01K2...",
  "type": "summary.completed",
  "at": "2026-11-11T14:03:21.000000000Z",
  "cassini_version": "0.4.0-alpha.2",
  "data": {
    "job_id": "01K2...",
    "attempt_number": 1,
    "stage": "seal",
    "state": "queued",
    "title": "Weekly operations sync",
    "room_name": "Weekly operations sync",
    "started_at": "2026-11-11T13:00:02.000000000Z",
    "duration_seconds": 1874,
    "record_started_at": "2026-11-11T13:00:02.000000000Z",
    "record_finished_at": "2026-11-11T13:31:16.000000000Z",
    "summary_model": "unsloth/Qwen3.8",
    "summary_markdown": "# Summary\n\n..."
  }
}
```

Common `data` fields where known: `job_id`, `attempt_number`, `stage`,
`state`, `title`, `room_name`, `started_at`, `duration_seconds`,
`record_started_at`, `record_finished_at`.

`stage` and `state` are the job's at the moment the event was written, so they
differ by event: `summary.completed` and `build.completed` from a build say
`seal`/`queued`, `recording.published` says `done`/`succeeded`, and a failure
says `failed` or `interrupted`. To learn where something failed, read `phase`,
not `stage`. `title` is the
published meeting's title, and before publication the title the meeting will
be published with. Every time is UTC, RFC 3339, with nanoseconds.

The summary body is the `summary.md` attachment that already ships inside the
portable meeting. No audio or transcript text is sent. The full summary can be
large; a receiver that posts to a chat channel should usually send an excerpt
or a link, not the whole document.

## Signature and headers

Headers on every request:

| Header | Value |
|---|---|
| `X-Cassini-Event` | Event type, e.g. `summary.completed` |
| `X-Cassini-Delivery` | Event id, unique per event; use it to deduplicate |
| `X-Cassini-Timestamp` | Unix seconds when the request was signed |
| `X-Cassini-Signature` | `sha256=<hex>` |
| `User-Agent` | `Cassini/<version>` |

The signature is:

```text
hex(HMAC-SHA256(secret, timestamp + "\n" + raw_request_body))
```

Verify the signature **before** parsing the JSON. In Go:

```go
mac := hmac.New(sha256.New, []byte(secret))
mac.Write([]byte(timestamp))
mac.Write([]byte("\n"))
mac.Write(body)
expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
if !hmac.Equal([]byte(expected), []byte(header)) {
    http.Error(w, "bad signature", http.StatusUnauthorized)
    return
}
```

A receiver can reject timestamps outside a short window (for example five
minutes) to limit replay. Delivery ids are stable across retries, so a
receiver should deduplicate by `X-Cassini-Delivery`.

## Delivery and retries

- Delivery is asynchronous. Recording, build, seal and publish never wait on
  the receiver.
- Events are first written to a SQLite outbox, so a receiver outage or an
  operator restart does not lose a lifecycle event.
- `2xx` marks delivered.
- Redirects are not followed. A `3xx` is a failed attempt: point
  `event_webhook_url` at the final address.
- Everything else — any `3xx`, `4xx` or `5xx`, a timeout, a connection error —
  is retried with exponential backoff, at most five minutes apart, until the
  event is 24 hours old. After that it is marked failed. A `401` or `404` is
  retried too: it usually means the receiver is mid-deploy or its secret is
  being rotated.
- Delivery is **at-least-once**: a crash between the POST and the database
  update redelivers the same delivery id.
- Delivery is best-effort relative to the recording: a failed webhook never
  changes the recording, publication or share result.

Delivery history lives in the `event_outbox` table. Delivered and failed rows
are kept for seven days as evidence of what was sent, then deleted, whether or
not a receiver is still configured.

## Windmill example

1. Create a Windmill webhook trigger and set its URL as `event_webhook_url`
   through `PUT /settings`.
2. In the trigger script, verify `X-Cassini-Signature` with the shared secret.
3. Ignore everything except `summary.completed` (or set
   `event_webhook_events: ["summary.completed"]`).
4. Post the summary excerpt and a link to the meeting to Mattermost or Talk.
5. Deduplicate on `X-Cassini-Delivery`.

The `data` object already includes the meeting title, room name, start time,
duration and the Markdown summary, so the flow does not need to call back into
Cassini. If you want a link instead, construct it from `job_id` and your
instance's Cassini viewer URL.

## Security and privacy

- The webhook receiver sees meeting titles, room names, timestamps, durations,
  failure text and (for `summary.completed`) the full summary Markdown.
- It never sees audio, raw transcripts, Nextcloud credentials or Talk tokens.
- Prefer a receiver on the same trusted network. If the receiver is remote,
  terminate TLS in front of it.
- Keep `event_webhook_secret` out of logs and client-side code; rotate it by
  updating the operator's settings, then the receiver's configuration.
- The outbox contains the same payloads. Treat the app's data volume as
  sensitive.

## Status

The outbox and retry state are visible in the operator database, but there is
no `/operator/events` admin endpoint yet. A small admin surface for the last
deliveries and a test event is a reasonable follow-up once the transport has
been used in production.
