# Backfill GPU transcripts into v2 for existing meetings

Date: 2026-05-20
Status: Shaping

## Motivation

After PR #26 (parakeet-v3), the producer emits v2 portable-meeting files by default with the GPU model. The viewer ships the transcript switcher. New recordings are v2 + GPU.

What's *not* covered is the back-catalogue: meetings recorded and published before that point are still v1, with a single CPU-parakeet-v2 transcript, and the new UI's switcher has nothing to switch between for them. The user-facing ask is "have all currently-transcribed meetings on george also be transcribed using GPU, with both transcriptions available under the new format".

## What the codebase already does

Inventoried before planning:

- `cassini-go-recorder/internal/transcribe/transcribe.go`: `CASSINI_STT_ADDITIONAL_MODELS` env var drives a second transcribe pass against the same audio, writing a sibling `transcript-<id>.words.v1.json`.
- `cassini-go-recorder/internal/cassini/portable_meeting_v2.go`: the v2 OpusTag builder already accepts multiple transcript inputs and emits one per-transcript chunk set; `assembleV2TranscriptInputs` handles both the "single inline" and "multi from manifest.json" paths.
- `cassini-operator/internal/operator/attempt_store.go::QueueRerunAttempt`: D-280 rerun re-runs the build (transcribe + readable + bundle + publish) on the canonical ready run, keeping the original recording. State machine queues at `build/queued`.
- `deployment/compose.yml`: operator container does not currently pass any STT model env — recorder uses the parakeet-v3 default.

Net: the pipeline is in place. The missing pieces are (a) identifying which jobs are still v1, (b) running a rerun for each with both models enabled, and (c) a bulk driver that respects "one at a time" on the GPU.

## Slices

### S1 — Spike: confirm pipeline reach + identify per-attempt env gap

Verified by reading: the v2 multi-transcript builder is unit-tested (`portable_meeting_v2_test.go::TestBuildPortableMeetingV2TagsFromSource_WithMultipleTranscripts` covers two raw-ASR entries with separate provenance and distinct payload chunk sets); `transcribe.go::runAdditionalTranscripts` writes sibling transcript files keyed off `CASSINI_STT_ADDITIONAL_MODELS`; D-280 `QueueRerunAttempt` queues a build attempt against the canonical ready run.

The gap surfaced: `build_runtime.go:107` does `cmd.Env = os.Environ()`. The build subprocess inherits the operator's process env unchanged. So setting `CASSINI_STT_ADDITIONAL_MODELS` globally on the operator would force every live recording to also run the legacy CPU pass — undesirable. We need a per-attempt env override path.

No live harness run done as part of the spike; the read-only inventory is sufficient and the live e2e is covered by S4.

### S2 — Operator: v1 detection + per-job backfill endpoint

- Predicate: a completed job's published `.opus` is v1 (read `CASSINI_FORMAT` tag, or check `version` in the manifest).
- New endpoint `POST /jobs/{id}/backfill-transcripts`: enqueues a D-280 rerun for that job, but tagging the attempt as `backfill-gpu` so the runtime knows to set `CASSINI_STT_ADDITIONAL_MODELS=parakeet-tdt-0.6b` for that build invocation.
- Reject if the published `.opus` is already v2 (idempotency).
- Operator tests: dummy fixture for a v1 job and a v2 job; verify the v1 one queues a backfill attempt with the model env recorded, the v2 one returns 409.

### S3 — Operator + control-panel: serial bulk backfill

- New endpoint `POST /jobs/backfill-transcripts:bulk` that enumerates v1 jobs and enqueues them one at a time (start next only after previous reaches a terminal state).
- Stoppable: respect the existing job-stop machinery for the in-flight one; abandon the queue cleanly.
- Control panel: a single "Backfill v1 meetings to v2 + GPU" button on the job list, with a progress indicator (`N of M complete`). Filter to surface only v1 jobs.

### S4 — Harness e2e

Extend the synthetic-meeting harness scenario:
1. Produce a v1 meeting (export `CASSINI_FORMAT_V1=1`).
2. Verify the published `.opus` is v1.
3. Trigger the backfill endpoint.
4. Wait for terminal.
5. Verify the new `.opus` is v2 with two transcript entries, default is the GPU model.
6. Hit the viewer and assert both transcripts appear in the switcher.

### S5 — Deploy + run

- Open PR, merge after green CI.
- CI deploys to george (existing D-246 bundle).
- Trigger the bulk endpoint from the control panel.
- Spot-check 2-3 backfilled meetings in the viewer.

## Risks / open questions

1. **GPU contention with live recordings**: serial-only is the user's chosen mitigation. The bulk driver must not start a backfill attempt while a live record/build is in flight — gate on operator's existing build-worker availability rather than driving from outside.
2. **Original CPU transcript preservation**: a fresh rerun produces a *new* CPU transcript from the same audio, not the original bytes. This is acceptable per the goal ("both transcriptions available") but the provenance step should record that this transcript was generated during backfill, not at original recording time. Decision: stamp the provenance step's `processedAtUtc` with the backfill run time; the original is still in git history of the published artifact if needed.
3. **CASSINI_STT_ADDITIONAL_MODELS as a per-attempt setting**: today it's process-wide. The operator launches the recorder as a subprocess for build, so we can set it on that subprocess's env without touching the operator's own env. Need to confirm the build subprocess shape.
4. **Old recordings on disk**: relies on george retaining the source recordings (user confirmed this). If a specific job's source is missing, the backfill for that job should fail cleanly with a clear error and move on.

## Success criteria

- All v1 meetings on george are republished as v2 with two transcript entries (parakeet-v3-gpu default, parakeet-v2-cpu secondary).
- The viewer's transcript switcher works on each backfilled meeting.
- No regression in live recording throughput on george during the bulk pass.
- The control-panel button is durable: re-clicking it on a partial run resumes from where it stopped.

## Reference

- v2 spec/proposal: `docs/proposals/multi-transcription-format.md`
- D-280 rerun: `planning/initiatives/mvp/D-280-rerun-only-postprocessing-jobs/`
- D-246 deployment bundle: `planning/initiatives/mvp/D-246-deployment-bundle-1/`
