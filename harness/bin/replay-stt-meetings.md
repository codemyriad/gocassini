# Full-meeting GPU replay

`replay-stt-meetings.py` runs **every audio stream**, including empty and bleed-only
tracks, sequentially through the tagged Go benchmark. It never requests models
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
hashes, model root, CUDA library paths and local MKV size/mtime. Treat remote
recordings, model contents and CUDA directories as immutable for one output
directory. Incomplete meetings restart in full, preserving the prior partial
JSONL file. Do not run two processes against one output directory.

Word counts and disagreement identify audit targets; they do not establish
recognition quality. Review the largest changes against audio and independent
references. Keep recordings, references and all result directories outside git.
