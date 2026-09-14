# Operator API reference

This page covers the operator’s HTTP API and SSE event stream.

The operator API is primarily an operational backend surface.

In normal browser use:

- the control panel calls this API
- the viewer does not

See also:

- [Control panel](../components/control-panel.md)
- [Operator stack](../operator-stack.md)

## Create a job

```http
POST /jobs?provider=nextcloud-talk
Content-Type: application/json

{
  "platform": "nextcloud-talk",
  "url": "https://...",
  "guestName": "CassiniRecorder",
  "duration": 120,
  "stopWhenRoomEmpty": true,
  "roomEmptyGrace": 30
}
```

Behavior:

- accepts only `provider=nextcloud-talk`
- requires `platform = nextcloud-talk`
- requires `url`
- supports optional `guestName`, `duration`, `stopWhenRoomEmpty`, and `roomEmptyGrace`
- normalizes defaults before persistence
- requires an available recording slot
- returns `202` on acceptance
- returns `503` when recording capacity is full
- creates **no** job row on recording-capacity rejection

## List jobs

```http
GET /jobs
```

Returns newest-first logical job summaries.

This is the summary read model, not the full attempt history view.

### Room fields

On `job`:

- `room_name` — the Talk conversation's display name as it was when the job ran

It is `null` for a non-Talk job, for a job whose room-name lookup never
completed, and for any job recorded before this field existed.

The conversation's **token** is stored alongside it but is deliberately not
returned. For a public conversation the token is also the link that joins it, so
what leaves the operator is a one-way derivation of it — the `roomId` in a
published catalog — and never the token itself.

## Get one job

```http
GET /jobs/:id
```

Returns:

- `job` — the logical summary row
- `attempts` — newest-first attempt history

Useful for:

- current stage/state
- current artifact pointers
- preserved failures and reruns
- attempt-local artifact and log paths

### Seal-stage fields

The `seal` stage adds these fields to the response.

On `job`:

- `seal_queued_at`, `seal_started_at`, `seal_finished_at`
- `artifact_opus_path` — the canonical promotion, `current/<id>.opus`
- `artifact_opus_sha256` — the SHA-256 of the sealed file

On each entry of `attempts`:

- the same five fields, where `artifact_opus_path` is that attempt’s own immutable
  sealed file, `runs/<id>--attempt-NNN.seal/<id>.opus`
- `seal_log_path` — `runs/<id>--attempt-NNN.logs/seal.log`

All of them are `null` until the stage that writes them runs.

The split is the same one every other stage uses:

```text
job row      current/<id>.opus                 what is canonical now
attempt row  runs/<id>--attempt-NNN.seal/...   what this attempt sealed
```

`artifact_opus_sha256` is written by the seal and re-checked by the publish that
follows it, so the two rows together say which exact bytes were delivered.

## Stop a job

```http
POST /jobs/:id/stop
```

Behavior:

- valid only for `record/running`
- returns `404` for unknown jobs
- returns `409` when the job is not stoppable
- returns `202` when stop is accepted or already in progress
- sends `SIGTERM` to the live `cassini record` subprocess first
- may escalate if the subprocess does not exit in time

Important meaning:

- this stops recording
- it does not automatically mean the whole job is abandoned
- if the recorder finalized a usable `.run`, the job can still continue through build and publish

## Rerun a job

```http
POST /jobs/:id/rerun
```

Behavior:

- valid only for terminal jobs
- requires a canonical ready `.run`
- creates a new attempt
- queues that attempt directly at `build/queued`
- returns `202` with the new attempt number

Current reruns are downstream-only:

- they do not re-record the meeting
- they reuse the preserved canonical `.run`
- they create fresh attempt-local `.meeting` and `.site` outputs

## Event stream

```http
GET /events
Accept: text/event-stream
```

Current event types:

- `job.created`
- `job.updated`
- `attempt.updated`

Each event carries:

- the current summary-row `job`
- the current `attempt` when available

The control panel uses this together with snapshot reads from `GET /jobs` and `GET /jobs/:id`.

## Summary of stage and state values

Stage values:

- `record`
- `build`
- `seal` — packing the portable `.opus` this attempt will publish
- `publish`
- `done`

State values:

- `queued`
- `running`
- `succeeded`
- `failed`
- `interrupted`

Typical successful initial lifecycle:

```text
record/queued
-> record/running
-> build/queued
-> build/running
-> seal/queued
-> seal/running
-> publish/queued
-> publish/running
-> done/succeeded
```

Typical rerun lifecycle:

```text
build/queued
-> build/running
-> seal/queued
-> seal/running
-> publish/queued
-> publish/running
-> done/succeeded
```

`publish/queued` is reachable only through a completed seal. A job that reaches
`done/failed` from `seal` did not produce a verifiable portable meeting, and its
`error` carries the pack failure; the retry is a rerun, exactly as it is for a
failed build or publish.

A restart mid-pipeline resolves like this:

```text
  at crash            after restart          resumes?
  ────────────────    ───────────────────    ──────────────────────────────
  record/*            record/interrupted     no  — rerun
  build/queued        build/queued           yes — requeue dispatcher
  seal/queued         seal/queued            yes — requeue dispatcher
  publish/queued      publish/queued         yes — requeue dispatcher
  build|seal|publish  <stage>/interrupted    no  — the subprocess died; rerun
    /running
```

