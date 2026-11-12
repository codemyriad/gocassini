import { get } from "svelte/store";
import { LL, numberFormat } from "cassini-viewer/i18n";
import type { SpeechModel, SpeechModelInventory, SpeechModelJob } from "./operator/types";

// The speaker separation model Settings offers (docs/speaker-separation.md).
// It always installs and runs on the CPU, whatever device transcribes.
export const VOICE_SEPARATION_MODEL_ID = "nemotron-3-diarization-int8";

// The speech model a quality tier and device transcribe with. Every GPU tier
// runs fp32 Parakeet Ultra; on the CPU, best is Ultra fp32, balanced its int8
// build and fast the 110M model.
// Copies: speechModelFor in operator/pipelineStatus.ts, modelForQuality in the
// operator's device.go, ModelForQuality in the recorder's policy.go.
export function transcriptionModelId(quality: string, device: string): string {
  if (device === "cuda") return "parakeet-ultra-0.6b";
  if (quality === "best") return "parakeet-ultra-0.6b";
  return quality === "fast" ? "parakeet-tdt-ctc-110m-en-int8-v2" : "parakeet-ultra-0.6b-int8-v2";
}

export function voiceSeparationModel(inventory: SpeechModelInventory | null): SpeechModel | undefined {
  return inventory?.models.find((m) => m.kind === "diarization" && m.id === VOICE_SEPARATION_MODEL_ID);
}

export function modelJob(inventory: SpeechModelInventory | null, model: SpeechModel | undefined, device: string): SpeechModelJob | undefined {
  if (!model) return undefined;
  return inventory?.jobs.find((j) => j.model === model.id && j.device === device && j.revision === model.revision);
}

export function jobRunning(job: SpeechModelJob | undefined): boolean {
  return !!job && !["ready", "failed", "cancelled"].includes(job.state);
}

// A size in GiB from 1 GiB up, MiB below, written for the reader's locale.
export function modelSize(n: number): string {
  if (n < 1024 ** 3) return mebibytes(n);
  const size = numberFormat({ minimumFractionDigits: 1, maximumFractionDigits: 1 }).format(n / 1024 ** 3);
  return get(LL).app.settings.speech_models.size_gib({ size });
}

export function mebibytes(n: number): string {
  const size = numberFormat({ minimumFractionDigits: 1, maximumFractionDigits: 1 }).format(n / 1024 / 1024);
  return get(LL).app.settings.speech_models.size_mib({ size });
}

// What a running model job is doing, in words. After the download the job
// waits for any meeting being processed (they share the same gate), then
// checks the model runs here; without saying so it looks stuck at 100%.
export function jobStepText(job: SpeechModelJob): string {
  const { completed_bytes: done, total_bytes: total } = job.progress;
  const t = get(LL).app.settings.speech_models.job_step;
  switch (job.state) {
    case "queued": return t.queued();
    case "downloading": return total > 0 ? t.downloading_percent({ percent: Math.min(100, Math.round((done / total) * 100)) }) : t.downloading();
    case "verifying": return t.verifying();
    case "unpacking": return t.unpacking();
    case "installed": return t.installed();
    case "checking": return t.checking();
    // A state this build does not know, as the operator sent it.
    default: return job.state;
  }
}
