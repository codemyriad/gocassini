import { describe, expect, it } from "vitest";
import { runStages, stepStatuses } from "./runStages";
import type { Job, JobAttempt, JobDetailResponse } from "./types";

const at = (minutes: number) => new Date(Date.UTC(2026, 9, 28, 9, 0) + minutes * 60_000).toISOString();

// A 58 minute meeting, built 60→70, sealed 70→71, published 71→72.
const job = { record_started_at: at(0), record_finished_at: at(58), record_queued_at: at(0), state: "succeeded", stage: "done" } as Job;
const steps = (from: number, build: number) => ({
  build_queued_at: at(from), build_started_at: at(from), build_finished_at: at(from + build),
  seal_queued_at: at(from + build), seal_started_at: at(from + build), seal_finished_at: at(from + build + 1),
  publish_queued_at: at(from + build + 1), publish_started_at: at(from + build + 1), publish_finished_at: at(from + build + 2),
});
const attempt = (n: number, kind: string, fields: Partial<JobAttempt>): JobAttempt =>
  ({ attempt_number: n, trigger_kind: kind, state: "succeeded", stage: "done", ...fields }) as JobAttempt;
const initial = attempt(1, "initial", { record_queued_at: at(0), record_started_at: at(0), record_finished_at: at(58), ...steps(60, 10), summary_status: "completed" });
const run = (...attempts: JobAttempt[]) => runStages({ job, attempts } as JobDetailResponse);
const view = (stages: ReturnType<typeof runStages>) =>
  Object.fromEntries(stages.map((s) => [s.key, [s.status, s.ms === null ? s.note ?? null : s.ms / 60_000, s.attempt ?? null]]));

describe("runStages", () => {
  it("shows the five steps of a meeting built in one go, its summary inside the build", () => {
    expect(run(initial).map((s) => s.label)).toEqual(["Record", "Build", "Summary", "Seal", "Publish"]);
    expect(view(run(initial))).toEqual({
      record: ["done", 58, null],
      build: ["done", 10, 1],
      summary: ["done", "In Build", 1],
      seal: ["done", 1, 1],
      publish: ["done", 1, 1],
    });
  });

  it("keeps the build that transcribed the meeting past a summary written later", () => {
    const summarize = attempt(2, "summarize", { ...steps(100, 0.5), summary_status: "completed" });
    expect(view(run(initial, summarize))).toMatchObject({
      build: ["done", 10, 1],
      summary: ["done", 0.5, 2],
      seal: ["done", 1, 2],
    });
  });

  it("says a summary was skipped, and credits one rewritten with speaker edits", () => {
    const skipped = { ...initial, summary_status: "skipped" };
    expect(view(run(skipped)).summary).toEqual(["skipped", "Skipped", 1]);
    expect(run({ ...skipped, summary_reason: "disabled" })[2].hint).toBe("Summaries are turned off");
    const refine = attempt(2, "refine", { ...steps(100, 0.2), summary_status: "completed" });
    expect(view(run(skipped, refine))).toMatchObject({ build: ["done", 10, 1], summary: ["done", "With speaker edits", 2] });
  });

  it("shows a rerun in progress over the build and keeps the live publish", () => {
    const rerun = attempt(2, "rerun", { state: "running", stage: "build", build_queued_at: at(100), build_started_at: at(100) });
    expect(view(run(initial, rerun))).toMatchObject({
      build: ["active", null, 2],
      summary: ["done", "In Build", 1],
      publish: ["done", 1, 1],
    });
  });

  it("marks where a later attempt failed, with how long it ran", () => {
    const summarize = attempt(2, "summarize", { state: "failed", stage: "build", build_queued_at: at(100), build_started_at: at(100), build_finished_at: at(101) });
    expect(view(run(initial, summarize))).toMatchObject({ build: ["done", 10, 1], summary: ["failed", 1, 2], seal: ["done", 1, 1] });
  });

  it("follows a first recording through its steps", () => {
    const recording = attempt(1, "initial", { state: "running", stage: "record", record_queued_at: at(0), record_started_at: at(0) });
    const live = runStages({ job: { ...job, record_finished_at: null, state: "running", stage: "record" }, attempts: [recording] } as JobDetailResponse);
    expect(view(live)).toMatchObject({ record: ["active", null, 1], build: ["pending", null, null], summary: ["pending", null, null] });
  });

  it("greys out a deleted recording", () => {
    expect(runStages({ job, attempts: [initial] } as JobDetailResponse, true)[0]).toMatchObject({ label: "Record (deleted)", status: "pending" });
  });
});

describe("stepStatuses", () => {
  it("puts a halted attempt's failure on the furthest step it reached", () => {
    const blocked = attempt(1, "initial", { state: "blocked", stage: "build", record_finished_at: at(58), build_queued_at: at(60) });
    expect(stepStatuses(blocked)).toEqual({ record: "done", build: "blocked", seal: "pending", publish: "pending" });
  });
});
