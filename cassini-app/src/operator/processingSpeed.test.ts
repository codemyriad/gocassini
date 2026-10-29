import { describe, expect, it } from "vitest";
import {
  describeProcessing,
  formatFactor,
  formatMultiplier,
  meetingLengthMs,
  processingSpeed,
  runProcessing,
  stepSpeed,
} from "./processingSpeed";
import type { Job, JobAttempt, JobDetailResponse } from "./types";

const at = (minutes: number) => new Date(Date.UTC(2026, 9, 28, 9, 0) + minutes * 60_000).toISOString();

// A 58 minute meeting: recorded 0→58, built 60→70, sealed 70→71, published 71→72.
const job = { record_started_at: at(0), record_finished_at: at(58) } as Job;
const processed: Partial<JobAttempt> = {
  build_started_at: at(60),
  build_finished_at: at(70),
  seal_started_at: at(70),
  seal_finished_at: at(71),
  publish_started_at: at(71),
  publish_finished_at: at(72),
};
const attempt = (n: number, kind: string, times: Partial<JobAttempt> = processed): JobAttempt =>
  ({ attempt_number: n, trigger_kind: kind, ...times }) as JobAttempt;
const detail = (attempts: JobAttempt[]) => ({ job, attempts }) as JobDetailResponse;

describe("processingSpeed", () => {
  it("times the build starting to the meeting publishing, against the recording", () => {
    const meetingMs = meetingLengthMs(job);
    expect(meetingMs).toBe(58 * 60_000);
    const result = processingSpeed(attempt(1, "initial"), meetingMs);
    expect(result?.ms).toBe(12 * 60_000);
    expect(result?.factor).toBeCloseTo(58 / 12);
    expect(describeProcessing(result!)).toBe("12m · 4.8× real time");
  });

  it("has nothing to say until the meeting is published", () => {
    expect(processingSpeed(attempt(1, "initial", { build_started_at: at(60) }), 1)).toBeNull();
  });

  it("times each finished step on its own", () => {
    const row = attempt(1, "initial", { ...processed, publish_finished_at: null });
    expect(stepSpeed(row, "build", 58 * 60_000)?.factor).toBeCloseTo(5.8);
    expect(stepSpeed(row, "seal", 58 * 60_000)?.factor).toBe(58);
    expect(stepSpeed(row, "publish", 58 * 60_000)).toBeNull();
  });

  it("gives the time alone when the meeting length is unknown", () => {
    const result = processingSpeed(attempt(1, "initial"), null);
    expect(result?.factor).toBeNull();
    expect(describeProcessing(result!)).toBe("12m");
  });
});

describe("runProcessing", () => {
  it("reads the newest attempt that processed the capture, past summaries and speaker edits", () => {
    const quick = { ...processed, build_started_at: at(71) };
    const result = runProcessing(
      detail([attempt(1, "initial"), attempt(2, "rerun", { ...processed, build_started_at: at(66) }), attempt(3, "summarize", quick), attempt(4, "refine", quick)]),
    );
    expect(result?.ms).toBe(6 * 60_000);
  });

  it("skips a rerun that has not finished", () => {
    expect(runProcessing(detail([attempt(2, "rerun", { build_started_at: at(80) }), attempt(1, "initial")]))?.ms).toBe(12 * 60_000);
    expect(runProcessing(detail([attempt(1, "initial", {})]))).toBeNull();
  });
});

describe("formatFactor", () => {
  it("keeps a decimal under 10× and drops it above", () => {
    expect(formatFactor(0.42)).toBe("0.4× real time");
    expect(formatMultiplier(13.4)).toBe("13×");
  });
});
