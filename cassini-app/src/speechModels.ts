import type { SpeechModel, SpeechModelInventory, SpeechModelJob } from "./operator/types";

// The speaker separation model Settings offers (docs/speaker-separation.md).
// It always installs and runs on the CPU, whatever device transcribes.
export const VOICE_SEPARATION_MODEL_ID = "nemotron-3-diarization-int8";

// The speech model a quality tier and device transcribe with. Only these
// three are ever offered as the transcription model.
export function transcriptionModelId(quality: string, device: string): string {
  if (device === "cuda" || quality === "best") return "parakeet-tdt-0.6b-v3";
  return quality === "fast" ? "parakeet-tdt-ctc-110m-en-int8" : "parakeet-tdt-0.6b-v3-int8";
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

export function mebibytes(n: number): string {
  return `${(n / 1024 / 1024).toFixed(1)} MiB`;
}
