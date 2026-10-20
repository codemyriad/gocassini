// What a run's transcription and summary steps did, as its detail says it
// (D-854, D-814). A meeting can publish without a transcript or a summary, and
// the run used to read "Published" either way. Pure so the wording can be
// tested without mounting the Operator.
import type { JobAttempt, JobDetailResponse } from "./types";

export type OutcomeTone = "error" | "warning" | "info";

export interface OutcomeNotice {
  tone: OutcomeTone;
  title: string;
  /** The operator's own account, when it has one: the line a crash ended on,
   *  or the resource that ran out. */
  detail?: string;
  /** What can be done about it, when something can. */
  hint?: string;
}

// The attempt whose outcomes the run detail shows: the published one, or, for
// a run that has not published yet, the newest attempt that has outcomes.
export function outcomeAttempt(detail: JobDetailResponse): JobAttempt | null {
  const published = detail.availability?.published_attempt ?? 0;
  if (published > 0) {
    const attempt = detail.attempts.find((candidate) => candidate.attempt_number === published);
    if (attempt) return attempt;
  }
  return (
    [...detail.attempts]
      .sort((a, b) => b.attempt_number - a.attempt_number)
      .find((attempt) => attempt.transcription_status || attempt.summary_status) ?? null
  );
}

export function transcriptionNotice(attempt: JobAttempt, rerunAvailable: boolean): OutcomeNotice | null {
  const status = attempt.transcription_status ?? "";
  const reason = attempt.transcription_reason ?? "";
  const detail = attempt.transcription_detail?.trim() || undefined;
  if (status === "" || status === "completed") return null;
  if (status === "skipped" && reason === "disabled") {
    return { tone: "info", title: "Published without a transcript: transcription is turned off" };
  }
  if (status === "skipped" && reason === "model_unavailable") {
    return {
      tone: "warning",
      title: "Published without a transcript: no speech model could run",
      detail,
      hint: "Check Speech models in Settings, then rerun to transcribe it.",
    };
  }
  const titles: Record<string, string> = {
    transcription_failed: "Published without a transcript: transcription failed",
    insufficient_resources: "Published without a transcript: not enough memory or GPU to transcribe",
  };
  return {
    tone: "error",
    title: titles[reason] ?? `Published without a transcript (${reason || status})`,
    detail,
    hint: rerunAvailable ? "Rerun transcribes it again." : undefined,
  };
}

export function summaryNotice(attempt: JobAttempt): OutcomeNotice | null {
  const status = attempt.summary_status ?? "";
  const reason = attempt.summary_reason ?? "";
  // No transcript means nothing to summarize; the transcription notice says
  // why, and a second notice would only repeat it.
  if (status === "" || status === "completed" || reason === "no_transcript") return null;
  if (status === "failed") {
    return {
      tone: "error",
      title: "No summary: the summary could not be generated",
      hint: "The build log has the language model's error.",
    };
  }
  if (reason === "disabled") return { tone: "info", title: "No summary: summaries are turned off" };
  if (reason === "not_configured") {
    return { tone: "warning", title: "No summary: no language model is set up for summaries", hint: "Add one in Settings." };
  }
  return { tone: "warning", title: `No summary (${reason || status})` };
}
