// Which models made an attempt's meeting (D-816), as the run detail names
// them: the speech model and where it ran, and the summary model. Empty for
// steps that did not run and for attempts built before this was recorded.
import type { JobAttempt } from "./types";

export function deviceLabel(device: string | undefined): string {
  if (device === "cuda") return "GPU";
  if (device === "cpu") return "CPU";
  return device ?? "";
}

export function speechModelLabel(attempt: JobAttempt): string | null {
  const model = attempt.transcription_model?.trim();
  if (!model) return null;
  const device = deviceLabel(attempt.transcription_device?.trim());
  return device ? `${model} on ${device}` : model;
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
      const recorded = attempt.record_finished_at ? "Recording ended" : "Recording";
      return attempt.stop_reason ? `${recorded} · ${stopReason(attempt.stop_reason)}` : recorded;
    }
    case "rerun":
      return "Rerun requested";
    case "summarize":
      return "Summary requested";
    case "refine":
      return attempt.summary_mode === "rewrite" ? "Speakers renamed, summary rewritten" : "Speakers renamed, summary kept";
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
    return { text: source ? `Reused from attempt ${source.attempt_number}` : "Reused", muted: true };
  }
  const label = speechModelLabel(attempt);
  return label ? { text: label } : null;
}

const SKIPPED_SUMMARY: Record<string, string> = {
  disabled: "Skipped: summaries are turned off",
  not_configured: "Skipped: no summary model is configured",
  no_transcript: "Skipped: there was no transcript",
};

// The summary model, or what the summary step did instead: skipped and why,
// failed, or kept by a speaker-edit save that did not ask to rewrite it.
export function attemptSummary(attempt: JobAttempt): FieldValue | null {
  // First: a kept summary still names the model of the build that wrote it.
  if (attempt.trigger_kind === "refine" && attempt.summary_mode !== "rewrite") return { text: "Kept as it was", muted: true };
  const model = summaryModelLabel(attempt);
  if (model) return { text: model };
  switch (attempt.summary_status) {
    case "skipped":
      return { text: SKIPPED_SUMMARY[attempt.summary_reason ?? ""] ?? "Skipped", muted: true };
    case "failed":
      return { text: attempt.summary_reason === "llm_failed" ? "Failed: the model returned an error" : "Failed", muted: true };
    case "completed":
      return { text: "Written; the model was not recorded", muted: true };
    default:
      return null;
  }
}

export const LOG_STAGES = [
  { key: "record", label: "Record", field: "record_log_path" },
  { key: "build", label: "Build", field: "build_log_path" },
  { key: "seal", label: "Seal", field: "seal_log_path" },
  { key: "publish", label: "Publish", field: "publish_log_path" },
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
      label: stage.label,
      written,
      present: written && attempt.files_present?.[`${stage.key}_log`] !== false,
    };
  });
}
