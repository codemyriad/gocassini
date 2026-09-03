import { describe, expect, it } from "vitest";
import type { MeetingCatalogEntry } from "./catalog";
import { NO_ROOM_KEY, UNDATED_MONTH_LABEL } from "./rooms";
import {
  ALL_BROWSE_TYPES,
  buildBrowseFeed,
  filterInsights,
  filterInsightsByRoom,
  formatInsightStatus,
  groupBrowseFeedByMonth,
  insightHeadline,
  insightRoomKeys,
  insightsForMeeting,
  isLastBrowseType,
  resolveInsightSources,
  toggleBrowseType,
  type InsightRecord,
} from "./insights";

function meeting(
  overrides: Partial<MeetingCatalogEntry> & { id: string },
): MeetingCatalogEntry {
  return {
    title: `Meeting ${overrides.id}`,
    dateLabel: "2026-08-18 14:30",
    audioPath: `${overrides.id}.opus`,
    ...overrides,
  };
}

function insight(overrides: Partial<InsightRecord> & { id: string }): InsightRecord {
  return {
    status: "succeeded",
    createdBy: "alice",
    attemptNumber: 1,
    workflowId: "summarise",
    workflowVersion: "v0",
    workflowSha256: "abc",
    meetingIds: [],
    roomIds: [],
    question: "",
    provider: "",
    model: "",
    documentPath: "",
    error: "",
    createdAt: "2026-08-20T09:00:00Z",
    updatedAt: "2026-08-20T09:01:00Z",
    ...overrides,
  };
}

describe("insightRoomKeys", () => {
  it("keys on every room the run drew from, not one of them", () => {
    // The decision this whole feature turns on: a selection spans rooms, so a
    // single room cannot describe where the insight belongs.
    expect(insightRoomKeys(insight({ id: "i1", roomIds: ["r1", "r2"] }))).toEqual([
      "id:r1",
      "id:r2",
    ]);
  });

  it("speaks the same key vocabulary the rail and the room filter do", () => {
    expect(insightRoomKeys(insight({ id: "i1", roomIds: ["r1"] }))[0]).toBe("id:r1");
  });

  it("files a roomless run under the no-room bucket rather than nowhere", () => {
    expect(insightRoomKeys(insight({ id: "i1", roomIds: [] }))).toEqual([NO_ROOM_KEY]);
  });

  it("survives a record that arrived without the field at all", () => {
    const stray = { ...insight({ id: "i1" }), roomIds: undefined } as unknown as InsightRecord;
    expect(insightRoomKeys(stray)).toEqual([NO_ROOM_KEY]);
  });

  it("collapses a room repeated across several source meetings", () => {
    expect(insightRoomKeys(insight({ id: "i1", roomIds: ["r1", "r1"] }))).toEqual(["id:r1"]);
  });
});

describe("filterInsightsByRoom", () => {
  const crossRoom = insight({ id: "i1", roomIds: ["r1", "r2"] });
  const single = insight({ id: "i2", roomIds: ["r2"] });
  const roomless = insight({ id: "i3", roomIds: [] });

  it("surfaces a cross-room insight under EVERY room it drew from", () => {
    expect(filterInsightsByRoom([crossRoom, single, roomless], "id:r1")).toEqual([crossRoom]);
    expect(filterInsightsByRoom([crossRoom, single, roomless], "id:r2")).toEqual([
      crossRoom,
      single,
    ]);
  });

  it("treats a null key as every room, like the meeting filter does", () => {
    expect(filterInsightsByRoom([crossRoom, single, roomless], null)).toHaveLength(3);
  });

  it("puts a roomless insight in the no-room bucket and nowhere else", () => {
    expect(filterInsightsByRoom([crossRoom, roomless], NO_ROOM_KEY)).toEqual([roomless]);
    expect(filterInsightsByRoom([crossRoom, roomless], "id:r1")).toEqual([crossRoom]);
  });
});

