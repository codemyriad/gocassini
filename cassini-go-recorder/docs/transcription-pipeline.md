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
| LLM integration | `internal/transcribe/llm.go` | OpenAI-compatible HTTP client for the summary call |
| Summary generation (V4) | `internal/transcribe/summary.go` | System prompt assembly and transcript flattening; the prompt bytes come from the workflow registry |
| Template (V0 contract) | `internal/insight/workflows/prompts/summarise-template.v0.md` | The summary's section structure — single source of truth |

## End-to-end flow

`BuildMeetingArtifact` in `transcribe.go` runs the following steps in order. Steps 8 and 9 are optional and gated on LLM configuration; everything else runs unconditionally.

| # | Step | What happens | Output written |
|---|---|---|---|
| 1 | Probe MKV | `ProbeMKV` reads stream layout and total duration via ffprobe | — |
| 2 | Mix to WebM | `MixDownToWebM` produces a single mono 48 kHz Opus track from all speaker streams | `meeting.webm` |
| 3 | Hash audio | `PCMsha256FromWebM` computes the decoded-PCM SHA-256 for integrity tracking | — |
| 4 | Ensure model + VAD | `EnsureModel` and `EnsureVAD` download/verify STT and VAD models in the cache dir | — |
| 5 | Create recognizer | `NewRecognizer` loads the sherpa-onnx model on the chosen device (`cpu`/`cuda`) | — |
| 6 | Per-speaker transcribe | For each input stream: extract floats, transcribe, assemble into segments | — |
| 7 | Merge + sort + write words | `MergeAndSortSegments` then `writeTranscriptWithHash(version="transcript.words.v1")` | `transcript.words.v1.json` |
| 8 | Captions | `WriteCaptionsVTT` renders the canonical transcript as WebVTT | `captions.vtt` |
| **9** | **Meeting summary *(optional)*** | **`writeSummaryArtifact` calls `BuildMeetingSummary` (system prompt embeds the V0 template) and writes the model's markdown output as a sidecar** | **`summary.md`** |
| 10 | Manifest | `WriteManifest` emits the artifact catalog with provenance for STT and the summary | `manifest.json` |

Step 9 summarises the canonical transcript, minus words the crosstalk gate marked low-confidence.

## Output bundle

After a successful build, `outputDir` contains:

```text
<meeting-output>/
  meeting.webm                    (always)
  transcript.words.v1.json        (always)
  captions.vtt                    (only if step 8 ran)
  summary.md                      (only if step 9 ran)
  manifest.json                   (always)
```

The viewer (`cassini-viewer`) treats `summary.md`, `captions.vtt`, and `chapters.vtt` as **optional** sidecars — see `cassini-viewer/scripts/demo-data-pull.mjs:144`. A 404 is tolerated; the UI falls back to a "no summary" state. This is what makes step 9's warn-and-skip semantics safe end-to-end.

The portable `.opus` packer (`internal/cassini/portable_meeting.go`) embeds the audio, the transcripts, and — when the bundle carries one — `summary.md` as a manifest attachment plus summary metadata (`format`, `templateVersion`, `model`), so a published file carries its summary inside.

For files sealed **before** summaries existed, `cassini meetings summarize <meeting.opus>` backfills one without re-running transcription: it reads the transcript back out of the file, makes the same single LLM call `cassini build` would make (configured by the same environment variables), and rewrites the metadata through the stage-verify-rename path with the audio bytes untouched. A file that already carries a summary is skipped unless `--force` replaces it, and a backfilled summary is recognisable by `provenance.meetingSummary.source = "backfill"` in the manifest.

## LLM integration

There is one HTTP client, `chatCompletion(cfg, system, user)` in `llm.go`, used by two distinct callers with two distinct configurations:

| Caller | Config field | Default model | Env override |
|---|---|---|---|
| `ReadableCleanup` (step 8) | `BuildConfig.LLM` | `openai/gpt-4o-mini` | `LLM_MODEL` |
| `BuildMeetingSummary` (step 9) | `BuildConfig.SummaryLLM` | inherits from `LLM_MODEL`; falls back to `openai/gpt-4o-mini` | `SUMMARY_MODEL` |

