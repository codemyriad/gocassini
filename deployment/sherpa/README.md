# Cassini native speech runtime

The recorder links sherpa-onnx 2.0.1 through `github.com/k2-fsa/sherpa-onnx-go/v2`
v2.0.1, with its platform modules replaced by Cassini's prebuilt libraries:
[codemyriad/sherpa-onnx-go-linux](https://github.com/codemyriad/sherpa-onnx-go-linux)
(amd64, arm64),
[codemyriad/sherpa-onnx-go-macos](https://github.com/codemyriad/sherpa-onnx-go-macos)
(arm64, x86_64) and
[codemyriad/sherpa-onnx-go-windows](https://github.com/codemyriad/sherpa-onnx-go-windows)
(x64, 386, arm64), all at `v2.0.1-cassini.3` (module paths `.../v2`), built
from the same native source. Linux skips `.2` so the three tags match. The
macOS and Windows modules ship only the C API library and ONNX Runtime (since
their `.2`: `.1` also kept upstream's stock C++ wrapper library, built against
the C diarization config without #4032's `sortformer` member, so it disagreed
with the C API on that struct's layout). Go never links the C++ wrapper; the
Linux fork rebuilds it from the same source. The recorder itself does not build for Windows yet (the
model store uses Unix file locking and `statfs`); the Windows module is pinned
so it is ready when it does. Each fork's GitHub workflow builds the library on
a runner and, before release, checks the version string, transcribes a quiet
sample with Parakeet v3 and the production Parakeet Ultra bundle, and runs
Nemotron diarization through Sortformer. The macOS arm64 library is cross-built on an Intel runner. For
`v2.0.1-cassini.3` it has also passed the full probe on Apple silicon (see the
fork's CASSINI.md). Standard `go build`, `go run` and
`go test` work without compiling sherpa-onnx. Linux arm32 uses the stock 2.0.1
library: it has upstream's quiet-audio fix, but cannot load Parakeet Ultra or
diarize, so there only the fast tier's 110M model transcribes.

The native source is the
[`v2.0.1-cassini-2` tag](https://github.com/codemyriad/sherpa-onnx/tree/v2.0.1-cassini-2)
of codemyriad/sherpa-onnx (commit `deede178`). It is four patches on upstream
master `935e5ded`, which is v2.0.1 plus 16 commits. Upstream has made no
release since, so the version keeps saying 2.0.1. Those commits include #4084
(a NeMo TDT `modified_beam_search` fix), #4123 (`--length-reward` parsing) and
#4130 (clang-tidy fixes); greedy decoding, Cassini's default, gives the same
transcripts as on `v2.0.1-cassini-1` (`c810dfcf`, upstream v2.0.1 itself). The
patches:

1. **Nemotron-3-Diarization (Sortformer)**, the net diff of upstream PR
   k2-fsa/sherpa-onnx#4032 (still open upstream). The model goes in
   `Segmentation.Sortformer.Model`; no embedding model or clustering is needed.
   The [distribution manifest](https://dist.gocassini.com/manifest.json)
   selects interface-version-2 exports, which this runtime accepts unchanged.
   "Separate voices" (0.2.0) uses it through `cassini speakers diarize`.
2. **NeMo TDT joiner detection from the joiner's output size.** Upstream
   selects TDT only when the encoder url contains `tdt`; Moondream Parakeet
   Ultra exports keep `https://huggingface.co/moondream/parakeet-ultra` and
   stock 2.0.1 exits the process while loading them. With the patch a joiner
   whose output is larger than the vocabulary is TDT. Not yet proposed upstream.
3. **Markers.** `GetVersion()` reports
   `2.0.1+cassini.parakeet-tdt-joiner-v1.nemotron-diarization-v2`.
4. **A `cudaGetDeviceProperties` cache, Linux x86_64 CUDA builds only.** ONNX
   Runtime 1.28.2 bundles cuDNN-frontend 1.24, which asks the CUDA runtime for
   the device properties (about 1 ms of kernel time) each time ORT's CUDA Conv
   builds a graph. That happens for each of the Parakeet encoder's 77 Conv
   nodes on every new VAD segment length, which made GPU builds 22-42% slower
   than on 1.13.7. `libsherpa-onnx-c-api.so` defines
   `cudaGetDeviceProperties(_v2)` itself. Programs link it before the ORT CUDA
   provider loads `libcudart.so.12`, so the provider's calls land there, and
   after the first real answer per device they get a copy. Output is
   unchanged; `CASSINI_CUDA_PROPS_CACHE=0` turns it off. It copies CUDA 12
   structs, so `build.sh` refuses a CUDA build whose ORT provider does not
   import `libcudart.so.12` or whose C API lacks the two symbols. Drop it when
   the pinned ORT bundles cuDNN-frontend 1.32 or later (it reads the SM
   version with `cudaDeviceGetAttribute`), or when ORT sets the SM version on
   its Conv graphs. CPU, macOS and Windows libraries do not contain it, and no
   marker names it: check a CUDA library with
   `nm -D --defined-only libsherpa-onnx-c-api.so | grep cudaGetDeviceProperties`.

The feature frontend is upstream's: since v2.0.1 (k2-fsa/sherpa-onnx#3999) NeMo
models skip the log floor and use an additive `FLT_EPSILON/2` guard, which is
what makes quiet audio decode. It replaces Cassini's Parakeet reference
frontend (`+cassini-parakeet-v3-reference-v1`, 1.13.7 runtimes up to
`v1.13.7-cassini.8`). On Ultra int8 (CPU) the two measure the same (MAI WER
9.18 vs 9.24, not significant); on Ultra fp32 (CUDA) 7.31 vs 7.92, also not
significant, all of it from one passage of one meeting. About 4-5% of words in
a transcript differ. The
recorder keeps its whole-utterance boundary policy for Parakeet Ultra (whole
VAD spans, 30 ms of recorded context, no synthetic padding) and records the
frontend as `nativeFrontend: "nemo-log-guard"` in provenance. Doctor and the
operator's `/status` (`stt.runtime_supports_model`) report whether the runtime
can run the selected model.

What the recorder reads from the version string:

| Check | Needs | Without it |
|---|---|---|
| Any model | base version 2.0.1 or later: the C API this build links (VAD segment start is `int64`; the diarization config gained the Sortformer member) | the recognizer refuses to start; a 1.13.7 library on the library path would be handed structures it lays out differently |
| Parakeet Ultra (both precisions) | `.parakeet-tdt-joiner-v1` | refused before loading (stock sherpa would exit) |
| Nemotron diarization | `.nemotron-diarization-v2` on 2.0.1 or later | `runtime_supported: false`, diarize exits 3 |

CUDA still requires native compilation. `build.sh` downloads the immutable
commit recorded in that script from the source fork and verifies its SHA-256.
It applies no local patches, checks that the built library reports the
expected version, and records it in `cassini-native-buildinfo.txt`. ONNX
Runtime archives are pinned by that release's CMake files: 1.28.2 (CUDA:
`onnxruntime-linux-x64-gpu_cuda12-1.28.2-patched.zip`). No model weights
change.

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
