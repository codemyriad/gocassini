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
- One transcription backend:
  - `auto`: use HTTP when `--transcriber-url` is set, otherwise prefer local execution and pick `cuda` when NVIDIA is available, else `cpu`
  - `http`: a compatible HTTP transcription service that accepts `POST` multipart audio uploads and returns JSON with `text` plus word-level timestamps
  - `local-whisper`: local Python package `faster-whisper`, plus an NVIDIA-capable CUDA runtime when `--whisper-device cuda` is used
- Optional readable transcript cleanup backend when `--readable-transcript-name` is set:
  - `auto`: use an OpenAI-compatible API when configured, otherwise Open WebUI when fully configured, otherwise local Ollama when available, otherwise disable readable cleanup
  - `none`: skip readable transcript cleanup
  - `openai-compatible`: direct OpenAI-style chat-completions API, including OpenRouter
  - `openwebui`: the existing Open WebUI settings
  - `local-transformers`: local Python packages `torch` and `transformers`, plus an NVIDIA-capable CUDA runtime when `--local-llm-device cuda` is used
  - `local-ollama`: local `ollama` CLI access, with the configured model pulled automatically unless `--ollama-no-auto-pull` is set

For the local NVIDIA path, the Python packages are listed in
`requirements-local-nvidia.txt`.

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

Local auto-selected run:

```bash
python3 cassini-transcriber/build-meeting-artifact.py \
  --input /path/to/meeting.mkv \
  --output-dir /tmp/meeting-artifact
```

One-command Docker wrapper:

```bash
./cassini-transcriber/bin/docker-run-local.sh \
  --input /path/to/meeting.mkv \
  --output-dir /tmp/meeting-artifact
```

One-command processor with stable output directories and optional rendered viewer bundle:

```bash
./cassini-transcriber/bin/process-meeting.sh \
  --input /path/to/meeting.mkv \
  --output-root /tmp/cassini-results
```

Add `--bundle-viewer` to also emit a static browser package for the meeting.

Useful flags:

- `--keep-work-dir` keeps extracted per-speaker WAV files and raw transcription responses.
- `--transcriber-backend` supports `auto`, `http`, and `local-whisper`.
- `--whisper-model auto` currently resolves to `large-v3` because quality is preferred over speed by default.
- `--whisper-device auto` prefers `cuda` when NVIDIA is available, else `cpu`.
- `--whisper-download-root` controls local model cache placement.
- `--segment-gap-ms` tunes pause-based segmentation.
- `--max-segment-ms` and `--max-segment-words` keep transcript chunks readable.
- `--readable-transcript-name` enables a second LLM-cleaned transcript artifact.
- `--readable-backend` supports `auto`, `none`, `openai-compatible`, `openwebui`, `local-transformers`, and `local-ollama`.
- `--api-base-url`, `--api-key`, and `--api-model` configure an OpenAI-compatible readable transcript backend.
- `--local-llm-model`, `--local-llm-device`, and `--local-llm-download-root` control the local readable transcript backend.
- `--ollama-model` and `--ollama-no-auto-pull` control the local Ollama readable transcript backend.
- `--openwebui-base-url`, `--openwebui-email`, `--openwebui-password`, and `--openwebui-model` configure the Open WebUI service used for readable transcript cleanup.
- `--keep-silence-ms` and `--compress-silence-to-ms` control digest silence compression.
- `--minimum-silence-ms`, `--minimum-activity-ms`, and `--silence-noise-db` tune speech activity detection.
- `--max-chunk-ms`, `--chunk-overlap-ms`, and `--max-bridge-gap-ms` control chunked transcription requests.

The CLI summary reports source duration, digest duration, reduction, chunk count, word count, and timeline segment count so batch runs are easier to inspect.

Readable transcript generation can also be configured through environment variables:

```bash
export CASSINI_READABLE_TRANSCRIPT_NAME=transcript.readable.v1.json
export OPENROUTER_API_KEY=...
export CASSINI_API_BASE_URL=https://openrouter.ai/api/v1
export CASSINI_API_MODEL=openai/gpt-4o-mini
export CASSINI_OPENWEBUI_BASE_URL=http://openwebui.example.internal
export CASSINI_OPENWEBUI_EMAIL=admin@example.com
export CASSINI_OPENWEBUI_PASSWORD=...
export CASSINI_OPENWEBUI_MODEL=qwen35-9b-q4
```

