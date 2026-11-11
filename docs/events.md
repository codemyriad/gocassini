# Lifecycle events (webhooks)

Cassini can POST signed JSON events to one HTTP receiver when a recording moves
through its lifecycle. This is the integration surface for automation: post a
summary to a channel, notify an incident tool, update a wiki, or start a
downstream process without polling the operator or parsing the `.opus`
portable meeting.

Events are opt-in and configured at deploy time with environment variables.
There is no UI setting yet.

## Configuration

| Variable | Required | Meaning |
|---|---|---|
| `CASSINI_EVENT_WEBHOOK_URL` | To enable events | Receiver URL. Empty disables delivery. |
| `CASSINI_EVENT_WEBHOOK_SECRET` | With the URL | HMAC-SHA256 secret used to sign every request. |
| `CASSINI_EVENT_WEBHOOK_EVENTS` | No | Comma-separated allow-list, e.g. `summary.completed,recording.failed`. Empty delivers the whole catalog. |
| `CASSINI_EVENT_WEBHOOK_TIMEOUT` | No | Per-request timeout, e.g. `10s`. Default `10s`; at most one minute. |

These are declared in `appinfo/info.xml`, so AppAPI passes them when they are
supplied through `--env` or the External Apps admin UI. Because AppAPI deploy
env is creation-time configuration, adding or changing them means recreating
the ExApp with its persistent volume preserved; see
[Updating deploy options](./exapp-install.md#updating-deploy-options-after-install).

A URL without a secret is refused and logged: every event must be verifiable.

## Event catalog

| Event | When it is emitted | Event-specific fields |
|---|---|---|
| `recording.started` | Talk's recording request was accepted and the recorder started. | `started_at` |
| `recording.stopped` | The recording stopped and the capture was safely stored. | `stopped_at`, `stop_reason` |
| `build.completed` | Transcription/build completed and the meeting was handed to seal. | `meeting_title` |
| `summary.completed` | A summary was written. | `summary_markdown`, `summary_model`, `summary_words` |
| `summary.failed` | A summary attempt failed. | `phase`, `error` |
| `recording.published` | The `.opus` was delivered and the job succeeded. | `published_location` |
| `recording.failed` | Recording, build, seal or publish reached a terminal failure. | `phase`, `error` |
| `shares.completed` | Publication finished and a share outcome is recorded. | `sharing` |

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
`publish`. A deferred build (temporary RAM/VRAM pressure) is not a failure and
does not emit `recording.failed`.

Secondary features — speaker edits, annotations/tags, insights, imports and
model installs — are not in this catalog yet. The transport and payload shape
are designed so the catalog can grow without breaking receivers.

## Envelope

Every POST has the same envelope:

```json
{
  "id": "evt_01K2...",
  "type": "summary.completed",
  "at": "2026-11-11T14:03:21Z",
  "cassini_version": "0.4.0-alpha.2",
  "data": {
    "job_id": "01K2...",
    "attempt_number": 1,
    "stage": "done",
    "state": "succeeded",
    "title": "Weekly operations sync",
    "room_name": "Weekly operations sync",
    "started_at": "2026-11-11T13:00:00Z",
    "duration_seconds": 1874,
    "record_started_at": "2026-11-11T13:00:02Z",
    "record_finished_at": "2026-11-11T13:31:16Z",
    "summary_model": "unsloth/Qwen3.8",
    "summary_words": 431,
    "summary_markdown": "# Summary\n\n..."
  }
}
```

Common `data` fields where known: `job_id`, `attempt_number`, `stage`,
`state`, `title`, `room_name`, `started_at`, `duration_seconds`,
`record_started_at`, `record_finished_at`.

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
- `4xx` (except `408` and `429`) is permanent and stops retrying.
- `5xx`, timeouts, `408` and `429` retry with exponential backoff, capped at
  ten attempts.
- Delivery is **at-least-once**: a crash between the POST and the database
  update redelivers the same delivery id.
- Delivery is best-effort relative to the recording: a failed webhook never
  changes the recording, publication or share result.

Delivery history lives in the `event_outbox` table. Delivered and permanently
failed rows are retained as operational evidence; they are not currently
pruned automatically.

## Windmill example

1. Create a Windmill webhook trigger and use its URL as
   `CASSINI_EVENT_WEBHOOK_URL`.
2. In the trigger script, verify `X-Cassini-Signature` with the shared secret.
3. Ignore everything except `summary.completed` (or use
   `CASSINI_EVENT_WEBHOOK_EVENTS=summary.completed`).
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
- Keep `CASSINI_EVENT_WEBHOOK_SECRET` out of logs and client-side code; rotate
  it by redeploying the ExApp.
- The outbox contains the same payloads. Treat the app's data volume as
  sensitive.

## Status

The outbox and retry state are visible in the operator database, but there is
no `/operator/events` admin endpoint yet. A small admin surface for the last
deliveries and a test event is a reasonable follow-up once the transport has
been used in production.
