# Ling-3.0-tiny for local meeting summaries

Investigation date: 2026-09-05. Status: measured CPU/CUDA pilot with an opt-in bundled implementation. Runtime feasibility passed; the factuality gate did not. No production deployment changed.

## Recommendation

Bundle **llama.cpp and the pinned Q4_K_M GGUF** for opt-in evaluation on CPU and CUDA. Keep the existing remote/off behavior by default. The model fits Roy's RTX 5060 and is fast, but it invents some owners and follow-ups even with additional grounding rules. Do not make it the default until factuality and longer meeting coverage improve. Compare Q5 or a stronger local model before claiming quality readiness.

The model is an MIT-licensed text model with 7.9B total parameters and 1.3B active per token. It summarizes the transcript after speech recognition; it does not replace Parakeet. Sparse activation reduces computation, but all expert weights still need storage and accessible memory. The publisher's general reasoning benchmarks do not establish meeting-summary quality. [Model card](https://huggingface.co/inclusionAI/Ling-3.0-tiny)

## Measured pilot on Roy

The session exposes a Ryzen 5 7600 (8 online logical CPUs), 12 GiB RAM, and an
RTX 5060 with 8151 MiB reported VRAM. An earlier out-of-session host check showed
30 GiB; that is not the memory available to this session. Baseline GPU allocation
was 87 MiB, with no active compute processes. The session's ancestor cgroup also
imposes a 6 GiB memory limit, which the final RAM check detects independently of
the 12 GiB `/proc/meminfo` figure. No meeting recordings were used.

Both final CPU and GPU comparisons use source
`6a1a922d269908a29cbd4b49c27e6a8e7fd10fae` (llama.cpp b10819), Q4_K_M revision
`56a85ebe2d6d9d2ad6ad00a5014b13667153e236`, and model SHA-256
`a21f717779203d86b53996239c4903941858b938c50e85b03e6a981132b5621a`.
The 4,823,895,104-byte download passed full checksum verification. CPU binaries
were built with the actual image builder. The CUDA module was built from the
same source with CUDA 12.8.93 targeting SM120, then loaded by that runtime.
The complete multi-architecture CUDA image and its STT compatibility are not
validated by that narrower runtime test.

| Measured workload, thinking off | CPU | RTX 5060 CUDA |
| --- | --- | --- |
| Four short synthetic cases, local grounding rules | 6.05–13.25 s | 0.81–2.23 s |
| Prompt processing, same short cases | 151–192 tokens/s | 1611–4127 tokens/s |
| Output generation, same short cases | 40–46 tokens/s | 185–188 tokens/s |
| 9737-token synthetic context fixture | 89.28 s; 131 prompt tokens/s; 25 output tokens/s | 3.94 s; 5691 prompt tokens/s; 182 output tokens/s |

These are single runs, not statistical benchmarks. HTTP times exclude model
loading and checksum verification. The standalone CPU server became ready in
about 5.9 seconds; the first exploratory CUDA server in about 8 seconds. The
complete Go CPU summary lifecycle (including verification, load, and teardown)
took 26.3 seconds while CUDA compilation was also running, so it is a functional
measurement rather than a clean CPU speed comparison. CPU RSS high-water mark
was approximately 5.60 GiB across short tests and 5.73 GiB for the long test. The pinned CUDA server's host
RSS high-water mark was approximately 4.82 GiB during load, falling to about
0.93 GiB afterward. Total GPU memory observed after its requests was 4954 MiB;
this is an observed allocation, not a sampled peak.

All four short cases and the long CUDA fixture preserved the seven headings.
That does **not** establish factual correctness:

- With Cassini's unchanged prompt, both temperature zero and publisher sampling
  assigned the unowned security review to Alice and the undecided budget to Bob.
- Added local grounding instructions removed those invented named owners in the
  short correction case, but still turned the undecided budget into an action
  and suggested an unsupported immediate follow-up.
- At 9737 prompt tokens, the pinned CUDA runtime again assigned the security
  review to Alice. It summarized 350 numbered status observations as checks
  0–100. The final launch date and PostgreSQL choice were retained.
- Italian and quoted-instruction cases preserved the output structure. They are
  smoke tests, not a multilingual or prompt-injection security evaluation.

