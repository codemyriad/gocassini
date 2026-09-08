# Transcription Pipeline Architecture

## Purpose

This document describes the post-recording transcription pipeline that turns a finished `.mkv` recording into a meeting artifact bundle. It is the counterpart to [`architecture-overview.md`](architecture-overview.md) — that doc covers live capture and remux; this one picks up where the `.mkv` is written and ends where the bundle is ready for the viewer or the portable `.opus` packer.

## Where it lives

| Layer | Files | Owns |
|---|---|---|
| CLI surface | `cmd/cassini/main.go`, `internal/cassini/cli.go`, `internal/cassini/build.go` | Argument parsing, `cassini build` subcommand, output-path policy |
| Pipeline orchestration | `internal/transcribe/transcribe.go` | The 10-step build flow described below |
| Audio + STT | `internal/transcribe/audio.go`, `models.go`, `stt.go` | ffmpeg probe/mix, model download/cache, sherpa-onnx recognizer |
| Segmentation + format | `internal/transcribe/format.go` | Segment assembly, JSON/VTT emitters, manifest writer |
| LLM integration | `internal/transcribe/llm.go` | OpenAI-compatible HTTP client shared by summaries and insights |
| Decoder hints | `internal/transcribe/hotwords.go` | Turns the vocabulary and participant names into sherpa-onnx contextual biasing |
| Summary generation (V4) | `internal/transcribe/summary.go` | System-prompt assembly and transcript flattening; prompt bytes come from the workflow registry |
| Template (V0 contract) | `internal/insight/workflows/prompts/summarise-template.v0.md` | The summary's section structure — single source of truth |

## End-to-end flow

`BuildMeetingArtifact` in `transcribe.go` runs the following steps in order. Step 9 is optional and gated on LLM configuration; everything else runs unconditionally.

| # | Step | What happens | Output written |
|---|---|---|---|
| 1 | Probe MKV | `ProbeMKV` reads stream layout and total duration via ffprobe | — |
| 2 | Mix to WebM | `MixDownToWebM` produces a single mono 48 kHz Opus track from all speaker streams | `meeting.webm` |
| 3 | Hash audio | `PCMsha256FromWebM` computes the decoded-PCM SHA-256 for integrity tracking | — |
| 4 | Ensure model + VAD | `EnsureModel` and `EnsureVAD` download/verify STT and VAD models in the cache dir | — |
| 5 | Create recognizer | `NewRecognizer` loads the sherpa-onnx model on the chosen device (`cpu`/`cuda`), biased towards configured vocabulary and participant names when supported and enabled (see Decoder hints below) | — |
| 6 | Per-speaker transcribe | For each input stream: extract floats, transcribe, assemble into segments | — |
| 7 | Merge + sort + write words | `MergeAndSortSegments` then `writeTranscriptWithHash(version="transcript.words.v1")` | `transcript.words.v1.json` |
| 8 | Captions | `WriteCaptionsVTT` renders the canonical transcript as WebVTT | `captions.vtt` |
| **9** | **Meeting summary *(optional)*** | **`writeSummaryArtifact` calls `BuildMeetingSummary`, which splices the registry's V0 template into its system prompt, and writes the model's markdown output as a sidecar** | **`summary.md`** |
| 10 | Manifest | `WriteManifest` emits the artifact catalog with provenance for STT, decoder hints, and the summary | `manifest.json` |

Step 9 summarises the canonical transcript, minus words the crosstalk gate marked low-confidence.

## Output bundle

After a successful build, `outputDir` contains:

```text
<meeting-output>/
  meeting.webm                    (always)
  transcript.words.v1.json        (always)
  captions.vtt                    (always)
  hotwords.txt                    (only when decoder hints are applied)
  summary.md                      (only if step 9 ran)
  manifest.json                   (always)
```

The viewer (`cassini-viewer`) treats `summary.md`, `captions.vtt`, and `chapters.vtt` as **optional** sidecars — see `cassini-viewer/scripts/demo-data-pull.mjs:144`. A 404 is tolerated; the UI falls back to a "no summary" state. This is what makes step 9's warn-and-skip semantics safe end-to-end.

The portable `.opus` packer (`internal/cassini/portable_meeting.go`) embeds the audio, the transcripts, and — when the bundle carries one — `summary.md` as a manifest attachment plus summary metadata (`format`, `templateVersion`, `model`), so a published file carries its summary inside.

