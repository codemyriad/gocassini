# Cassini Transcriber

`cassini-transcriber` turns a multitrack Cassini meeting recording into the artifact expected by the transcript viewer work:

- one final mono Opus/WebM audio file,
- one canonical `transcript.words.v1.json`,
- one optional cleaned `transcript.readable.v1.json`,
- one derived `captions.vtt`,
- one small `manifest.json` with digest/transcript summary metadata.

The pipeline assumes the source MKV carries one audio stream per participant and uses each audio stream title as the speaker label when available.

The intended production design is documented in [docs/architecture.md](docs/architecture.md).
Cassini-specific timing behavior is documented in [docs/sparse-stream-timing.md](docs/sparse-stream-timing.md).

## Contract

The generated artifact directory contains:

```text
meeting-artifact/
  meeting.webm
  transcript.words.v1.json
  transcript.readable.v1.json
  captions.vtt
  timeline.map.v1.json
  manifest.json
```

Key properties:

- Transcript timings are integer milliseconds.
- Transcript timings are aligned to the final delivered audio file.
- The canonical JSON is the source of truth; captions are derived from it.
- The readable JSON is optional derived presentation text; it is not the sync source of truth.
- Speaker IDs are stable and derived from stream titles.
- Overlapping speaker segments are preserved in the canonical transcript.
- `manifest.json` records the gap-preserving source-audio timeline duration, which may differ slightly from the container's nominal `format.duration`.

## Requirements

- `ffmpeg`
- `ffprobe`
- Python 3.10+
- A compatible HTTP transcription service that accepts `POST` multipart audio uploads and returns JSON with `text` plus word-level timestamps

Expected response shape:

```json
{
  "text": "Hello world",
  "words": [
    { "word": "Hello", "start": 0.12, "end": 0.41 },
    { "word": "world", "start": 0.42, "end": 0.77 }
  ]
}
```

## Usage

From the repo root:

```bash
python3 cassini-transcriber/build-meeting-artifact.py \
  --input /path/to/meeting.mkv \
  --output-dir /tmp/meeting-artifact \
  --transcriber-url http://127.0.0.1:8000/v1/transcribe
```

Useful flags:

- `--keep-work-dir` keeps extracted per-speaker WAV files and raw transcription responses.
- `--segment-gap-ms` tunes pause-based segmentation.
- `--max-segment-ms` and `--max-segment-words` keep transcript chunks readable.
- `--readable-transcript-name` enables a second LLM-cleaned transcript artifact.
- `--openwebui-base-url`, `--openwebui-email`, `--openwebui-password`, and `--openwebui-model` configure the Open WebUI service used for readable transcript cleanup.
- `--keep-silence-ms` and `--compress-silence-to-ms` control digest silence compression.
- `--minimum-silence-ms`, `--minimum-activity-ms`, and `--silence-noise-db` tune speech activity detection.
- `--max-chunk-ms`, `--chunk-overlap-ms`, and `--max-bridge-gap-ms` control chunked transcription requests.

The CLI summary reports source duration, digest duration, reduction, chunk count, word count, and timeline segment count so batch runs are easier to inspect.

Readable transcript generation can also be configured through environment variables:

```bash
export CASSINI_READABLE_TRANSCRIPT_NAME=transcript.readable.v1.json
export CASSINI_OPENWEBUI_BASE_URL=http://openwebui.example.internal
export CASSINI_OPENWEBUI_EMAIL=admin@example.com
export CASSINI_OPENWEBUI_PASSWORD=...
export CASSINI_OPENWEBUI_MODEL=qwen35-9b-q4
```

## Notes

- The pipeline detects speech activity per speaker track, unions that activity at the meeting level, and compresses long all-speaker silence on the final digest timeline.
- The source MKV may carry sparse audio packet timestamps; per-speaker decode must preserve those gaps or transcript timings will drift badly.
- Each speaker track is transcribed separately in chunked requests, then merged into one time-ordered transcript.
- Multiple audio streams with the same normalized title are treated as one speaker in the final artifact, so rejoin tracks do not create fake extra speakers.
- `captions.vtt` is derived from `transcript.words.v1.json` and should not be edited independently.
- `transcript.readable.v1.json`, when enabled, is a second-pass LLM cleanup pass over timed source spans and should be treated as presentation text only.
- The target architecture uses an explicit source-to-digest timeline remap so long all-speaker silence can be compressed without breaking transcript sync.
- `timeline.map.v1.json` is a debugging and audit artifact that records the source-to-digest time remap.

## Credibility Checks

When verifying an artifact:

- compare the source audio stream packet timing to the decoded per-speaker track timeline, not to compact PCM extracted without gap preservation,
- compare late joiners against the source-to-digest mapped time in `timeline.map.v1.json`, not against raw source milliseconds,
- treat large multi-second cross-speaker overlaps as suspicious unless the source meeting actually contains interruptions.

## Tests

```bash
python3 -m unittest discover -s cassini-transcriber/tests -t cassini-transcriber
```
