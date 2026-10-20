import { describe, expect, it } from "vitest";
import { outcomeAttempt, summaryNotice, transcriptionNotice } from "./stepOutcomes";
import type { JobAttempt, JobDetailResponse } from "./types";

const attempt = (n: number, outcomes: Partial<JobAttempt> = {}): JobAttempt =>
  ({ attempt_number: n, ...outcomes }) as JobAttempt;

describe("outcomeAttempt", () => {
  it("shows the published attempt", () => {
    const detail = {
      availability: { source: "present", output: "present", published_attempt: 1 },
      attempts: [attempt(2, { transcription_status: "completed" }), attempt(1, { transcription_status: "failed" })],
    } as JobDetailResponse;
    expect(outcomeAttempt(detail)?.attempt_number).toBe(1);
  });

  it("falls back to the newest attempt with outcomes before anything is published", () => {
    const detail = { attempts: [attempt(3), attempt(2, { summary_status: "failed" }), attempt(1, { summary_status: "completed" })] } as JobDetailResponse;
    expect(outcomeAttempt(detail)?.attempt_number).toBe(2);
    expect(outcomeAttempt({ attempts: [attempt(1)] } as JobDetailResponse)).toBeNull();
  });
});

describe("transcriptionNotice", () => {
  it("says nothing for a transcribed meeting or an unknown one", () => {
    expect(transcriptionNotice(attempt(1, { transcription_status: "completed" }), true)).toBeNull();
    expect(transcriptionNotice(attempt(1), true)).toBeNull();
  });

  it("names a crash, keeps the operator's detail and offers a rerun", () => {
    const notice = transcriptionNotice(
      attempt(1, { transcription_status: "failed", transcription_reason: "transcription_failed", transcription_detail: "CUDA error: out of memory" }),
      true,
    );
    expect(notice).toEqual({
      tone: "error",
      title: "Published without a transcript: transcription failed",
      detail: "CUDA error: out of memory",
      hint: "Rerun transcribes it again.",
    });
  });

  it("names resources that never freed, and offers no rerun that cannot run", () => {
    const notice = transcriptionNotice(attempt(1, { transcription_status: "failed", transcription_reason: "insufficient_resources" }), false);
    expect(notice?.title).toBe("Published without a transcript: not enough memory or GPU to transcribe");
    expect(notice?.hint).toBeUndefined();
  });

  it("tells a model problem and a switched-off step from a failure", () => {
    expect(transcriptionNotice(attempt(1, { transcription_status: "skipped", transcription_reason: "model_unavailable" }), true)?.tone).toBe("warning");
    expect(transcriptionNotice(attempt(1, { transcription_status: "skipped", transcription_reason: "disabled" }), true)).toEqual({
      tone: "info",
      title: "Published without a transcript: transcription is turned off",
    });
  });
});

describe("summaryNotice", () => {
  it("says nothing for a written summary, an unknown one, or a meeting with no transcript", () => {
    expect(summaryNotice(attempt(1, { summary_status: "completed" }))).toBeNull();
    expect(summaryNotice(attempt(1))).toBeNull();
    expect(summaryNotice(attempt(1, { summary_status: "skipped", summary_reason: "no_transcript" }))).toBeNull();
  });

  it("tells a failed summary from one that is off or not set up", () => {
    expect(summaryNotice(attempt(1, { summary_status: "failed", summary_reason: "llm_failed" }))?.tone).toBe("error");
    expect(summaryNotice(attempt(1, { summary_status: "skipped", summary_reason: "disabled" }))).toEqual({
      tone: "info",
      title: "No summary: summaries are turned off",
    });
    expect(summaryNotice(attempt(1, { summary_status: "skipped", summary_reason: "not_configured" }))?.hint).toBe("Add one in Settings.");
  });
});