For files sealed **before** summaries existed, `cassini meetings summarize <meeting.opus>` backfills one without re-running transcription: it reads the transcript back out of the file, makes the same single LLM call `cassini build` would make (configured by the same environment variables), and rewrites the metadata through the stage-verify-rename path with the audio bytes untouched. A file that already carries a summary is skipped unless `--force` replaces it, and a backfilled summary is recognisable by `provenance.meetingSummary.source = "backfill"` in the manifest.

## LLM integration

There is one HTTP client, `ChatCompletion(ctx, cfg, system, user)` in `llm.go`,
shared by automatic summary generation and on-demand insights. In the build
pipeline it is called as follows:

| Caller | Config field | Default model | Env override |
|---|---|---|---|
| `BuildMeetingSummary` (step 9) | `BuildConfig.SummaryLLM` | inherits from `LLM_MODEL`; falls back to `openai/gpt-4o-mini` | `SUMMARY_MODEL` |

The summary call reads the shared auth (`OPENROUTER_API_KEY`) and base URL (`OPENROUTER_BASE_URL`, falling back to `LLM_BASE_URL`), unless given its own endpoint: `SUMMARY_BASE_URL` / `SUMMARY_API_KEY` / `SUMMARY_MODEL` override the shared values. An endpoint override brings its own key — the shared key is never sent to a different host. The operator emits these from its persisted LLM settings.

**The base URL is the switch, not the key.** `IsConfigured()` requires a base URL and an unset kill-switch; the API key is optional, because a self-hosted OpenAI-compatible server (llama.cpp, vLLM, Ollama) usually has none. When the key is empty the `Authorization` header is omitted entirely rather than sent as an empty bearer token, which some self-hosted servers reject. With no base URL, the summary step is skipped silently.

Summarisation is the only LLM step in the automatic build pipeline. On-demand
insights run separately through `cassini insight run`. Transcript text is never
rewritten by a model: the words a reader sees are the words the decoder
produced.

`CASSINI_SUMMARY_DISABLED=1` turns summaries off while keeping the endpoint configuration in place.

`SUMMARY_MODEL` overrides `LLM_MODEL` for the summary call, so a stronger model can be picked without changing the shared default.

### Failure semantics

- **Summary generation (step 9):** warn-and-skip only — no strict mode. If the LLM fails or returns empty, the pipeline succeeds without a `summary.md`. This matches the V4 acceptance criterion that disabling summary generation does not break the pipeline.

## Configuration surface

The fields in `BuildConfig` (in `transcribe.go`) relevant to summary generation
and decoder hints are shown here (abridged):

```go
type BuildConfig struct {
    Device       string    // "auto" | "cpu" | "cuda"
    ModelID      ModelID   // STT model
    CacheDir     string    // model cache root
    SummaryLLM   LLMConfig // step 9
    Vocabulary   []string  // preferred spellings, biases the decoder
    NumThreads   int
}
```

Relevant environment variables read by `DefaultBuildConfig()` include:

| Env var | Maps to |
|---|---|
| `OPENROUTER_API_KEY` | `SummaryLLM.APIKey`; also implies the OpenRouter base URL when neither base URL is set |
| `OPENROUTER_BASE_URL` (or `LLM_BASE_URL`) | `SummaryLLM.BaseURL` |
| `LLM_MODEL` | `SummaryLLM.Model` until overridden |
| `SUMMARY_BASE_URL` / `SUMMARY_API_KEY` / `SUMMARY_MODEL` / `SUMMARY_TIMEOUT_SEC` / `SUMMARY_MAX_TOKENS` | summary-only endpoint, key, model, timeout, and response-token overrides |
| `CASSINI_LLM_TIMEOUT_SEC` | `SummaryLLM.TimeoutSec` (default 900; raise for CPU-bound local models) |
| `CASSINI_LLM_MAX_TOKENS` | `SummaryLLM.MaxTokens` (default 4096) |
| `CASSINI_SUMMARY_DISABLED` | `SummaryLLM.Disabled` |
| `CASSINI_TRANSCRIPTION_TERMS` | `Vocabulary` (JSON array of preferred spellings) |
| `CASSINI_STT_HINTS_DISABLED` | turns decoder biasing off while keeping the vocabulary |
| `CASSINI_STT_HINTS_SCORE` | overrides the hotword boost (default 2.0) |
| `CASSINI_CACHE_ROOT` | `CacheDir` (default cache location) |

## How V4 (D-242) fits in

V4 introduces step 9 — meeting summary generation — and nothing else in the pipeline shape. Specifically:

