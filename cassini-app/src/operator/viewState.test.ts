import { describe, expect, it } from "vitest";

import { meetingLabel, shouldShowDetailLoading } from "./viewState";

const detail = (id: string) => ({ job: { id } });

describe("shouldShowDetailLoading", () => {
  it("shows loading before the first detail response", () => {
    expect(shouldShowDetailLoading(true, null, "job-1")).toBe(true);
  });

  it("keeps the current job visible during a background refresh", () => {
    expect(shouldShowDetailLoading(true, detail("job-1"), "job-1")).toBe(false);
  });

  it("shows loading instead of stale details while switching jobs", () => {
    expect(shouldShowDetailLoading(true, detail("job-1"), "job-2")).toBe(true);
  });

  it("never shows loading after the request settles", () => {
    expect(shouldShowDetailLoading(false, null, "job-1")).toBe(false);
    expect(shouldShowDetailLoading(false, detail("job-1"), "job-2")).toBe(false);
  });
});

describe("meetingLabel", () => {
  const talkRequest = JSON.stringify({ url: "https://nc.test/call/tok123" });

  it("names a job after its conversation", () => {
    expect(meetingLabel({ request_json: talkRequest, room_name: "Weekly sync" })).toBe("Weekly sync");
  });

  it("falls back to the call token for a job recorded before the room was a column", () => {
    expect(meetingLabel({ request_json: talkRequest, room_name: null })).toBe("Call tok123");
    expect(meetingLabel({ request_json: talkRequest })).toBe("Call tok123");
  });

  it("treats a blank room name as no name rather than an empty title", () => {
    expect(meetingLabel({ request_json: talkRequest, room_name: "   " })).toBe("Call tok123");
  });

  it("prefers the room name over a request URL it cannot parse", () => {
    expect(meetingLabel({ request_json: "not json", room_name: "Weekly sync" })).toBe("Weekly sync");
    expect(meetingLabel({ request_json: "not json" })).toBe("Recording");
  });

  it("keeps a non-URL request value visible instead of inventing a token", () => {
    expect(meetingLabel({ request_json: JSON.stringify({ url: "lobby" }) })).toBe("lobby");
  });
});
