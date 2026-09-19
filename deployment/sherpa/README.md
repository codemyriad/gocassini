# Cassini native Parakeet frontend

Linux amd64/arm64 CPU builds use the prebuilt libraries from
[codemyriad/sherpa-onnx-go-linux](https://github.com/codemyriad/sherpa-onnx-go-linux).
The Go wrapper and platform declarations match upstream v1.13.7. Standard
`go build`, `go run` and `go test` work without compiling sherpa-onnx.
macOS, Windows and Linux arm32 use stock v1.13.7 libraries: Parakeet v3 remains
usable with a warning and the standard decode policy. The patched frontend is
reported in doctor, operator status and transcription provenance.

CUDA still requires native compilation. `build.sh` downloads the immutable
commit recorded in that script from the `v1.13.7-cassini` source fork and verifies
its SHA-256. It applies no local patches. ONNX Runtime archives are pinned by
that release's CMake files. No model weights change.

The frontend activates only for the exact NVIDIA Parakeet TDT 0.6B v3 metadata
URL, 128 features, 16 kHz and per-feature normalization. It uses centered Hann
framing, global preemphasis, Nyquist mel filters, an additive log guard and
sample-variance normalization. Other model frontends retain upstream behavior.
The Cassini release reports `+cassini-parakeet-v3-reference-v1`; libraries
without that marker use the standard decode policy instead of preventing startup.

```sh
# CPU: Go and a C compiler
cassini-go-recorder/scripts/build-cassini-bin.sh
cassini-go-recorder/scripts/build-cassini-bin.sh --test ./internal/transcribe
# CUDA: Linux x86_64, C++17 compiler, CMake >=3.15, curl, tar, unzip,
# plus matching CUDA/cuDNN runtime libraries
cassini-go-recorder/scripts/build-cassini-bin-gpu.sh
```

The CUDA wrapper isolates native builds and replacement Go bindings in
`cassini-go-recorder/.build-cache`, without modifying go.mod or the module cache.
It emits the executable and shared libraries in `dist`. Its preparation lock
serializes builds; tests and recording run after releasing it. Libraries and
workspaces are published atomically. Native cross-compilation is unsupported.

`build.sh cpu|cuda OUTPUT_LIB_DIR WORK_DIR` can also rebuild the CPU runtime.
It defaults to two compiler jobs (`CASSINI_NATIVE_BUILD_JOBS` overrides).
`--fingerprint` covers the builder, including its source pin. CUDA base-image
cache keys include this fingerprint, and the thin CUDA Dockerfile refuses a
mismatching base. Paired `SHERPA_ONNXRUNTIME_INCLUDE_DIR` and
`SHERPA_ONNXRUNTIME_LIB_DIR` overrides are available for diagnostics; the wrapper
rebuilds against pinned archives on the next default invocation.

The patches under `harness/patches` are historical research experiments and
are not production build inputs. The source fork contains native regression
tests and an independent numerical reference generator for upstream review.
