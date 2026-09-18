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

These are explicit model tests: ordinary unit tests do not load gigabytes of
weights. Selecting the build tag requires the models and fails if they are
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

The test is opt-in and is not yet wired into an automatic CI job. Its measured
CPU run took about eight seconds with already-cached models. The checked-in
fixture is about 208 KiB; synthesizer weights are unnecessary to run the test.

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
