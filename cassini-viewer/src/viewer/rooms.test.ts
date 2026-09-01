import { describe, expect, it } from "vitest";
import type { MeetingCatalogEntry } from "./catalog";
import {
  NO_ROOM_KEY,
  NO_ROOM_LABEL,
  UNDATED_MONTH_LABEL,
  buildRoomBuckets,
  filterMeetingsByRoom,
  groupMeetingsByMonth,
  roomKeyOf,
  roomLabelOf,
} from "./rooms";

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

describe("roomKeyOf", () => {
  it("prefers the durable id over the display name", () => {
    expect(
      roomKeyOf(meeting({ id: "a", roomId: "r1", roomName: "Design" })),
    ).toBe("id:r1");
  });

  it("groups a renamed room's meetings together by id", () => {
    const before = meeting({ id: "a", roomId: "r1", roomName: "Design" });
    const after = meeting({ id: "b", roomId: "r1", roomName: "Design review" });
    expect(roomKeyOf(before)).toBe(roomKeyOf(after));
  });

  it("keeps two same-named rooms apart when they have distinct ids", () => {
    const left = meeting({ id: "a", roomId: "r1", roomName: "Daily" });
    const right = meeting({ id: "b", roomId: "r2", roomName: "Daily" });
    expect(roomKeyOf(left)).not.toBe(roomKeyOf(right));
  });

  it("falls back to the name when only a name was recorded", () => {
    expect(roomKeyOf(meeting({ id: "a", roomName: "Ad hoc" }))).toBe(
      "name:Ad hoc",
    );
  });

  it("returns the no-room key when neither field is present", () => {
    expect(roomKeyOf(meeting({ id: "a" }))).toBe(NO_ROOM_KEY);
  });
});

describe("roomLabelOf", () => {
  it("shows the id when a room was recorded but never named", () => {
    expect(roomLabelOf(meeting({ id: "a", roomId: "r1" }))).toBe("r1");
  });

  it("distinguishes an unnamed room from no room at all", () => {
    expect(roomLabelOf(meeting({ id: "a" }))).toBe(NO_ROOM_LABEL);
  });
});

describe("buildRoomBuckets", () => {
  it("counts meetings per room and sorts rooms alphabetically", () => {
    const buckets = buildRoomBuckets([
      meeting({ id: "a", roomId: "r-ops", roomName: "Ops & Infra" }),
      meeting({ id: "b", roomId: "r-daily", roomName: "Daily" }),
      meeting({ id: "c", roomId: "r-daily", roomName: "Daily" }),
    ]);
    expect(buckets).toEqual([
      { key: "id:r-daily", name: "Daily", count: 2, hasRoom: true },
      { key: "id:r-ops", name: "Ops & Infra", count: 1, hasRoom: true },
    ]);
  });

  it("keeps meetings with no room visible, in a bucket pinned last", () => {
    const buckets = buildRoomBuckets([
      meeting({ id: "a" }),
      meeting({ id: "b", roomId: "r-zulu", roomName: "Zulu" }),
      meeting({ id: "c" }),
    ]);
    expect(buckets.at(-1)).toEqual({
      key: NO_ROOM_KEY,
      name: NO_ROOM_LABEL,
      count: 2,
      hasRoom: false,
    });
  });

  it("names a renamed room after its newest meeting", () => {
    // Callers pass a newest-first list, so the first entry seen wins.
    const buckets = buildRoomBuckets([
      meeting({ id: "b", roomId: "r1", roomName: "Design review" }),
      meeting({ id: "a", roomId: "r1", roomName: "Design" }),
    ]);
    expect(buckets).toEqual([
      { key: "id:r1", name: "Design review", count: 2, hasRoom: true },
    ]);
  });

  it("returns nothing for an empty catalog", () => {
    expect(buildRoomBuckets([])).toEqual([]);
  });
});

describe("filterMeetingsByRoom", () => {
  const meetings = [
    meeting({ id: "a", roomId: "r1", roomName: "Design" }),
    meeting({ id: "b" }),
    meeting({ id: "c", roomId: "r2", roomName: "Daily" }),
  ];

  it("returns every meeting when no room is selected", () => {
    expect(filterMeetingsByRoom(meetings, null).map((m) => m.id)).toEqual([
      "a",
      "b",
      "c",
    ]);
  });

  it("narrows to one room", () => {
    expect(filterMeetingsByRoom(meetings, "id:r1").map((m) => m.id)).toEqual([
      "a",
    ]);
  });

  it("selects the roomless meetings — the one thing 'all' does not mean", () => {
    expect(filterMeetingsByRoom(meetings, NO_ROOM_KEY).map((m) => m.id)).toEqual(
      ["b"],
    );
  });
});

describe("groupMeetingsByMonth", () => {
  it("splits into month headings without reordering the list", () => {
    const groups = groupMeetingsByMonth([
      meeting({ id: "a", dateLabel: "2026-08-18 14:30" }),
      meeting({ id: "b", dateLabel: "2026-08-01" }),
      meeting({ id: "c", dateLabel: "2026-07-30 09:15" }),
    ]);
    expect(groups.map((group) => [group.label, group.meetings.map((m) => m.id)])).toEqual([
      ["August 2026", ["a", "b"]],
      ["July 2026", ["c"]],
    ]);
  });

  it("emits one heading per month even if the list is not perfectly ordered", () => {
    const groups = groupMeetingsByMonth([
      meeting({ id: "a", dateLabel: "2026-08-18" }),
      meeting({ id: "b", dateLabel: "2026-07-30" }),
      meeting({ id: "c", dateLabel: "2026-08-02" }),
    ]);
    expect(groups.map((group) => group.key)).toEqual(["2026-08", "2026-07"]);
    expect(groups[0].meetings.map((m) => m.id)).toEqual(["a", "c"]);
  });

  it("gives an unparseable date label a heading of its own", () => {
    const groups = groupMeetingsByMonth([
      meeting({ id: "a", dateLabel: "2026-08-18" }),
      meeting({ id: "b", dateLabel: "job-7g0xe5r36x" }),
    ]);
    expect(groups.map((group) => group.label)).toEqual([
      "August 2026",
      UNDATED_MONTH_LABEL,
    ]);
  });

  it("does not straddle a month boundary at the end of a day", () => {
    // The label carries no timezone; grouping reads its digits as UTC, so
    // "2026-07-31 23:30" is July no matter where the browser is.
    const groups = groupMeetingsByMonth([
      meeting({ id: "a", dateLabel: "2026-07-31 23:30" }),
    ]);
    expect(groups[0].key).toBe("2026-07");
  });

  it("returns nothing for an empty list", () => {
    expect(groupMeetingsByMonth([])).toEqual([]);
  });
});
