# Cassini transcription benchmark

This is the repeatable, private-audio benchmark for short interjections,
overlap, cross-track bleed, and vocabulary spelling. It has three CUDA speech
decoders:

- Voxtral Mini 3B offline, with ordinary transcription, vocabulary prompts,
  and three synchronized tracks for attribution review.
- Voxtral Mini 4B Realtime, parameterized across supported transcription delay
  values.
- Parakeet TDT 0.6B v3 through sherpa-onnx, comparing greedy search,
  modified-beam search, and conservative hotword scores.

The eight WAVs are private and ignored by git. Their immutable names, byte
lengths, whole-file and decoded-PCM SHA256 hashes, true sample counts, roles, expected terms, and absent
control terms are committed in `fixtures/manifest.v1.json`. The verifier parses
RIFF chunks itself: FFmpeg streaming WAV headers with either `0x7fffffff` or
`0xffffffff` as the declared data length are measured from actual PCM bytes,
not misreported as hours-long audio.

## Targets:

- `make fixtures` copies the known `/tmp/cassini-vast-bench-20260828/audio`
  fixture set, then verifies it. Copying prevents later source rewrites from
  moving the benchmark target.
- `make verify` checks every WAV against the immutable manifest.
- `make test syntax` runs dependency-free tests and syntax checks; no ASR runs.
- `make bootstrap-offline` or `make bootstrap-realtime` creates an isolated
  Voxtral environment while retaining the CUDA-enabled PyTorch from the image.
- `make run-voxtral-offline` and `make run-voxtral-realtime` run CUDA-only
  model matrices.
- `make run-parakeet` runs the bounded hotword matrix with explicit runtime
  inputs described in `PARAKEET_RUNTIME.md`.
- `make score` reports expected-term recall and fails on absent-control
  injection or speech attributed to the two Gemini-reviewed bleed-only tracks.
  It recursively discovers supported result schemas, including collected Vast
  worker directories.

Quick local setup:

```sh
cd harness/transcription-bench
make fixtures test syntax
make bootstrap-offline
make run-voxtral-offline VOXTRAL_OFFLINE_ARGS='--suite baseline'
make score
```

All model runners refuse CPU inference. They cap host math libraries at one
thread and record device, CUDA/PyTorch versions, model-load time, RTF, and peak
CUDA allocation. Direct dependencies and default Hugging Face model revisions
are pinned; local/manual runs can still override both for comparison. The two
pinned revisions use explicit Transformers-file allowlists, so the duplicate
`consolidated.safetensors` weights are never downloaded. `--snapshot-dir`,
`--model-cache-dir`, and `--local-files-only` make cache behavior explicit.
Run `nvidia-smi` before starting locally and do not overlap a local run with
another GPU workload. The small one-thread Silero VAD used by the Parakeet
harness is segmentation, not speech decoding.

Each Voxtral invocation atomically updates transcript-free timing sidecars in
`results/.telemetry/`: `<output>.progress.json` and
`<output>.first-result.json`. The first-result file appears after the first
condition is decoded, includes runner-start-to-first-result time, and never
contains transcript or prompt text. Progress becomes `completed` only after
the final structured result is durable.

## Fast parameter iteration

The fixture selector and output path are ordinary arguments, so short loops do
not require editing code:

```sh
.venv-voxtral-offline/bin/python runners/voxtral_offline.py \
  --manifest fixtures/manifest.v1.json --audio-dir fixtures/audio \
  --suite vocabulary --fixtures 2026-03-06_0742.wav \
  --approved-term 'RAM spikes' --output results/offline-vocab.json

.venv-voxtral-realtime/bin/python runners/voxtral_realtime.py \
  --manifest fixtures/manifest.v1.json --audio-dir fixtures/audio \
  --delays 240,480,960,2400 --output results/realtime-delay-grid.json
```

Score any result set explicitly:

