# Cassini Transcriber Architecture Overview

## Purpose

`cassini-transcriber` is the post-processing stage that turns a recorded Cassini meeting into a publishable meeting artifact.

Its input is a multitrack meeting recording, typically the `.mkv` produced by the recorder. Its outputs are the files the viewer and downstream consumers use:

- final listenable meeting audio
- canonical timed transcript JSON
- optional readable transcript JSON
- captions
- manifest and timeline metadata

The package does not record meetings and does not render the browser UI. It is the bridge between raw meeting capture and the artifact the rest of the product can ship.

## Top-Level Shape

The package is organized around a single main pipeline in `cassini_transcriber/pipeline.py`, with supporting modules for timing, speech activity, and optional LLM cleanup:

- `cli.py`: command-line interface
- `pipeline.py`: end-to-end artifact build orchestration
- `timeline.py`: source-to-digest timeline model
- `speech_activity.py`: silence detection and chunk planning
- `llm.py`: optional readable-transcript rewrite pass via Open WebUI
- `common.py`: shared helpers

The architectural center is the transcript artifact contract, especially `transcript.words.v1.json`.

## Core Architectural Idea

This package uses two timelines:

- source timeline: the original meeting recording timeline
- digest timeline: the final published audio timeline after silence compression

That split is the key design choice in this package. It allows Cassini to:

- transcribe from the most faithful source audio
- shorten long all-speaker silence in the final deliverable
- keep transcript timings aligned to the final audio users actually listen to

Without that explicit timeline map, transcript sync would drift as soon as silence compression is introduced.

## Main Runtime Flow

The main entrypoint is `build_meeting_artifact()` in `pipeline.py`. The flow is:

1. Validate tool availability (`ffmpeg`, `ffprobe`).
2. Probe the source MKV and identify audio streams plus speaker labels.
3. Extract one analysis WAV per speaker track while preserving packet-time gaps.
4. Detect speech activity on each speaker track.
5. Build a mixed master audio file.
6. Build a meeting-level activity map by unioning speaker activity.
7. Build a source-to-digest timeline map that compresses long all-speaker silence.
8. Render the final digest audio file.
9. Plan transcription chunks per speaker track.
10. Send each chunk to a transcription HTTP service.
11. Normalize and remap returned word timings from chunk-local time to source time and then to digest time.
12. Group words into readable transcript segments and assign stable IDs.
13. Emit canonical transcript JSON, captions, optional readable transcript JSON, timeline map, and manifest.

The result is a self-contained meeting artifact directory.

## Key Subsystems

### 1. CLI Layer

File:

- `cassini_transcriber/cli.py`

Responsibilities:

- expose the pipeline as a scriptable CLI
- accept tuning parameters for segmentation, silence compression, chunking, and readable-transcript generation

The CLI is deliberately thin. It mostly forwards configuration into `build_meeting_artifact()`.

### 2. Media Probe and Track Analysis

File:

- `cassini_transcriber/pipeline.py`

Responsibilities:

- inspect the source media with `ffprobe`
- derive speaker IDs and labels from stream metadata
- extract per-track WAV files
- preserve the sparse meeting timeline during decode

The sparse-timeline requirement is important. Cassini recordings may have late joins or silent gaps that must remain visible in the decoded analysis audio. If those gaps are collapsed too early, all downstream timing becomes unreliable.

### 3. Speech Activity and Chunk Planning

File:

- `cassini_transcriber/speech_activity.py`

Responsibilities:

- run `ffmpeg` silence detection on per-track audio
- turn silence ranges into active speech spans
- merge and bridge nearby spans
- split long spans into overlapping transcription chunks

This layer exists so the transcription model does not have to process long useless silence and so long recordings can be chunked deterministically.

### 4. Timeline Mapping

File:

- `cassini_transcriber/timeline.py`

Responsibilities:

- represent source and digest spans explicitly
- compress long all-speaker silent ranges
- map timestamps from source time to digest time

This is the clock discipline layer of the package. It is the main reason the final transcript can stay aligned to the published audio.

### 5. Transcription Adapter

File:

- `cassini_transcriber/pipeline.py`

Responsibilities:

- extract chunk audio
- submit chunks to a compatible HTTP transcription endpoint
- normalize provider responses into Cassini word timing objects
- filter overlapped chunk boundaries and remap timings

The package is intentionally provider-agnostic at the contract level. It expects an HTTP endpoint returning text and word timestamps, then normalizes that response into Cassini’s own schema.

### 6. Canonical Transcript Assembly

File:

- `cassini_transcriber/pipeline.py`

Responsibilities:

- merge per-speaker word lists
- segment them into timed transcript segments
- assign stable segment and word IDs
- validate the final transcript payload
- derive VTT captions from the canonical JSON

The canonical output is `transcript.words.v1.json`. Everything else is derived from that.

### 7. Optional Readable Transcript Rewrite

File:

- `cassini_transcriber/llm.py`

Responsibilities:

- group transcript segments into readable windows
- submit them to Open WebUI-backed chat completion
- rewrite for readability without changing the underlying timing source of truth

This is explicitly a presentation layer. It improves readability, but it is not the canonical timing contract.

## Output Contract

The artifact directory is structured like this:

```text
meeting-artifact/
  meeting.webm
  transcript.words.v1.json
  transcript.readable.v1.json   # optional
  captions.vtt
  timeline.map.v1.json
  manifest.json
```

The contract hierarchy is:

- source of truth: `transcript.words.v1.json`
- derived presentation: `captions.vtt`
- optional presentation rewrite: `transcript.readable.v1.json`
- audit/debug metadata: `timeline.map.v1.json`, `manifest.json`

## External Dependencies

This package depends on:

- `ffmpeg`
- `ffprobe`
- a compatible transcription HTTP service
- optionally Open WebUI for readable transcript rewriting

The package deliberately keeps model hosting outside the repo. It integrates through stable HTTP contracts instead.

## What This Package Owns

This package owns:

- artifact-building from a recorded meeting file
- speech activity analysis
- timestamp remapping
- transcript/captions/manifest generation
- optional readable transcript post-processing

This package does not own:

- meeting capture
- stream/session artifact recording
- playback UI
- long-term meeting library storage
- semantic query or RAG over many meetings

## Current Architectural Strengths

- explicit timing model rather than “best effort” timestamps
- provider-agnostic transcription boundary
- strong separation between canonical transcript data and readable presentation output
- deterministic chunk planning and validation

## Current Architectural Limits

- the pipeline assumes a Cassini-style multitrack recording with useful speaker labels
- the main implementation is still a single pipeline module, so the control flow is easy to follow but not deeply split into subpackages yet
- readable transcript generation is coupled to Open WebUI rather than a more general LLM abstraction

## Recommended Reading Order

If you are new to this package, read in this order:

1. `README.md`
2. `cassini_transcriber/cli.py`
3. `cassini_transcriber/pipeline.py`
4. `cassini_transcriber/timeline.py`
5. `cassini_transcriber/speech_activity.py`
6. `cassini_transcriber/llm.py`
7. `docs/architecture.md`
8. `docs/sparse-stream-timing.md`

That will give you the operational flow first, then the timing model, then the deeper design notes.
