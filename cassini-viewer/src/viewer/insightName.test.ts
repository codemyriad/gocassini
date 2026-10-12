import { describe, expect, it } from "vitest";
import type { MeetingCatalogEntry } from "./catalog";
import { defaultInsightName, formatDateRange, INSIGHT_NAME_MAX } from "./insightName";

function meeting(overrides: Partial<MeetingCatalogEntry> & { id: string }): MeetingCatalogEntry {
  return {
    title: `Meeting ${overrides.id}`,
    dateLabel: "2026-10-03 14:30",
    audioPath: `${overrides.id}.opus`,
    ...overrides,
  };
}

describe("defaultInsightName", () => {
  it("is the question as typed, for a template that takes one", () => {
    expect(
      defaultInsightName({ templateName: "Ask", question: "  What did we decide?  ", entries: [meeting({ id: "a" })] }),
    ).toBe("What did we decide?");
  });

  it("names one meeting by its title and day", () => {
    expect(
      defaultInsightName({
        templateName: "Summary",
        entries: [meeting({ id: "a", title: "Design review", dateLabel: "2026-10-03 14:30" })],
      }),
    ).toBe("Summary · Design review, 3 Oct 2026");
  });

  it("names several meetings from one room by the room, a count and the span", () => {
    const entries = [
      meeting({ id: "a", roomId: "r1", roomName: "Design", dateLabel: "2026-10-12 10:00" }),
      meeting({ id: "b", roomId: "r1", roomName: "Design", dateLabel: "2026-10-01 10:00" }),
    ];
    expect(defaultInsightName({ templateName: "Summary", entries })).toBe(
      "Summary · Design, 2 meetings, 1–12 Oct 2026",
    );
  });

  it("names meetings from several rooms by a count and the span", () => {
    const entries = [
      meeting({ id: "a", roomId: "r1", roomName: "Design", dateLabel: "2026-09-28" }),
      meeting({ id: "b", roomId: "r2", roomName: "Sales", dateLabel: "2026-10-12 09:00" }),
      meeting({ id: "c", roomId: "r2", roomName: "Sales", dateLabel: "" }),
    ];
    expect(defaultInsightName({ templateName: "Summary", entries })).toBe(
      "Summary · 3 meetings, 28 Sept – 12 Oct 2026",
    );
  });

  it("does not call 'No room' a room", () => {
    const entries = [meeting({ id: "a" }), meeting({ id: "b" })];
    expect(defaultInsightName({ templateName: "Summary", entries })).toBe("Summary · 2 meetings, 3 Oct 2026");
  });

  it("says Insight where the template's name cannot be read", () => {
    expect(defaultInsightName({ templateName: null, entries: [meeting({ id: "a", title: "Standup" })] })).toBe(
      "Insight · Standup, 3 Oct 2026",
    );
  });

  it("leaves the range out when nothing is dated", () => {
    const entries = [meeting({ id: "a", dateLabel: "" }), meeting({ id: "b", dateLabel: "someday" })];
    expect(defaultInsightName({ templateName: "Summary", entries })).toBe("Summary · 2 meetings");
  });

  it("stays inside the operator's bound", () => {
    const name = defaultInsightName({ templateName: "Summary", question: "x".repeat(300), entries: [] });
    expect(Array.from(name)).toHaveLength(INSIGHT_NAME_MAX);
    expect(name.endsWith("…")).toBe(true);
  });
});

describe("formatDateRange", () => {
  it("spans years in full", () => {
    expect(formatDateRange(["2026-01-03", "2025-12-28 09:00"])).toBe("28 Dec 2025 – 3 Jan 2026");
  });

  it("is one date for one day", () => {
    expect(formatDateRange(["2026-10-03 09:00", "2026-10-03 15:00", undefined])).toBe("3 Oct 2026");
  });

  it("is empty when nothing is dated", () => {
    expect(formatDateRange(["", undefined])).toBe("");
  });
});
