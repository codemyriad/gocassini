import { describe, expect, it } from "vitest";

import {
  hasGPUResourceNotice,
  isBuildGPUBlocked,
  isBuildWaitingForGPU,
  isJobActive,
  isRerunnableJob,
  jobStatusLabel,
  jobStatusToneClass,
} from "./Operator.svelte";

describe("Operator GPU build status", () => {
  it("presents a deferred build as a transient GPU wait", () => {
    const job = {
      stage: "build",
      state: "queued",
      build_retry_not_before: "2026-08-28T12:15:00.000000000Z",
    };

    expect(isBuildWaitingForGPU(job)).toBe(true);
    expect(isBuildGPUBlocked(job)).toBe(false);
    expect(hasGPUResourceNotice(job)).toBe(true);
    expect(jobStatusLabel(job)).toBe("Waiting for GPU");
    expect(jobStatusToneClass(job)).toBe("text-warning");
    expect(isJobActive(job)).toBe(true);
  });

  it("presents a blocked build as GPU unavailable and stops polling it", () => {
    const job = {
      stage: "build",
      state: "blocked",
      build_retry_not_before: null,
    };

    expect(isBuildWaitingForGPU(job)).toBe(false);
    expect(isBuildGPUBlocked(job)).toBe(true);
    expect(hasGPUResourceNotice(job)).toBe(true);
    expect(jobStatusLabel(job)).toBe("GPU unavailable");
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

    expect(hasGPUResourceNotice(job)).toBe(false);
    expect(jobStatusLabel(job)).toBe("Queued");
    expect(jobStatusToneClass(job)).toBe("text-base-content/70");
  });
});
