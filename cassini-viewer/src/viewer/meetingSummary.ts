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

export type SummaryRequestRunState = "idle" | "writing" | "failed";

export interface SummaryRequestState {
  available: boolean;
  /** not_published, no_transcript, summaries_off, no_source_audio or busy. */
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
  if (summary.status === "failed") return "No summary: it could not be generated when the meeting was published.";
  switch (summary.reason) {
    case "disabled":
      return "No summary: summaries were turned off when this meeting was published.";
    case "not_configured":
      return "No summary: no language model was set up for summaries when this meeting was published.";
    default:
      return null;
  }
}

// "1 call to deepseek/deepseek-v4.1-flash (Open Router) · about 7.5k input tokens, 5,213 words"
export function summaryCostLine(state: SummaryRequestState): string {
  const calls = `${state.calls} ${state.calls === 1 ? "call" : "calls"}`;
  const target = state.model
    ? `${state.model}${state.provider ? ` (${state.provider})` : ""}`
    : state.provider || "the summary model";
  return `${calls} to ${target} · about ${approxTokens(state.estimatedInputTokens)} input tokens, ${state.words.toLocaleString("en-US")} words`;
}

export function summaryEndpointLine(state: SummaryRequestState): string {
  return state.external
    ? "The transcript is sent to an external service, which may bill for it."
    : "The model runs on your own network.";
}

function approxTokens(tokens: number): string {
  if (tokens < 1000) return String(tokens);
  return `${(Math.round(tokens / 100) / 10).toLocaleString("en-US")}k`;
}

export function describeSummaryRequestError(error: unknown): string {
  if (error instanceof SummaryRequestError) {
    switch (error.code) {
      case "busy":
        return "A summary is already being written for this meeting.";
      case "has-summary":
        return "This meeting has a summary now. Reload it to see it.";
      case "forbidden":
        return "Only an administrator or a moderator of the room can replace this summary.";
      case "rate-limited":
        return "Too many summaries asked for in a short time. Try again later.";
      case "unavailable":
        return "A summary can't be written for this meeting now.";
    }
  }
  return `Couldn't ask for the summary: ${error instanceof Error ? error.message : String(error)}`;
}
