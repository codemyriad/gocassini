# D-266 — Operator control panel implementation

## Scope actually delivered

This unit of work implemented the narrow first cut shaped for D-266:

- start a job from the browser
- stop an eligible running job
- observe jobs history and selected-job detail
- keep history/detail live through operator-owned SSE updates

It did **not** attempt to finish the full original Linear wish-list. That remaining work is listed in `./followup.md`.

## Delivered outcome

A new browser UI now exists at:

- `cassini-control-panel/`

It provides:

- a jobs history list from `GET /jobs`
- selected-job detail from `GET /jobs/:id`
- meeting URL trigger via `POST /jobs?provider=nextcloud-talk`
- stop for eligible `record/running` jobs via `POST /jobs/:id/stop`
- live updates via `GET /events`
- reconnect-aware snapshot refresh plus polling fallback while disconnected

## Final architecture

### 1. New control-panel app

A standalone Svelte/Vite app was added under `cassini-control-panel/`, following `cassini-viewer` conventions.

Key files:

- `cassini-control-panel/src/App.svelte`
- `cassini-control-panel/src/operator/client.ts`
- `cassini-control-panel/src/operator/config.ts`
- `cassini-control-panel/src/operator/types.ts`
- `cassini-control-panel/vite.config.ts`
- `cassini-control-panel/README.md`

### 2. Operator-owned read/write boundary

The browser does not read SQLite, work roots, or published-site storage directly.
It talks only to `cassini-operator`.

Used endpoints:

- `GET /jobs`
- `GET /jobs/:id`
- `POST /jobs?provider=nextcloud-talk`
- `POST /jobs/:id/stop`
- `GET /events`

### 3. Live updates in operator

`cassini-operator` was extended with:

- an in-process event hub
- an SSE endpoint at `GET /events`
- event emission after successful persisted job/attempt state writes

Key files:

- `cassini-operator/internal/operator/events.go`
- `cassini-operator/internal/operator/run.go`
- `cassini-operator/internal/operator/attempt_store.go`
- `cassini-operator/internal/operator/build_store.go`
- `cassini-operator/internal/operator/publish_store.go`

Current event types:

- `job.created`
- `job.updated`
- `attempt.updated`

Payloads reuse current `Job` / `JobAttempt` shapes.

### 4. Same-origin proxy model instead of operator CORS

The final implementation moved away from direct browser-to-operator cross-origin calls.

Final serving model:

- browser calls a same-origin operator base path, default `/operator`
- Vite dev/preview proxies that path to `CASSINI_OPERATOR_URL`
- browser config uses `CASSINI_OPERATOR_BASE_PATH`
- operator no longer needs to emit browser CORS headers

This is the main post-slice hardening change relative to the original shaping notes.

## UI behavior delivered

### Jobs history

The control panel renders a newest-first run history list showing:

- job id
- high-level stage/state
- current attempt number
- request URL
- updated timestamp
- top-level error summary when present

### Selected-job detail

The selected run view shows:

- provider
- meeting URL
- current attempt number
- rerun count
- created / updated / completed / interrupted timestamps
- stop metadata when present
- top-level error when present
- attempt history
- attempt artifact paths and completion/error state

### Trigger / stop controls

Delivered controls:

- Start job from meeting URL input
- Stop selected job only when the selected logical job is `record / running`

### Live behavior

The panel now:

- bootstraps from snapshots
- opens one SSE stream
- updates history and selected-job state from incoming events
- shows stream connection state
- refreshes snapshots on stream reconnect
- falls back to polling while disconnected if the selected job is active

## Slice-by-slice delivery history

### Slice 1

Committed as `7472809`

Delivered:

- new `cassini-control-panel/` scaffold
- snapshot-driven jobs history
- snapshot-driven selected-job detail
- initial config wiring

### Slice 2

Committed as `f4ced91`

Delivered:

- start job action
- stop job action
- selected-job eligibility handling for stop
- polling-based active-job refresh before SSE landed

### Slice 3

Committed as `cafb80d`

Delivered:

- operator SSE event hub
- `GET /events`
- live list/detail updates from one event stream
- operator tests for event emission / streaming

### Proxy / CORS hardening

Committed as `03853d1`

Delivered:

- same-origin proxy boundary for browser traffic
- removal of operator CORS middleware
- Vite dev/preview proxy support
- Playwright validation that the panel reaches `connected`

## Verification performed

Implementation was validated with:

- `cd cassini-control-panel && npm run build`
- `cd cassini-operator && go test ./...`

Proxy/connectivity validation also confirmed:

- `http://localhost:5174/operator/jobs` proxies successfully
- `http://localhost:5174/operator/events` streams successfully
- Playwright inspection reached panel state `connected`

## Important implementation notes

- The implemented v1 is intentionally narrow: start + stop + observe.
- The control panel uses the existing `nextcloud-talk` provider path only.
- The UI is functional, not polished.
- The operator event feed is persisted-state driven; it does not expose richer internal substages.
- The final config/runtime model differs from the original framing note that foregrounded browser-side `CASSINI_OPERATOR_URL`: the browser now targets a same-origin base path, while `CASSINI_OPERATOR_URL` remains the upstream proxy target for dev/preview serving.

## Remaining scope

See `./followup.md` for the work that was still desired by the Linear ticket but was not completed in this unit of work.
