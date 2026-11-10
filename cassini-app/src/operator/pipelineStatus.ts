// What each publish-pipeline step does with the SAVED settings, as the status
// strip at the top of the Publishing card says it. Pure so the wording and the
// warnings can be tested without mounting the panel.
import { get } from "svelte/store";
import { LL } from "cassini-viewer/i18n";
import type { RetentionPolicy } from "./retention";
import type { SettingsQuality, SpeechModel, SpeechModelInventory, SpeechModelJob } from "./types";

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
   *  having been through the runtime check yet is fine: builds run it first,
   *  and use the model it replaced (activeModelStandIn) once it failed. */
  activeModelInstalled?: boolean | null;
  /** The effective device from the hardware facts: "cuda" or "cpu". */
  device: string;
  /** Whether the model for the saved quality is installed; null while unknown. */
  modelInstalled: boolean | null;
  /** A model download in progress, if any; percent is null when its size is unknown. */
  download: { percent: number | null } | null;
  /** The saved model's own download, if it is being installed (an upgrade
   *  that replaced the model queues one); recordings wait for it, or are
   *  transcribed with the model it replaces while that one's files are still
   *  installed. */
  activeModelDownload?: { percent: number | null } | null;
  /** Whether the model the saved one replaced is still installed and
   *  transcribes recordings until the saved one is ready: while its files are
   *  missing, or installed but failed their runtime check. */
  activeModelStandIn?: boolean;
  summaryEnabled: boolean;
  /** Display name of the saved summary provider. */
  providerName: string;
  /** Null when the AI provider settings could not be read. */
  hasProvider: boolean | null;
  keep: KeepChoice;
  /** The Recordings retention policy; null when retention could not be loaded. */
  recordingsPolicy: RetentionPolicy | null;
}

const copy = () => get(LL).app.operator.pipeline_status;

const QUALITY_LABELS: Record<SettingsQuality, () => string> = {
  fast: () => get(LL).app.quality.fast(),
  balanced: () => get(LL).app.quality.balanced(),
  best: () => get(LL).app.quality.best(),
};

// The tier a speech model belongs to. Builds use the saved model, not the
// quality setting, so the strip names what actually transcribes.
const MODEL_TIERS: Record<string, SettingsQuality> = {
  "parakeet-tdt-ctc-110m-en-int8": "fast",
  "parakeet-ultra-0.6b-int8": "balanced",
  "parakeet-ultra-0.6b": "best",
};

// The model a quality tier runs on a device. On the GPU every tier runs the
// full-precision Parakeet Ultra; on the CPU, best is Ultra fp32, balanced its
// int8 build and fast the small 110M model.
export function speechModelFor(quality: string, device: string): string {
  if (device === "cuda") return "parakeet-ultra-0.6b";
  if (quality === "best") return "parakeet-ultra-0.6b";
  return quality === "fast" ? "parakeet-tdt-ctc-110m-en-int8" : "parakeet-ultra-0.6b-int8";
}

const FINISHED_JOB_STATES = ["ready", "failed", "cancelled"];

// A transcription model being downloaded. Voice separation's download is not
// one: nothing in the pipeline waits for it.
// With a model id, only that model's download counts.
export function runningDownload(inventory: SpeechModelInventory | null, model?: string): { percent: number | null } | null {
  const separation = new Set(inventory?.models.filter((m) => m.kind === "diarization").map((m) => m.id));
  const job: SpeechModelJob | undefined = inventory?.jobs.find((j) => !separation.has(j.model) && (model === undefined || j.model === model) && !FINISHED_JOB_STATES.includes(j.state));
  if (!job) return null;
  const { completed_bytes: done, total_bytes: total } = job.progress;
  return { percent: total > 0 ? Math.min(100, Math.round((done / total) * 100)) : null };
}

/** What happens to recordings with the model the Transcript card names, when
 *  it is not ready: the model it replaced transcribes them meanwhile
 *  ("stand-in"), they wait for its download ("downloading"), or they get no
 *  transcript ("missing"). The operator names a stand-in for an installed
 *  model only once its runtime check failed, so that one is "stand-in" too.
 *  Otherwise an installed model is "installed", checked or not: builds check
 *  it first and transcribe with it when the check passes. */
export type InUseModelState = "installed" | "stand-in" | "downloading" | "missing";