Reproducible fixtures, full response/timing capture, compact measurements, and
representative outputs live in [harness/ling-summary](../../harness/ling-summary).
The exploratory GPU baseline used an older runtime (b10795, image digest
`sha256:fc76b629103635823c4ba847acc493d09da5ad279c3de9ebe2d6338770ad120a`);
its results are identified separately and are not the pinned-source comparison
above. Docker GPU launch failed on this host's cgroup device hook, so the runtime
was tested directly with existing CUDA libraries; no host daemon/driver settings
were changed. Vulkan found no usable device in this session.

## Implemented opt-in integration

The draft bundles the model in the same read-only ExApp model root as STT and
builds CPU/CUDA runtimes into their respective images. The standalone operator
bundles its runtime and caches the model on demand, like its STT models.
`CASSINI_SUMMARY_BACKEND=local` selects an authenticated, ephemeral loopback
server launched after this build's recognizers close. It ignores remote keys,
URLs and proxies, never follows redirects, and exits on completion/failure or
recorder death on Linux. Shared-cache local runs serialize, and the operator's
existing admission lock already serializes complete builds. RAM checks include
host and cgroup v2 headroom; they are not memory reservations.

The Go integration checks actual chat-template token counts before generation,
reserves output space, and rejects overflow, truncation, reasoning markup and
invalid/empty sections. Summary failures leave transcripts publishable. Runtime
and model settings, download policy, image size impact and the unchanged default
are documented in [local summaries](../local-summaries.md).

The transcribe suite, subprocess lifecycle/failure tests, race checks, image
fetcher integrity checks and ExApp manifest checks passed. Real CPU Go inference
and its oversized-input guard passed before the final RAM admission check was
added. The conservative CPU admission budget now skips loading within this
6 GiB session limit; a host with enough headroom is needed to rerun the complete
CPU integration with that check enabled. The final CUDA Go integration passed
in 8.05 seconds including model verification/loading, summary generation and
teardown; the summary plus oversized-input rejection test took 12.18 seconds.
The complete CUDA image, Q5 comparison,
real 15/60/120-minute meetings, and coexistence with independent recording/STT
workloads remain release/adoption checks. This pilot deliberately does not add
chunking or claim that heading validation prevents semantic hallucinations.

## CPU and GPU options

| Target | Proposed runtime | Assessment |
| --- | --- | --- |
| x86/ARM CPU | llama.cpp CPU build, GGUF Q4_K_M or Q5_K_M | First baseline; measure long-prompt processing as well as generation. |
| NVIDIA GPU | llama.cpp CUDA build, same GGUF | Preferred first accelerated path; full offload if memory permits, partial offload otherwise. |
| AMD GPU | llama.cpp Vulkan, with HIP as a separate candidate | Validate the exact device, driver, quantization and context before shipping. |
| Apple Silicon | llama.cpp Metal candidate; experimental Ollama MLX alternative | Unified memory is useful; neither Mac performance nor compatibility was tested here. |
| Dedicated NVIDIA inference service | SGLang BF16/FP8/INT4; vLLM alternative | Consider when batching/concurrency warrants a separate Python/CUDA serving stack. |

