import { describe, expect, it } from "vitest";

import {
  cleanStopDetail,
  formatDuration,
  formatStopReason,
  hasJobError,
  hasResourceNotice,
  isBuildBlocked,
  isBuildWaitingForResources,
  isHttpUrl,
  isJobActive,
  isRerunnableJob,
  jobStatusLabel,
  jobStatusToneClass,
  meetingLabel,
  parseRequestJSON,
  requestUrlLabel,
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

describe("Operator meeting label and URL formatting", () => {
  it("resolves Nextcloud Talk payloads with baseURL and roomToken", () => {
    const talkPayload = JSON.stringify({
      platform: "nextcloud-talk",
      baseURL: "https://cloud.example.test",
      roomToken: "3qbs6123vxx",
    });

    expect(parseRequestJSON(talkPayload)).toEqual({
      platform: "nextcloud-talk",
      baseURL: "https://cloud.example.test",
      roomToken: "3qbs6123vxx",
      url: undefined,
      guestName: undefined,
    });
    expect(meetingLabel(talkPayload)).toBe("Call 3qbs6123vxx");
    expect(requestUrlLabel(talkPayload)).toBe("https://cloud.example.test/call/3qbs6123vxx");
  });

  it("handles trailing slashes on baseURL cleanly", () => {
    const talkPayload = JSON.stringify({
      baseURL: "https://cloud.example.test///",
      roomToken: "xyz789",
    });

    expect(meetingLabel(talkPayload)).toBe("Call xyz789");
    expect(requestUrlLabel(talkPayload)).toBe("https://cloud.example.test/call/xyz789");
  });

  it("resolves full call URLs with token in pathname", () => {
    const urlPayload = JSON.stringify({
      url: "https://nextcloud.company.com/call/room-alpha-123",
    });

    expect(meetingLabel(urlPayload)).toBe("Call room-alpha-123");
    expect(requestUrlLabel(urlPayload)).toBe("https://nextcloud.company.com/call/room-alpha-123");
  });

  it("prefers explicit url over baseURL and roomToken when both are provided", () => {
    const dualPayload = JSON.stringify({
      url: "https://nextcloud.company.com/call/explicit-room",
      baseURL: "https://other.company.com",
      roomToken: "other-room",
    });

    expect(meetingLabel(dualPayload)).toBe("Call explicit-room");
    expect(requestUrlLabel(dualPayload)).toBe("https://nextcloud.company.com/call/explicit-room");
  });

  it("falls back to short job ID when request JSON contains no room or URL", () => {
    expect(meetingLabel("{}", "01M1KD3CMJ9J5AJ3QBS6123VXX")).toBe("Recording 01M1KD3C");
    expect(meetingLabel("not-valid-json", "01M1KD3CMJ9J5AJ3QBS6123VXX")).toBe("Recording 01M1KD3C");
    expect(meetingLabel("", "short-id")).toBe("Recording short-id");
    expect(meetingLabel("")).toBe("Recording");
    expect(requestUrlLabel("{}")).toBe("—");
  });

  it("identifies valid HTTP/HTTPS URLs", () => {
    expect(isHttpUrl("https://example.com/call/abc")).toBe(true);
    expect(isHttpUrl("http://localhost:4000/call/abc")).toBe(true);
    expect(isHttpUrl("—")).toBe(false);
    expect(isHttpUrl("Call 3qbs6123")).toBe(false);
    expect(isHttpUrl(null)).toBe(false);
  });
});

describe("Operator error and stop status classification", () => {
  it("does not classify clean, normal stops as job errors", () => {
    const normalStoppedJob = {
      error: null,
      state: "succeeded",
      record_exit_code: 0,
      stop_reason: "operator_requested",
    };
    expect(hasJobError(normalStoppedJob)).toBe(false);

    const roomEmptyBuildingJob = {
      error: null,
      state: "running",
      record_exit_code: 0,
      stop_reason: "room_empty",
    };
    expect(hasJobError(roomEmptyBuildingJob)).toBe(false);
  });

  it("classifies real failures as job errors", () => {
    expect(
      hasJobError({
        error: "cassini record failed",
        state: "failed",
        record_exit_code: 1,
        stop_reason: "record_process_exit_nonzero",
      }),
    ).toBe(true);

    expect(
      hasJobError({
        error: null,
        state: "failed",
        record_exit_code: null,
        stop_reason: null,
      }),
    ).toBe(true);

    expect(
      hasJobError({
        error: null,
        state: "succeeded",
        record_exit_code: 137,
        stop_reason: null,
      }),
    ).toBe(true);

    expect(
      hasJobError({
        error: null,
        state: "succeeded",
        record_exit_code: 0,
        stop_reason: "join_failed",
      }),
    ).toBe(true);
  });

  it("humanizes stop reasons into readable descriptions", () => {
    expect(formatStopReason("operator_requested")).toBe("Stopped by operator");
    expect(formatStopReason("room_empty")).toBe("Room empty");
    expect(formatStopReason("duration_limit")).toBe("Duration limit reached");
    expect(formatStopReason("signaling_connection_error")).toBe("Signaling connection error");
    expect(formatStopReason("join_failed")).toBe("Failed to join room");
    expect(formatStopReason("record_process_exit_nonzero")).toBe("Process exited with error");
    expect(formatStopReason("custom_trigger_event")).toBe("custom trigger event");
    expect(formatStopReason(null)).toBe("—");
  });

  it("suppresses internal 'context canceled' details on normal clean exit", () => {
    expect(cleanStopDetail("context canceled", 0, false)).toBeNull();
    expect(cleanStopDetail("context canceled", null, false)).toBeNull();
    expect(cleanStopDetail("Context Canceled", 0, false)).toBeNull();
    // But keeps real details or details on error
    expect(cleanStopDetail("context canceled", 1, true)).toBe("context canceled");
    expect(cleanStopDetail("duration limit reached", 0, false)).toBe("duration limit reached");
  });
});

describe("Operator recording duration formatting", () => {
  it("formats seconds, minutes, and hours accurately", () => {
    const start = "2026-09-03T12:00:00.000Z";
    const end45s = "2026-09-03T12:00:45.000Z";
    const end5m = "2026-09-03T12:05:00.000Z";
    const end34m25s = "2026-09-03T12:34:25.000Z";
    const end1h = "2026-09-03T13:00:00.000Z";
    const end1h15m = "2026-09-03T13:15:00.000Z";

    expect(formatDuration(start, end45s)).toBe("45s");
    expect(formatDuration(start, end5m)).toBe("5m");
    expect(formatDuration(start, end34m25s)).toBe("34m 25s");
    expect(formatDuration(start, end1h)).toBe("1h");
    expect(formatDuration(start, end1h15m)).toBe("1h 15m");
    expect(formatDuration(null, end5m)).toBeNull();
    expect(formatDuration(start, null)).toBeNull();
    expect(formatDuration("invalid", end5m)).toBeNull();
  });
});

