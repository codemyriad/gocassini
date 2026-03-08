# Cassini Transcriber

`cassini-transcriber` turns a multitrack Cassini meeting recording into the artifact expected by the transcript viewer work:

- one final mono Opus/WebM audio file,
- one canonical `transcript.words.v1.json`,
- one derived `captions.vtt`,
- one small `manifest.json`.

The pipeline assumes the source MKV carries one audio stream per participant and uses each audio stream title as the speaker label when available.

The intended production design is documented in [docs/architecture.md](/home/silvio/dev/gocassini/cassini-transcriber/docs/architecture.md).

## Contract

The generated artifact directory contains:

```text
meeting-artifact/
  meeting.webm
  transcript.words.v1.json
  captions.vtt
  timeline.map.v1.json
  manifest.json
```

Key properties:

- Transcript timings are integer milliseconds.
- Transcript timings are aligned to the final delivered audio file.
- The canonical JSON is the source of truth; captions are derived from it.
- Speaker IDs are stable and derived from stream titles.
- Overlapping speaker segments are preserved in the canonical transcript.

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
- `--keep-silence-ms` and `--compress-silence-to-ms` control digest silence compression.
- `--minimum-silence-ms`, `--minimum-activity-ms`, and `--silence-noise-db` tune speech activity detection.
- `--max-chunk-ms` and `--chunk-overlap-ms` control chunked transcription requests.

## Notes

- The pipeline detects speech activity per speaker track, unions that activity at the meeting level, and compresses long all-speaker silence on the final digest timeline.
- Each speaker track is transcribed separately in chunked requests, then merged into one time-ordered transcript.
- `captions.vtt` is derived from `transcript.words.v1.json` and should not be edited independently.
- The target architecture uses an explicit source-to-digest timeline remap so long all-speaker silence can be compressed without breaking transcript sync.
- `timeline.map.v1.json` is a debugging and audit artifact that records the source-to-digest time remap.

## Tests

```bash
python3 -m unittest discover -s cassini-transcriber/tests -t cassini-transcriber
```