```sh
python3 scripts/score_results.py --manifest fixtures/manifest.v1.json \
  --result results/offline-vocab.json --result results/realtime-delay-grid.json \
  --output results/score.json --fail-on-forbidden --fail-on-false-speech
```

For WER and overlap evaluation, create an ignored private
`fixtures/ground-truth.local.json` from `fixtures/ground-truth.example.json`
and validate its shape against `fixtures/ground-truth.schema.json`. The private
file is keyed by fixture basename and carries a reviewed `referenceTranscript`
plus optional `overlapCount` and `directSpeech`:

```sh
make score GROUND_TRUTH=fixtures/ground-truth.local.json
```

Score one collected Vast run without enumerating workers:

```sh
make score RESULTS_DIR=results/vast/<run-id> \
  GROUND_TRUTH=fixtures/ground-truth.local.json
```

The scorer reports normalized token Levenshtein WER, exact aligned-token
recall, overlap-count agreement when the model emits structured overlap data,
and direct-speech accuracy. Ground-truth content stays local and is excluded
from both git and Vast payloads.

Three-track Voxtral results are normalized rather than treated as opaque chat
answers. Each result names its `challengeCase`, maps scoring to the first
meeting-mix input (`mixFixture`), and records the parsed `transcript`, Ivan's
`directSpeech` observation, and `overlapIntervals`/`overlapCount`. The original
model text and parsed object are retained for audit. Fenced JSON and harmless
surrounding prose are accepted; malformed JSON or missing fields remain
explicit parse/validation errors, with an empty transcript, so they cannot
accidentally receive credit. In private ground truth, put the reviewed mix
transcript and overlap count under `mix.wav` or `okay-mix.wav`; `directSpeech`
on those entries evaluates the named `directSpeechTrack` (currently Ivan), not
whether the meeting mix contains any speech.

Term recall is intentionally lightweight; it does not pretend to be WER. Empty
output is required for `expectedDirectSpeech: false` isolated tracks, making
the observed Voxtral `I'm sorry` / `Yeah!` bleed hallucinations an explicit
gate rather than an anecdote.

## Parallel Vast.ai matrix

`vast/matrix.py` uses only the standard library and `VAST_AI_API_KEY`. It never
prints or persists the key. API error bodies are suppressed because remote
instance responses can contain credentials. Workers are independent instances,
so model installs, downloads, and inference run in parallel without competing
for one RTX 5060.

Plan first (read-only and zero rental cost), inspect prices and selected GPUs,
then execute that exact plan:

```sh
export VAST_AI_API_KEY=... # keep this out of shell history where possible
python3 vast/matrix.py plan --matrix vast/matrix.example.json \
  --plan .vast-state/plan.json --jobs 4
jq '{runId, projectedDph, projectedBenchmarkCost, projectedMinimumLifecycleCost, workers: [.workers[] | {name, offer}]}' \
  .vast-state/plan.json
python3 vast/matrix.py execute --plan .vast-state/plan.json \
  --state .vast-state/state.json --audio-dir fixtures/audio \
  --output-dir results/vast --jobs 4
```

`execute` launches, waits for authenticated SSH, uploads one verified payload,
dispatches all workers concurrently, collects structured results, and destroys
the exact owned instances in `finally`. Teardown is the default even when a
worker fails. `--keep-instances` is the explicit, billing-continuing escape
hatch. Destruction only accepts IDs in the state file and first requires each
live label to exactly match `gocassini-tbench-<run-id>-<worker>`; it never does a
broad label or account deletion.

