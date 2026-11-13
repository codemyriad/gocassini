# Lifecycle events (webhooks)

Cassini can POST signed JSON events to one HTTP receiver when a recording moves
through its lifecycle. This is the integration surface for automation: post a
summary to a channel, notify an incident tool, update a wiki, or start a
downstream process without polling the operator or parsing the `.opus`
portable meeting.

Events are opt-in. The receiver is configured in the running app, not in
AppAPI's creation-time deploy environment, so enabling events, rotating the
secret or changing the allow-list does not require recreating the ExApp.

Events are an early feature and the contract is experimental until Cassini
1.0. Event names, payload fields and the settings endpoint may change in any
release before then, stable releases included; every change is listed in the
changelog.

## Configuration

`GET` and `PUT operator/settings/webhook` (administrators only, like the rest
of the operator's settings) read and write the receiver:

| Field | Required | Meaning |
|---|---|---|
| `url` | To enable events | Absolute `http` or `https` receiver URL. Empty turns delivery off. |
| `secret` | With the URL | HMAC-SHA256 secret used to sign every request. Write-only: responses never return it and report `secret_set` instead. |
| `events` | No | Array allow-list, e.g. `["summary.completed", "recording.failed"]`. Empty delivers the whole catalog. Unknown names and blank entries are rejected. |
| `timeout` | No | How long one request may take, as a duration such as `10s` or `30s`. Default `10s`; at most `1m`. |

A field left out of a `PUT` keeps its stored value, so a client can send back
what it read without erasing the secret. Any invalid value is a `400`.

```http
PUT operator/settings/webhook
{"url": "https://flows.example/hooks/cassini", "secret": "…", "events": ["summary.completed"]}

200 OK
{"url": "https://flows.example/hooks/cassini", "secret_set": true, "events": ["summary.completed"], "timeout": "10s"}
```

The settings are stored in `event-webhook-settings.json` beside the operator's
database, readable by the app only. If that file cannot be read or is not
valid at startup, delivery is off, the reason is logged and `GET` reports it
as `error`; no other setting is affected, and the next valid `PUT` replaces
the file. There is no webhook card in the Operator UI yet.

Logs name only the receiver's scheme and host, never its path or query string,
so a token embedded in the URL is not logged.

## Event catalog

| Event | When it is emitted | Event-specific fields |
|---|---|---|
| `recording.started` | Talk's recording request was accepted and the recorder started. Sent before Talk has answered with the room's name, so `title` and `room_name` may be missing; every later event carries them. | `started_at` |
| `recording.stopped` | The recording stopped and the capture was safely stored. | `stopped_at`, `stop_reason` |
| `recording.published` | A recording's `.opus` was delivered and the job succeeded. | none |
| `recording.failed` | Recording, build, seal or publish of a recording failed, or an operator restart interrupted it. | `phase`, `error`, `interrupted` (restarts and shutdowns only) |
| `summary.completed` | A summary was written: a normal build's summary, one written or regenerated on request, or a speaker-edit rewrite. A speaker edit that keeps the existing summary emits nothing. | `summary_markdown`, `summary_model` (not on a speaker-edit rewrite) |
| `summary.failed` | A summary could not be written: the summary step of a build failed, or an attempt that only writes a summary failed at any stage. The recording itself is unaffected. | `phase`, `error`, `interrupted` (restarts and shutdowns only) |

`recording.failed` carries `phase` equal to `record`, `build`, `seal` or
`publish`. A build that is waiting for memory or a GPU is not a failure and
emits nothing, whether it is retried on its own or parked for an administrator
to rerun: the capture is kept either way.

`recording.published` announces a recording: its first build, or a full rerun.
Regenerating a summary or editing speakers republishes a meeting that is
already out and does not emit it, so a "new meeting" automation does not fire
again for every edit. A summary written that way still emits
`summary.completed`.

`summary.failed` carries `phase` equal to `summary`, or to `seal` or `publish`
when an attempt that only writes a summary got that far. Such an attempt, and a
speaker-edit attempt, work on a copy of a meeting that is already published, so
neither ever emits `recording.failed`; a failed speaker-edit attempt emits
nothing.

A failure event carries `interrupted: true` when the stage did not fail on its
own but was cut short by the operator: stopped for an upgrade while the stage
was running, or found unfinished at the next start after a crash. The job needs
a rerun either way; the flag tells a restart from a fault in the recording.

Build completion, share outcomes and secondary features (speaker edits,
annotations, insights, imports, model installs) are not in the catalog yet.
The transport and payload shape are designed so it can grow without breaking
receivers.

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
    "title": "Weekly operations sync",
    "room_name": "Weekly operations sync",
    "record_started_at": "2026-11-11T13:00:02.000000000Z",
    "record_finished_at": "2026-11-11T13:31:16.000000000Z",
    "summary_model": "unsloth/Qwen3.8",
    "summary_markdown": "# Summary\n\n..."
  }
}
```

`data` always has `job_id` and `attempt_number`, and these common fields where
they are known: `title`, `room_name`, `record_started_at`,
`record_finished_at`, and, once the meeting has been published, `started_at`
and `duration_seconds`. The event-specific fields in the catalog are added to
them. `title` is the published meeting's title, and before publication the
title the meeting will be published with. Every time is UTC, RFC 3339, with
nanoseconds.

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
- Events are written to a SQLite outbox before they are sent, so a receiver
  outage or an operator restart does not lose an event that was queued. The
  write happens right after the state change it reports, not in the same
  transaction: a crash between the two can lose that one event.
- `2xx` marks delivered. Anything else — a `3xx`, `4xx` or `5xx`, a timeout, a
  connection error — is retried with exponential backoff, at most five minutes
  apart, until the event is 24 hours old. After that it is marked failed.
- Redirects are not followed: point `url` at the final address.
- Delivery is **at-least-once**: a crash between the POST and the database
  update redelivers the same delivery id.
- A queued event goes to whichever receiver is configured when it is sent. If
  you change `url` while events are waiting, they go to the new receiver.
  Nothing is queued while delivery is off; events that were already waiting
  when it was turned off are sent if it is turned back on within 24 hours.
- Delivery is best-effort relative to the recording: a failed webhook never
  changes the recording, publication or share result.

Delivery history lives in the `event_outbox` table. A row is deleted a week
after it was written.

## Windmill example

1. Create a Windmill webhook trigger and set its URL as `url` with
   `PUT operator/settings/webhook`.
2. In the trigger script, verify `X-Cassini-Signature` with the shared secret.
3. Ignore everything except `summary.completed` (or set
   `"events": ["summary.completed"]`).
4. Post the summary excerpt to Mattermost or Talk.
5. Deduplicate on `X-Cassini-Delivery`.

The `data` object already includes the meeting title, room name and the
Markdown summary, so the flow does not need to call back into Cassini.

## Security and privacy

- The webhook receiver sees meeting titles, room names, timestamps, durations,
  failure text and (for `summary.completed`) the full summary Markdown.
- It never sees audio, raw transcripts, Nextcloud credentials or Talk tokens.
- Prefer a receiver on the same trusted network. If the receiver is remote,
  terminate TLS in front of it.
- Keep the secret out of logs and client-side code; rotate it with a `PUT`,
  then update the receiver's configuration. Requests the receiver rejects in
  between are retried.
- The outbox contains the same payloads for a week. Treat the app's data volume
  as sensitive.

## Status

The outbox and retry state are visible in the operator database, but there is
no admin endpoint for them yet. A settings card with the last deliveries and a
test event is a planned follow-up.
