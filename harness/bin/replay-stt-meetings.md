# Full-meeting replay and audio identity audit

`replay-stt-meetings.py` runs **every audio stream** by default, including empty
and bleed-only tracks, sequentially through the tagged Go benchmark. It never requests models
or credentials. Use an existing model root containing `models/<model-id>/` and
`vad/silero_vad.onnx`, a compiled `boundarybench` test binary, and compatible CUDA
libraries. Compile the same revision for both profiles.

Private manifest example (paths are resolved from the runner's working directory):

```json
[
  {"id":"meeting-a","remoteMkvPath":"/recordings/a.mkv","expectedDurationMs":1200000,
   "hotwordsFiles":{"1":"/private/hotwords-a.txt"}},
  {"id":"meeting-b","mkvPath":"/private/b.mkv","expectedDurationMs":900000}
]
```

```sh
python3 harness/bin/replay-stt-meetings.py \
  --manifest /private/meetings.json --profile production \
  --test-binary /private/boundary.test --runtime-lib /private/reference/lib \
  --ffmpeg-bin /private/ffmpeg/bin --ffmpeg-lib /private/ffmpeg/lib \
  --cuda-lib /usr/local/cuda/lib64 --model-root /private/model-cache \
  --output-dir /private/replay-production --ssh-host george --ssh-sudo
```

Use a **separate output directory** and the original runtime library for `legacy`.
Production v3 requires Cassini's marked reference runtime; legacy preserves beam
search and fixture vocabulary. Optional `--model` defaults to fp32 v3; `--timeout`
defaults to `12h` per meeting. Both profiles skip warmup and process full tracks,
without scoring intervals. Runtime measurements are diagnostic, not speed claims.

Each hashed meeting directory contains private fixtures, probe metadata, logs,
results and atomically updated status. Status records expected duration, probed
duration, expected/completed track counts, elapsed time and errors. Expected
duration is a diagnostic, not a trim instruction. All newly created files use
0600 permissions. Remote MKVs are copied one at a time via quoted `ssh cat`
(`sudo -n` when requested) and removed after both success and handled failure.
Local MKVs are never modified. Failures retain logs/results and the runner
continues with subsequent meetings; its exit status is nonzero if any fail.

Reruns resume only completed meetings with matching configuration and result
hashes. The fingerprint includes binary, manifest, runtime, FFmpeg and hotword
hashes, hint-disable/score environment, model root, CUDA library paths and local MKV size/mtime. Treat remote
recordings, model contents and CUDA directories as immutable for one output
directory. Incomplete meetings restart in full, preserving the prior partial
JSONL file. Do not run two processes against one output directory.

Word counts and disagreement identify audit targets; they do not establish
recognition quality. Review the largest changes against audio and independent
references. Keep recordings, references and all result directories outside git.

## Audio-only identity audit

`--profile audio-audit` extracts each track with the actual `ExtractSpeakerFloats`
helper and records sample count plus SHA-256 of little-endian float32 PCM. It
performs **no model loading, VAD, recognition, warmup or GPU inference**. Rows
explicitly say `pipeline=audio-audit`, `device=none`, and contain an empty word
list. The native runtime argument remains necessary to load the tagged Go binary;
model-root/model arguments are unused in this mode. Probe results are cached once
per MKV within the benchmark. Audio-audit fixtures must not contain sample limits.

When an extraction helper changes, compare both PCM hash and sample count with
prior inference rows. Byte-identical audio can reuse prior results only when the
model, decoder and boundary policy also remain identical. Changed audio requires
new inference. Audit rows themselves are not transcription results.

## Targeted replay

A manifest entry may include `"streamIndices": [1, 5]` to select changed audio
tracks for a later replay. The list must be nonempty, unique nonnegative integers,
and every selected index must exist and be audio. Omit it to process all audio.
Status records `availableTracks`, `availableStreamIndices`, selected
`expectedTracks`/`streamIndices`, and `targetedReplay`. A successful targeted
replay is **not** proof that every track in the meeting was tested: the caller
must combine it with verified unchanged results and check complete coverage.

Production replay derives participant hints from the complete MKV stream metadata
by default, using the build's vocabulary resolver and per-speaker own-name
exclusion. A meeting manifest may supply `configuredVocabulary: ["Project name"]`
and explicitly set `deriveParticipantHints: false`. Historical configured
vocabulary is not recoverable from audio and is never guessed: when absent,
only recorded participant labels are available. Output `hints` describes the
build-level resolution; the selected stream's hotword hash and score describe
its actual decoder input. A policy that disables hints records that fact.

Explicit `hotwordsFiles` remains a manual override for the supplied streams;
file contents are not regenerated. Combining manual files with configured
vocabulary or explicit automatic hint derivation is rejected. At the fixture
level the corresponding fields are `hotwordsFile`, `configuredVocabulary`, and
`deriveParticipantHints`; automatic resolution is supported by the production
profile only. Manual score ablations remain separate from production vocabulary,
which follows the normal operator score configuration.

Recorded benchmark fixtures also accept `useVAD: false` for the merged-audio
fallback path; omission defaults to `true` for individual tracks. Every inference
row records the effective value. Ablation conditions install the same boundary
policy on the recognizer before non-VAD decoding, so that path measures the
requested condition rather than the recognizer's original policy.
