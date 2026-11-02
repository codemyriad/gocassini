// What a run's transcription and summary steps did, as its detail says it
// (D-854, D-814). A meeting can publish without a transcript or a summary, and
// the run used to read "Published" either way. Pure so the wording can be
// tested without mounting the Operator.
import type { JobAttempt, JobDetailResponse, JobShareOutcome, SkippedShare } from "./types";

export type OutcomeTone = "error" | "warning" | "info";

export interface OutcomeNotice {
  tone: OutcomeTone;
  title: string;
  /** The operator's own account, when it has one: the line a crash ended on,
   *  or the resource that ran out. */
  detail?: string;
  /** What can be done about it, when something can. */
  hint?: string;
  /** Who it concerns, behind a disclosure: the list can be long, and the
   *  title already says how many. */
  items?: { summary: string; entries: string[] };
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

const principalKinds: Record<string, string> = { group: "group", circle: "Team" };

function describeSkipped(skipped: SkippedShare): string {
  const kind = principalKinds[skipped.type];
  const who = kind ? `${skipped.id} (${kind})` : skipped.id;
  return skipped.reason ? `${who}: ${skipped.reason}` : who;
}

function recipients(count: number): string {
  return count === 1 ? "1 recipient" : `${count} recipients`;
}

// Who a meeting could not be shared with (D-885). The person who started the
// recording must receive it or it is not published; anyone else Nextcloud
// refused is listed, because otherwise the meeting simply does not appear for
// them and nobody can tell why.
export function shareNotice(sharing: JobShareOutcome | null | undefined, rerunAvailable = true): OutcomeNotice | null {
  if (!sharing) return null;
  const skipped = sharing.skipped ?? [];
  if (sharing.status === "starter_refused") {
    return {
      tone: "error",
      title: "Not published: Nextcloud refused to share it with the person who started the recording",
      detail: skipped.length > 0 ? describeSkipped(skipped[0]) : undefined,
      hint: rerunAvailable
        ? "Doctor → Recording sharing names the setting and how to change it. Rerun then publishes it and shares it with the whole room."
        : "Doctor → Recording sharing names the setting and how to change it, so that later recordings publish. This run cannot be rerun.",
    };
  }
  if (sharing.status !== "partial" || skipped.length === 0) return null;
  return {
    tone: "warning",
    title: `Published, but not shared with everyone: Nextcloud refused ${recipients(skipped.length)}`,
    items: { summary: `Not shared with ${recipients(skipped.length)}`, entries: skipped.map(describeSkipped) },
    hint: "Doctor → Recording sharing names the setting and how to change it. A rerun does not share an already published meeting again.",
  };
}