These llama.cpp backend families are documented upstream. Architecture support alone does not certify every backend/device combination. [llama.cpp](https://github.com/ggml-org/llama.cpp)

BailingMoE3 support, including Tiny's Q-LoRA path, merged into upstream llama.cpp on **August 17, 2026**. Instructions requiring the old unmerged branch are stale. Pin a tested post-merge commit/release and matching GGUF conversion, rather than following a moving branch in deployment. [Merged PR](https://github.com/ggml-org/llama.cpp/pull/26608)

The conversion author's repository supplies GGUFs and reports CPU/CUDA checks, including RTX 3060/4070. These are community conversions, separate from inclusionAI's original checkpoints. Record the model revision and SHA-256 in the pilot. [GGUF repository and validation](https://huggingface.co/bloomer010/Ling-3.0-tiny-GGUF)

Memory planning below is an **engineering starting budget**, not measured peak usage or a purchasing recommendation:

| Format | Weight storage scale | Initial capacity target for a short-context, single-request pilot |
| --- | --- | --- |
| GGUF Q4/Q5 | Approximately 5–6 GB, depending on quant | 16 GB host RAM; 8 GB GPU candidate, 12 GB gives more headroom |
| FP8 | Approximately 8 GB plus higher-precision tensors | 12–16 GB GPU candidate, subject to kernel support |
| BF16 | Approximately 15.8 GB by parameter-count arithmetic | 24 GB GPU candidate or 32 GB host RAM |

Caches, compute buffers, context length, parallel requests and Parakeet increase requirements. Full 128K context is not implied by these targets. SGLang lists approximately 15.8/7.9/5.8 GB for BF16/FP8/INT4 and requires its dedicated runtime for the documented INT4 backend combinations. [SGLang cookbook](https://docs.sglang.io/cookbook/autoregressive/InclusionAI/Ling-3.0-tiny)

The **original feasibility-review workstation**, not Roy, exposes a Ryzen AI 9 HX PRO 370 / Radeon 890M, 12 physical CPU cores, approximately 58 GiB RAM and `/dev/dri/renderD128`. Available RAM was approximately 19 GiB during inspection. Neither `llama-server` nor `nvidia-smi` was found on PATH. CPU evaluation is plausible here; Vulkan needs device/runtime checks. No CUDA benchmark can be claimed from this inspection.

## Integration at the inspected baseline (2f39d23)

- [`llm.go`](../../cassini-go-recorder/internal/transcribe/llm.go) already calls `/chat/completions` with a model and system/user messages. It fixes temperature at zero, output at 4096 tokens, and timeout at 240 seconds. It reads only `message.content`, ignores `finish_reason` and usage, and requires a nonempty API key even for a local endpoint.
- [`summary.go`](../../cassini-go-recorder/internal/transcribe/summary.go) sends the complete speaker-labelled transcript in one request. There is no token budgeting or chunking. It removes outer Markdown fences and rejects empty output, but does not validate the seven headings or factual grounding.
- [`transcribe.go`](../../cassini-go-recorder/internal/transcribe/transcribe.go) excludes low-confidence words before summarization. Configuration precedence is `OPENROUTER_BASE_URL` → `LLM_BASE_URL`; model precedence is `SUMMARY_MODEL` → `LLM_MODEL` → default. API failure logs a warning and publication proceeds without a summary.
- [`deployment/compose.yml`](../../deployment/compose.yml) already forwards these settings. The summary remains a `summary.md` sidecar; this integration does not require changing the transcript contract.

## Initial standalone pilot recipe

The original feasibility recipe follows; the implemented lifecycle and settings are documented in [local summaries](../local-summaries.md). Build a pinned post-merge llama.cpp checkout with `cmake -S . -B build -DCMAKE_BUILD_TYPE=Release`, then `cmake --build build -j --target llama-server`. Use a separate build directory with `-DGGML_CUDA=ON` for NVIDIA or `-DGGML_VULKAN=ON` for Vulkan; those builds need their backend development dependencies.

Download a revision-pinned `Ling-3.0-tiny-Q4_K_M.gguf` from the linked conversion repository and verify its checksum. Start the CPU baseline:

```sh
./build/bin/llama-server \
  -m /models/Ling-3.0-tiny-Q4_K_M.gguf \
  --alias ling-3.0-tiny \
  --host 127.0.0.1 --port 8080 \
  --ctx-size 16384 --parallel 1 --n-gpu-layers 0 \
  --jinja --chat-template-kwargs '{"enable_thinking":false}' \
  --reasoning-format deepseek
```

For a GPU build, replace `--n-gpu-layers 0` with `--n-gpu-layers 999`, check startup logs for actual offload, and measure memory. If full offload fails, lower the layer count. The template kwarg disables thinking; the reasoning parser separates thoughts if emitted. Setting reasoning format to `none` would leave thoughts in content and is not a substitute for disabling thinking. [Server options](https://raw.githubusercontent.com/ggml-org/llama.cpp/master/tools/server/README.md)

For Cassini running directly on the same host:

```sh
export OPENROUTER_BASE_URL=http://127.0.0.1:8080/v1
export OPENROUTER_API_KEY=local-pilot
export SUMMARY_MODEL=ling-3.0-tiny
export CASSINI_SUMMARY_DISABLED=0
```

`local-pilot` satisfies the existing client gate for a loopback server without authentication; it is not a real credential. Set the URL explicitly together with it, since a key without a URL activates the current OpenRouter default. A container's loopback does not reach the host: for Compose, run the inference service on the same private network and use `http://ling-summary:8080/v1`, with the server listening on its container interface and no publicly published inference port. Use a matching server/client key if authentication is enabled.

This should exercise the current integration without Go changes. Cassini's explicit temperature zero overrides server sampling defaults; it does not test the publisher's recommended temperature 1.0 / top-p 0.95 / top-k 20. Compare those settings through a direct evaluation client, then make model-specific sampling configurable if beneficial. [Publisher sampling guidance](https://huggingface.co/inclusionAI/Ling-3.0-tiny)

## Documentation conflicts and runtime risks

The released configuration sets `max_position_embeddings=131072` and `num_nextn_predict_layers=0`. Do not copy the model card's NEXTN/256K launch command into this pilot. The current SGLang cookbook explicitly says Tiny has no MTP draft layer and specifies `deepseek-r1` for reasoning parsing, unlike the card's `ling3` example. Start at native context or below, without speculative decoding. [Checkpoint configuration](https://huggingface.co/inclusionAI/Ling-3.0-tiny/raw/main/config.json), [runtime guidance](https://docs.sglang.io/cookbook/autoregressive/InclusionAI/Ling-3.0-tiny)

Ollama's linked MLX implementation is still an open PR at inspection time, and the card documents a custom Apple Silicon build. Do not assume a stock Ollama install supports this recipe. [Ollama PR](https://github.com/ollama/ollama/pull/17643)

An open, unconfirmed llama.cpp issue reports repetitive output around 30K context during tool calling on an RX 570/Vulkan configuration with quantized caches. This is not evidence that ordinary meeting summaries fail, but it makes long-context regression testing necessary, particularly before recommending AMD acceleration. [Issue #27876](https://github.com/ggml-org/llama.cpp/issues/27876)

## Changes needed before a production default

1. Add explicit local/remote summary configuration, optional local authentication and a configurable deadline. Preserve existing environment compatibility and ensure local mode never falls back to a remote URL.
2. Add per-model thinking/sampling settings. Check `finish_reason`, reject incomplete or repetitive output, and validate required Markdown sections before writing the artifact.
3. Count tokens with the serving tokenizer, including template and output reserve. At 16K context with the existing 4096-token output cap, input must fit in the remaining budget. For overflow, extract facts from bounded transcript chunks with speaker/segment references, then synthesize; preserve ownership, uncertainty and later corrections across chunks.
4. Schedule inference with STT memory use in mind. Start with one summary slot; an always-resident GPU server can occupy VRAM even while idle. Consider on-demand loading/unloading or a separate device. Make readiness, resolved device, model revision and summary failures observable.
5. Update deployment packaging and privacy documentation for local endpoints. Keep failure nonfatal to transcript publication and record summary model/prompt provenance for evaluation.

## Evaluation and adoption gate

Use synthetic or explicitly selected local transcripts; do not automatically send recordings to a hosted comparison service. Test 15/60/120-minute equivalents, short and near-limit token counts, the deployment's languages, overlapping speakers, uncertain ASR, corrected decisions, missing owners/dates, and instruction-like statements spoken during meetings.

Compare CPU Q4, GPU Q4 and Q5 at identical prompts and settings. Use a higher-precision local reference where feasible. Measure cold load, prompt tokens/second, generation tokens/second, full summary latency, peak RAM/VRAM and interference with transcription/recording. Report both prefill and decode: a fast token generator can still be slow on a long meeting transcript. Choose latency targets from the deployment's actual requirements; no measured throughput is available from this investigation.

Require every fixture to preserve the template, reject truncation, avoid invented decisions/owners/dates, and keep transcripts publishable when inference is unavailable. Human-review action-item precision/recall and decision coverage against annotated facts. Exercise timeout, context overflow, empty content, reasoning separation and local-only routing through HTTP integration tests when implementing the client changes.

Decision: **proceed to a controlled CPU/GPU pilot; defer making Ling the default until task quality, long-context reliability and resource contention are measured.**
