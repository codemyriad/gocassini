import { describe, expect, it } from "vitest";

import { validateMeetingCatalog } from "./catalog";

describe("validateMeetingCatalog", () => {
  it("accepts a valid runtime meeting catalog", () => {
    const catalog = validateMeetingCatalog({
      version: "cassini.viewer.catalog.v1",
      meetings: [
        {
          id: "daily-meeting--2026-03-10--15-04-53",
          artifactPath: "./meetings/daily-meeting--2026-03-10--15-04-53",
          title: "Daily Meeting",
          dateLabel: "2026-03-10 15:04",
          speakerCount: 6,
          segmentCount: 9,
          digestDurationMs: 34536,
        },
      ],
    });

    expect(catalog.meetings).toHaveLength(1);
    expect(catalog.meetings[0]?.artifactPath).toBe(
      "./meetings/daily-meeting--2026-03-10--15-04-53",
    );
  });

  it("rejects invalid catalog versions", () => {
    expect(() =>
      validateMeetingCatalog({
        version: "old-version",
        meetings: [],
      }),
    ).toThrow(/unsupported catalog version/i);
  });

  it("rejects malformed meeting entries", () => {
    expect(() =>
      validateMeetingCatalog({
        version: "cassini.viewer.catalog.v1",
        meetings: [
          {
            id: "meeting-a",
            artifactPath: "",
            title: "Meeting A",
            dateLabel: "2026-03-10 15:04",
            speakerCount: 3,
            segmentCount: 7,
            digestDurationMs: 12000,
          },
        ],
      }),
    ).toThrow(/artifactPath/i);
  });
});
