# Cassini Transcriber Architecture

## Objective

Turn a multitrack Cassini meeting recording into a compact meeting artifact that contains:

- one final listenable audio file,
- one canonical `transcript.words.v1.json`,
- one derived `captions.vtt`,
- one small manifest.

The output must align transcript timings to the exact delivered audio artifact.

## Core Decision

Use two timelines:

- `source timeline`: the original multitrack MKV
- `digest timeline`: the final published audio after silence compression

This lets the pipeline transcribe from the highest-fidelity source while still shipping a shorter digest whose transcript remains perfectly aligned to the delivered audio.

## Silence Strategy

Silence handling is a pipeline responsibility, not a model responsibility.

The pipeline should:

1. detect speech activity per speaker track,
2. union all speaker activity into a meeting-level activity map,
3. identify gaps where all speakers are silent,
4. preserve short silence,
5. compress long all-speaker silence to a fixed cap,
6. record a source-to-digest time map.

Recommended v1 defaults:

- keep silence up to `900 ms`
- compress longer all-speaker silence to `800 ms`
- add `200 ms` speech padding on both sides before building the cut map

This keeps turn-taking natural while still producing a materially shorter digest.

## Transcription Strategy

Do not transcribe the final mixed meeting audio.

Instead:

1. extract one mono WAV per participant audio stream,
2. keep the source timeline offsets from the MKV,
3. chunk each speaker track by speech activity,
4. transcribe chunks independently through a compatible HTTP service,
5. map chunk-local timestamps back onto the source timeline,
6. remap source timestamps onto the digest timeline,
7. merge all speakers into one canonical transcript.

Why:

- speaker labels already exist in the MKV metadata,
- diarization is unnecessary for this source format,
- chunking avoids long-silence waste and large-request instability,
- the transcript remains speaker-aware without extra ML steps.

## Artifact Pipeline

1. Probe the MKV and collect audio streams plus participant labels.
2. Extract per-speaker analysis audio.
3. Detect speech intervals for each speaker.
4. Build a meeting-level digest plan from the union of speech intervals.
5. Render final `meeting.webm` from the mixed audio using the digest plan.
6. Chunk per-speaker audio using the speech intervals.
7. Transcribe each chunk with word timestamps.
8. Remap words and segments from source time to digest time.
9. Emit `transcript.words.v1.json`.
10. Derive `captions.vtt` from the canonical transcript.
11. Emit `manifest.json`.

## Model Policy

Preferred primary model:

- GPU-backed Parakeet for English meeting transcription with direct word timestamps

Possible fallback:

- Whisper-style provider with an alignment layer when Parakeet is unavailable

The repository should remain model-agnostic:

- accept a generic HTTP transcription endpoint,
- isolate provider-specific assumptions to the response normalization layer,
- keep environment-specific deployment details out of the repo.

## Validation Rules

The pipeline must enforce:

- integer millisecond timings,
- stable speaker IDs,
- transcript timings aligned to the delivered audio,
- segment and word bounds sanity,
- derived captions from the canonical JSON,
- reproducible source-to-digest remapping.

## Implementation Steps

1. implement timeline mapping and silence compression planning,
2. implement speech-activity-based chunk planning,
3. implement chunked transcription + response normalization,
4. implement digest rendering and timestamp remapping,
5. validate outputs and document operations.
