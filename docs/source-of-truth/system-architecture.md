# System architecture

Cassini is a file-driven meeting system with two operating layers:

- a **product layer** centered on the `cassini` CLI
- an **operator layer** that runs the same pipeline as a long-lived service

The core architectural contract is a chain of durable artifacts.

```text
Nextcloud Talk room
  -> .run bundle
  -> .meeting bundle
  -> .site bundle
  -> viewer site
```

Portable `.opus` output is not a separate recording pipeline. It is a packaging form layered over the same capture/build flow.

## The main components

| Component | Owns | Notes |
|---|---|---|
| `cassini` CLI | user-facing command surface | Exposes `record`, `build`, `publish`, `doctor`, `inspect`, `serve`, and `operator start`. |
| `cassini-go-recorder/` | live Talk capture, `.run`, `.meeting`, portable packing | Live capture and transcription/build logic live here. |
| `cassini-publisher/` plus viewer export scripts | static-site export | Turns ready meetings into a viewer site. |
| `cassini-operator/` | orchestration, persistence, scheduling | Runs the CLI pipeline asynchronously with jobs and attempts. |
| `cassini-control-panel/` | operator UI | Browser control surface for jobs and attempts. |
| `cassini-viewer/` | read-only meeting consumption UI | Browser playback/transcript UI over static files. |
| `deployment/` | packaged runtime topology | Composes operator, control panel, and viewer around shared storage. |

## Architecture by responsibility

### 1. CLI and artifact production

The `cassini` CLI owns the standalone user flow.

It is responsible for:

- producing `.run` bundles from live meetings
- producing `.meeting` bundles from `.run` or `.mkv`
- packing `.opus` files
- publishing `.site` bundles
- serving and inspecting outputs locally

The CLI is therefore the primary artifact producer.

### 2. Recorder/build implementation

The recording and build implementation owns:

- joining Nextcloud Talk rooms
- capturing reusable media
- finalizing `.run` bundles
- running speech-to-text
- generating readable transcript and summary sidecars when configured
- finalizing `.meeting` bundles
- packing portable `.opus` files

This is the system's media-processing core.

Important boundary:

- the operator does **not** duplicate this logic
- it shells out to the CLI instead

### 3. Publish/export implementation

Publish is the bridge from built artifacts to browser delivery.

It owns:

- staging ready `.meeting` bundles
- exporting a viewer shell plus meeting library
- writing `catalog.json`
- producing a ready `.site` bundle

It does **not** own capture, transcription, or scheduling.

### 4. Operator orchestration

The operator turns the CLI flow into a service.

It owns:

- job admission
- attempt history
- SQLite persistence
- stage-specific concurrency rules
- live stop control for recording
- downstream reruns
- SSE event streaming
- promotion of successful publish attempts into the live shared site

It is a control-plane service, not a media-processing service.

### 5. Browser surfaces

Cassini has two separate browser-facing applications.

### Control panel

- talks only to the operator API
- starts, stops, and reruns jobs
- shows job and attempt state
- is operational, not end-user playback UI

### Viewer

- reads only static published files
- loads either artifact directories or portable `.opus` files
- plays audio and renders transcripts
- does not talk to the operator

That separation is deliberate.

## Core boundaries

## Durable artifacts are the primary contracts

The system communicates across stage boundaries through files and manifests.

Consequences:

- failures leave inspectable partial outputs
- later stages can be rerun from preserved artifacts
- deployment can stay mostly static-file-based

## The operator is an orchestrator, not a second implementation

The operator owns runtime control, but not record/build/publish internals.

It reuses the CLI for:

- `cassini doctor --target record`
- `cassini record`
- `cassini build`
- `cassini publish`

Consequences:

- one source of truth for artifact production
- easier parity between standalone and operator-managed runs
- cleaner separation between control-plane code and media-processing code

## The control panel and viewer are intentionally separate

The control panel is about **operating** jobs.
The viewer is about **consuming** published meetings.

They do not share a backend contract.

- control panel -> operator HTTP API
- viewer -> static files only

## Standalone flow vs operator-managed flow

### Standalone CLI flow

```text
cassini record --out demo.run
cassini build demo.run --out demo.meeting
cassini publish ./meetings --out site
cassini serve ./site
```

In this mode:

- the caller manages input/output paths directly
- each stage writes the next artifact in place
- publish expects a fresh empty output directory

### Operator-managed flow

```text
HTTP create job
  -> operator record stage
  -> attempt-local .run
  -> canonical current/<job>.run
  -> attempt-local .meeting
  -> canonical current/<job>.meeting
  -> attempt-local .site
  -> promoted live site root
```

In this mode:

- the operator owns the work root and site root
- job/attempt state lives in SQLite
- publish always exports from the operator's canonical `current/` library
- the live site is replaced only after a successful publish attempt

## Why the current operator layout looks the way it does

The current operator architecture is easiest to understand as a stack of capability layers:

1. **asynchronous job admission and persistence**
2. **real live Nextcloud Talk recording**
3. **browser control panel plus SSE state updates**
4. **attempt history and rerun support**
5. **downstream-only reruns from canonical preserved capture**
6. **retained attempt-local `.site` bundles plus safe live-site promotion**
7. **deployment packaging around shared published-site storage**

That layering explains several current design choices:

- why jobs and attempts are separate
- why reruns start at build instead of re-recording
- why `current/` and `runs/` both exist
- why publish is serialized
- why the live site lives under a shared parent directory instead of being written in place by `cassini publish`

## Artifact libraries, not just single artifacts

Cassini can operate on one meeting at a time, but publish is library-oriented.

That matters in operator mode.

The operator's canonical `current/` directory is a small artifact library containing:

- one canonical `.run` per job
- one canonical `.meeting` per job

Publish points `cassini publish` at `current/` and relies on the exporter to stage only ready `.meeting` bundles.

So each successful operator publish rebuilds the full current meeting library, not just the most recently completed meeting.

## Storage architecture

### Standalone artifacts

Standalone CLI use can create any of these directly:

- `.run`
- `.meeting`
- `.site`
- `.opus`

### Operator-owned storage

The operator derives two roots from `work-root`:

### `current/`

Canonical reusable artifacts per logical job:

- `current/<job-id>.run`
- `current/<job-id>.meeting`

These are the stable inputs for downstream reruns and full-site publish.

### `runs/`

Attempt-local retained outputs and logs:

- `runs/<job-id>--attempt-001.run`
- `runs/<job-id>--attempt-002.meeting`
- `runs/<job-id>--attempt-002.site`
- `runs/<job-id>--attempt-002.logs/`

This split is central to the current architecture.

It lets Cassini preserve attempt history while keeping one canonical current artifact set per logical job.

### Published site storage

The live site is separate from `work-root`.

That separate root exists because:

- publish writes into fresh empty directories
- the live viewer site must stay readable while a new publish is prepared
- operator-managed deployment needs promotion and rollback behavior around the live root

## Data-model architecture

The operator persists two related views of work.

#### Jobs summary row

One row per logical job.

Use it for:

- history lists
- current or winning stage/state
- canonical artifact pointers
- the live site pointer

#### Job attempts row

One row per execution attempt.

Use it for:

- preserved failure history
- rerun history
- attempt-local artifact paths
- attempt-local log paths
- stop metadata for that specific attempt

The summary row is not separate from attempt history; it is the projected current view over it.

## What the architecture optimizes for

The current design optimizes for:

- recoverable file-driven pipelines
- explicit inspection after failure
- safe operator reruns without rejoining old meetings
- simple deployment with static published output
- clear separation between control-plane UI and playback UI
