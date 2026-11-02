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
