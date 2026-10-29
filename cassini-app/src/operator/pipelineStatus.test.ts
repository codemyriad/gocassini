import { describe, expect, it } from "vitest";
import { inUseModelState, pipelineStatus, retentionSpan, runningDownload, speechModelFor, type PipelineInput } from "./pipelineStatus";

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

  it("names the tier of the saved GPU model", () => {
    const status = pipelineStatus({ ...team, device: "cuda", quality: "balanced", activeModel: "parakeet-ultra-0.6b" });
    expect(status.transcribe.state).toBe("On · Best · GPU");
  });

  it("omits the policy part when retention is unknown and names the GPU", () => {
    const status = pipelineStatus({ ...team, keep: "audio-video", recordingsPolicy: null, device: "cuda", quality: "best" });
    expect(status.keep.state).toBe("Audio + video");
    expect(status.transcribe.state).toBe("On · Best · GPU");
  });
});

describe("helpers", () => {
  it("names the model a tier runs", () => {
    expect(speechModelFor("balanced", "cpu")).toBe("parakeet-ultra-0.6b-int8");
    expect(speechModelFor("best", "cpu")).toBe("parakeet-ultra-0.6b");
    expect(speechModelFor("fast", "cpu")).toBe("parakeet-tdt-ctc-110m-en-int8");
    for (const quality of ["fast", "balanced", "best"]) {
      expect(speechModelFor(quality, "cuda")).toBe("parakeet-ultra-0.6b");
    }
  });
  it("reads a running download", () => {
    const job = { id: "j", model: "m", revision: "r", device: "cpu", updated_at: "", progress: { phase: "", completed_bytes: 42, total_bytes: 100, reused_bytes: 0 } };
    expect(runningDownload({ models: [], jobs: [{ ...job, state: "downloading" }], downloads_allowed: true, device: "cpu" })).toEqual({ percent: 42 });
    expect(runningDownload({ models: [], jobs: [{ ...job, state: "cancelled" }], downloads_allowed: true, device: "cpu" })).toBeNull();
    // Asked for one model, another model's download does not count.
    expect(runningDownload({ models: [], jobs: [{ ...job, state: "downloading" }], downloads_allowed: true, device: "cpu" }, "m")).toEqual({ percent: 42 });
    expect(runningDownload({ models: [], jobs: [{ ...job, state: "downloading" }], downloads_allowed: true, device: "cpu" }, "other")).toBeNull();
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
    const status = pipelineStatus({ ...team, quality: "best", activeModel: "parakeet-ultra-0.6b-int8" });
    expect(status.transcribe.state).toBe("On · Balanced · CPU");
  });

  it("warns when transcription is on but its model is not installed", () => {
    // Another model's download does not make the saved one any closer.
    const status = pipelineStatus({ ...team, activeModel: "parakeet-ultra-0.6b", activeModelInstalled: false, download: { percent: 10 }, activeModelDownload: null });
    expect(status.transcribe).toEqual({ state: "On · Best · CPU", tone: "amber", reason: "model not installed" });
    // Transcription is on, so publishing does not wait on the download.
    expect(status.publish.badge).toBeUndefined();
  });

  it("shows the saved model downloading after an upgrade replaced it", () => {
    const status = pipelineStatus({ ...team, activeModel: "parakeet-ultra-0.6b-int8", activeModelInstalled: false, download: { percent: 40 }, activeModelDownload: { percent: 40 } });
    expect(status.transcribe).toEqual({ state: "On · Balanced · CPU", tone: "busy", reason: "downloading the model · 40%" });
    const unsized = pipelineStatus({ ...team, activeModel: "parakeet-ultra-0.6b-int8", activeModelInstalled: false, download: { percent: null }, activeModelDownload: { percent: null } });
    expect(unsized.transcribe.reason).toBe("downloading the model");
  });

  it("says the previous model transcribes while the saved one is not ready", () => {
    const downloading = pipelineStatus({ ...team, activeModel: "parakeet-ultra-0.6b-int8", activeModelInstalled: false, download: { percent: 40 }, activeModelDownload: { percent: 40 }, activeModelStandIn: true });
    expect(downloading.transcribe).toEqual({ state: "On · Balanced · CPU", tone: "busy", reason: "downloading the model · 40% · previous model transcribes meanwhile" });
    // A failed install, or downloads disabled: still not healthy, but not
    // publishing without a transcript either.
    const missing = pipelineStatus({ ...team, activeModel: "parakeet-ultra-0.6b-int8", activeModelInstalled: false, activeModelDownload: null, activeModelStandIn: true });
    expect(missing.transcribe).toEqual({ state: "On · Balanced · CPU", tone: "amber", reason: "model not installed · previous model transcribes meanwhile" });
    // Installed but not through its runtime check: the operator names v3 only
    // then, and builds transcribe with it while the check fails.
    expect(pipelineStatus({ ...team, activeModel: "parakeet-ultra-0.6b-int8", activeModelInstalled: true, activeModelStandIn: true }).transcribe).toEqual({ state: "On · Balanced · CPU", tone: "amber", reason: "model check not passed · previous model transcribes meanwhile" });
    // Once the saved model is ready it runs, and nothing stands in for it.
    expect(pipelineStatus({ ...team, activeModel: "parakeet-ultra-0.6b-int8", activeModelInstalled: true, activeModelStandIn: false }).transcribe).toEqual({ state: "On · Balanced · CPU", tone: "on" });
    // While the inventory is unknown the strip claims nothing.
    expect(pipelineStatus({ ...team, activeModel: "parakeet-ultra-0.6b-int8", activeModelInstalled: null, activeModelStandIn: true }).transcribe).toEqual({ state: "On · Balanced · CPU", tone: "on" });
  });
});

describe("inUseModelState", () => {
  it("says recordings get no transcript only when nothing transcribes or installs the model", () => {
    // After the upgrade Ultra downloads while v3 stands in: the Transcript card
    // must not say recordings get no transcript, which the strip contradicts.
    expect(inUseModelState({ installed: false, stand_in: "parakeet-tdt-0.6b-v3-int8" }, { percent: 40 })).toBe("stand-in");
    // A failed install or downloads disabled, with v3 still there.
    expect(inUseModelState({ installed: false, stand_in: "parakeet-tdt-0.6b-v3-int8" }, null)).toBe("stand-in");
    // A fresh install: recordings wait for the download.
    expect(inUseModelState({ installed: false }, { percent: null })).toBe("downloading");
    expect(inUseModelState({ installed: false }, null)).toBe("missing");
    // Installed but not through its runtime check: the operator names v3 only
    // then, and builds use it while Ultra's check fails, so the card must not
    // say Ultra transcribes.
    expect(inUseModelState({ installed: true, stand_in: "parakeet-tdt-0.6b-v3-int8" }, null)).toBe("stand-in");
    // Installed with nothing standing in runs (checked first if need be).
    expect(inUseModelState({ installed: true }, null)).toBe("installed");
  });
});
