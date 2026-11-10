// A meeting's summary as its readers can ask for it (D-814): the operator's
// `annotations/meetings/<id>/summary` route, and what the page says about a
// meeting that has none.
//
// Writing a summary calls a language model, which may be billed, so nothing is
// asked for before the reader has seen how many calls, to which model, of what
// size, and whether the transcript leaves the network. Anyone who can read the
// meeting may fill a missing summary; replacing one is for an administrator or
// a moderator of the meeting's room, which the operator decides and reports as
// canRegenerate.

import { get } from "svelte/store";

import { LL } from "../i18n";
import { formatCompactCount, numberFormat } from "../i18n/format";

export type SummaryRequestRunState = "idle" | "writing" | "failed";

export interface SummaryRequestState {
  available: boolean;
  /** not_published, no_transcript, summaries_off, no_local_copy or busy. */
  reason?: string;
  hasSummary: boolean;
  /** No summary, and one can be written now. */
  canWrite: boolean;
  /** A summary, and this reader may replace it. */
  canRegenerate: boolean;
  calls: number;
  words: number;
  estimatedInputTokens: number;
  provider?: string;
  model?: string;
  /** The model is not on this network, so the call may be billed. */
  external: boolean;
  state: SummaryRequestRunState;
}

/** What a speaker-edit save does with the meeting's summary. */
export type SpeakerSummaryChoice = "keep" | "rewrite";

export class SummaryRequestError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "SummaryRequestError";
  }
}

// What the summary step did, as the meeting file records it
// (processing.meetingSummary). Absent in files from before it was recorded.
export interface SummaryStatus {
  status: "completed" | "skipped" | "failed";
  reason?: string;
}

export function readSummaryStatus(processing: unknown): SummaryStatus | undefined {
  if (!processing || typeof processing !== "object") return undefined;
  const item = (processing as Record<string, unknown>).meetingSummary;
  if (!item || typeof item !== "object") return undefined;
  const { status, reason } = item as Record<string, unknown>;
  if (status !== "completed" && status !== "skipped" && status !== "failed") return undefined;
  return { status, reason: typeof reason === "string" ? reason : undefined };
}

// Why a meeting has no summary, from what its file says. Null when it says
// nothing useful: a file from before the outcome was recorded, or one with no
// transcript, which the transcript itself already explains.
export function missingSummaryText(summary: SummaryStatus | undefined): string | null {
  if (!summary || summary.status === "completed") return null;
  const t = get(LL).viewer.insights.summary;
  if (summary.status === "failed") return t.missing_failed();
  switch (summary.reason) {
    case "disabled":
      return t.missing_disabled();
    case "not_configured":
      return t.missing_not_configured();
    default:
      return null;
  }
}

// "1 call to deepseek/deepseek-v4.1-flash (Open Router) · about 7.5K input tokens, 5,213 words"
// Shared with the administrator's run detail (cassini-app summaryAction.ts).
export function summaryCostLine(
  state: Pick<SummaryRequestState, "calls" | "words" | "estimatedInputTokens" | "provider" | "model">,
): string {
  const t = get(LL).common.summary_request;
  const target = state.model
    ? state.provider
      ? t.cost_target({ model: state.model, provider: state.provider })
      : state.model
    : state.provider || t.cost_target_fallback();
  const tokens = approxTokens(state.estimatedInputTokens);
  const words = numberFormat().format(state.words);
  return state.calls === 1
    ? t.cost_one({ target, tokens, words })
    : t.cost_other({ count: numberFormat().format(state.calls), target, tokens, words });
}

export function summaryEndpointLine(state: Pick<SummaryRequestState, "external">): string {
  const t = get(LL).common.summary_request;
  return state.external ? t.endpoint_external() : t.endpoint_local();
}

// "850", "7.5K": the locale's short form for a large count.
function approxTokens(tokens: number): string {
  return formatCompactCount(tokens);
}

export function describeSummaryRequestError(error: unknown): string {
  const t = get(LL).viewer.insights.summary;
  if (error instanceof SummaryRequestError) {
    switch (error.code) {
      case "busy":
        return t.error_busy();
      case "has-summary":
        return t.error_has_summary();
      case "forbidden":
        return t.error_forbidden();
      case "rate-limited":
        return t.error_rate_limited();
      case "unavailable":
        return t.error_unavailable();
    }
  }
  return t.error_other({ error: error instanceof Error ? error.message : String(error) });
}