Voxtral instances receive a deterministic role-specific Vast `onstart` program
at create time. It contains only public package pins, model IDs, exact commits,
and file sizes: no Vast/OpenRouter/Hugging Face/repository credentials,
fixtures, vocabulary, participant names, or private ground truth. Rendered
scripts are rejected at 12 KiB and currently stay near 6.5 KiB. They install a
wheel-only 30-package offline or 40-package realtime overlay and download the
selected public model into `/workspace/.cassini-prewarm`, guarded by `flock`
and a configuration fingerprint. Python 3.11, Torch 2.8.0, CUDA 12.8, BF16,
every overlay version, inherited CUDA Torch, every model file size, and a
second local-only snapshot lookup must all validate before an atomic
`ready.json` appears. Failures produce a sanitized atomic `failed.json` and
bounded log tail.

The transfer is not serialized behind the multi-gigabyte model download. Once
all planned workers pass authenticated SSH readiness, dispatch uploads and
verifies the small private payload concurrently while `onstart` continues.
Only inference waits for a matching role and
fingerprint sentinel. The runner then uses the prewarmed versioned Python and
snapshot with `HF_HUB_OFFLINE=1`; the post-upload installer remains only as a
legacy/manual fallback. Model, revision, cache, telemetry, manifest, audio, and
output arguments are reserved in a Vast matrix so a hand-edited plan cannot
silently bypass this contract. The read-only plan records the exact prewarm
fingerprint; execution refuses if the renderer has changed and requires a new
plan rather than silently launching different dependencies.

The matrix requires a true aggregate `maxTotalCostUsd` lifecycle cap, chooses
distinct physical machine IDs, and re-checks live hourly prices before
dispatch. The conservative billing clock starts before the first create and
covers readiness, packaging/upload, remote preparation and inference, result
collection, and teardown. Every blocking phase is clamped to the same
wall-clock deadline and the last five minutes are withheld for exact-label
teardown. If the live aggregate hourly rate changes, the deadline is tightened.
An unavailable provider can of course prevent a destroy confirmation; that is
reported as incomplete cleanup rather than falsely claiming the cap or cleanup
was achieved. SSH readiness is an
actual authenticated `ssh ... true` probe: a public IPv4 `22/tcp` mapping is
preferred when Vast supplies one, with the Vast SSH proxy retained as a
fallback. Upload and collection try both endpoints and have keepalives plus
hard local timeouts, so a wedged transport cannot prevent default teardown.

State records create request/contract, provider-running, SSH-ready, payload
packaging/upload, remote preparation, prewarm-ready, runner start/end,
first-result, early collection, and confirmed destruction times. Bootstrap
`ready.json`, remote status JSON, and runner telemetry preserve remote phase
durations; host observation times remain the authoritative lifecycle clock.
This supports create-to-first-result, runner-to-first-result, prewarm time,
cache-hit, and estimated cost-to-first-result comparisons without assuming
provider and container clocks are perfectly synchronized.

For fastest unattended use, `run` plans and executes in one command:

```sh
python3 vast/matrix.py run --matrix vast/matrix.example.json \
  --audio-dir fixtures/audio --work-dir .vast-state \
  --output-dir results/vast --jobs 4
```

Vast offers can disappear between a read-only plan and create. This happened
in the 2026-08-28 run `20260828t081214z`: offer `47323102` returned HTTP 400
five seconds after planning while two sibling creates succeeded. Cleanup did
destroy both created instances. The all-in-one `run` now treats only definitive
HTTP 400/404/410 create rejection as a stale offer, reconciles and tears down
the exact run labels, then makes a fresh plan/run ID, at most three attempts.
Charges conservatively estimated for failed attempts are subtracted from the
same original cost cap. `execute` deliberately runs the inspected plan exactly
once and never silently substitutes an offer.

A timeout, invalid response, network error, or interruption during create is
not assumed to mean “not created.” The harness polls only the persisted exact
labels for a bounded five-minute provider-visibility window, records recovered
IDs, and performs exact-label cleanup. Such ambiguous outcomes are never
automatically retried. If reconciliation itself cannot complete, the error
names every pending label and the state file, says cleanup is unconfirmed, and
requires an explicit `recover` followed by `destroy`.

