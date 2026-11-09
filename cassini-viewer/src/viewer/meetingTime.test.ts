import { afterEach, describe, expect, it } from "vitest";

import {
  formatMeetingStartDay,
  formatMeetingStartForExport,
  formatMeetingStartShort,
  formatMeetingStartWithDay,
  hasMeetingStart,
  meetingStart,
  meetingStartDayKey,
  meetingStartMonth,
  meetingStartSortKey,
  parseRecordedAtUtc,
  withRecordedAtUtc,
} from "./meetingTime";

// The unit suite runs with TZ=Europe/Rome (vitest.config.ts), so "the reader's
// clock" below is Rome unless a test switches it. Rome is UTC+1 in winter and
// UTC+2 in summer; New York is UTC-5 / UTC-4.
const realTZ = process.env.TZ;

afterEach(() => {
  process.env.TZ = realTZ;
});

describe("parseRecordedAtUtc", () => {
  it("accepts RFC 3339 UTC with a Z, with or without fractional seconds", () => {
    expect(parseRecordedAtUtc("2026-11-09T11:31:00Z")).toBe("2026-11-09T11:31:00Z");
    expect(parseRecordedAtUtc("2026-11-09T11:31:00.123456789Z")).toBe("2026-11-09T11:31:00.123456789Z");
  });

  it("drops anything that is not that shape, instead of throwing", () => {
    for (const value of [
      undefined,
      null,
      42,
      "",
      "2026-11-09 11:31",
      "2026-11-09T11:31Z",
      "2026-11-09T11:31:00+01:00",
      "2026-11-09T11:31:00",
      "2026-11-09t11:31:00z",
      " 2026-11-09T11:31:00Z",
      "2026-02-30T11:31:00Z",
      "2026-11-09T24:00:00Z",
      "2026-13-09T11:31:00Z",
    ]) {
      expect(parseRecordedAtUtc(value), String(value)).toBeUndefined();
    }
  });
});

describe("meeting start rendering", () => {
  const instant = { dateLabel: "2026-11-09 11:31", recordedAtUtc: "2026-11-09T11:31:00Z" };
  const label = { dateLabel: "2026-11-09 11:31" };

  it("renders an instant in the reader's clock: Rome", () => {
    expect(formatMeetingStartShort(instant)).toBe("9 Nov 2026, 12:31");
    expect(formatMeetingStartWithDay(instant)).toBe("Mon 9 Nov 2026, 12:31");
    expect(formatMeetingStartDay(instant)).toBe("9 Nov 2026");
  });

  it("renders an instant in the reader's clock: west of UTC", () => {
    process.env.TZ = "America/New_York";
    expect(formatMeetingStartWithDay(instant)).toBe("Mon 9 Nov 2026, 06:31");
  });

  it("follows an injected zone over the runtime's", () => {
    expect(formatMeetingStartWithDay(instant, "America/Los_Angeles")).toBe("Mon 9 Nov 2026, 03:31");
    expect(formatMeetingStartWithDay(instant, "Asia/Tokyo")).toBe("Mon 9 Nov 2026, 20:31");
  });

  it("can move the day: late UTC evening is the next morning in Rome", () => {
    const late = { dateLabel: "2026-11-09 23:30", recordedAtUtc: "2026-11-09T23:30:00Z" };
    expect(formatMeetingStartWithDay(late)).toBe("Tue 10 Nov 2026, 00:30");
    expect(meetingStartDayKey(late)).toBe("2026-11-10");
  });

  // D-484, still: a label names no zone, so its digits are shown as they are,
  // whatever the reader's clock, and no zone is claimed.
  it("renders a label's own digits in every zone (D-484)", () => {
    expect(formatMeetingStartWithDay(label)).toBe("Mon 9 Nov 2026, 11:31");
    process.env.TZ = "America/New_York";
    expect(formatMeetingStartWithDay(label)).toBe("Mon 9 Nov 2026, 11:31");
    expect(formatMeetingStartWithDay(label, "Asia/Tokyo")).toBe("Mon 9 Nov 2026, 11:31");
  });

  it("falls back to the label when the instant is malformed", () => {
    expect(formatMeetingStartWithDay({ dateLabel: "2026-11-09 11:31", recordedAtUtc: "yesterday" })).toBe(
      "Mon 9 Nov 2026, 11:31",
    );
  });

  it("passes an undated label through and reports no start", () => {
    const undated = { dateLabel: "01KWEKPZVEJWP9BYBPBX9ZRNDQ" };
    expect(formatMeetingStartShort(undated)).toBe("01KWEKPZVEJWP9BYBPBX9ZRNDQ");
    expect(formatMeetingStartDay(undated)).toBe("");
    expect(hasMeetingStart(undated)).toBe(false);
    expect(meetingStart(undated).kind).toBe("none");
    expect(meetingStartSortKey(undated)).toBeNull();
  });

  it("has a start when only the instant is known", () => {
    const onlyInstant = { dateLabel: "weekly-sync", recordedAtUtc: "2026-11-09T11:31:00Z" };
    expect(hasMeetingStart(onlyInstant)).toBe(true);
    expect(formatMeetingStartShort(onlyInstant)).toBe("9 Nov 2026, 12:31");
  });
});

