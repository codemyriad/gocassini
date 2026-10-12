import { describe, expect, it } from "vitest";
import { pipelineStatus, retentionSpan, runningDownload, speechModelFor, type PipelineInput } from "./pipelineStatus";

const fresh: PipelineInput = {
  meetingFormat: "opus", transcriptionEnabled: false, quality: "balanced", device: "cpu", modelInstalled: false,
  download: null, summaryEnabled: false, providerName: "", hasProvider: false, keep: "audio-only",
  recordingsPolicy: { forever: true },
};
const team: PipelineInput = {
  ...fresh, transcriptionEnabled: true, modelInstalled: true, summaryEnabled: true, providerName: "OpenRouter",
  hasProvider: true, recordingsPolicy: { forever: false, count: 30, unit: "days" },
};

describe("pipelineStatus", () => {
  it("fresh install: nothing configured", () => {
    expect(pipelineStatus(fresh)).toEqual({
      transcribe: { state: "Off", tone: "off", badge: "download a model" },
      summarise: { state: "Off", tone: "off", badge: "needs a transcript" },
      publish: { state: "Audio, no transcript", tone: "on" },
      keep: { state: "Audio only · forever", tone: "on" },
    });
  });

  it("set up for a team", () => {
    expect(pipelineStatus(team)).toEqual({
      transcribe: { state: "On · Balanced · CPU", tone: "on" },
      summarise: { state: "On · OpenRouter", tone: "on" },
      publish: { state: "Audio + transcript", tone: "on" },
      keep: { state: "Audio only · 30 days", tone: "on" },
    });
  });

  it("transcript only with transcription off warns on Publish", () => {
    const status = pipelineStatus({ ...fresh, meetingFormat: "json" });
    expect(status.publish).toEqual({ state: "Transcript only", tone: "amber", reason: "needs Transcribe on" });
    // Transcribe itself is never amber.
    expect(status.transcribe.tone).toBe("off");
  });

  it("keeping nothing is valid with a transcript", () => {
    const status = pipelineStatus({ ...team, meetingFormat: "json", keep: "nothing" });
    expect(status.publish).toEqual({ state: "Transcript only", tone: "on" });
    expect(status.keep).toEqual({ state: "Nothing", tone: "off", badge: "no reprocessing later" });
  });

  it("model downloading", () => {
    const status = pipelineStatus({ ...fresh, download: { percent: 42 } });
    expect(status.transcribe).toEqual({ state: "Off · downloading 42%", tone: "busy" });
    expect(status.publish).toEqual({ state: "Audio, no transcript", tone: "on", badge: "until the model is ready" });
  });

  it("asks for a provider once transcription is on", () => {
    const status = pipelineStatus({ ...fresh, transcriptionEnabled: true, modelInstalled: true });
    expect(status.summarise).toEqual({ state: "Off", tone: "off", badge: "add a provider" });
    expect(pipelineStatus({ ...team, transcriptionEnabled: false, summaryEnabled: false }).summarise)
      .toEqual({ state: "Off", tone: "off", badge: "needs a transcript" });
  });

  it("summary on with transcription off warns on Summarise", () => {
    const status = pipelineStatus({ ...team, transcriptionEnabled: false });
    expect(status.summarise).toEqual({ state: "On · OpenRouter", tone: "amber", reason: "needs a transcript" });
  });

  it("omits the policy part when retention is unknown and names the GPU", () => {
    const status = pipelineStatus({ ...team, keep: "audio-video", recordingsPolicy: null, device: "cuda", quality: "best" });
    expect(status.keep.state).toBe("Audio + video");
    expect(status.transcribe.state).toBe("On · Best · GPU");
  });
});

describe("helpers", () => {
  it("names the model a tier runs", () => {
    expect(speechModelFor("balanced", "cpu")).toBe("parakeet-tdt-0.6b-v3-int8");
    expect(speechModelFor("fast", "cuda")).toBe("parakeet-tdt-0.6b-v3");
  });
  it("reads a running download", () => {
    const job = { id: "j", model: "m", revision: "r", device: "cpu", updated_at: "", progress: { phase: "", completed_bytes: 42, total_bytes: 100, reused_bytes: 0 } };
    expect(runningDownload({ models: [], jobs: [{ ...job, state: "downloading" }], downloads_allowed: true, device: "cpu" })).toEqual({ percent: 42 });
    expect(runningDownload({ models: [], jobs: [{ ...job, state: "cancelled" }], downloads_allowed: true, device: "cpu" })).toBeNull();
    // Voice separation's download holds nothing in the pipeline up.
    const diarizer = { id: "m", kind: "diarization", name: "", revision: "r", download_bytes: 1, installed_bytes: 1, installed: false, ready: false, device: "cpu" };
    expect(runningDownload({ models: [diarizer], jobs: [{ ...job, state: "downloading" }], downloads_allowed: true, device: "cpu" })).toBeNull();
  });
  it("says a retention span", () => {
    expect(retentionSpan({ forever: false, count: 1, unit: "days" })).toBe("1 day");
    expect(retentionSpan({ forever: true })).toBe("forever");
    expect(retentionSpan(null)).toBeNull();
  });

  it("names the tier of the saved model, which is what builds use", () => {
    const status = pipelineStatus({ ...team, quality: "best", activeModel: "parakeet-tdt-0.6b-v3-int8" });
    expect(status.transcribe.state).toBe("On · Balanced · CPU");
  });

  it("warns when transcription is on but its model is not installed", () => {
    const status = pipelineStatus({ ...team, activeModel: "parakeet-tdt-0.6b-v3", activeModelReady: false, download: { percent: 10 } });
    expect(status.transcribe).toEqual({ state: "On · Best · CPU", tone: "amber", reason: "model not installed" });
    // Transcription is on, so publishing does not wait on the download.
    expect(status.publish.badge).toBeUndefined();
  });
});
