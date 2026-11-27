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

Administrators set the receiver in **Operator › Settings › Webhooks**: its
address, a signing secret, the events to send and how long to wait for an
answer. The panel also shows how delivery is going and can send a test event.

Behind it, `GET` and `PUT operator/settings/webhook` (administrators only, like
the rest of the operator's settings) read and write the receiver:

| Field | Required | Meaning |
|---|---|---|
| `url` | To enable events | Absolute `http` or `https` receiver URL, with a plain host (see [What people see](#what-people-see)). Empty turns delivery off and deletes the stored secret, events and `summary_notice`. |
| `secret` | With the URL | HMAC-SHA256 secret used to sign every request, at least 32 characters. Write-only: responses never return it and report `secret_set` instead. Required again whenever `url` changes. |
| `events` | With the URL | The events to send, e.g. `["summary.completed", "recording.failed"]`. At least one; unknown names and blank entries are rejected. There is no "all events" value: an event added in a later release stays off until it is added here. |
| `timeout_seconds` | No | How long one request may take, in whole seconds from `1` to `60`. Default `10`. |
| `summary_notice` | No | Why summaries are sent, shown after the [summary notice](#what-people-see). One line of plain text, at most 200 characters; spaces are trimmed and collapsed. Kept while `summary.completed` is unticked. |

A field left out of a `PUT` keeps its stored value, so a client can send back
what it read without erasing the secret. The exception is a new address: a
`PUT` that changes a saved `url` must carry a `secret` too, so a receiver at
the old address cannot sign for the new one. A secret 0.4.0 saved that is
shorter than 32 characters keeps working until it is replaced.

With an empty `url`, a `secret` or `events` in the same `PUT` are discarded
unchecked, and turning delivery on again needs both again. Only
`timeout_seconds` is kept.

0.4.0 settings are converted when the operator starts, and saved back: a
receiver with no `events` list (which then meant the whole catalog) gets the
events 0.4.0 sent, so it keeps getting what it got, and the `timeout` duration
becomes `timeout_seconds`, rounded up (`500ms` becomes `1`). Events 0.4.0
queued are still sent to the stored URL. The API no longer reads or returns
`timeout`.

Any invalid value is a `400` with `error` (an English sentence) and
`error_code`, which names the field:

| `error_code` | When |
|---|---|
| `webhook_url_invalid` | `url` is not an absolute `http` or `https` URL, or its host is not a plain one. |
| `webhook_secret_required` | `url` is set and there is no secret. |
| `webhook_secret_required_for_new_url` | `url` changes a saved address to another one and the body has no `secret`. |
| `webhook_secret_too_short` | `secret` is shorter than 32 characters. |
| `webhook_events_required` | `url` is set and `events` is empty. |
| `webhook_event_unknown` | An entry in `events` is blank or not in the catalog. |
| `webhook_timeout_invalid` | `timeout_seconds` is not a whole number from 1 to 60. |
| `webhook_summary_notice_invalid` | `summary_notice` has a line break, a control character, a direction override or isolate (U+202A–202E, U+2066–2069), or an invisible space (U+200B, U+2060–2064, U+FEFF). |
| `webhook_summary_notice_mention` | A word in `summary_notice` starts with `@`, which Talk would turn into a mention. |
| `webhook_summary_notice_too_long` | `summary_notice` is longer than 200 characters. |

A body that is not valid JSON, or has a field not listed above, is a plain
`400` with `error: "invalid request body"`.

```http
PUT operator/settings/webhook
{"url": "https://flows.example/hooks/cassini", "secret": "…", "events": ["summary.completed"]}

200 OK
{
  "url": "https://flows.example/hooks/cassini",
  "secret_set": true,
  "events": ["summary.completed"],
  "timeout_seconds": 10,
  "summary_notice": "",
  "catalog": ["recording.started", "recording.stopped", "recording.published",
              "recording.failed", "summary.completed", "summary.failed"],
  "delivery": {
    "last_delivered_at": "2026-11-17T09:08:11.000000000Z",
    "delivered": 38,
    "waiting": 0,
    "failed": 0,
    "last_error": "",
    "last_error_at": ""
  }
}
```

`GET` answers the same way. `catalog` is every event a receiver may ask for,
in the order of the [event catalog](#event-catalog); `events` comes back in
that order.

`delivery` covers the last seven days of the current `url` only, and is all
zeros when no receiver is set:

| Field | Meaning |
|---|---|
| `delivered` | Events the receiver accepted. |
| `waiting` | Events not yet accepted and still being retried, of the types `events` lists now. |
| `failed` | Events given up on after 24 hours. |
| `last_delivered_at` | When the newest delivery was accepted; empty if none. |
| `last_error` | The newest error on an event that is still `waiting`; empty if none. An event given up on counts in `failed`, but its error is not reported here. |
| `last_error_at` | When the attempt that failed with that error was made. Empty for an error recorded by an earlier release. |

The settings are stored in `event-webhook-settings.json` beside the operator's
database, readable by the app only. If that file cannot be read or is not
valid at startup, delivery is off, the reason is logged and `GET` reports it
as `error`, with `url`, `secret_set` and `events` empty, until a valid `PUT`
replaces it.

Logs name only the receiver's scheme and host, never its path or query string,
so a token embedded in the URL is not logged.

## Test event

`POST operator/settings/webhook/test` (administrators only) sends one event
to the saved receiver at once and reports what happened. It is signed and sent
exactly like a real event: the same headers, the same signature, the
configured timeout, and redirects are not followed. It is not queued, not
retried, not counted in `delivery`, and sent whatever `events` lists.

```json
{
  "id": "evt_01K2...",
  "type": "webhook.test",
  "at": "2026-11-17T09:10:02.000000000Z",
  "cassini_version": "0.4.0",
  "data": {}
}
```

`webhook.test` is not in the catalog and cannot be asked for. A receiver
should answer it with a `2xx` and ignore it.

| Response | Meaning |
|---|---|
| `200 {"status": 204, "duration_ms": 182}` | The receiver answered. Any status is reported, a `3xx`, `4xx` or `5xx` included; only a `2xx` means real events will be accepted. |
| `200 {"error": "dial tcp 10.0.0.5:443: connect: connection refused", "duration_ms": 3}` | There was no HTTP answer: the connection was refused, the name did not resolve, or nothing came back within the timeout (`no answer within 10s`). |
| `200 {"error": "address not allowed: link-local/metadata (169.254.169.254)", "error_code": "webhook_address_not_allowed", "duration_ms": 1}` | The receiver's address, or the address its name resolves to, is one Cassini never connects to (see [Security and privacy](#security-and-privacy)). |
| `409`, `error_code: webhook_not_configured` | No receiver is saved. |

The route is new in `info.xml`: an in-place deploy that keeps the same
`<version>` leaves AppAPI's routes as they were, and the test answers `404`
until the app is re-registered.

## Event catalog

| Event | When it is emitted | Event-specific fields |
|---|---|---|
| `recording.started` | Talk's recording request was accepted and the recorder started. Usually sent before Talk has answered with the room's name, so `title` and `room_name` are usually missing (see [Envelope](#envelope)). | `started_at` |
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

`data` always has `job_id` and `attempt_number`. The common fields below are
added only when Cassini knows them as the event is queued, so a receiver must
treat each of them as optional, on every event:

| Field | Value, and when it is present |
|---|---|
| `record_started_at` | When the recorder started. Present from `recording.started` on. |
| `record_finished_at` | When the recording stopped. Present from `recording.stopped` on. |
| `started_at` | When the meeting started: the start the published meeting states once it has one, otherwise the recorder's start. Present from `recording.started` on. |
| `duration_seconds` | How long the meeting runs: the length of the published audio once it is known, otherwise the time from `record_started_at` to `record_finished_at`. Present from `recording.stopped` on. |
| `room_name` | The Talk conversation's name. Cassini asks Talk for it just after the recorder starts, so it is usually missing on `recording.started`. It stays missing on every later event if Talk did not answer. |
| `title` | The published meeting's title. Before publication, the title it will be published with: the room's name, or for a one-to-one call the two participants' names. Missing while neither is known, which is usual on `recording.started`. |

The event-specific fields in the catalog are added to them. Every time is UTC,
RFC 3339, with nanoseconds.

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
- A receiver that is down is backed off as a whole: no answer, a `5xx`, a
  `429` or a `3xx` ends the delivery pass, and nothing else is sent until the
  receiver's own backoff (growing the same way) is over. Any other `4xx`
  answers that one request: the event backs off, and the pass goes on.
- A `429` or `503` with `Retry-After` (seconds or an HTTP date) is not tried
  again before that time, nor before the usual backoff, and at most 24 hours
  later.
- Redirects are not followed: point `url` at the final address.
- Delivery is **at-least-once**: a crash between the POST and the database
  update redelivers the same delivery id.
- Events can arrive **out of order**. Each event is retried on its own, so a
  retry can land after a later event (a `recording.stopped` before the
  `recording.started` that failed first), and a `4xx` on one event does not
  hold back the next. Order events by `at` (when the event happened, the same
  on every retry) and group them by `data.job_id`; do not rely on arrival
  order.
- A queued event is only ever sent to the `url` it was queued for. Changing
  the `url`, removing the receiver or unticking an event deletes the waiting
  events the receiver no longer wants; ticking an event again does not bring
  them back. An event a recording path queues while a save is under way can
  still be tagged with the old address: it is never sent to the new one, and
  is given up on after 24 hours (if that old address is saved again before
  then, it is sent there).
- Delivery is best-effort relative to the recording: a failed webhook never
  changes the recording, publication or share result.

Delivery history lives in the `event_outbox` table; a row is deleted a week
after it was written. Its payload is emptied as soon as the event is delivered
or given up on. An hourly check gives up on any event more than 24 hours and 5
minutes old, whoever it was queued for and even with delivery off, so a
payload is kept at most about 25 hours. An event is never both delivered and
given up: a delivery wins.

## Windmill example

1. Create a Windmill webhook trigger and set its URL as the receiver address
   in Operator › Settings › Webhooks (or `url` with
   `PUT operator/settings/webhook`).
2. In the trigger script, verify `X-Cassini-Signature` with the shared secret.
3. Tick only `summary.completed` (`"events": ["summary.completed"]`), and
   answer `webhook.test` with a `2xx`.
4. Post the summary excerpt to Mattermost or Talk.
5. Deduplicate on `X-Cassini-Delivery`.

The `data` object already includes the Markdown summary and, when Talk has
told Cassini the room's name, the meeting title and room name, so the flow
does not need to call back into Cassini.
Send a test event from the panel to check the address and the secret before
the first meeting.

## What people see

With `summary.completed` ticked, summaries leave the Nextcloud server, and the
people who can open a meeting are told where they went:

- **The meeting** shows a highlighted "Sent to flows.example.org" chip once
  the receiver has accepted that meeting's `summary.completed` (any `2xx`).
  Its popover says "A summary of this meeting was sent to flows.example.org."
  followed by `summary_notice`. The host and reason are the ones saved when
  the summary was queued. A refused, given-up or deleted event shows no chip;
  a summary rewritten later and not sent leaves the chip as it was.
- **Talk** gets a second, silent message right after the "Meeting recording
  available" post, when the summary is queued for the current receiver:
  "This meeting's summary will also be sent to flows.example.org." and the
  reason, in English. It goes out once, with the first post, so a summary
  requested later is sent and shows the chip but is not announced. Meetings
  shared only with attendees or moderators get no chat posts.
- **The host** is the URL's host only, never its port, path, query or
  credentials. Cassini names only plain hosts, written as a browser writes
  them: ASCII names (an international name in its `xn--` form), dotted
  decimal IPv4, compressed IPv6. Any other address is refused on save. A
  receiver saved by 0.4.0 with another address keeps receiving events, its
  summaries without a chip, until its address is replaced.
- Status events show nothing, though every event can carry the meeting's
  title and room name. Audio and transcripts are never sent. Imported and
  uploaded meetings send no events.

Before ticking `summary.completed`, consider Talk's recording consent
setting (**Administration settings › Talk › Recording consent**).

## Security and privacy

- The webhook receiver sees meeting titles, room names, timestamps, durations,
  failure text and (for `summary.completed`) the full summary Markdown.
- It never sees audio, raw transcripts, Nextcloud credentials or Talk tokens.
- Prefer a receiver on the same trusted network. If the receiver is remote,
  terminate TLS in front of it.
- Cassini never connects a webhook request, real or test, to a link-local or
  cloud-metadata address: `169.254.0.0/16`, `fe80::/10`, `fd00:ec2::254`
  (AWS), `fd20:ce::254` (GCP), `fd00:c1::a9fe:a9fe` (Oracle Cloud),
  `100.100.100.200` (Alibaba Cloud) and
  `168.63.129.16` (Azure WireServer). The check is made on the address
  actually dialled, after the name is resolved, so a name that points there is
  refused too, and an IPv4-mapped or NAT64 (`64:ff9b::/96`) address is checked
  as the IPv4 address in it.
  Private ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`) and
  loopback are allowed. When an HTTP proxy is configured through the
  environment (`HTTP_PROXY`, `HTTPS_PROXY`), Cassini connects to the proxy,
  and the check applies to the proxy's address only.
- Keep the secret out of logs and client-side code; rotate it with a `PUT`,
  then update the receiver's configuration. Requests the receiver rejects in
  between are retried. A new address always gets a new secret.
- The outbox keeps a payload only while its event waits (see
  [Delivery and retries](#delivery-and-retries)). Treat the app's data volume
  as sensitive all the same.