- **No new artifact contract.** `summary.md` is plain markdown. The contract is the V0 template in `internal/insight/workflows/prompts/summarise-template.v0.md`, which the summary step reads from the workflow registry (D-718) rather than embedding a second copy — so the bytes the pipeline sends and the bytes `cassini insight workflows` lists are the same file, and a change to it is caught by the prompt gate in `lint.yml`.
- **No new dependencies.** Reuses `ChatCompletion` from `llm.go`.
- **Summary provenance travels with the artifact.** `manifest.json` lists `summary.md` and records the model that produced it; the portable packer carries both into the sealed `.opus`.
- **No new CLI flags.** Operators set an endpoint (`LLM_BASE_URL`, or `OPENROUTER_API_KEY` which implies the OpenRouter one) and optionally `SUMMARY_MODEL`, and the summary path turns on automatically. With no endpoint, step 9 is skipped silently; `CASSINI_SUMMARY_DISABLED=1` also skips it.
- **Test mocking** uses the `func` package-var pattern (`buildMeetingSummaryFn`), so no live LLM is called in CI.

The acceptance criteria from D-242 map to:

| AC | Where it lives |
|---|---|
| Pipeline produces summary in V0 template format | `summary.go` → `summarySystemPrompt` splices the registry's `summarise` prompt (`internal/insight/workflows/prompts/summarise*.v0.md`); tests pin both ends |
| Summary renders correctly in V3 viewer | `summary.md` written next to other artifacts; viewer's optional-sidecar logic at `cassini-viewer/scripts/demo-data-pull.mjs:144` already handles it |
| Disabling summary generation does not break pipeline | `SummaryLLM.IsConfigured()` gate in `writeSummaryArtifact`; warn-and-skip on errors |
| Summary written alongside transcript artifacts | `os.WriteFile(filepath.Join(outputDir, "summary.md"), …)` in `writeSummaryArtifact` |

## Reading order if you are new to the pipeline

1. `cmd/cassini/main.go` and `internal/cassini/build.go` — how the CLI reaches the pipeline
2. `internal/transcribe/transcribe.go` — the orchestrator, top-to-bottom
3. `internal/transcribe/format.go` — the artifact contracts (transcript JSON, captions, manifest)
4. `internal/transcribe/llm.go` — the LLM HTTP layer
5. `internal/transcribe/summary.go` + `internal/insight/workflows/prompts/summarise*.v0.md` — step 9 (V4)
6. `internal/portable/manifest.go` — the portable `.opus` manifest schema, which downstream code packs into a single file

## Decoder hints (the participant and project vocabulary)

The operator-configured vocabulary reaches the recorder as
`CASSINI_TRANSCRIPTION_TERMS`, a JSON array of preferred spellings. It is
applied to the **decoder**, not to finished text.

Transducer models decode with `modified_beam_search` by default, whether or not
a vocabulary is set. Hotwords are only read under beam search, and a decoder
that changed under the operator depending on whether a text box happened to be
empty would be worse than one that is simply stable. When a vocabulary is set
and the model can take it, `hotwords.go` writes a hotwords file into the build
directory and the recognizer builds a context graph from the terms. A term is
only ever emitted where the acoustics already support it, so the vocabulary
cannot introduce a word nobody said. With non-empty terms,
`CASSINI_STT_HINTS_DISABLED=1` restores the previous `greedy_search` decoder as
well as disabling the hints.

The CTC tier keeps greedy search. sherpa-onnx has no hotword support for CTC, so
the wider beam would cost decode time and buy nothing.

Applying hints requires both of these model properties:

1. the model must be a transducer. The `nemo_ctc` tier cannot be biased.
2. the model bundle must ship `bpe.vocab`, and `modeling_unit` must be `bpe`.
   These two are the dangerous pair: with an empty `bpe_vocab` sherpa fails to
   construct the recognizer (loud), but with `modeling_unit` left unset the
   terms are tokenised as whole words, fail to encode, and the biasing is
   silently a no-op while everything looks healthy. Upstream publishes some
   Parakeet archives without `bpe.vocab`; regenerate those bundles with
   upstream `scripts/nemo/generate_bpe_vocab.py` rather than deriving a
   flat-score substitute at runtime.

When terms exist and either requirement does not hold, the build records
`provenance.speechToText.hints` with
`applied: false` and a reason, and decodes unbiased. A vocabulary that could not
be applied is always visible in the manifest rather than silently ignored.
