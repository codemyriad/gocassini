import { describe, expect, it } from "vitest";

import {
  hasResourceNotice,
  isBuildBlocked,
  isBuildWaitingForResources,
  isJobActive,
  isRerunnableJob,
  jobStatusLabel,
  jobStatusToneClass,
} from "./Operator.svelte";

describe("Operator build resource status", () => {
  it("presents a deferred build as a transient resource wait", () => {
    const job = {
      stage: "build",
      state: "queued",
      build_retry_not_before: "2026-08-28T12:15:00.000000000Z",
    };

    expect(isBuildWaitingForResources(job)).toBe(true);
    expect(isBuildBlocked(job)).toBe(false);
    expect(hasResourceNotice(job)).toBe(true);
    expect(jobStatusLabel(job)).toBe("Waiting for resources");
    expect(jobStatusToneClass(job)).toBe("text-warning");
    expect(isJobActive(job)).toBe(true);
  });

  it("presents a blocked build as blocked and stops polling it", () => {
    const job = {
      stage: "build",
      state: "blocked",
      build_retry_not_before: null,
    };

    expect(isBuildWaitingForResources(job)).toBe(false);
    expect(isBuildBlocked(job)).toBe(true);
    expect(hasResourceNotice(job)).toBe(true);
    expect(jobStatusLabel(job)).toBe("Build blocked");
    expect(jobStatusToneClass(job)).toBe("text-warning");
    expect(isJobActive(job)).toBe(false);
  });

  it("permits rerunning a blocked build only when its recording exists", () => {
    expect(
      isRerunnableJob({
        stage: "build",
        state: "blocked",
        artifact_run_path: "/recordings/run-1.run",
      }),
    ).toBe(true);
    expect(
      isRerunnableJob({
        stage: "build",
        state: "blocked",
        artifact_run_path: null,
      }),
    ).toBe(false);
    expect(
      isRerunnableJob({
        stage: "done",
        state: "failed",
        artifact_run_path: "/recordings/run-1.run",
      }),
    ).toBe(true);
  });

  it("leaves an ordinary queued build unchanged", () => {
    const job = {
      stage: "build",
      state: "queued",
      build_retry_not_before: null,
    };

    expect(hasResourceNotice(job)).toBe(false);
    expect(jobStatusLabel(job)).toBe("Queued");
    expect(jobStatusToneClass(job)).toBe("text-base-content/70");
  });
});
