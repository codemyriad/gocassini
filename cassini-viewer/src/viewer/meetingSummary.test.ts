import { describe, expect, it } from "vitest";
import {
  SummaryRequestError,
  describeSummaryRequestError,
  missingSummaryText,
  readSummaryStatus,
  summaryCostLine,
  summaryEndpointLine,
  type SummaryRequestState,
} from "./meetingSummary";

const state: SummaryRequestState = {
  available: true, hasSummary: false, canWrite: true, canRegenerate: false, calls: 1, words: 5213,
  estimatedInputTokens: 7480, provider: "Open Router", model: "deepseek/deepseek-v4.1-flash", external: true, state: "idle",
};

describe("meeting summary", () => {
  it("reads what the summary step did from the file", () => {
    expect(readSummaryStatus({ meetingSummary: { status: "failed", reason: "llm_failed" } })).toEqual({ status: "failed", reason: "llm_failed" });
    expect(readSummaryStatus({ transcription: { status: "completed" } })).toBeUndefined();
    expect(readSummaryStatus({ meetingSummary: { status: "maybe" } })).toBeUndefined();
    expect(readSummaryStatus(undefined)).toBeUndefined();
  });

  it("says why a meeting has no summary only when the file knows", () => {
    expect(missingSummaryText({ status: "failed", reason: "llm_failed" })).toContain("could not be generated");
    expect(missingSummaryText({ status: "skipped", reason: "disabled" })).toContain("turned off");
    expect(missingSummaryText({ status: "skipped", reason: "not_configured" })).toContain("no language model");
    // No transcript is the transcript's to explain; an older file says nothing.
    expect(missingSummaryText({ status: "skipped", reason: "no_transcript" })).toBeNull();
    expect(missingSummaryText(undefined)).toBeNull();
    expect(missingSummaryText({ status: "completed" })).toBeNull();
  });

  it("says the cost before anything is sent", () => {
    // Compact numbers differ between ICU versions ("7.5K" in older CLDR, "7.5k" in newer en-GB).
    const tokens = new Intl.NumberFormat("en-GB", { notation: "compact", maximumFractionDigits: 1 }).format(7480);
    expect(summaryCostLine(state)).toBe(`1 call to deepseek/deepseek-v4.1-flash (Open Router) · about ${tokens} input tokens, 5,213 words`);
    expect(summaryEndpointLine(state)).toContain("external service");
    expect(summaryEndpointLine({ ...state, external: false })).toBe("The model runs on your own network.");
  });

  it("explains the operator's refusals", () => {
    expect(describeSummaryRequestError(new SummaryRequestError(403, "forbidden", "x"))).toContain("administrator or a moderator");
    expect(describeSummaryRequestError(new SummaryRequestError(409, "busy", "x"))).toContain("already being written");
    expect(describeSummaryRequestError(new Error("offline"))).toBe("Couldn't ask for the summary: offline");
  });
});
