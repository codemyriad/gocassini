import { describe, expect, it } from "vitest";
import { render } from "svelte/server";

import SpeechModelsSource from "./SpeechModels.svelte?raw";
import VoiceSeparationModel from "./VoiceSeparationModel.svelte";
import type { SpeechModel, SpeechModelInventory, SpeechModelJob } from "./operator/types";
import { jobStepText, modelJob, transcriptionModelId, VOICE_SEPARATION_MODEL_ID, voiceSeparationModel } from "./speechModels";

const diarizer: SpeechModel = {
  id: VOICE_SEPARATION_MODEL_ID, kind: "diarization", name: "Nemotron 3 Diarization — int8", description: "Speaker diarization",
  revision: "c".repeat(64), download_bytes: 64915668, installed_bytes: 103967426, installed: false, ready: false, device: "cpu", runtime_supported: true,
};
const speech: SpeechModel = {
  id: "parakeet-tdt-ctc-110m-en-int8", kind: "speech", name: "Parakeet", description: "Fast", revision: "a".repeat(64),
  download_bytes: 1, installed_bytes: 2, installed: true, ready: true, device: "cpu",
};
function inventory(models: SpeechModel[], jobs: SpeechModelJob[] = []): SpeechModelInventory {
  return { models, jobs, downloads_allowed: true, device: "cpu" };
}
function job(state: string, device = "cpu"): SpeechModelJob {
  return { id: "j1", model: diarizer.id, revision: diarizer.revision, device, state, updated_at: "now", progress: { phase: state, completed_bytes: 5, total_bytes: 10, reused_bytes: 0 } };
}
function card({ model, job, downloadsAllowed = false }: { model: SpeechModel; job?: SpeechModelJob; downloadsAllowed?: boolean }) {
  const inv = { ...inventory([speech, model], job ? [job] : []), downloads_allowed: downloadsAllowed };
  return render(VoiceSeparationModel, { props: { inventory: inv } }).body;
}

describe("voice separation in the speech models", () => {
  it("renders nothing without a separation model", () => {
    expect(render(VoiceSeparationModel, { props: { inventory: inventory([speech]) } }).body).not.toContain("Voice separation");
  });

  it("is found by its kind, and never as the transcription model", () => {
    expect(voiceSeparationModel(inventory([speech, diarizer]))).toEqual(diarizer);
    // An operator that does not label kinds lists no separation model.
    expect(voiceSeparationModel(inventory([speech, { ...diarizer, kind: undefined }]))).toBeUndefined();
    for (const device of ["cpu", "cuda"]) {
      for (const quality of ["fast", "balanced", "best"]) {
        expect(transcriptionModelId(quality, device)).toMatch(/^parakeet-/);
      }
    }
  });

  it("runs Parakeet Ultra on every tier but the CPU fast one", () => {
    for (const quality of ["fast", "balanced", "best"]) {
      expect(transcriptionModelId(quality, "cuda")).toBe("parakeet-ultra-0.6b");
    }
    expect(transcriptionModelId("best", "cpu")).toBe("parakeet-ultra-0.6b");
    expect(transcriptionModelId("balanced", "cpu")).toBe("parakeet-ultra-0.6b-int8");
    expect(transcriptionModelId("fast", "cpu")).toBe("parakeet-tdt-ctc-110m-en-int8");
  });

  it("follows its own CPU job whatever device transcribes", () => {
    const inv = inventory([diarizer], [job("downloading", "cuda"), { ...job("downloading"), id: "j2" }]);
    expect(modelJob(inv, diarizer, "cpu")?.id).toBe("j2");
    // It has its own Settings card; the transcript step no longer lists it.
    expect(SpeechModelsSource).not.toContain("VoiceSeparationModel");
  });

  it("offers the download with its size, and nothing that selects it for transcription", () => {
    const html = card({ model: diarizer, downloadsAllowed: true });
    expect(html).toContain("Voice separation");
    expect(html).toContain("61.9 MiB");
    expect(html).toContain(">Download<");
    expect(html).toContain("Not installed");
    expect(html).not.toContain("Use this model");
    expect(html).not.toContain("Enable transcription");
  });

  it("says when it is ready, being installed, or cannot run here", () => {
    const ready = card({ model: { ...diarizer, installed: true, ready: true } });
    expect(ready).toContain("Ready");
    expect(ready).toContain("from its participant list");
    expect(ready).not.toContain("<button");

    expect(card({ model: diarizer, job: job("downloading"), downloadsAllowed: true })).toContain("Voice separation download progress");
    expect(card({ model: diarizer, job: { ...job("failed"), error: "disk full" }, downloadsAllowed: true })).toContain(">Retry<");

    const unsupported = card({ model: { ...diarizer, installed: true, runtime_supported: false }, downloadsAllowed: true });
    expect(unsupported).toContain("Not available in this server's Cassini build");
    expect(unsupported).not.toContain("<button");

    // Air-gapped: no download button until the files are imported.
    expect(card({ model: diarizer, downloadsAllowed: false })).not.toContain("<button");
  });
});

describe("model job steps", () => {
  it("says what happens after the download instead of sitting at 100%", () => {
    expect(jobStepText(job("downloading"))).toBe("Downloading · 50%");
    expect(jobStepText(job("installed"))).toBe("Downloaded · checks once no meeting is being processed");
    expect(jobStepText(job("checking"))).toBe("Checking it runs on this server");
    // The bar belongs to the download only.
    expect(card({ model: diarizer, job: job("installed"), downloadsAllowed: true })).not.toContain("download progress");
  });
});