describe("meetingStartMonth", () => {
  // The month boundary: 23:30 UTC on 31 January is 00:30 on 1 February in
  // Rome, and the row says so, so its heading must say February.
  it("files an instant by the reader's month", () => {
    const entry = { dateLabel: "2026-01-31 23:30", recordedAtUtc: "2026-01-31T23:30:00Z" };
    expect(meetingStartMonth(entry)).toEqual({ key: "2026-02", label: "February 2026" });
    process.env.TZ = "America/New_York";
    expect(meetingStartMonth(entry)).toEqual({ key: "2026-01", label: "January 2026" });
  });

  it("files a label by its own digits", () => {
    expect(meetingStartMonth({ dateLabel: "2026-01-31 23:30" })).toEqual({ key: "2026-01", label: "January 2026" });
  });

  it("is null without a date", () => {
    expect(meetingStartMonth({ dateLabel: "job-2026" })).toBeNull();
  });
});

describe("meetingStartSortKey", () => {
  it("puts an instant on the wall-clock scale the reader sees", () => {
    // 11:31Z is 12:31 in Rome: it sorts after a label reading 12:00 and before
    // one reading 13:00, exactly where the printed times put it.
    const instant = meetingStartSortKey({ dateLabel: "", recordedAtUtc: "2026-11-09T11:31:00Z" })!;
    expect(instant).toBeGreaterThan(meetingStartSortKey({ dateLabel: "2026-11-09 12:00" })!);
    expect(instant).toBeLessThan(meetingStartSortKey({ dateLabel: "2026-11-09 13:00" })!);
  });
});

describe("formatMeetingStartForExport", () => {
  it("names the zone for an instant, since the copy is read elsewhere", () => {
    expect(formatMeetingStartForExport({ dateLabel: "", recordedAtUtc: "2026-11-09T11:31:00Z" })).toBe(
      "Mon 9 Nov 2026, 12:31 CET",
    );
    expect(
      formatMeetingStartForExport({ dateLabel: "", recordedAtUtc: "2026-11-09T11:31:00Z" }, "America/New_York"),
    ).toBe("Mon 9 Nov 2026, 06:31 GMT-5");
  });

  it("claims no zone for a label (D-484)", () => {
    expect(formatMeetingStartForExport({ dateLabel: "2026-11-09 11:31" })).toBe("Mon 9 Nov 2026, 11:31");
  });
});

describe("withRecordedAtUtc", () => {
  it("lets the file's own instant complete or replace the entry's", () => {
    const entry = { id: "m", dateLabel: "2026-11-09 11:31" };
    expect(withRecordedAtUtc(entry, "2026-11-09T11:31:00Z")).toEqual({
      id: "m",
      dateLabel: "2026-11-09 11:31",
      recordedAtUtc: "2026-11-09T11:31:00Z",
    });
  });

  it("leaves the entry alone for a missing or malformed value", () => {
    const entry = { id: "m", dateLabel: "2026-11-09 11:31" };
    expect(withRecordedAtUtc(entry, null)).toBe(entry);
    expect(withRecordedAtUtc(entry, "2026-11-09T11:31:00+01:00")).toBe(entry);
    expect(withRecordedAtUtc(null, "2026-11-09T11:31:00Z")).toBeNull();
  });
});
