# Control panel

The control panel is the **Operator** section of `cassini-app`, alongside the
meeting browser in the unified Cassini app. Only administrators can open it
in Nextcloud.

It is for:

- starting jobs
- stopping a live recording
- rerunning a finished job
- watching job history
- inspecting preserved attempts

It is **not** the meeting playback UI. That role belongs to the viewer.

## Backend boundary

Job controls and settings use the operator HTTP API. Nextcloud provisioning
also calls Nextcloud APIs through the administrator's browser session when
those actions require password confirmation.

It does not read:

- SQLite directly
- operator work-root files directly
- published-site files directly

That boundary is intentional. The control panel is an operator client, not a storage browser.

## Data flow

The panel combines:

- **snapshot reads** for truth
- **Server-Sent Events** for liveness
- **polling fallback** when disconnected from the event stream

Main reads:

- `GET /jobs`
- `GET /jobs/:id`
- `GET /events`

## Same-origin model

The browser talks to a same-origin operator path such as:

- `/`
- `/operator`
- `/api/operator`

In development and deployment packaging, the UI host proxies those requests upstream to the operator.

That means the browser usually does **not** call the operator origin directly.

## Main UI areas

### Trigger form

The current top form starts a new Nextcloud Talk job.

Current UI scope:

- collects a meeting URL
- sends `POST /jobs?provider=nextcloud-talk`

Current limitation:

- the operator API supports extra fields like `guestName`, `duration`, `stopWhenRoomEmpty`, and `roomEmptyGrace`
- the current control panel does not expose those fields yet

### Jobs history

The history list shows logical jobs, not raw attempts.

Typical fields surfaced:

- job id
- current stage/state
- current attempt number
- stored meeting URL
- updated time
- top-level error when present

### Selected job detail

The detail panel shows the current logical job plus attempt history.

Typical fields shown:

- job id
- provider
- meeting URL
- current stage/state
- current attempt number
- rerun count
- timestamps
- top-level stop reason or error
- attempt list

### Attempt history

Attempts are shown newest first.

The current UI displays useful high-level attempt detail, but it is intentionally narrower than the full API payload.

Attempt details include artifact paths, stage log paths and stage timings.
These are server-side paths for diagnosis, not links that expose the operator’s
filesystem to the browser.

## Actions

### Start job

Effect:

- sends `POST /jobs?provider=nextcloud-talk`
- refreshes snapshots after success
- selects the new job

### Stop job

Enabled only when the selected job is `record/running`.

Effect:

- sends `POST /jobs/:id/stop`
- refreshes snapshots after success

Important meaning:

- this stops the live record subprocess
- it is not a general cancel-everything action
- the job may continue if a usable `.run` was finalized

### Rerun job

Enabled only when the selected logical job is terminal and has a usable canonical run artifact.

Effect:

- sends `POST /jobs/:id/rerun`
- refreshes snapshots after success

Current rerun behavior:

- reruns are downstream-only
- the operator does not re-record the meeting
- the new attempt starts from build using the preserved canonical `.run`

## Local development

Run the control panel against a local operator:

```bash
cd cassini-app
CASSINI_OPERATOR_URL=http://127.0.0.1:4000 npm run dev
```

Optional custom public base path:

```bash
export CASSINI_OPERATOR_BASE_PATH=/api/operator
```

Build and preview:

```bash
cd cassini-app
npm run build
CASSINI_OPERATOR_URL=http://127.0.0.1:4000 npm run preview
```

## What the control panel is good for

Use it when you want to:

- create operator-managed work
- understand where a job is in the pipeline
- confirm whether stop was requested
- inspect whether a rerun created a new attempt

Do not use it as:

- a playback UI
- a log browser
- a published-site browser
- a direct filesystem inspector

## See also

- [Operator stack](../operator-stack.md)
- [Operator API reference](../reference/api.md)
- [Viewer](./viewer.md)
