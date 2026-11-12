// What a run's transcription and summary steps did, as its detail says it
// (D-854, D-814). A meeting can publish without a transcript or a summary, and
// the run used to read "Published" either way. Pure so the wording can be
// tested without mounting the Operator.
import { get } from "svelte/store";
import { formatCount, LL, locale } from "cassini-viewer/i18n";
import type { JobAttempt, JobDetailResponse, JobShareOutcome, SkippedShare } from "./types";

const copy = () => get(LL).app.operator.step_outcomes;

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
  const t = copy().transcription;
  if (status === "skipped" && reason === "disabled") {
    return { tone: "info", title: t.disabled() };
  }
  if (status === "skipped" && reason === "model_unavailable") {
    return {
      tone: "warning",
      title: t.model_unavailable(),
      detail,
      hint: t.model_unavailable_hint(),
    };
  }
  const titles: Record<string, () => string> = {
    transcription_failed: t.failed,
    insufficient_resources: t.insufficient_resources,
  };
  return {
    tone: "error",
    title: titles[reason]?.() ?? t.other({ reason: reason || status }),
    detail,
    hint: rerunAvailable ? t.rerun_hint() : undefined,
  };
}

export function summaryNotice(attempt: JobAttempt): OutcomeNotice | null {
  const status = attempt.summary_status ?? "";
  const reason = attempt.summary_reason ?? "";
  // No transcript means nothing to summarize; the transcription notice says
  // why, and a second notice would only repeat it.
  const t = copy().summary;
  if (status === "completed" && reason === "template_fallback") {
    // Written, but not by the template the summary step is set to run: that
    // one could not run, and the built-in wrote it instead (D-819).
    return {
      tone: "warning",
      title: t.template_fallback(),
      hint: t.template_fallback_hint({ templates: get(LL).common.place.insight_templates(), pipeline: get(LL).common.place.publish_pipeline() }),
    };
  }
  if (status === "" || status === "completed" || reason === "no_transcript") return null;
  if (status === "failed") {
    return {
      tone: "error",
      title: t.failed(),
      hint: t.failed_hint(),
    };
  }
  if (reason === "disabled") return { tone: "info", title: t.disabled() };
  if (reason === "not_configured") {
    return { tone: "warning", title: t.not_configured(), hint: t.not_configured_hint() };
  }
  return { tone: "warning", title: t.other({ reason: reason || status }) };
}

// The kind of recipient, as the small label beside its id. "Team" is
// Nextcloud's name for a circle.
function principalKind(type: string): string {
  const t = copy().principal;
  if (type === "user") return t.person();
  if (type === "group") return t.group();
  if (type === "circle") return t.team();
  return type;
}

function refusedEntry(skipped: SkippedShare): RefusedEntry {
  return { id: skipped.id, kind: principalKind(skipped.type), reason: skipped.reason ?? "" };
}

// The one-line form of an entry, for a sentence: "bob", "marketing (group)",
// with the reason after a colon where there is one.
// Nextcloud's reason arrives in English.
export function describeRefused(entry: RefusedEntry): string {
  const t = copy().refused;
  // A person is the plain case, written with no label in a sentence.
  const who = entry.kind === copy().principal.person() ? entry.id : t.entry_with_kind({ id: entry.id, kind: entry.kind });
  return entry.reason ? t.entry_with_reason({ who, reason: entry.reason }) : who;
}

export function recipients(count: number): string {
  return copy().recipients({ count });
}

// "bob", "bob and carol", "bob, carol and dave", "bob, carol, dave and 2 others".
// The list itself is in the reader's language (Intl.ListFormat): "and" is a word,
// so it follows the language, not the date-and-number locale.
export function listRefusedIds(ids: string[], shown = 3): string {
  const named = ids.slice(0, shown);
  const rest = ids.length - named.length;
  if (rest > 0) {
    const names = new Intl.ListFormat(get(locale), { type: "unit", style: "short" }).format(named);
    return copy().refused.list_more({ names, count: rest });
  }
  if (named.length <= 1) return named.join("");
  return new Intl.ListFormat(get(locale), { type: "conjunction" }).format(named);
}

// What a re-share's answer says beside the button: everyone, or who is still
// refused and where the setting is named, and how many Nextcloud's share
// limit (20 new shares per account every 10 minutes) held back again.
export function reshareOutcome(refusedIds: string[], deferredCount = 0): string {
  const t = copy().reshare;
  const heldBack = t.held_back({ recipients: recipients(deferredCount) });
  if (refusedIds.length === 0) return deferredCount > 0 ? t.shared_again_held_back({ held_back: heldBack }) : t.shared_with_everyone();
  const refused = t.still_refused({ recipients: recipients(refusedIds.length), names: listRefusedIds(refusedIds) });
  return deferredCount > 0 ? t.two_sentences({ first: refused, second: heldBack }) : refused;
}

// Who a meeting could not be shared with (D-885). The person who started the
// recording must receive it or it is not published; anyone else Nextcloud
// refused is listed, because otherwise the meeting simply does not appear for
// them and nobody can tell why. Doctor names the setting; the run names the
// people.
export function shareNotice(
  sharing: JobShareOutcome | null | undefined,
  rerunAvailable = true,
  reshareAvailable = true,
  audienceMode: "room" | "attendees" = "room",
): OutcomeNotice | null {
  if (!sharing) return null;
  const skipped = sharing.skipped ?? [];
  const t = copy().share;
  const attendees = audienceMode === "attendees";
  if (sharing.status === "starter_refused") {
    const starter = skipped[0]?.id ?? "";
    return {
      tone: "error",
      title: starter ? t.starter_refused_named({ starter }) : t.starter_refused(),
      detail: skipped.length > 0 ? describeRefused(refusedEntry(skipped[0])) : undefined,
      hint: rerunAvailable
        ? attendees
          ? t.starter_refused_hint_rerun_attendees()
          : t.starter_refused_hint_rerun()
        : t.starter_refused_hint_no_rerun(),
    };
  }
  const reshareAction = (): OutcomeNotice["action"] =>
    reshareAvailable
      ? {
          kind: "reshare",
          label: attendees ? t.reshare_button_attendees() : t.reshare_button(),
          note: attendees ? t.reshare_note_attendees() : t.reshare_note(),
        }
      : undefined;
  // Who Nextcloud's share limit held back: not refused, but not shared with
  // either until the administrator re-shares once the limit allows.
  const deferred = sharing.deferred ?? [];
  if (sharing.status !== "partial" || skipped.length === 0) {
    if (deferred.length === 0) return null;
    return {
      tone: "warning",
      title: t.held_back({ recipients: recipients(deferred.length) }),
      action: reshareAction(),
    };
  }
  const audience = typeof sharing.audience_count === "number" && sharing.audience_count > 0 ? sharing.audience_count : 0;
  return {
    tone: "warning",
    title: audience
      ? t.partial_of({ count: skipped.length, audience })
      : t.partial({ recipients: recipients(skipped.length) }),
    items: { summary: t.who_was_refused({ count: formatCount(skipped.length) }), entries: skipped.map(refusedEntry) },
    hint: t.partial_hint(),
    heldBack: deferred.length > 0 ? t.also_held_back({ count: deferred.length }) : undefined,
    // Offered only where the operator would do it: not while the meeting is
    // being processed again.
    action: reshareAction(),
  };
}
