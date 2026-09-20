# Parakeet hotword runtime contract

The hotword matrix deliberately does not commit binaries, model weights, CUDA
libraries, or private audio. `runners/parakeet_hotword.sh` requires these
explicit inputs:

- `PARAKEET_BENCH_BIN`: the `hotword-bench` ELF binary. It must implement the
  `gocassini.hotword-benchmark.v1` output and guard modes used by the runner.
- `SHERPA_DIST_DIR`: a sherpa-onnx 1.13.1 GPU distribution containing
  `libsherpa-onnx-c-api.so`, `libonnxruntime.so`, and
  `libonnxruntime_providers_cuda.so`. The binary must resolve
  `libsherpa-onnx-c-api.so` from this directory.
- `PARAKEET_MODEL_DIR`: the fp32 Parakeet TDT 0.6B v3 ONNX files
  `encoder.onnx`, `encoder.weights`, `decoder.onnx`, `joiner.onnx`,
  `tokens.txt`, and `bpe.vocab`.
- `SILERO_VAD_MODEL`: the Silero VAD ONNX model. VAD runs with one CPU thread;
  speech decoding is always requested with sherpa provider `cuda`.
- Optionally, `CUDA_RUNTIME_LIB_DIRS`: colon-separated CUDA library paths;
  `/usr/local/cuda/lib64` is the default.

The current reproducible source for `hotword-bench` is the bounded Go harness
created against `github.com/k2-fsa/sherpa-onnx-go v1.13.1`. Its safety contract
is checked at runtime: `CUDA_VISIBLE_DEVICES` must be explicit, the recognizer
provider is `cuda`, it refuses a recognizer that has no NVIDIA allocation, it
uses one host thread, and each WAV is limited to 30 seconds. The runner adds a
120-second timeout per condition and blocks when another CUDA workload exists.

To use an existing staged runtime:

```sh
PARAKEET_BENCH_BIN=/runtime/hotword-bench \
SHERPA_DIST_DIR=/runtime/dist \
PARAKEET_MODEL_DIR=/runtime/parakeet-tdt-0.6b-v3 \
SILERO_VAD_MODEL=/runtime/silero_vad.onnx \
make run-parakeet
```

Before accepting a different binary, inspect it with `ldd`, pin the sherpa
version, and retain its source alongside the result bundle. The result bundle
records CUDA allocation after recognizer initialization; lack of an allocation
is a hard failure, never a CPU fallback.

