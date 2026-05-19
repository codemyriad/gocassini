# Control panel

The control panel is the browser UI for operating the Cassini operator.

It is the runtime control surface for:

- starting jobs
- stopping a live recording
- rerunning a terminal job
- watching job history
- inspecting preserved attempt history
- staying in sync through operator SSE events

It is **not** the meeting playback UI. That role belongs to the viewer.

## Strict backend boundary

The control panel talks only to the operator HTTP API.

It does not read:

- SQLite directly
- operator work-root files directly
- published-site files directly

This boundary is deliberate.

The control panel is a control-plane client, not a storage browser.

## Transport model

### Snapshot reads

The panel bootstraps from operator snapshots:

- `GET /jobs` for the history list
- `GET /jobs/:id` for selected-job detail

### Live updates

The panel opens one `EventSource` stream against:

- `GET /events`

It listens for:

- `job.created`
- `job.updated`
- `attempt.updated`

Incoming events are used to update:

- the jobs history list
- the currently selected job detail

### Reconnect and polling behavior

The panel tracks its own stream transport state separately from job state.

Current stream states are:

- `idle`
- `connecting`
- `connected`
- `reconnecting`
- `disconnected`

Behavior:

- on initial stream open, the panel refreshes snapshots again
- on reconnect, it refreshes snapshots again
- if the selected job is still active and the stream is not connected, the panel falls back to periodic polling

So the model is:

- snapshots for truth
- SSE for liveness
- polling as degraded-mode fallback

### Same-origin operator path

The browser talks to a same-origin operator base path, not directly to the operator origin.

Examples:

- `/`
- `/operator`
- `/api/operator`

The packaged deployment currently defaults this path to `/`, so the browser calls `/jobs`, `/events`, and `/jobs/:id` on the control-panel origin.

In development, preview, and deployment packaging, proxying is used so the browser remains same-origin with the UI host.

This means:

- the browser does not need direct CORS access to the operator
- the operator does not need browser-oriented CORS behavior for the control panel

## Main UI areas

### 1. Trigger form

The top form starts a new Nextcloud Talk job.

Current UI behavior:

- collects only a meeting URL
- sends `POST /jobs?provider=nextcloud-talk`
- sends a minimal body with `platform` and `url`

Important current limitation:

- the operator API supports `guestName`, `duration`, `stopWhenRoomEmpty`, and `roomEmptyGrace`
- the current control panel does **not** expose those advanced request fields yet

### 2. Jobs history

The history list is the logical-job view, not the attempt-history view.

It is driven from `GET /jobs` and surfaces, per row:

- job id
- current stage/state
- current attempt number
- meeting URL from stored request JSON
- updated timestamp
- top-level error when present

Jobs are shown newest first.

### 3. Selected job detail

The detail panel is the summary-plus-history view.

It shows:

- logical job id
- provider
- meeting URL
- current stage/state
- current attempt number
- rerun count
- created / updated / completed / interrupted timestamps
- top-level stop reason and stop-request time when present
- top-level error when present
- attempt history

### 4. Attempt history

The panel shows attempts newest first.

The current UI renders, per attempt:

- attempt number
- trigger kind (`initial` or `rerun`)
- stage/state
- queued timestamp field currently wired to `record_queued_at`
- completed timestamp
- run artifact path
- meeting artifact path
- attempt-level error when present

Important current limitation:

- the API also carries attempt `artifact_site_path` and stage log paths
- the current UI does **not** render those yet
- the API also carries richer per-stage timestamps than the current attempt card shows

So the detail surface is useful, but still intentionally narrow.

## Actions

### Start job

Enabled when a meeting URL is present.

Effect:

- sends `POST /jobs?provider=nextcloud-talk`
- refreshes snapshots after success
- selects the new job

### Stop job

Enabled only when the selected logical job summary is:

- `record/running`

Effect:

- sends `POST /jobs/:id/stop`
- refreshes snapshots after success

Important meaning:

- this stops the live recording subprocess
- it is not a whole-job cancellation action
- if the recorder finalizes a usable `.run`, the job may still continue into build and publish

### Rerun job

Enabled only when the selected logical job summary is terminal and has a persisted run artifact path.

Effect:

- sends `POST /jobs/:id/rerun`
- refreshes snapshots after success

Important nuance:

- the button uses the summary-row artifact pointer as the quick eligibility check
- the operator still enforces the authoritative rule: rerun requires a canonical ready `.run`
- current reruns are downstream-only and do not re-record the meeting

### What the panel is for

Use the control panel when you need to:

- create operator-managed work
- understand where a job is in the pipeline
- see whether a stop was requested
- inspect whether a rerun created a new attempt
- compare the logical job summary against preserved attempts

Do not use it as:

- a playback UI
- a log browser
- a published-site browser
- a direct filesystem inspector

### Current scope and limitations

The current panel is intentionally functional rather than exhaustive.

It currently does **not** provide:

- advanced job-creation fields
- direct links into attempt log files
- direct links into attempt `.site` outputs
- delete/archive/prune actions
- authentication or multi-user workflow features

Its main job is to make the operator's start/stop/rerun/read surfaces usable from the browser.