describe("resolveInsightSources", () => {
  it("returns the sources in the order the run recorded them", () => {
    const meetings = [meeting({ id: "m1" }), meeting({ id: "m2" }), meeting({ id: "m3" })];
    const record = insight({ id: "i1", meetingIds: ["m3", "m1"] });

    const sources = resolveInsightSources([record], meetings).get("i1");

    expect(sources?.map((entry) => entry.id)).toEqual(["m3", "m1"]);
  });

  it("drops a source this caller was not served, and does not count it", () => {
    // The per-caller filter's whole point: a meeting they may not read must be
    // absent, and a count that said "2 meetings" over one row would disclose
    // that the other one exists.
    const record = insight({ id: "i1", meetingIds: ["m1", "secret"] });

    const sources = resolveInsightSources([record], [meeting({ id: "m1" })]).get("i1");

    expect(sources?.map((entry) => entry.id)).toEqual(["m1"]);
    expect(sources).toHaveLength(1);
  });

  it("gives every insight an entry, including one with nothing readable left", () => {
    const resolved = resolveInsightSources(
      [insight({ id: "i1", meetingIds: ["gone"] })],
      [meeting({ id: "m1" })],
    );

    expect(resolved.get("i1")).toEqual([]);
  });
});

describe("insightsForMeeting", () => {
  it("finds the insights that read a meeting, in list order", () => {
    const first = insight({ id: "i1", meetingIds: ["m1", "m2"] });
    const second = insight({ id: "i2", meetingIds: ["m2"] });

    expect(insightsForMeeting([first, second], "m2")).toEqual([first, second]);
    expect(insightsForMeeting([first, second], "m1")).toEqual([first]);
  });

  it("answers nothing for a meeting no insight read", () => {
    expect(insightsForMeeting([insight({ id: "i1", meetingIds: ["m1"] })], "m9")).toEqual([]);
  });
});

describe("insightHeadline", () => {
  it("names an insight by the question it was asked", () => {
    expect(insightHeadline(insight({ id: "i1", question: "  What did we decide?  " }))).toBe(
      "What did we decide?",
    );
  });

  it("falls back to the workflow id rather than inventing a display name", () => {
    expect(insightHeadline(insight({ id: "i1", question: "", workflowId: "todos" }))).toBe(
      "todos",
    );
  });
});

describe("formatInsightStatus", () => {
  it("keeps the operator's four words", () => {
    expect(formatInsightStatus("queued")).toBe("Queued");
    expect(formatInsightStatus("running")).toBe("Running");
    expect(formatInsightStatus("succeeded")).toBe("Succeeded");
    expect(formatInsightStatus("failed")).toBe("Failed");
  });

  it("shows a status it does not know rather than guessing one", () => {
    expect(formatInsightStatus("cancelled")).toBe("cancelled");
  });
});

describe("filterInsights", () => {
  const asked = insight({ id: "i1", question: "What did we decide about pricing?" });
  const plain = insight({ id: "i2", question: "", workflowId: "todos" });

  it("matches the question and the workflow id, case-insensitively", () => {
    expect(filterInsights([asked, plain], "pricing")).toEqual([asked]);
    expect(filterInsights([asked, plain], "TODOS")).toEqual([plain]);
  });

  it("returns everything for an empty query", () => {
    expect(filterInsights([asked, plain], "   ")).toHaveLength(2);
  });
});

describe("toggleBrowseType", () => {
  it("turns a kind off and back on", () => {
    const off = toggleBrowseType(ALL_BROWSE_TYPES, "insights");
    expect(off).toEqual({ meetings: true, insights: false });
    expect(toggleBrowseType(off, "insights")).toEqual(ALL_BROWSE_TYPES);
  });

  it("refuses to switch off the last kind standing", () => {
    // Everything hidden is indistinguishable on screen from nothing being here.
    const insightsOnly = toggleBrowseType(ALL_BROWSE_TYPES, "meetings");
    expect(isLastBrowseType(insightsOnly, "insights")).toBe(true);
    expect(toggleBrowseType(insightsOnly, "insights")).toBe(insightsOnly);
  });
});

