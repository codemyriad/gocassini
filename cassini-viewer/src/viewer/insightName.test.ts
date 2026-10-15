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
  it("names a question run by its template and what it read, never by the question", () => {
    // A question is typed over several lines and can be a paragraph; the
    // operator refuses a line break in a name. The question is quoted in the
    // drawer instead, under a name that says something else.
    expect(
      defaultInsightName({ templateName: "Ask", entries: [meeting({ id: "a", title: "Design review" })] }),
    ).toBe("Ask · Design review, 3 Oct 2026");
  });

  it("folds a title's line breaks, tabs and invisible characters into one line", () => {
    expect(
      defaultInsightName({
        templateName: " Summary\n",
        entries: [meeting({ id: "a", title: "Design\treview\n\nnotes\u200b" })],
      }),
    ).toBe("Summary · Design review notes, 3 Oct 2026");
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
    const name = defaultInsightName({ templateName: "Summary", entries: [meeting({ id: "a", title: "x".repeat(300) })] });
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