For recovery or inspection, the same lifecycle is available as separate
`launch`, `wait`, `dispatch`, `collect`, and `destroy` subcommands. Use the
persisted state file rather than copying instance IDs by hand. Standalone
`launch` requires the explicit `--manual-lifecycle` billing acknowledgment;
normal use should stay with default-teardown `execute`. `--extra PATH`
adds a local runtime tree under `/workspace/cassini-bench/extras/`; this is how
to supply a pinned Parakeet binary/model bundle to a Parakeet worker. Private
fixtures and extras disappear with the default instance teardown.

Each worker is copied through a unique local staging directory and published
under `results/vast/<run-id>/<worker>/` only after SCP and integrity validation
succeed. Voxtral publication requires the exact v2 result schema, successful
remote status, pinned model/revision, fixture-manifest hash, and matching
completed progress telemetry. Parakeet requires its worker-nested summary,
successful status, and the same fixture hash. Replacement is staged and
renamed as a whole, so retries cannot merge stale files from an earlier copy.
Successful state retains the validated bootstrap summary and fingerprint;
failure bundles retain bounded diagnostics under `prewarm/`.
In `run` and `execute`, a worker is collected immediately when it finishes,
while slower siblings continue. Final collection revalidates existing early
bundles before skipping them and retries invalid copies. During a manually
separated lifecycle, another terminal can retrieve any completed worker without
waiting for the matrix:

```sh
python3 vast/matrix.py collect --state .vast-state/state.json \
  --output-dir results/vast --worker offline-baseline
```

Collection refuses a named worker still marked running. A nonzero remote runner
status or validated prewarm diagnostic may be retained as a partial bundle,
but `.collection-integrity.json` marks it `partial`; it can never satisfy a
worker marked completed. If prewarm fails, its sanitized sentinels and the last
4 MiB of its log are placed under that worker's `prewarm/` result directory
before default teardown when SSH remains available.

Expected labels are persisted before create calls. If the client is killed in
the narrow interval before a create response is saved, `recover` queries only
the exact labels belonging to that run and records their IDs; `destroy` runs
that recovery automatically. After an abrupt client or host crash, use:

```sh
python3 vast/matrix.py recover --state .vast-state/state.json
python3 vast/matrix.py destroy --state .vast-state/state.json --jobs 4
```

The payload builder uses an explicit source allowlist. Extras reject symlinks,
special files, `.env`/SSH directories, private-key suffixes, and a payload
destination nested inside the extra tree.

For Parakeet, stage one explicit `parakeet-runtime/` directory with
`hotword-bench`, `dist/`, `model/`, and `silero_vad.onnx`, then use the checked
`vast/matrix.parakeet.example.json` plan and pass
`--extra /path/to/parakeet-runtime` to `execute`. The remote arguments refer to
the resulting `extras/parakeet-runtime/...` paths. Runtime input hashes are
recorded in the structured summary.

The REST flow follows Vast.ai's documented search (`POST /bundles/`), create
(`PUT /asks/{offer_id}/`), inspect (`GET /instances/{id}/`), and destroy
(`DELETE /instances/{id}/`) lifecycle. Search/create API calls are rate-gated;
SSH dispatch and collection remain parallel.

### MAI-Transcribe-2 through OpenRouter

With `OPENROUTER_API_KEY` set, run `python3 runners/openrouter_stt.py --manifest fixtures/manifest.v1.json --audio-dir fixtures/audio --output results/mai-transcribe-2.json`.
This uploads the private meeting fixtures to the hosted provider. The runner verifies fixture hashes, repairs streaming WAV headers without changing PCM, discards one full warm-up pass, and measures three sequential passes. It requests verbatim text and word timestamps without vocabulary hints. Timing includes upload and response; provider warm state is unobservable. Reported API cost includes warm-up. Only the first measured pass is exposed to the quality scorer. Local reference transcripts are model-adjudicated, not human ground truth.