describe("buildBrowseFeed", () => {
  const meetings = [
    meeting({ id: "m1", dateLabel: "2026-08-20 10:00" }),
    meeting({ id: "m2", dateLabel: "2026-08-18 14:30" }),
  ];
  const record = insight({ id: "i1", createdAt: "2026-08-19T09:00:00Z" });

  it("interleaves an insight among the meetings by date", () => {
    const feed = buildBrowseFeed({ meetings, insights: [record], types: ALL_BROWSE_TYPES });

    expect(feed.map((item) => item.key)).toEqual([
      "meeting:m1",
      "insight:i1",
      "meeting:m2",
    ]);
  });

  it("keeps the meetings in the order the caller sorted them", () => {
    // The list renders the catalog's own order; this only decides where an
    // insight lands in it.
    const unsorted = [
      meeting({ id: "m2", dateLabel: "2026-08-18 14:30" }),
      meeting({ id: "m1", dateLabel: "2026-08-20 10:00" }),
    ];

    const feed = buildBrowseFeed({ meetings: unsorted, insights: [], types: ALL_BROWSE_TYPES });

    expect(feed.map((item) => item.key)).toEqual(["meeting:m2", "meeting:m1"]);
  });

  it("sorts an insight above a meeting recorded at the same instant", () => {
    const sameInstant = buildBrowseFeed({
      meetings: [meeting({ id: "m1", dateLabel: "2026-08-19 09:00" })],
      insights: [insight({ id: "i1", createdAt: "2026-08-19T09:00:00Z" })],
      types: ALL_BROWSE_TYPES,
    });

    expect(sameInstant.map((item) => item.key)).toEqual(["insight:i1", "meeting:m1"]);
  });

  it("drops the kind the type filter switched off", () => {
    expect(
      buildBrowseFeed({
        meetings,
        insights: [record],
        types: { meetings: true, insights: false },
      }).map((item) => item.kind),
    ).toEqual(["meeting", "meeting"]);

    expect(
      buildBrowseFeed({
        meetings,
        insights: [record],
        types: { meetings: false, insights: true },
      }).map((item) => item.kind),
    ).toEqual(["insight"]);
  });

  it("keys the two kinds apart so one row cannot be reused for the other", () => {
    const collide = buildBrowseFeed({
      meetings: [meeting({ id: "same" })],
      insights: [insight({ id: "same" })],
      types: ALL_BROWSE_TYPES,
    });

    expect(new Set(collide.map((item) => item.key)).size).toBe(2);
  });

  it("puts an unreadable date last, whichever kind carries it", () => {
    const feed = buildBrowseFeed({
      meetings: [meeting({ id: "m1", dateLabel: "not-a-date" })],
      insights: [insight({ id: "i1", createdAt: "2026-08-19T09:00:00Z" })],
      types: ALL_BROWSE_TYPES,
    });

    expect(feed.map((item) => item.key)).toEqual(["insight:i1", "meeting:m1"]);
  });
});

describe("groupBrowseFeedByMonth", () => {
  it("puts both kinds under one heading per month", () => {
    const feed = buildBrowseFeed({
      meetings: [
        meeting({ id: "m1", dateLabel: "2026-08-20 10:00" }),
        meeting({ id: "m2", dateLabel: "2026-07-30 10:00" }),
      ],
      insights: [insight({ id: "i1", createdAt: "2026-08-19T09:00:00Z" })],
      types: ALL_BROWSE_TYPES,
    });

    const groups = groupBrowseFeedByMonth(feed);

    expect(groups.map((group) => group.label)).toEqual(["August 2026", "July 2026"]);
    expect(groups[0].items.map((item) => item.key)).toEqual(["meeting:m1", "insight:i1"]);
  });

  it("files a row whose date cannot be read under one honest heading", () => {
    const groups = groupBrowseFeedByMonth(
      buildBrowseFeed({
        meetings: [meeting({ id: "m1", dateLabel: "job-2026" })],
        insights: [insight({ id: "i1", createdAt: "whenever" })],
        types: ALL_BROWSE_TYPES,
      }),
    );

    expect(groups).toHaveLength(1);
    expect(groups[0].label).toBe(UNDATED_MONTH_LABEL);
  });

  it("groups a meeting by the month its own label shows", () => {
    // Identical to rooms.groupMeetingsByMonth, so a list with no insights in it
    // is grouped exactly as it was before D-721.
    const groups = groupBrowseFeedByMonth(
      buildBrowseFeed({
        meetings: [meeting({ id: "m1", dateLabel: "2026-03-31 23:30" })],
        insights: [],
        types: ALL_BROWSE_TYPES,
      }),
    );

    expect(groups[0].label).toBe("March 2026");
  });
});
