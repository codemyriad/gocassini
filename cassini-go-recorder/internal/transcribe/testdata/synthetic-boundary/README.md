# Synthetic missing-speech regression

`garden.mkv` is a small, two-participant Opus meeting made entirely from invented
scripts and stock synthetic voices. No private meeting audio, utterances,
participant names, or cloned voices are included. Track 1 describes gardening;
track 2 gives instructions for opening a library and starts three seconds later.
Both tracks overlap on the meeting timeline but remain independent audio streams.

The test exercises `ProbeMKV`, `ExtractSpeakerFloats`, the production decoder
resolver, `NewRecognizer`, Silero VAD, Parakeet v3 and word filtering. It checks
specific spoken phrases, including words before and after the omission and a
second-track control. It does not select experimental policies, stub decoding,
add the expected words as hints, or pass based on word count alone.

## Verified red/green result

The identical test and fixture were compiled against the real pre-PR revision
`010768e6bad8e1401e9b5981502e0ee2e187ad62` and PR revision
`c580c39402842a4a6462f8feb8f418dcb82e24a2`. Each used its corresponding native
runtime and the same cached FP32 Parakeet v3 model.

| Revision | CPU | CUDA | Observed result |
| --- | --- | --- | --- |
| Before PR | fails | fails on 3 runs | Omits “if the weather clears tomorrow”; surrounding speech and track 2 survive |
| PR | passes | passes on 3 runs | All required phrases survive on both tracks |

This is a reproduction of missing interior speech with invented audio. It does
not establish that every real-meeting omission has the same native cause, or
that all the PR's known recognition regressions are resolved. The diagnostic
harness's legacy profile dropped a larger, 22-word span; the table above uses
actual old source, not that emulation.

## Run

These model tests run automatically in the normal `CI` workflow, in the
`Unit tests (cassini-go-recorder)` job, on every code PR and main/master push.
They use FP32 Parakeet on the hosted CPU runner; no `full-ci` label or GPU is
needed. Fast local unit runs can still omit the model-test build tag. Selecting the build tag requires the models and fails if they are
missing; it does not silently skip the speech assertion or download models.
FFmpeg/ffprobe and the cached FP32 `parakeet-tdt-0.6b-v3` and Silero models are
required. From `cassini-go-recorder`:

```sh
CASSINI_CACHE_ROOT="$HOME/.cache/cassini" \
  scripts/build-cassini-bin.sh --test -tags asrregression \
  -run '^TestSyntheticMeetingRetainsInteriorSpeech$' -count=1 -v ./internal/transcribe
```

For the CUDA native package, add `--backend cuda` before `--test` and set
`CASSINI_ASR_REGRESSION_DEVICE=cuda`. Tests use the FP32 model on either device;
this fixture does not certify the separately quantized INT8 bundle.

CI caches the model weights and production CPU native library. It verifies
all inference model files against `models.sha256`, including the external
encoder weights, before running the tests. Verified caches are saved before
recognition assertions so a genuine test failure does not cause another large
download on the next run. Results are uploaded as `synthetic-asr-cpu-results`.
The garden test took about eight seconds locally with already-cached models. The checked-in
fixture is about 208 KiB; synthesizer weights are unnecessary to run the test.

## Empty-decode acknowledgement regression

`acknowledgement.wav` is 1.621 seconds of stock `am_adam` synthetic speech:
“Mm-hmm, right, that makes sense.” The test asserts only “right that makes
sense”; it does not require a particular spelling of the nasal acknowledgement.
The waveform has a fixed −6 dB gain adjustment and no added silence.

`TestSyntheticAcknowledgementRetainsSpeech` passed on the actual old source
but failed on PR commit `2600c9a7`, on CPU and on three CUDA runs. The tight VAD
crop returned no words, while decoding the complete waveform retained the phrase.
The fix retries an empty v3 VAD decode once with available recorded context,
using the detector's existing 500 ms silence interval. It preserves the same
decoder and hints, adds no synthetic silence, and excludes words belonging
entirely to neighbouring context. Successful initial decodes remain unchanged.

The regression also checks recording-start, recording-end, interior, repeated
turns and silence variants, including word counts and recording timestamp bounds.
Assertions remain strict; there are no skipped or expected-failure cases. The
garden test continues to check the original recovered interior phrase.

```sh
CASSINI_CACHE_ROOT="$HOME/.cache/cassini" \
  scripts/build-cassini-bin.sh --test -tags asrregression \
  -run '^TestSynthetic' -count=1 -v ./internal/transcribe
```

`acknowledgement.json` records the synthetic script, voice, waveform hash and
old/new validation. This separate candidate used `kokoro-onnx` 0.6.1; do not
regenerate it with the garden fixture's older TTS package versions:

```sh
uv run --python 3.12 --with-requirements acknowledgement-requirements.txt \
  python generate_acknowledgement.py \
  --assets "$HOME/.cache/gocassini/kokoro-onnx" \
  --output /tmp/new-acknowledgement.wav
```

## Provenance and regeneration

Scripts, voices, package versions, source WAV hashes and fixture hash are in
`manifest.json`. The fixture uses [Kokoro-82M v1.0](https://huggingface.co/hexgrad/Kokoro-82M)
(the model card identifies Apache-2.0 weights), exported by
[kokoro-onnx](https://github.com/thewh1teagle/kokoro-onnx). Model and voice assets
are not included; their exact SHA-256 hashes are checked by `generate.py`.
The original package versions are pinned in `requirements.txt`.

```sh
uv run --python 3.12 --with-requirements requirements.txt \
  python generate.py \
  --assets "$HOME/.cache/gocassini/kokoro-onnx" \
  --output /tmp/new-synthetic-boundary-fixture
```

Run from this directory. The output directory must not already exist. Generation
uses CPU only, two inference threads, fixed stock voices, speed 1.0, and 700 ms
of leading/trailing silence before Opus encoding. The original encoder was
FFmpeg `7.1.4-0+deb13u1`; the second track's container offset is three seconds.

The committed bytes are the stable test input. A different synthesizer, codec
build or platform can produce different bytes; Matroska also has generated
container metadata. Regeneration must be followed by checking decoded audio and
repeating the real old/new recognition comparison before replacing the fixture.