export function inUseModelState(model: Pick<SpeechModel, "installed" | "stand_in">, download: { percent: number | null } | null): InUseModelState {
  if (model.stand_in) return "stand-in";
  if (model.installed) return "installed";
  return download ? "downloading" : "missing";
}

/** "30 days", "1 day" or "forever"; null when the policy is unknown. */
export function retentionSpan(policy: RetentionPolicy | null | undefined): string | null {
  if (!policy) return null;
  const t = copy().retention;
  if (policy.forever) return t.forever();
  if (!policy.count || policy.count <= 0) return null;
  return t.days({ count: policy.count });
}

export function pipelineStatus(input: PipelineInput): PipelineStatus {
  const t = copy();
  const transcribing = input.transcriptionEnabled;
  const downloading = input.download !== null;

  let transcribe: StepStatus;
  if (transcribing) {
    const tier = (input.activeModel && MODEL_TIERS[input.activeModel]) || input.quality;
    const state = t.on_with({ detail: `${QUALITY_LABELS[tier]?.() || input.quality} · ${input.device === "cuda" ? "GPU" : "CPU"}` });
    // Switched on with a model that is not there: recordings publish without a
    // transcript, so the strip must not read as healthy. One being installed
    // is different: recordings wait for it. Either way, while the model it
    // replaced is still installed (v3, after the upgrade to Ultra), recordings
    // are transcribed with that one meanwhile, and the strip says so. So it
    // does for an installed model whose runtime check failed: the operator
    // names a stand-in for an installed model only then, not while its check
    // is still to run.
    const installing = input.activeModelDownload;
    const meanwhile = input.activeModelInstalled != null && input.activeModelStandIn ? ` · ${t.reason.previous_model_meanwhile()}` : "";
    if (input.activeModelInstalled === false && installing) {
      const reason = installing.percent === null
        ? t.reason.downloading_model()
        : t.reason.downloading_model_percent({ percent: installing.percent });
      transcribe = { state, tone: "busy", reason: reason + meanwhile };
    } else if (meanwhile) {
      const why = input.activeModelInstalled ? t.reason.model_check_failed() : t.reason.model_not_installed();
      transcribe = { state, tone: "amber", reason: why + meanwhile };
    } else {
      transcribe = input.activeModelInstalled === false
        ? { state, tone: "amber", reason: t.reason.model_not_installed() }
        : { state, tone: "on" };
    }
  } else if (input.download) {
    const percent = input.download.percent;
    transcribe = { state: percent === null ? t.off_downloading() : t.off_downloading_percent({ percent }), tone: "busy" };
  } else {
    transcribe = { state: t.off(), tone: "off", ...(input.modelInstalled === false ? { badge: t.badge.download_model() } : {}) };
  }

  let summarise: StepStatus;
  if (input.hasProvider === null) {
    summarise = { state: t.unknown(), tone: "off" };
  } else if (input.summaryEnabled && input.hasProvider) {
    const state = input.providerName ? t.on_with({ detail: input.providerName }) : t.on();
    summarise = transcribing ? { state, tone: "on" } : { state, tone: "amber", reason: t.reason.needs_transcript() };
  } else if (!transcribing) {
    // Nothing is wrong: the summary waits on a transcript before a provider.
    summarise = { state: t.off(), tone: "off", badge: t.badge.needs_transcript() };
  } else {
    summarise = { state: t.off(), tone: "off", ...(input.hasProvider ? {} : { badge: t.badge.add_provider() }) };
  }

  let publish: StepStatus;
  if (input.meetingFormat === "json") {
    publish = transcribing
      ? { state: t.publish.transcript_only(), tone: "on" }
      : { state: t.publish.transcript_only(), tone: "amber", reason: t.reason.needs_transcribe_on() };
  } else {
    publish = {
      state: transcribing ? t.publish.audio_transcript() : t.publish.audio_no_transcript(),
      tone: "on",
      ...(downloading && !transcribing ? { badge: t.badge.until_model_ready() } : {}),
    };
  }

  let keep: StepStatus;
  if (input.keep === "nothing") {
    keep = { state: get(LL).app.keep.nothing(), tone: "off", badge: t.badge.no_reprocessing() };
  } else {
    const span = retentionSpan(input.recordingsPolicy);
    const media = input.keep === "audio-video" ? get(LL).app.keep.audio_video() : get(LL).app.keep.audio_only();
    keep = { state: span ? `${media} · ${span}` : media, tone: "on" };
  }

  return { transcribe, summarise, publish, keep };
}