Local runtime environment variables:

```bash
export CASSINI_TRANSCRIBER_BACKEND=local-whisper
export CASSINI_WHISPER_MODEL=auto
export CASSINI_WHISPER_DEVICE=auto
export CASSINI_READABLE_BACKEND=none
```

OpenRouter-backed readable transcript run:

```bash
export OPENROUTER_API_KEY=...
python3 cassini-transcriber/build-meeting-artifact.py \
  --input /path/to/meeting.mkv \
  --output-dir /tmp/meeting-artifact \
  --readable-transcript-name transcript.readable.v1.json \
  --readable-backend openai-compatible \
  --api-base-url https://openrouter.ai/api/v1 \
  --api-model openai/gpt-4o-mini
```

## Notes

- The pipeline detects speech activity per speaker track, unions that activity at the meeting level, and compresses long all-speaker silence on the final digest timeline.
- Backend prerequisites are validated before audio extraction starts so missing URLs, Python packages, or CUDA support fail early.
- Local execution uses one heavy model stage at a time. ASR finishes before any optional readable-cleanup backend starts, so 8 GB cards are usable.
- The Docker runner now keeps `_work` by default, so restarting the same output directory reuses chunk responses and extracted audio instead of recomputing everything.
- Chunk transcription responses are cached per backend/model under `_work/responses`, so retrying with a different ASR backend or model reuses extracted audio while keeping response caches separate.
- `bin/process-meeting.sh --bundle-viewer` skips the static HTML export when the rendered bundle is already newer than the artifact manifest.
- The source MKV may carry sparse audio packet timestamps; per-speaker decode must preserve those gaps or transcript timings will drift badly.
- Each speaker track is transcribed separately in chunked requests, then merged into one time-ordered transcript.
- Multiple audio streams with the same normalized title are treated as one speaker in the final artifact, so rejoin tracks do not create fake extra speakers.
- `captions.vtt` is derived from `transcript.words.v1.json` and should not be edited independently.
- `transcript.readable.v1.json`, when enabled, is a second-pass LLM cleanup pass over timed source spans and should be treated as presentation text only.
- The target architecture uses an explicit source-to-digest timeline remap so long all-speaker silence can be compressed without breaking transcript sync.
- `timeline.map.v1.json` is a debugging and audit artifact that records the source-to-digest time remap.
- On March 9, 2026, `OPENROUTER_API_KEY` was verified against `https://openrouter.ai/api/v1/models` and `https://openrouter.ai/api/v1/chat/completions`.
- On the same date, the first readable-transcript batch from the sample meeting was compared across `openai/gpt-4o-mini`, `google/gemini-2.5-flash`, `anthropic/claude-3.7-sonnet`, and `qwen/qwen3-32b`; `openai/gpt-4o-mini` gave the cleanest adherence to Cassini's rewrite format, while `qwen/qwen3-32b` returned a non-string message payload through OpenRouter and is not the current recommendation.
- `bin/compare-readable-models.py` is the repeatable harness for re-running those prompt and model checks on any existing `transcript.words.v1.json`.

## Credibility Checks

When verifying an artifact:

- compare the source audio stream packet timing to the decoded per-speaker track timeline, not to compact PCM extracted without gap preservation,
- compare late joiners against the source-to-digest mapped time in `timeline.map.v1.json`, not against raw source milliseconds,
- treat large multi-second cross-speaker overlaps as suspicious unless the source meeting actually contains interruptions.

## Tests

```bash
python3 -m unittest discover -s cassini-transcriber/tests -t cassini-transcriber
```

## Benchmarks

Measured on `/mnt/data/cassini/initial-experiments/daily-meeting--2026-03-09--12:32:04.mkv`
inside container `100`, with readable cleanup disabled, `small.en` used only as a
device-comparison control model, and one-off image/model downloads excluded:

- local GPU (`cuda`, warm cache): `53` seconds
- local CPU (`cpu`): `196` seconds

These numbers are not the current quality-first default. The current `auto`
model policy resolves to `large-v3` until a Parakeet-based local backend is in
place.
