// Which models made an attempt's meeting (D-816), as the run detail names
// them: the speech model and where it ran, and the summary model. Empty for
// steps that did not run and for attempts built before this was recorded.
import { get } from "svelte/store";
import { LL } from "cassini-viewer/i18n";
import type { JobAttempt } from "./types";

const copy = () => get(LL).app.operator.attempt_models;

export function deviceLabel(device: string | undefined): string {
  if (device === "cuda") return "GPU";
  if (device === "cpu") return "CPU";
  return device ?? "";
}

export function speechModelLabel(attempt: JobAttempt): string | null {
  const model = attempt.transcription_model?.trim();
  if (!model) return null;
  const device = deviceLabel(attempt.transcription_device?.trim());
  return device ? copy().model_on_device({ model, device }) : model;
}

export function summaryModelLabel(attempt: JobAttempt): string | null {
  return attempt.summary_model?.trim() || null;
}

// A field's value as the run detail shows it: what is known, or, muted, why
// there is nothing to name (D-816).
export interface FieldValue {
  text: string;
  /** Not a value but an account of its absence: reused, skipped, kept. */
  muted?: boolean;
}

// Only these kinds transcribe; the others reuse the transcript they find.
const TRANSCRIBING_KINDS = new Set(["initial", "rerun"]);

// Why the attempt ran. A speaker-edit save says whether it asked for the
// summary to be written again for the new names.
export function attemptReason(attempt: JobAttempt, stopReason: (reason: string) => string): string {
  switch (attempt.trigger_kind) {
    case "initial": {
      const t = copy().reason;
      const recorded = attempt.record_finished_at ? t.recording_ended() : t.recording();
      return attempt.stop_reason ? `${recorded} · ${stopReason(attempt.stop_reason)}` : recorded;
    }
    case "rerun":
      return copy().reason.rerun();
    case "summarize":
      return copy().reason.summarize();
    case "refine":
      return attempt.summary_mode === "rewrite" ? copy().reason.refine_rewrite() : copy().reason.refine_keep();
    default:
      return attempt.trigger_kind;
  }
}

// The speech model, or, for an attempt that transcribed nothing, where its
// transcript came from: the newest transcription before it.
export function attemptSpeech(attempt: JobAttempt, attempts: JobAttempt[]): FieldValue | null {
  if (!TRANSCRIBING_KINDS.has(attempt.trigger_kind)) {
    const source = attempts
      .filter((a) => TRANSCRIBING_KINDS.has(a.trigger_kind) && a.state === "succeeded" && a.attempt_number < attempt.attempt_number)
      .sort((a, b) => b.attempt_number - a.attempt_number)[0];
    return { text: source ? copy().speech.reused_from({ attempt: source.attempt_number }) : copy().speech.reused(), muted: true };
  }
  const label = speechModelLabel(attempt);
  return label ? { text: label } : null;
}

const SKIPPED_SUMMARY: Record<string, () => string> = {
  disabled: () => copy().summary.skipped_disabled(),
  not_configured: () => copy().summary.skipped_not_configured(),
  no_transcript: () => copy().summary.skipped_no_transcript(),
};

// The summary model, or what the summary step did instead: skipped and why,
// failed, or kept by a speaker-edit save that did not ask to rewrite it.
export function attemptSummary(attempt: JobAttempt): FieldValue | null {
  // First: a kept summary still names the model of the build that wrote it.
  const t = copy().summary;
  if (attempt.trigger_kind === "refine" && attempt.summary_mode !== "rewrite") return { text: t.kept(), muted: true };
  const model = summaryModelLabel(attempt);
  if (model) return { text: model };
  switch (attempt.summary_status) {
    case "skipped":
      return { text: SKIPPED_SUMMARY[attempt.summary_reason ?? ""]?.() ?? t.skipped(), muted: true };
    case "failed":
      return { text: attempt.summary_reason === "llm_failed" ? t.failed_llm() : t.failed(), muted: true };
    case "completed":
      return { text: t.written_unrecorded(), muted: true };
    default:
      return null;
  }
}

export const LOG_STAGES = [
  { key: "record", field: "record_log_path" },
  { key: "build", field: "build_log_path" },
  { key: "seal", field: "seal_log_path" },
  { key: "publish", field: "publish_log_path" },
] as const;

export type LogStage = (typeof LOG_STAGES)[number]["key"];

export interface AttemptLog {
  stage: LogStage;
  label: string;
  /** False for a step that has not run, or ran before logs were kept. */
  written: boolean;
  /** False when there is no log to open: never written, or since removed. */
  present: boolean;
}

// Every step's log, in pipeline order, so the run detail always offers the
// same four and says which cannot be opened.
export function attemptLogs(attempt: JobAttempt): AttemptLog[] {
  return LOG_STAGES.map((stage) => {
    const written = !!attempt[stage.field];
    return {
      stage: stage.key,
      // The step's name, as the run's stage bar says it.
      label: get(LL).app.operator.run_stages.stage[stage.key](),
      written,
      present: written && attempt.files_present?.[`${stage.key}_log`] !== false,
    };
  });
}
