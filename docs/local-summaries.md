# Bundled local meeting summaries (pilot)

The ExApp CPU and CUDA images bundle Ling-3.0-tiny Q4_K_M alongside Parakeet,
plus a pinned llama.cpp runtime. Enable it with `CASSINI_SUMMARY_BACKEND=local`.
No API key, external inference service, open inference port, or model download
is needed in those images. This is an **opt-in pilot**: small synthetic tests
show good speed but also unsupported follow-ups and action items. Review the
summary against the transcript; Ling is not yet the production default.

| Setting | Behavior |
| --- | --- |
| `CASSINI_SUMMARY_BACKEND` | `remote` (default) preserves existing API behavior; `local` ignores all remote keys/URLs; `auto` prefers a configured remote key and otherwise the advertised bundled runtime; `off` disables summaries. |
| `CASSINI_SUMMARY_DISABLED=1` | Overrides every backend and disables summaries. |
| `CASSINI_LLM_DEVICE` | `auto` (default), `cpu`, or `cuda`. Auto uses CUDA only in a CUDA-capable image with an NVIDIA device. Explicit CUDA fails if the runtime cannot use CUDA0. |
| `CASSINI_LLM_CONTEXT_SIZE` | 16384 by default; 8192–131072 accepted. Actual hardware capacity may be lower. |
| `CASSINI_LLM_TIMEOUT_SEC` | 900 seconds for model loading and inference; positive integer. Installation and waiting for another summary are separate. |
| `CASSINI_LLM_SERVER` | Image-provided executable `/opt/cassini/llm/llama-server`; override for a standalone CLI installation. |
| `CASSINI_DISALLOW_MODEL_DOWNLOAD=1` | Forbids downloading missing models, as with STT. |

The server starts after this build's speech recognizers close. It binds an
ephemeral loopback port with a random per-process key, uses one slot with thinking
disabled, and exits on success, failure, or timeout. On Linux it is also killed
if the recorder dies. Local calls ignore HTTP proxy settings and refuse redirects.
Local summaries sharing a cache serialize model residency. The operator's
existing admission lock serializes complete builds, including STT and summaries.
Independent operators or CLI builds can still contend with one another's speech
recognizers, so schedule those separately when sharing a GPU.
The pilot checks available host RAM and cgroup v2 headroom before loading, using
an approximate 7 GiB CPU / 2.5 GiB GPU host working-memory budget at 16K context.
GPU loading also touches reclaimable memory-mapped model pages and can show
higher host RSS. These checks
are snapshots, not reservations; they cannot prevent unrelated workloads from
consuming memory afterward. GPU allocation failure skips the summary.

The runtime counts the chat-template tokens before generation and reserves 4096
output tokens plus 64 tokens of margin. Oversized transcripts are skipped with a
warning, not truncated. Chunking is not implemented in this pilot. Truncated
responses, missing/out-of-order headings, empty sections, and reasoning markup
are rejected. Inference failure does not block publication of the transcript.
The output remains the existing `summary.md` artifact.

Both ExApp images gain **4,823,895,104 bytes of model weights**, plus runtime
libraries. The CUDA base uses CUDA 12.8.1 to support Blackwell; its driver and STT
compatibility must be included in image validation before release. CUDA compute
targets are 7.5, 8.0, 8.6, 8.9, 9.0, 10.0, and 12.0. Full 128K context is not a
promise for an 8 GiB card. See the [measured investigation](./proposals/ling-3.0-tiny-summarization.md).

The standalone operator image bundles the CPU runtime and downloads the model
once into its persistent cache when local mode is first enabled, matching its STT
model lifecycle. CLI users can build a runtime with
`bash deployment/llm/build.sh cpu /absolute/path/to/llm` (requires CMake, Ninja,
a C++ compiler, curl and OpenSSL development files), then set
`CASSINI_LLM_SERVER=/absolute/path/to/llm/llama-server` and
`CASSINI_SUMMARY_BACKEND=local`. CUDA builds use `cuda` and need a CUDA 12.8
development toolkit. They use the same descriptor and checksum as the images.

Public assets are pinned in `internal/transcribe/ling-model.json` and
`deployment/llm/build.sh`. Downloads use staging, checksum and size verification,
and a shared model-cache lock. A declared-but-missing/corrupt image model fails
instead of downloading a replacement. The model is MIT licensed; image layers
include its descriptor and llama.cpp's license and source revision.
