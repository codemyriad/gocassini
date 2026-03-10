import { describe, expect, it } from "vitest";

import { describeMeeting } from "./export-static-meetings.mjs";

describe("describeMeeting", () => {
  it("parses colon-separated legacy meeting ids", () => {
    expect(describeMeeting("daily-meeting--2026-03-05--12:38:29")).toEqual({
      title: "Daily Meeting",
      dateLabel: "2026-03-05 12:38",
    });
  });

  it("parses dash-separated legacy meeting ids", () => {
    expect(describeMeeting("daily-meeting--2026-03-04--12-36-53")).toEqual({
      title: "Daily Meeting",
      dateLabel: "2026-03-04 12:36",
    });
  });

  it("parses compact stamped meeting ids", () => {
    expect(describeMeeting("synthetic-pied-piper-v1--20260310T150453")).toEqual({
      title: "Synthetic Pied Piper V1",
      dateLabel: "2026-03-10 15:04",
    });
  });
});
