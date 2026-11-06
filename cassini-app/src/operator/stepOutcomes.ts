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
  /** A sentence under the rest: who Nextcloud's share limit also held
   *  back, beside the refused. */
  heldBack?: string;
  /** Who it concerns, behind a disclosure closed by default: the list can be
   *  long, and the summary says how many. */
  items?: { summary: string; entries: RefusedEntry[] };
  /** Something the panel can do about it, rendered as a button, with a
   *  small line under it saying what the button does. */
  action?: { kind: "reshare"; label: string; note: string };
}

/** One recipient Nextcloud refused: the id, its kind as a small label
 *  (person, group or Team) and Nextcloud's reason, muted. */
export interface RefusedEntry {
  id: string;
  kind: string;
  reason: string;
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
  if (status === "completed" && reason === "template_fallback") {
    // Written, but not by the template the summary step is set to run: that
    // one could not run, and the built-in wrote it instead (D-819).
    return {
      tone: "warning",
      title: "Summarised with the built-in template: the configured summary template could not run",
      hint: "The build log says why. Fix the template in Insight templates, or choose another one for summaries in Settings.",
    };
  }
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

const principalKinds: Record<string, string> = { user: "person", group: "group", circle: "Team" };

function refusedEntry(skipped: SkippedShare): RefusedEntry {
  return { id: skipped.id, kind: principalKinds[skipped.type] ?? skipped.type, reason: skipped.reason ?? "" };
}

// The one-line form of an entry, for a sentence: "bob", "marketing (group)",
// with the reason after a colon where there is one.
export function describeRefused(entry: RefusedEntry): string {
  const who = entry.kind === "person" ? entry.id : `${entry.id} (${entry.kind})`;
  return entry.reason ? `${who}: ${entry.reason}` : who;
}

export function recipients(count: number): string {
  return count === 1 ? "1 recipient" : `${count} recipients`;
}

// "bob", "bob and carol", "bob, carol and dave", "bob, carol, dave and 2 more".
export function listRefusedIds(ids: string[], shown = 3): string {
  const named = ids.slice(0, shown);
  const rest = ids.length - named.length;
  if (rest > 0) return `${named.join(", ")} and ${rest} more`;
  if (named.length <= 1) return named.join("");
  return `${named.slice(0, -1).join(", ")} and ${named[named.length - 1]}`;
}

// What a re-share's answer says beside the button: everyone, or who is still
// refused and where the setting is named, and how many Nextcloud's share
// limit (20 new shares per account every 10 minutes) held back again.
export function reshareOutcome(refusedIds: string[], deferredCount = 0): string {
  const heldBack = `Nextcloud's share limit still held back ${recipients(deferredCount)}; re-share in 10 minutes.`;
  if (refusedIds.length === 0) return deferredCount > 0 ? `Shared again. ${heldBack}` : "Shared with everyone.";
  const refused = `Shared again. Nextcloud still refused ${recipients(refusedIds.length)} (${listRefusedIds(refusedIds)}). Check Doctor › Recording sharing.`;
  return deferredCount > 0 ? `${refused} ${heldBack}` : refused;
}

// Who a meeting could not be shared with (D-885). The person who started the
// recording must receive it or it is not published; anyone else Nextcloud
// refused is listed, because otherwise the meeting simply does not appear for
// them and nobody can tell why. Doctor names the setting; the run names the
// people.
export function shareNotice(sharing: JobShareOutcome | null | undefined, rerunAvailable = true, reshareAvailable = true): OutcomeNotice | null {
  if (!sharing) return null;
  const skipped = sharing.skipped ?? [];
  if (sharing.status === "starter_refused") {
    const starter = skipped[0]?.id ?? "";
    return {
      tone: "error",
      title: `Not published: Nextcloud refused to share it with ${starter || "the person who started the recording"}${starter ? ", who started the recording" : ""}.`,
      detail: skipped.length > 0 ? describeRefused(refusedEntry(skipped[0])) : undefined,
      hint: rerunAvailable
        ? "Fix the setting Doctor › Recording sharing names, then Rerun. Rerun publishes it and shares it with the whole room."
        : "Fix the setting Doctor › Recording sharing names so later recordings publish. This run cannot be rerun.",
    };
  }
  // Who Nextcloud's share limit held back: not refused, but not shared with
  // either until the administrator re-shares once the limit allows.
  const deferred = sharing.deferred ?? [];
  if (sharing.status !== "partial" || skipped.length === 0) {
    if (deferred.length === 0) return null;
    return {
      tone: "warning",
      title: `Published, but Nextcloud's share limit held back ${recipients(deferred.length)}. Re-share in 10 minutes, or raise the limit (Doctor › Recording sharing).`,
      action: reshareAvailable
      ? {
          kind: "reshare",
          label: "Re-share with the room",
          note: "Adds whoever is missing, including anyone removed by hand. People who accept shares by hand will find it waiting in Browse.",
        }
      : undefined,
    };
  }
  const audience = typeof sharing.audience_count === "number" && sharing.audience_count > 0 ? sharing.audience_count : 0;
  return {
    tone: "warning",
    title: audience
      ? `Published, but not shared with ${skipped.length} of ${audience} recipients`
      : `Published, but not shared with ${recipients(skipped.length)}`,
    items: { summary: `Who was refused (${skipped.length})`, entries: skipped.map(refusedEntry) },
    hint: "Nextcloud refused them because of a sharing setting. Doctor › Recording sharing names it and the fix.",
    heldBack: deferred.length > 0 ? `Nextcloud's share limit also held back ${deferred.length}.` : undefined,
    // Offered only where the operator would do it: not while the meeting is
    // being processed again.
    action: reshareAvailable
      ? {
          kind: "reshare",
          label: "Re-share with the room",
          note: "Adds whoever is missing, including anyone removed by hand. People who accept shares by hand will find it waiting in Browse.",
        }
      : undefined,
  };
}
