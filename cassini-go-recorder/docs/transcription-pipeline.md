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
| LLM integration | `internal/transcribe/llm.go` | OpenAI-compatible HTTP client, readable-cleanup batch logic |
| Summary generation (V4) | `internal/transcribe/summary.go` | Embedded V0 template, system prompt, transcript flattening |
| Template (V0 contract) | `internal/transcribe/templates/summary.v0.md` | The summary's section structure — single source of truth |

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
| 8 | LLM readable cleanup *(optional)* | `writeReadableArtifacts` batches segments through `chatCompletion`, reapplies cleaned text via `ApplyReadableText`, redistributes word timings, writes captions | `transcript.readable.v1.json`, `captions.vtt` |
| **9** | **Meeting summary *(optional)*** | **`writeSummaryArtifact` calls `BuildMeetingSummary` (system prompt embeds the V0 template) and writes the model's markdown output as a sidecar** | **`summary.md`** |
| 10 | Manifest | `WriteManifest` emits the artifact catalog with provenance for STT and LLM cleanup | `manifest.json` |

Step 9 prefers the cleaned (post-step-8) segments when available, and falls back to raw words when readable cleanup is disabled or skipped. This keeps the summary input quality consistent with what the viewer would render in the readable transcript.

## Output bundle

After a successful build, `outputDir` contains:

```text
<meeting-output>/
  meeting.webm                    (always)
  transcript.words.v1.json        (always)
  transcript.readable.v1.json     (only if step 8 ran)
  captions.vtt                    (only if step 8 ran)
  summary.md                      (only if step 9 ran)
  manifest.json                   (always)
```

The viewer (`cassini-viewer`) treats `summary.md`, `captions.vtt`, and `chapters.vtt` as **optional** sidecars — see `cassini-viewer/scripts/demo-data-pull.mjs:144`. A 404 is tolerated; the UI falls back to a "no summary" state. This is what makes step 9's warn-and-skip semantics safe end-to-end.

The portable `.opus` packer (`internal/cassini/portable_meeting.go`) currently embeds the audio, transcript, and readable transcript into a base64-gzip payload. It does **not** yet read `summary.md` — a Followups item, since the V0 contract is a sidecar file and V6 (self-host bundle) is the right place to decide if/how to embed it.

## LLM integration

There is one HTTP client, `chatCompletion(cfg, system, user)` in `llm.go`, used by two distinct callers with two distinct configurations:

| Caller | Config field | Default model | Env override |
|---|---|---|---|
| `ReadableCleanup` (step 8) | `BuildConfig.LLM` | `openai/gpt-4o-mini` | `LLM_MODEL` |
| `BuildMeetingSummary` (step 9) | `BuildConfig.SummaryLLM` | inherits from `LLM_MODEL`; falls back to `openai/gpt-4o-mini` | `SUMMARY_MODEL` |

Both share the same auth (`OPENROUTER_API_KEY`) and base URL (`OPENROUTER_BASE_URL`, falling back to `LLM_BASE_URL`).

**The base URL is the switch, not the key.** `IsConfigured()` requires a base URL and an unset kill-switch; the API key is optional, because a self-hosted OpenAI-compatible server (llama.cpp, vLLM, Ollama) usually has none. When the key is empty the `Authorization` header is omitted entirely rather than sent as an empty bearer token, which some self-hosted servers reject. With no base URL, both steps are skipped silently.

Each step also has its own kill-switch — `CASSINI_SUMMARY_DISABLED` and `CASSINI_READABLE_DISABLED` — applied to a copy of the shared config, so disabling one never disables the other.

This split exists because the two tasks have different cost/quality profiles — readable cleanup runs many small batches (cheap is fine), while summary generation is one large prompt where a stronger frontier model is justified. Operators set `SUMMARY_MODEL=anthropic/claude-...` (or similar) without disturbing cleanup.

### Failure semantics

