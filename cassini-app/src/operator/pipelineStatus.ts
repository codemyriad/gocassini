// What each publish-pipeline step does with the SAVED settings, as the status
// strip at the top of the Publishing card says it. Pure so the wording and the
// warnings can be tested without mounting the panel.
import type { RetentionPolicy } from "./retention";
import type { SettingsQuality, SpeechModelInventory, SpeechModelJob } from "./types";

export type StepTone = "on" | "off" | "amber" | "busy";
export interface StepStatus { state: string; tone: StepTone; badge?: string; reason?: string }
export interface PipelineStatus { transcribe: StepStatus; summarise: StepStatus; publish: StepStatus; keep: StepStatus }

export type KeepChoice = "nothing" | "audio-video" | "audio-only";

export interface PipelineInput {
  meetingFormat: "opus" | "json";
  transcriptionEnabled: boolean;
  quality: SettingsQuality;
  /** The saved model new recordings transcribe with; it names the tier when set. */
  activeModel?: string;
  /** Whether that saved model's files are installed; null while unknown. Not
   *  having passed the runtime check yet is fine: builds run it first. */
  activeModelInstalled?: boolean | null;
  /** The effective device from the hardware facts: "cuda" or "cpu". */
  device: string;
  /** Whether the model for the saved quality is installed; null while unknown. */
  modelInstalled: boolean | null;
  /** A model download in progress, if any; percent is null when its size is unknown. */
  download: { percent: number | null } | null;
  summaryEnabled: boolean;
  /** Display name of the saved summary provider. */
  providerName: string;
  /** Null when the AI provider settings could not be read. */
  hasProvider: boolean | null;
  keep: KeepChoice;
  /** The Recordings retention policy; null when retention could not be loaded. */
  recordingsPolicy: RetentionPolicy | null;
}

const QUALITY_LABELS: Record<SettingsQuality, string> = { fast: "Fast", balanced: "Balanced", best: "Best" };

// The tier a speech model belongs to. Builds use the saved model, not the
// quality setting, so the strip names what actually transcribes.
const MODEL_TIERS: Record<string, string> = {
  "parakeet-tdt-ctc-110m-en-int8": "Fast",
  "parakeet-tdt-0.6b-v3-int8": "Balanced",
  "parakeet-tdt-0.6b-v3": "Best",
  "parakeet-ultra-0.6b": "Best",
};

// The model a quality tier runs on a device. On the GPU every tier runs the
// full-precision Parakeet Ultra; a GPU install pinned to v3 fp32 keeps it.
export function speechModelFor(quality: string, device: string): string {
  if (device === "cuda") return "parakeet-ultra-0.6b";
  if (quality === "best") return "parakeet-tdt-0.6b-v3";
  return quality === "fast" ? "parakeet-tdt-ctc-110m-en-int8" : "parakeet-tdt-0.6b-v3-int8";
}

const FINISHED_JOB_STATES = ["ready", "failed", "cancelled"];

// A transcription model being downloaded. Voice separation's download is not
// one: nothing in the pipeline waits for it.
export function runningDownload(inventory: SpeechModelInventory | null): { percent: number | null } | null {
  const separation = new Set(inventory?.models.filter((m) => m.kind === "diarization").map((m) => m.id));
  const job: SpeechModelJob | undefined = inventory?.jobs.find((j) => !separation.has(j.model) && !FINISHED_JOB_STATES.includes(j.state));
  if (!job) return null;
  const { completed_bytes: done, total_bytes: total } = job.progress;
  return { percent: total > 0 ? Math.min(100, Math.round((done / total) * 100)) : null };
}

/** "30 days", "1 day" or "forever"; null when the policy is unknown. */
export function retentionSpan(policy: RetentionPolicy | null | undefined): string | null {
  if (!policy) return null;
  if (policy.forever) return "forever";
  if (!policy.count || policy.count <= 0) return null;
  return policy.count === 1 ? "1 day" : `${policy.count} days`;
}

export function pipelineStatus(input: PipelineInput): PipelineStatus {
  const transcribing = input.transcriptionEnabled;
  const downloading = input.download !== null;

  let transcribe: StepStatus;
  if (transcribing) {
    const state = `On · ${(input.activeModel && MODEL_TIERS[input.activeModel]) || QUALITY_LABELS[input.quality] || input.quality} · ${input.device === "cuda" ? "GPU" : "CPU"}`;
    // Switched on with a model that is not there: recordings publish without a
    // transcript, so the strip must not read as healthy.
    transcribe = input.activeModelInstalled === false
      ? { state, tone: "amber", reason: "model not installed" }
      : { state, tone: "on" };
  } else if (input.download) {
    const percent = input.download.percent;
    transcribe = { state: percent === null ? "Off · downloading" : `Off · downloading ${percent}%`, tone: "busy" };
  } else {
    transcribe = { state: "Off", tone: "off", ...(input.modelInstalled === false ? { badge: "download a model" } : {}) };
  }

  let summarise: StepStatus;
  if (input.hasProvider === null) {
    summarise = { state: "Unknown", tone: "off" };
  } else if (input.summaryEnabled && input.hasProvider) {
    const state = input.providerName ? `On · ${input.providerName}` : "On";
    summarise = transcribing ? { state, tone: "on" } : { state, tone: "amber", reason: "needs a transcript" };
  } else if (!transcribing) {
    // Nothing is wrong: the summary waits on a transcript before a provider.
    summarise = { state: "Off", tone: "off", badge: "needs a transcript" };
  } else {
    summarise = { state: "Off", tone: "off", ...(input.hasProvider ? {} : { badge: "add a provider" }) };
  }

  let publish: StepStatus;
  if (input.meetingFormat === "json") {
    publish = transcribing
      ? { state: "Transcript only", tone: "on" }
      : { state: "Transcript only", tone: "amber", reason: "needs Transcribe on" };
  } else {
    publish = {
      state: transcribing ? "Audio + transcript" : "Audio, no transcript",
      tone: "on",
      ...(downloading && !transcribing ? { badge: "until the model is ready" } : {}),
    };
  }

  let keep: StepStatus;
  if (input.keep === "nothing") {
    keep = { state: "Nothing", tone: "off", badge: "no reprocessing later" };
  } else {
    const span = retentionSpan(input.recordingsPolicy);
    const media = input.keep === "audio-video" ? "Audio + video" : "Audio only";
    keep = { state: span ? `${media} · ${span}` : media, tone: "on" };
  }

  return { transcribe, summarise, publish, keep };
}
