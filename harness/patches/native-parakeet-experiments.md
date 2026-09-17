# Isolated native Parakeet experiments

These patches are research artifacts, not changes to the installed runtime.
Use a separate sherpa-onnx v1.13.7 source checkout and build directory, a C++
compiler, CMake, and matching ONNX Runtime headers/libraries. The recording
experiments used ONNX Runtime 1.27.1 with CUDA and the cached FP32 Parakeet v3
model. Do not overwrite the production library or Go module cache.

Apply the frontend patch to the clean checkout. The beam diagnostics are
optional; final-ranking instrumentation depends on the legal-blank patch:

```sh
git apply /path/to/parakeet-v3-reference-frontend-sherpa-v1.13.7.patch
git apply /path/to/sherpa-onnx-v1.13.7-tdt-legal-blank-experiment.patch
git apply /path/to/sherpa-onnx-v1.13.7-tdt-final-ranking-experiment.patch

SHERPA_ONNXRUNTIME_INCLUDE_DIR=/private/ort-1.27.1/include \
SHERPA_ONNXRUNTIME_LIB_DIR=/private/ort-1.27.1/lib \
cmake -S . -B /private/sherpa-experiment-build \
  -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=ON \
  -DSHERPA_ONNX_ENABLE_GPU=ON \
  -DSHERPA_ONNX_ENABLE_BINARY=OFF \
  -DSHERPA_ONNX_ENABLE_PORTAUDIO=OFF \
  -DSHERPA_ONNX_ENABLE_WEBSOCKET=OFF \
  -DSHERPA_ONNX_ENABLE_TTS=OFF \
  -DSHERPA_ONNX_ENABLE_SPEAKER_DIARIZATION=OFF \
  -DSHERPA_ONNX_BUILD_C_API_EXAMPLES=OFF
cmake --build /private/sherpa-experiment-build -j 8
```

Put that build's `lib` directory first in `LD_LIBRARY_PATH`, followed by the
matching ONNX Runtime and CUDA libraries, when running the recorded-audio
benchmark. Verify the loaded library path (for example `/proc/PID/maps` on
Linux). Keep source, patch, model and library hashes with the private results.

All experimental flags default off and require the exact value `1`:

| Flag | Effect |
| --- | --- |
| `CASSINI_PARAKEET_REFERENCE_FRONTEND` | Model-specific reference frontend; exact Parakeet v3 metadata required. |
| `CASSINI_TDT_LEGAL_BLANK` | Score a positive blank duration actually used by beam search. |
| `CASSINI_TDT_SCORE_NORM` | Rank final TDT beam candidates by score / (token count + 1). |
| `CASSINI_TDT_TRACE` | Print compact final-candidate and transition diagnostics. |

First compare flags-off results against the original runtime. Then change one
factor at a time. The reference pipeline candidate uses only the frontend
flag, `CASSINI_BOUNDARY_DECODER=greedy_search`, and this benchmark condition:

```json
[
  {
    "id": "reference-whole-vad",
    "preserveVADSpan": true,
    "contextMs": 30,
    "headMs": 0,
    "tailMs": 0
  }
]
```

Keep 0 and 60 ms context variants as sensitivity checks, not automatic
per-recording selection. `preserveVADSpan` removes the additional ten-second
subdivision; the existing VAD maximum and emergency split remain. Greedy does
not apply hotword bias. See the [benchmark instructions](../../docs/parakeet-boundary-benchmark.md)
for corpus schema and commands, and the [investigation](../../docs/parakeet-boundary-investigation-2026-09-17.md)
for measured gains, regressions, and unresolved limitations.

The C API ABI is unchanged. The internal C++ feature configuration layout is
changed, so never mix old and rebuilt C++ components in one process.
