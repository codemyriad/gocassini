import { describe, expect, it } from "vitest";
import { render } from "svelte/server";

import SpeechModelsSource from "./SpeechModels.svelte?raw";
import VoiceSeparationModel from "./VoiceSeparationModel.svelte";
import type { SpeechModel, SpeechModelInventory, SpeechModelJob } from "./operator/types";
import { modelJob, transcriptionModelId, VOICE_SEPARATION_MODEL_ID, voiceSeparationModel } from "./speechModels";

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
function card(props: { model: SpeechModel; job?: SpeechModelJob; downloadsAllowed?: boolean }) {
  return render(VoiceSeparationModel, { props }).body;
}

describe("voice separation in the speech models", () => {
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

  it("follows its own CPU job whatever device transcribes", () => {
    const inv = inventory([diarizer], [job("downloading", "cuda"), { ...job("downloading"), id: "j2" }]);
    expect(modelJob(inv, diarizer, "cpu")?.id).toBe("j2");
    expect(SpeechModelsSource).toContain('modelJob(inventory, separation, "cpu")');
    expect(SpeechModelsSource).toContain('action(kind, separation, separationJob, "cpu")');
  });

  it("offers the download with its size, and nothing that selects it for transcription", () => {
    const html = card({ model: diarizer, downloadsAllowed: true });
    expect(html).toContain("Voice separation (optional)");
    expect(html).toContain("download 61.9 MiB · installed 99.2 MiB");
    expect(html).toContain("Download voice separation");
    expect(html).toContain("Not installed");
    expect(html).not.toContain("Use this model");
    expect(html).not.toContain("Enable transcription");
  });

  it("says when it is ready, being installed, or cannot run here", () => {
    const ready = card({ model: { ...diarizer, installed: true, ready: true } });
    expect(ready).toContain("Ready");
    expect(ready).toContain("separate voices in a meeting's participant list");
    expect(ready).not.toContain("<button");

    expect(card({ model: diarizer, job: job("downloading"), downloadsAllowed: true })).toContain("Voice separation download progress");
    expect(card({ model: diarizer, job: { ...job("failed"), error: "disk full" }, downloadsAllowed: true })).toContain("Retry download");

    const unsupported = card({ model: { ...diarizer, installed: true, runtime_supported: false }, downloadsAllowed: true });
    expect(unsupported).toContain("cannot run voice separation");
    expect(unsupported).not.toContain("<button");

    // Air-gapped: no download button until the files are imported.
    expect(card({ model: diarizer, downloadsAllowed: false })).not.toContain("<button");
  });
});
