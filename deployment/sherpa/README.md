# Cassini native Parakeet frontend

Normal Linux CPU/CUDA images and the Linux/macOS developer wrapper compile
sherpa-onnx v1.13.7 with `parakeet-v3-reference.patch`. The source archive is
SHA-256 verified; native dependencies and ONNX Runtime archives are pinned by
that upstream release's CMake files. No model weights change.

The frontend activates only for the exact NVIDIA Parakeet TDT 0.6B v3 metadata
URL, 128 features, 16 kHz and per-feature normalization. It matches the model's
centered Hann framing, global preemphasis, Nyquist mel bank, additive log guard
and sample-variance normalization. Other model frontends retain upstream
behavior. There is no experimental environment switch. The native version has
`+cassini-parakeet-v3-reference-v1`; the Go recognizer requires that marker for
v3 so an obsolete library cannot silently run the old frontend.

From the repository root:

```sh
# Native compiler, CMake >=3.15, curl, tar, patch, unzip, git and Go required.
cassini-go-recorder/scripts/build-cassini-bin.sh
cassini-go-recorder/scripts/build-cassini-bin.sh --test ./internal/transcribe
cassini-go-recorder/scripts/build-cassini-bin-gpu.sh
# Legacy recorder / other module executables use the same native runtime:
cassini-go-recorder/scripts/build-cassini-bin.sh --run ./cmd/gocassini --help
```

`bin/cassini` uses the CPU wrapper automatically. CUDA requires Linux x86_64
and matching CUDA/cuDNN runtime libraries. Native cross-compilation is rejected;
build on the target architecture (Docker buildx can provide emulation). Linux
x86_64 CPU/CUDA builds were exercised locally; macOS build support is provided
but requires validation on a Mac. Raw `go test` remains appropriate for unit
tests without model loading; actual v3 inference must use the wrapper or a
packaged image. The wrapper keeps replacement Go bindings and native builds in
`cassini-go-recorder/.build-cache`, without changing go.mod or the module cache.
Its executable and runtime libraries are emitted together in `dist`. Cache
preparation is serialized across CPU/CUDA invocations; tests and recording run
after releasing the lock. Runtime libraries and workspace files are published
atomically. An interrupted process normally releases its lock; after SIGKILL,
the next invocation identifies a stale lock and asks for explicit removal.

`build.sh cpu|cuda OUTPUT_LIB_DIR WORK_DIR` is the common Docker/developer
builder. It defaults to two compiler jobs (`CASSINI_NATIVE_BUILD_JOBS` overrides).
`--fingerprint` covers both helper and patch. CUDA base-image cache keys include
this fingerprint, and the thin CUDA Dockerfile refuses a mismatching base.
For offline diagnostics, paired `SHERPA_ONNXRUNTIME_INCLUDE_DIR` and
`SHERPA_ONNXRUNTIME_LIB_DIR` can use an existing runtime; its header/library hashes
are recorded in build metadata. A later default wrapper invocation rebuilds
against the pinned runtime instead of silently reusing that diagnostic override.
Packaged images use pinned upstream archives.

The separate `harness/patches` patch preserves the earlier opt-in experiment.
Production builds use only the patch in this directory. Numerical feature
validation is reproducible with `harness/bin/check-parakeet-native-features.cc`;
the Python frontend comparison documents the matching reference equations.
