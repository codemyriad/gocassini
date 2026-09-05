# Ling summary evaluation

`evaluate.py` sends only generated synthetic transcripts. It reads Cassini's
actual summary prompt/template files and formats turns as `speaker: text`, just
like `formatTranscriptForSummary`. `--local-rules` adds the local pilot's grounding
instructions. Results include complete requests, responses, inference timings,
wall-clock latency, finish reasons, and exact heading checks. Heading checks do
not establish factuality: manually inspect the summaries against the fixtures.

Start the pinned runtime and checksum-verified model described in
[`docs/local-summaries.md`](../../docs/local-summaries.md), with alias
`ling-3.0-tiny`, one slot, 16384 context, thinking disabled, and loopback binding.
The standalone synthetic runner expects an unauthenticated test endpoint; the
production integration always starts its own authenticated endpoint.

```sh
python3 harness/ling-summary/evaluate.py --url http://127.0.0.1:18089 --output /tmp/ling-baseline
python3 harness/ling-summary/evaluate.py --url http://127.0.0.1:18089 --output /tmp/ling-publisher --sampling publisher
python3 harness/ling-summary/evaluate.py --url http://127.0.0.1:18089 --output /tmp/ling-grounded --local-rules
python3 harness/ling-summary/evaluate.py --url http://127.0.0.1:18089 --output /tmp/ling-long --local-rules --case near_limit
```

The short cases cover revised decisions, missing owners/dates, Italian, a meeting
with no project discussion, and an instruction quoted inside a meeting. The
long case adds 350 synthetic status observations before the final corrections.
It is a context stress fixture, not representative meeting-duration data.
Prompt caching is disabled and seed 42 is requested. CPU/GPU numerical differences
can still change the output. Performance is host/run-specific; compare both
prompt-processing and output throughput.

For a real test of the Go lifecycle, token-budget guard, and local-only routing:

```sh
cd cassini-go-recorder
CASSINI_TEST_LING_SERVER=/absolute/path/to/llama-server \
CASSINI_TEST_LING_MODEL=/absolute/path/to/Ling-3.0-tiny-Q4_K_M.gguf \
CASSINI_TEST_LING_DEVICE=cpu \
go test ./internal/transcribe -run '^TestBundledLingIntegration$' -v -count=1
```

Use `cuda` with a CUDA-enabled runtime for the GPU test. This test creates a
temporary read-only model reference, forbids downloads, and never reads recordings.
Set `CASSINI_TEST_LING_OUTPUT` to retain the generated synthetic summary.
Ordinary tests use subprocess/HTTP fixtures and require no model or GPU.