## Insights

Ask one question of several meetings and keep the answer.

These are the operator's only **USER** routes with a body: every logged-in
Nextcloud account may call them, and each call acts only on meetings that caller
can already read. They also sit at the container root, not under the operator
base path — `/insights`, the same shape as `/published/*`, not `/operator/…`.

### Create a run

```http
POST /insights
Content-Type: application/json

{
  "meetingIds": ["..."],
  "workflow": "summarise",
  "question": "",
  "provider": "",
  "model": ""
}
```

Behavior:

- requires at least one `meetingIds` entry
- `workflow` is optional — it falls back to the configured insight template, then
  to the shipped default. `cassini insight workflows --json` lists the registry
- `question` belongs only to a workflow that has somewhere to put one, and is
  refused **both ways**: sent to a workflow that takes none it would be dropped
  without being asked, and withheld from one that needs it the prompt would go
  out with its placeholder still in it. Either mistake is a `400`. The shipped
  `ask` workflow ("Ask your own question") takes one and requires it; every
  other shipped workflow asks its own
- `provider` is optional — the id of one of the configured AI endpoints
  (`GET /operator/ai/providers` lists them). Absent, the deployment's
  configured insight endpoint answers; an unknown id is a `400`
- `model` is optional and needs `provider` (a model with no provider is a
  `400`). Absent or empty, the run asks for that endpoint's default model — the
  one set on the provider in AI providers. The app never sends one today; the
  field stays so a per-run override can return without a wire change
- returns `201` with the run

### List the caller's runs

```http
GET /insights
```

Returns `{"insights": [ ... ]}` — the runs that caller created, newest first,
and nobody else's.

### Get one run

```http
GET /insights/:id
```

Returns the run plus `document`, the finished markdown, once it has succeeded.

### Retry a failed run

```http
POST /insights/:id/retry
```

Valid only for a run in `failed`. The id is stable across attempts; each retry
increments `attemptNumber`. A retry replays the request as it was made — the
same workflow, meetings, question and the endpoint the caller picked — and
falls back to the deployment's configured endpoint only when the request named
none, or named one that has since been removed.

### The run object

```json
{ "id": "ins_0123456789abcdef", "status": "queued",
  "createdBy": "alice", "attemptNumber": 1,
  "workflowId": "summarise", "workflowVersion": "v0", "workflowSha256": "...",
  "meetingIds": ["..."], "roomIds": ["..."], "question": "",
  "requestedProvider": "", "requestedModel": "",
  "provider": "", "model": "", "documentPath": "",
  "reason": "", "error": "",
  "createdAt": "RFC3339", "updatedAt": "RFC3339" }
```

`requestedProvider` and `requestedModel` are what the caller asked for (the
create body's `provider` and `model`); `provider` and `model` are what the latest
attempt actually reached, and are empty until an attempt has resolved them.

`status` is `queued`, `running`, `succeeded` or `failed` — the same four words
the `cassini insight` CLI uses, deliberately not the job pipeline's five.

`reason` says why a `failed` run failed, as one token; it is `""` on any other
status. The operator stores only the token — the sentence a reader sees for it
is the app's, so a run that failed months ago reads in the current words. The
technical detail (the child's stderr, an HTTP status) is in the operator log,
never on the run.

| `reason` | Meaning |
|---|---|
| `bad-request` | `cassini insight run` refused the request (exit 2) |
| `no-provider` | no AI endpoint is configured (exit 3) |
| `provider-refused` | the endpoint refused the call: key, quota, model name (exit 4) |
| `model-failed` | the model did not answer (exit 5) |
| `write-failed` | the answer was produced but could not be written (exit 1) |
| `deliver-failed` | the document could not be PUT into the caller's Nextcloud files |
| `timeout` | the attempt hit the run timeout |
| `staging-failed` | the staging directory or catalog could not be written |
| `catalog-failed` | the caller's meeting list could not be read, or was empty |
| `meeting-unavailable` | a picked meeting is not readable by the caller (401/403/404) |
| `download-failed` | a recording could not be fetched from Nextcloud |
| `assemble-failed` | `cassini meetings context` could not build the bundle |
| `interrupted` | the operator shut down or restarted, or the attempt stopped, before the run finished |
| `unknown` | an exit code the contract does not name |

`error` carries the same token for one release, for an app built against the
old field. Runs that failed before tokens existed still carry a sentence there,
prefixed with one of the first four tokens.

### Status codes

| Code | When |
|---|---|
| `400` | A request that cannot be run: no meetings, too many, an unknown workflow, a malformed id. Refused before any Nextcloud call |
| `404` | A meeting, or a run, the caller may not read — indistinguishable from one that does not exist |
| `409` | A retry against a run that is not `failed` — `queued` or `running` (it is already moving), or `succeeded` (it already has an answer) |
| `502` | A scan that failed, or a request with no caller identity |

A failure is never an empty `200`.

## What the API does not try to be

The current API is not:

- a viewer content API
- a direct filesystem browser
- a durable work-queue API

It is the operator’s control and inspection surface. The one exception is
`/insights`: it is per-caller and multi-user by construction, which is why it
lives outside the ADMIN operator surface rather than beside `/jobs`.
