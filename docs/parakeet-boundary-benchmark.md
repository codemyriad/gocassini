# Recorded-audio boundary benchmark

Use this opt-in benchmark to test the window, overlap, synthetic silence, and
real-context choices in Cassini's **actual Go transcription path**. It runs the
same Silero detector, Parakeet decoder, overlap reconciliation, timestamp
conversion, and energy filter as a meeting build. The benchmark does not change
production settings through environment variables.

See [the primary-source review](parakeet-boundary-literature-2026-09-17.md) for
why streaming context, independent offline chunks, and synthetic padding are
different interventions. No universal optimum follows from those sources.

## Public synthetic regression

The `asrregression` build tag runs a [two-track synthetic meeting regression](../cassini-go-recorder/internal/transcribe/testdata/synthetic-boundary/README.md) with no private audio or meeting text. The identical test fails on the actual pre-PR source and passes on this PR on CPU and CUDA, checking the missing spoken phrase rather than configuration or word counts. The fixture, generator, provenance and reproduction commands are included. It requires cached FP32 Parakeet v3 and Silero models and is explicitly opt-in; selecting the tag without the models fails rather than skipping.

## Inputs

Keep recordings, reference transcripts, and raw results outside the repository.
A corpus is a JSON array. Each entry names either an audio file or an original
MKV and its **container stream index** (not its position among audio streams):

```json
[
  {
    "id": "isolated-track",
    "mkvPath": "/private/recording.mkv",
    "streamIndex": 2,
    "sampleLimitMs": 120000,
    "scoreStartMs": 60000,
    "scoreEndMs": 90000,
    "hotwordsFile": "/private/hotwords-participant.txt",
    "hotwordScore": 2.0,
    "referenceText": "A separately reviewed transcript of the scoring interval.",
    "referenceKind": "human reviewed"
  },
  {
    "id": "silence-control",
    "audioPath": "/private/silence.wav",
    "referenceText": "",
    "directSpeech": false,
    "referenceKind": "reviewed isolated track"
  }
]
```

MKVs use `ProbeMKV` and `ExtractSpeakerFloats`, retaining initial offsets and
internal timestamp gaps. An audio file should already have those gaps restored.
`sampleLimitMs` trims only after extraction. Scoring intervals filter output by
word start time; references cut at an edge may therefore disagree because of
partial words or timestamp jitter. Keep margins and inspect boundary errors.

Omitting `hotwordsFile` uses unbiased modified beam search, width four. Providing
it retains the actual participant-specific vocabulary. Do not add the expected
missing words as hints when measuring their recovery.
`hotwordScore` is optional and defaults to 2.0; supply the deployed score when
production overrides it. An explicit zero is preserved. The benchmark uses
beam width four for modified beam search.
`CASSINI_BOUNDARY_DECODER=greedy_search` runs the decoder control and omits
hotwords, which greedy decoding cannot apply. Compare beam without hotwords as
well when separating search behavior from contextual bias.

Conditions are another JSON array:

```json
[
  {"id":"existing","windowMs":10000,"overlapMs":500,"headMs":0,"tailMs":500},
  {"id":"leading-silence","windowMs":10000,"overlapMs":500,"headMs":100,"tailMs":500},
  {"id":"real-context","windowMs":10000,"overlapMs":500,"headMs":0,"tailMs":500,"contextMs":500},
  {"id":"whole-vad-real-context","preserveVADSpan":true,"headMs":0,"tailMs":0,"contextMs":30}
]
```

`headMs` and `tailMs` add zeros **after window selection**, independently to each
decode. Leading time is subtracted from word timestamps. `contextMs` instead
expands each VAD segment into the original waveform, clips to its real bounds,
and reconciles overlaps between adjacent expanded segments. It also changes
subsequent window positions; its effect includes that boundary shift.

Window grace remains 500 ms. Terminal windows keep at least five seconds for
windows of ten seconds or more; shorter-window experiments use half the main
window. Those choices are held constant within the corresponding family and
must be reported alongside window length.

`preserveVADSpan` is false by default in manual ablation conditions. When true,
it bypasses the independent VAD-window subdivision and preserves the detected
span plus its requested real context. Window length, overlap, grace and terminal
window settings are ignored. The existing VAD maximum (25 seconds) and decoder
safety fallback (55 seconds at 16 kHz) remain in place. This tests utterance
preservation without choosing a new lexical window constant. Production Parakeet
v3 now uses this policy with 30 ms real context, modified beam search and no synthetic
padding; other models retain their existing policies.

