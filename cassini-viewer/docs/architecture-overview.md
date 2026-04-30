# Cassini Viewer Architecture Overview

## Purpose

`cassini-viewer` is the browser-facing package for a single Cassini meeting artifact.

Its job is simple:

- load the audio and transcript files in a meeting artifact directory
- render a readable transcript in the browser
- keep transcript and playback synchronized
- support basic search and click-to-seek review

It is intentionally not a backend application or a full recordings library. The package is designed to be statically built and shipped next to artifact files.

## Top-Level Shape

The viewer is a small Svelte application with three architectural layers:

1. UI state and interaction in `src/App.svelte`
2. artifact loading in `src/viewer/*`
3. transcript schema validation, indexing, and search helpers in `src/core/*`

The package is intentionally compact. Most of the product complexity is expected to live in the artifact contract rather than in a heavy frontend framework.

## Entrypoint

The entrypoint is:

- `src/main.ts`

It mounts `App.svelte` into the page and loads shared CSS from `src/app.css`.

The build toolchain is standard Vite plus Svelte 5. There is no server runtime in this package.

## Main Runtime Flow

At runtime the flow is:

1. Mount the root Svelte app.
2. Parse the current URL for an optional meeting selector and playback time hash.
3. Load a meeting artifact from either:
   - bundled/default local files, or
   - a configured demo directory
4. Fetch `transcript.words.v1.json`.
5. Optionally fetch `transcript.readable.v1.json`, `captions.vtt`, and `chapters.vtt`.
6. Validate the transcript payload and build an in-memory index.
7. Resolve the audio source path from the transcript metadata.
8. Bind the audio element to transcript state.
9. Highlight the active segment and word during playback.
10. Support search, manual seeking, and follow-playback scrolling.

The viewer is therefore an artifact reader, not a data authoring system.

## Key Subsystems

### 1. App Shell and Playback State

File:

- `src/App.svelte`

Responsibilities:

- hold the main UI state
- manage current playback time and duration
- track follow-playback and manual scroll lock
- switch between canonical and readable transcript views
- coordinate seek behavior, keyboard shortcuts, and demo selection

Today most of the UI logic lives in this single component. That is a conscious tradeoff: the app is still small enough that a centralized stateful component is simpler than a broader component hierarchy.

### 2. Artifact Loading

Files:

- `src/viewer/loadArtifact.ts`
- `src/viewer/demoMeetings.ts`

Responsibilities:

- load the required JSON and media assets for a meeting artifact
- resolve relative asset URLs
- probe optional files without failing the whole view
- support a publish-safe “no built-in demos” posture by default

`loadArtifact.ts` is the boundary between the static filesystem-like artifact layout and the browser app state.

### 3. Transcript Validation and Indexing

Files:

- `src/core/transcript.ts`
- `src/core/types.ts`

Responsibilities:

- validate the `transcript.words.v1` and `transcript.readable.v1` payloads
- define the TypeScript contracts used by the UI
- build a search/playback index from transcript segments
- answer questions like:
  - which segment is active at this time?
  - which word is active?
  - which segments match this query?

This layer is important because the viewer does not trust arbitrary JSON. It validates the artifact contract before rendering it.

## Data Contract

The viewer expects a meeting artifact directory roughly like this:

```text
meeting-artifact/
  index.html
  assets/...
  meeting.webm
  transcript.words.v1.json
  transcript.readable.v1.json   # optional
  captions.vtt                  # optional
  chapters.vtt                  # optional
```

The primary source of truth is `transcript.words.v1.json`.

That means:

- transcript rendering comes from canonical JSON, not loose text parsing
- audio source location comes from transcript metadata
- search operates over the indexed transcript segments in memory

## Search and Playback Model

Search is currently:

- local to one loaded artifact
- in-memory
- token-based over normalized segment text

Playback synchronization is currently:

- driven by the HTML audio element
- mapped to segments using a binary-search-style start-time index
- refined to active word highlighting inside the active segment

This is enough for a single-meeting review experience without introducing a server or database.

## What This Package Owns

This package owns:

- artifact loading in the browser
- transcript validation and indexing
- playback/transcript synchronization
- per-meeting search and review UX

This package does not own:

- meeting capture
- transcript generation
- backend APIs
- multi-meeting persistence
- user accounts
- global semantic search

## Current Architectural Strengths

- very small deployment surface
- static-build friendly
- strong dependence on explicit artifact contracts instead of ad hoc parsing
- low coupling to any backend service

## Current Architectural Limits

- the main UI is still concentrated in one large Svelte component
- there is no backend-backed library or cross-meeting index
- the viewer assumes artifact files are already present and correctly produced upstream

## Recommended Reading Order

If you are new to this package, read in this order:

1. `README.md`
2. `src/viewer/loadArtifact.ts`
3. `src/core/types.ts`
4. `src/core/transcript.ts`
5. `src/App.svelte`

That path shows the file contract first, then the validation/index layer, then the UI behavior on top.
