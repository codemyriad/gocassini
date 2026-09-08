# Cassini Go Recorder Architecture Overview

## Purpose

`cassini-go-recorder` is the live capture side of Cassini. Its job is to join a Nextcloud Talk room as a participant, subscribe to remote media, preserve packet-level truth during the meeting, and turn that truth into durable recording artifacts after the meeting ends.

The package is intentionally narrow:

- input: a Nextcloud Talk room URL
- live responsibility: capture signaling and media reliably
- offline responsibility: compose the final meeting output from captured artifacts
- output: a final `.mkv`, a session artifact directory, and recorder-side diagnostics

It is not the viewer, the publisher, or a scheduling/control-plane product. Post-recording transcription used to live in a separate `cassini-transcriber` Python package; the active path is now an in-tree Go pipeline under `internal/transcribe`, driven by `cassini build`. Live recording remains the heart of the package; transcription is layered on top of the recorded MKV.

## Top-Level Shape

The package is built in layers:

1. CLI entrypoints in `cmd/`
2. runtime configuration and mode selection in `internal/config` and `internal/app`
3. provider adapters for Nextcloud OCS and signaling in `internal/nextcloud` and `internal/signaling`
4. live room/session management in `internal/talk`
5. on-disk packet/session formats in `internal/cassette` and `pkg/core/{session,store}`
6. offline remux, validation, timeline analysis, and inspection in `pkg/core/*` plus `cmd/gocassini-inspect` and `cmd/gocassini-remux`
7. post-recording transcription pipeline in `internal/transcribe` (STT + readable-text cleanup + summary generation), driven by `internal/cassini` (`cassini build`)

The architectural center is the session artifact. Live capture writes packet truth first, and final outputs are derived from that truth.

## Entrypoints

The main binaries are:

- `cmd/gocassini`: main recorder CLI
- `cmd/gocassini-inspect`: inspects legacy `.csr` archives or new session artifacts
- `cmd/gocassini-remux`: rebuilds a final MKV from a recorded session artifact

`cmd/gocassini` parses flags through `internal/config/config.go`, then dispatches through `internal/app/run.go`.

There are two modes:

- `simulate`: synthetic packet generation for smoke/debug work
- `talk`: real Nextcloud Talk recording

## Main Runtime Flow

In `talk` mode the flow is:

1. Parse the call URL and derive output paths.
2. Create the session artifact directory next to the final output path.
3. Use the OCS client to validate the room, mark the recorder participant active, and fetch signaling settings.
4. Connect to the signaling websocket.
5. Join the signaling room and call context.
6. Create subscriber peer connections for remote participants.
7. For each remote track, open a packet stream in the session artifact and write RTP/RTCP records as they arrive.
8. Track participant churn, stream churn, and room-empty shutdown behavior.
9. On shutdown, close peers, flush artifacts, and compose the final MKV from the session artifact using the remux path.
10. Emit the final report and optionally clean intermediates.

Two design choices matter here:

- live capture does as little transformation as possible
- final media composition happens from persisted artifacts, not from transient in-memory state

## Key Subsystems

### 1. App and Config Layer

Files:

- `internal/config/config.go`
- `internal/app/run.go`

Responsibilities:

- parse CLI flags
- select runtime mode
- set output policy, duration limits, room-empty behavior, signaling retry behavior, and TURN policy

This layer is intentionally thin. It exists to keep the recorder runtime separate from flag parsing.

### 2. Nextcloud and Signaling Adapters

Files:

- `internal/nextcloud/*`
- `internal/signaling/client.go`

Responsibilities:

- parse Talk URLs
- call Nextcloud OCS APIs
- fetch signaling configuration
- connect to the websocket signaling server
- provide request/response and event-stream primitives to the recorder

These packages isolate provider-specific behavior from the rest of the runtime. Today the provider is explicitly Nextcloud Talk.

### 3. Live Recorder Orchestration

Files:

- `internal/talk/recorder.go`
- `internal/recorder/rtp_recorder.go`

Responsibilities:

- manage the room lifecycle
- manage subscriber peers per remote session
- react to signaling events
- capture remote tracks
- stop cleanly on timeout, cancellation, or room-empty conditions

`internal/talk/recorder.go` is the orchestration core. It knows about the room, the peers, shutdown policy, and post-run cleanup. It does not directly define the on-disk packet format; that is delegated downward.

### 4. Session Artifact Capture

Files:

- `internal/talk/session_artifact.go`
- `pkg/core/session/types.go`
- `pkg/core/store/*`

Responsibilities:

- create `session.json`
- write `events.ndjson`
- create per-stream `streams/*.rtplog`
- rotate streams when RTP identity changes make a seam explicit
- preserve enough metadata for deterministic offline reconstruction

This is the most important architectural boundary in the recorder. The session artifact is the source of truth for debugging, remuxing, and future downstream processing.

High-level artifact layout:

```text
sessions/<session_id>/
  session.json
  events.ndjson
  streams/
    s_000001.rtplog
    s_000001.idx
    ...
```

`session.json` is the index of the session. `events.ndjson` captures lifecycle events. Each `.rtplog` stores packet records for one stream segment.

### 5. Legacy Capture Compatibility

Files:

- `internal/cassette/*`

Responsibilities:

- support the older `.csr` archive format
- keep simulate-mode and compatibility tooling working

The package is in a migration state: the new session artifact model is the long-term core, while `.csr` remains for compatibility and inspection.

### 6. Offline Remux and Timeline Recovery

Files:

- `pkg/core/remux/*`
- `pkg/core/depacket/*`
- `pkg/core/mux/mux.go`
- `pkg/core/timeline/*`

Responsibilities:

- depacketize captured RTP logs into elementary media
- estimate stream start positions and timeline adjustments
- build per-stream plans
- merge streams into the final multitrack MKV

The important idea is that the final `.mkv` is derived from persisted packet logs, not from whatever happened to be buffered live. This keeps drift debugging reproducible and makes remux possible after the fact.

### 7. Post-Recording Transcription Pipeline

Files:

- `internal/cassini/build.go`
- `internal/transcribe/*`

Responsibilities:

- take a finished `.mkv` recording and produce a meeting artifact bundle (`meeting.webm`, `transcript.words.v1.json`, `captions.vtt`, optional `summary.md`, `manifest.json`)
- run the local STT model (sherpa-onnx + Parakeet by default) per speaker track
- call an OpenAI-compatible LLM for optional readable-transcript cleanup and meeting summary generation
- write integrity-tagged artifacts (PCM SHA-256 embedded in the transcript JSON)

This is the layer that turns a recorder output into a viewer input. It is intentionally pipeline-shaped — every step writes a durable artifact so failures can be re-run from the last good output. See [`transcription-pipeline.md`](transcription-pipeline.md) for the full step list and configuration surface.

### 8. Inspection and Validation

Files:

- `cmd/gocassini-inspect/main.go`
- `pkg/core/validate/*`

Responsibilities:

- inspect session artifacts or legacy archives
- surface churn, validation issues, and timing diagnostics
- reduce debugging from “rerun the meeting” to “inspect the recorded truth”

This is why the recorder stores more than just a final `.mkv`.

## Data Contracts

The recorder currently works with three important contracts:

- legacy archive: `.csr`
- session artifact: `session.json` + `events.ndjson` + `streams/*.rtplog`
- final deliverable: `.mkv` plus a JSON sidecar report

The intended direction is:

- session artifact is the durable engineering truth
- final `.mkv` is the user-facing playback/transcription input
- diagnostics stay rich enough that drift or churn can be analyzed offline

## What This Package Owns

This package owns:

- live meeting capture
- packet/session persistence
- final remux into the recorder’s main media artifact
- recorder-side diagnostics
- post-recording transcription, readable-text cleanup, and meeting summary generation (`internal/transcribe`)

This package does not own:

- meeting viewer UX
- library/search experience across many meetings
- bot scheduling or workflow orchestration outside the room session itself
- the static-site publisher (lives in `cassini-publisher/`)

## Current Architectural Strengths

- clear artifact-first design
- good separation between provider adapters, live orchestration, and offline remux
- deterministic debugging path through inspectable session artifacts
- strong local E2E story through the shared `test/` harness in the repo root

## Current Architectural Limits

- input platform is still effectively Nextcloud Talk
- operator control is CLI-first, not productized
- final output is strong, but service-level orchestration is outside this package
- there is still some migration complexity because legacy `.csr` and newer session artifacts coexist

## Recommended Reading Order

If you are new to this package, read in this order:

1. `README.md`
2. `internal/config/config.go`
3. `internal/app/run.go`
4. `internal/talk/recorder.go`
5. `internal/talk/session_artifact.go`
6. `pkg/core/session/types.go`
7. `pkg/core/remux/artifact.go`
8. `docs/architecture-migration-status.md`
9. `docs/formats.md`, `docs/timelines.md`, `docs/muxing.md`, and `docs/mkv-format.md`
10. `internal/cassini/build.go` and `docs/transcription-pipeline.md`

That path will give you the “what”, then the runtime flow, then the artifact contract, then the deeper timing/remux design, then the transcription pipeline that consumes the recorder’s output.