The summary call reads the shared auth (`OPENROUTER_API_KEY`) and base URL (`OPENROUTER_BASE_URL`, falling back to `LLM_BASE_URL`), unless given its own endpoint: `SUMMARY_BASE_URL` / `SUMMARY_API_KEY` / `SUMMARY_MODEL` override the shared values. An endpoint override brings its own key — the shared key is never sent to a different host. The operator emits these from its persisted LLM settings.

**The base URL is the switch, not the key.** `IsConfigured()` requires a base URL and an unset kill-switch; the API key is optional, because a self-hosted OpenAI-compatible server (llama.cpp, vLLM, Ollama) usually has none. When the key is empty the `Authorization` header is omitted entirely rather than sent as an empty bearer token, which some self-hosted servers reject. With no base URL, both steps are skipped silently.

`CASSINI_SUMMARY_DISABLED=1` turns summaries off while keeping the endpoint configuration in place.

`SUMMARY_MODEL` overrides `LLM_MODEL` for the summary call, so a stronger model can be picked without changing the shared default.

### Failure semantics

- **Summary generation (step 9):** warn-and-skip only — no strict mode. If the LLM fails or returns empty, the pipeline succeeds without a `summary.md`. This matches the V4 acceptance criterion that disabling summary generation does not break the pipeline.

## Configuration surface

`BuildConfig` (in `transcribe.go`) carries every knob the pipeline reads:

```go
type BuildConfig struct {
    Device                string    // "cpu" | "cuda"
    ModelID               ModelID   // STT model
    CacheDir              string    // model cache root
    LLM                   LLMConfig // step 8
    SummaryLLM            LLMConfig // step 9
    NumThreads            int
}
```

`DefaultBuildConfig()` reads these env vars in this order:

| Env var | Maps to |
|---|---|
| `OPENROUTER_API_KEY` | `LLM.APIKey` and `SummaryLLM.APIKey` |
| `OPENROUTER_BASE_URL` (or `LLM_BASE_URL`) | `LLM.BaseURL` and `SummaryLLM.BaseURL` |
| `LLM_MODEL` | `LLM.Model` (and `SummaryLLM.Model` until overridden) |
| `SUMMARY_BASE_URL` / `SUMMARY_API_KEY` / `SUMMARY_MODEL` | `SummaryLLM.BaseURL` / `SummaryLLM.APIKey` / `SummaryLLM.Model` for the summary alone |
| `CASSINI_LLM_TIMEOUT_SEC` | `TimeoutSec` on both (default 900; raise for CPU-bound local models) |
| `CASSINI_LLM_MAX_TOKENS` | `MaxTokens` on both (default 4096) |
| `CASSINI_SUMMARY_DISABLED` | `SummaryLLM.Disabled` |
| `CASSINI_CACHE_ROOT` | `CacheDir` (default cache location) |

## How V4 (D-242) fits in

V4 introduces step 9 — meeting summary generation — and nothing else in the pipeline shape. Specifically:

- **No new artifact contract.** `summary.md` is plain markdown. The contract is the V0 template in `internal/insight/workflows/prompts/summarise-template.v0.md`, which the summary step reads from the workflow registry (D-718) rather than embedding a second copy — so the bytes the pipeline sends and the bytes `cassini insight workflows` lists are the same file, and a change to it is caught by the prompt gate in `lint.yml`.
- **No new dependencies.** Reuses `chatCompletion` from `llm.go`.
- **No manifest schema change.** `manifest.json` does not currently list `summary.md` or summary provenance — see Followups for V6.
- **No new CLI flags.** Operators set an endpoint (`LLM_BASE_URL`, or `OPENROUTER_API_KEY` which implies the OpenRouter one) and optionally `SUMMARY_MODEL`, and the summary path turns on automatically. With no endpoint, step 9 is skipped silently along with step 8; `CASSINI_SUMMARY_DISABLED=1` skips step 9 alone.
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