- **Readable cleanup (step 8):** warn-and-skip by default; can be made strict via `CASSINI_READABLE_STRICT_BATCHES=1` (`BuildConfig.StrictReadableCleanup`).
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
    StrictReadableCleanup bool      // strict gate for step 8
    NumThreads            int
}
```

`DefaultBuildConfig()` reads these env vars in this order:

| Env var | Maps to |
|---|---|
| `OPENROUTER_API_KEY` | `LLM.APIKey` and `SummaryLLM.APIKey` |
| `OPENROUTER_BASE_URL` (or `LLM_BASE_URL`) | `LLM.BaseURL` and `SummaryLLM.BaseURL` |
| `LLM_MODEL` | `LLM.Model` (and `SummaryLLM.Model` until overridden) |
| `SUMMARY_MODEL` | `SummaryLLM.Model` (overrides the default) |
| `CASSINI_LLM_TIMEOUT_SEC` | `TimeoutSec` on both (default 900; raise for CPU-bound local models) |
| `CASSINI_LLM_MAX_TOKENS` | `MaxTokens` on both (default 4096) |
| `CASSINI_SUMMARY_DISABLED` | `SummaryLLM.Disabled` |
| `CASSINI_READABLE_DISABLED` | `LLM.Disabled` |
| `CASSINI_READABLE_STRICT_BATCHES` | `StrictReadableCleanup` |
| `CASSINI_CACHE_ROOT` | `CacheDir` (default cache location) |

## How V4 (D-242) fits in

V4 introduces step 9 — meeting summary generation — and nothing else in the pipeline shape. Specifically:

- **No new artifact contract.** `summary.md` is plain markdown. The contract is the V0 template in `internal/transcribe/templates/summary.v0.md`, embedded into the system prompt at compile time via `go:embed` so edits to the template propagate without code changes.
- **No new dependencies.** Reuses `chatCompletion` from `llm.go`.
- **No manifest schema change.** `manifest.json` does not currently list `summary.md` or summary provenance — see Followups for V6.
- **No new CLI flags.** Operators set an endpoint (`LLM_BASE_URL`, or `OPENROUTER_API_KEY` which implies the OpenRouter one) and optionally `SUMMARY_MODEL`, and the summary path turns on automatically. With no endpoint, step 9 is skipped silently along with step 8; `CASSINI_SUMMARY_DISABLED=1` skips step 9 alone.
- **Test mocking** uses the same `func` package-var pattern step 8 introduced (`readableCleanupFn` / `buildMeetingSummaryFn`), so no live LLM is called in CI.

The acceptance criteria from D-242 map to:

| AC | Where it lives |
|---|---|
| Pipeline produces summary in V0 template format | `summary.go` → `summarySystemPrompt` embeds `summary.v0.md`; tests pin both ends |
| Summary renders correctly in V3 viewer | `summary.md` written next to other artifacts; viewer's optional-sidecar logic at `cassini-viewer/scripts/demo-data-pull.mjs:144` already handles it |
| Disabling summary generation does not break pipeline | `SummaryLLM.IsConfigured()` gate in `writeSummaryArtifact`; warn-and-skip on errors |
| Summary written alongside transcript artifacts | `os.WriteFile(filepath.Join(outputDir, "summary.md"), …)` in `writeSummaryArtifact` |

## Reading order if you are new to the pipeline

1. `cmd/cassini/main.go` and `internal/cassini/build.go` — how the CLI reaches the pipeline
2. `internal/transcribe/transcribe.go` — the orchestrator, top-to-bottom
3. `internal/transcribe/format.go` — the artifact contracts (transcript JSON, captions, manifest)
4. `internal/transcribe/llm.go` — the LLM HTTP layer
5. `internal/transcribe/summary.go` + `templates/summary.v0.md` — step 9 (V4)
6. `internal/portable/manifest.go` — the portable `.opus` manifest schema, which downstream code packs into a single file