## Running

`CASSINI_BOUNDARY_PIPELINE` selects a complete pipeline:

- `ablation` (default): explicit conditions and optional decoder override, using
  the private legacy constructor so production v3 defaults cannot override an experiment.
- `legacy`: prior beam decoder, ten-second VAD windows and 500 ms synthetic tail.
- `audio-audit`: full-track PCM extraction and SHA-256 only; no model, VAD,
  recognizer or warmup. Emits `device=none` and empty words; rejects sample limits.
- `production`: the normal model-specific constructor and boundary policy; v3
  requires the marked Cassini reference runtime and retains beam decoding, whole
  VAD spans, 30 ms real context and no synthetic padding. Supported vocabulary and participant hints are applied through the normal build resolver. The tokenizer-less INT8 bundle and mixed-audio recovery fallback retain greedy decoding.

The named production, legacy and audio-audit profiles do not need
`CASSINI_BOUNDARY_POLICIES` and ignore decoder and condition overrides. Inference
profiles emit the effective policy; audio-audit emits its profile ID. For a
full meeting, omit sample limits and scoring intervals and provide every track.
Compare legacy against the original runtime and production against the packaged
reference runtime; record both library hashes. The profile flag alone does not
restore an old native frontend. Build the patched CPU test binary via
`scripts/build-cassini-bin.sh --test -tags boundarybench ./internal/transcribe`.


From `cassini-go-recorder`, with a complete cached Parakeet v3 model and Silero:

```sh
CASSINI_CACHE_ROOT=/private/model-cache \
CASSINI_BOUNDARY_CORPUS=/private/corpus.json \
CASSINI_BOUNDARY_POLICIES=/private/conditions.json \
CASSINI_BOUNDARY_OUTPUT=/private/results.jsonl \
CASSINI_BOUNDARY_DEVICE=cpu \
go test -tags boundarybench ./internal/transcribe \
  -run '^TestRecordedBoundaryBenchmark$' -count=1 -v -timeout 45m
```

Use `CASSINI_BOUNDARY_DEVICE=cuda` with a compatible CUDA sherpa-onnx/ONNX Runtime
library distribution when reproducing GPU production. `CASSINI_BOUNDARY_MODEL`
defaults to the fp32 Parakeet v3 bundle; the runner requires a 16 kHz transducer.
Bundled model roots use Cassini's ordinary `CASSINI_BUNDLED_MODEL_ROOT` setting.

The runner refuses to overwrite results, writes each completed condition as a
private JSONL row, and records model, device, policy, decoded-PCM SHA-256, sample
count, elapsed decode time, and words. Decoder metadata records the effective
hotword-file SHA-256, hotword score, and beam width. Runs without hotwords record
an empty hash and score zero; greedy additionally records beam width zero.
Rows also record `pipeline` and `warmup`. It warms the recognizer once per fixture
unless `CASSINI_BOUNDARY_SKIP_WARMUP=1` is set (useful for full-meeting replay);
runtime is diagnostic, not a statistically controlled performance comparison.
Keep model/runtime checksums and the source revision with an experiment.

## Reading results

Choose candidates without inspecting held-out references; freeze the candidate
list, then evaluate held-out recordings. Include known previous failures, quiet
short turns, longer continuous speech, overlaps, and bleed-only tracks.

Measure substitutions, deletions, insertions, repeated boundary words, and false
speech on controls. More words alone is not improved recognition. Gemini
transcriptions should be requested blind, without expected wording; comparisons
against them measure **model-reference disagreement**, not human-verified WER.
Names, fillers, overlapping speech, and clipped edges deserve separate review.

Compare the exact deployed baseline separately from fixes to overlap merging.
Otherwise a gain from merging punctuation variants can be incorrectly credited
to a new window length. Re-run frozen candidates to check stability before
changing defaults or reprocessing a published meeting.

Score a private run with the dependency-free companion:

```sh
python3 harness/bin/score-stt-boundaries.py \
  --corpus /private/corpus.json --results /private/results.jsonl \
  --holdout independent-meeting --output /private/scores.json
```

Run that command from the repository root. The output includes private text;
only publish anonymized aggregate measurements.
