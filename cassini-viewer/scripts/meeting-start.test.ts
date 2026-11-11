import { describe, expect, it } from "vitest";

import { formatMeetingStartWithDay } from "../src/viewer/meetingTime";
import { portableMeetingStart, portableRecordedAtUtc, rfc3339OffsetInstantMs } from "./meeting-start.mjs";

// The unit project pins TZ to Europe/Rome (vitest.config.ts), so an instant
// rendered in the reader's clock differs visibly from its UTC digits.
describe("portableMeetingStart (format SPEC 'When the meeting happened')", () => {
  it("runs in a zone that is not UTC", () => {
    expect(process.env.TZ).toBe("Europe/Rome");
  });

  // Shaped like conformance vector 027-recorded-at-utc.
  it("takes meeting.recordedAtUtc first, over DATE and the label", () => {
    const start = portableMeetingStart(
      {
        meeting: {
          createdAtUtc: "2026-11-09T10:47:02Z",
          recordedAtUtc: "2026-11-09T09:31:48Z",
          recordedAtLocal: "2026-11-09T08:00:00",
        },
      },
      { DATE: "2026-11-09T07:00:00+01:00" },
    );
    expect(start).toEqual({
      source: "recordedAtUtc",
      value: "2026-11-09T09:31:48Z",
      recordedAtUtc: "2026-11-09T09:31:48Z",
    });
  });

  // Shaped like conformance vector 029-date-with-offset.
  it("takes DATE with an offset over a misleading recordedAtLocal, and renders it in the reader's zone", () => {
    const manifest = {
      meeting: { createdAtUtc: "2026-11-09T10:47:02Z", recordedAtLocal: "2026-11-09T09:31:48" },
    };
    const tags = { DATE: "2026-11-09T10:31:48+01:00", CASSINI_RECORDED_AT_LOCAL: "2026-11-09T09:31:48" };
    const start = portableMeetingStart(manifest, tags);
    expect(start).toEqual({
      source: "DATE",
      value: "2026-11-09T10:31:48+01:00",
      recordedAtUtc: "2026-11-09T09:31:48.000Z",
    });
    // 09:31:48Z is 10:31 in Rome; the label's "09:31" is not what is shown.
    expect(formatMeetingStartWithDay({ recordedAtUtc: start!.recordedAtUtc })).toBe("Mon, 9 Nov 2026, 10:31");
  });

  // Shaped like conformance vector 028-date-is-created-at.
  it("ignores a DATE that names the same instant as createdAtUtc, falling to the label", () => {
    const manifest = {
      meeting: { createdAtUtc: "2026-11-09T10:47:02Z", recordedAtLocal: "2026-11-09T09:31:48" },
    };
    expect(portableMeetingStart(manifest, { DATE: "2026-11-09T10:47:02Z" })).toEqual({
      source: "recordedAtLocal",
      value: "2026-11-09T09:31:48",
    });
    // Same instant, other spelling.
    expect(portableMeetingStart(manifest, { DATE: "2026-11-09T11:47:02+01:00" })?.source).toBe("recordedAtLocal");
    expect(portableRecordedAtUtc(manifest, { DATE: "2026-11-09T10:47:02Z" })).toBe("");
  });

  it("ignores a DATE without an offset or that is not a real date-time", () => {
    const manifest = { meeting: { createdAtUtc: "2026-11-09T10:47:02Z" } };
    for (const date of ["2026-11-09", "2026-11-09T10:31:48", "2026-02-30T10:00:00Z", "2026-11-09T10:31:48+25:00", "yesterday"]) {
      expect(portableMeetingStart(manifest, { DATE: date }), date).toBeNull();
    }
  });

  it("accepts the RFC 3339 spellings the reference reader accepts", () => {
    expect(rfc3339OffsetInstantMs("2026-11-09 10:31:48z")).toBe(Date.parse("2026-11-09T10:31:48Z"));
    expect(rfc3339OffsetInstantMs("2026-11-09t10:31:48.25-05:00")).toBe(Date.parse("2026-11-09T15:31:48.250Z"));
  });

  it("never reads the CASSINI_RECORDED_AT_UTC mirror in place of the manifest", () => {
    expect(
      portableMeetingStart(
        { meeting: { createdAtUtc: "2026-11-09T10:47:02Z", recordedAtLocal: "2026-11-09T09:31:48" } },
        { CASSINI_RECORDED_AT_UTC: "2026-11-09T08:31:48Z" },
      ),
    ).toEqual({ source: "recordedAtLocal", value: "2026-11-09T09:31:48" });
  });

  it("is null when nothing applies, and never answers with createdAtUtc", () => {
    expect(portableMeetingStart({ meeting: { createdAtUtc: "2026-11-09T10:47:02Z" } }, {})).toBeNull();
    expect(portableMeetingStart(undefined, undefined)).toBeNull();
  });
});
