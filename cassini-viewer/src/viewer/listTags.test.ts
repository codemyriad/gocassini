import { describe, expect, it } from "vitest";

import { tagsByMeeting, type TagVocabulary, type VocabularyTag } from "./annotations";
import { filterMeetingCatalogEntries, type MeetingCatalogEntry } from "./catalog";
import {
  applyEach,
  bulkReport,
  filterByTags,
  planBulkTag,
  rowChips,
  wholeTagRequest,
  wholeTagState,
} from "./listTags";
import { filterMeetingsByRoom } from "./rooms";

function tag(tagId: string, label: string): VocabularyTag {
  return { tagId, namespace: "ns", label, meetings: 1, marks: 1, color: "", icon: "", changedBy: "", changedAtUtc: "" };
}
const entry = (id: string, title: string, roomId?: string): MeetingCatalogEntry => ({
  id,
  title,
  dateLabel: "2026-09-01",
  roomId,
  roomName: roomId,
});

const meetings = [
  entry("m1", "Hiring sync", "r1"),
  entry("m2", "Budget review", "r1"),
  entry("m3", "Hiring budget", "r2"),
  entry("m4", "Standup", "r2"),
];
const vocabulary: TagVocabulary = {
  tags: [tag("t_h", "hiring"), tag("t_b", "budget"), tag("t_x", "exec")],
  meetings: [
    { meetingId: "m1", tags: [{ tagId: "t_h", whole: true, stretches: 0 }] },
    { meetingId: "m2", tags: [{ tagId: "t_b", whole: false, stretches: 2 }] },
    {
      meetingId: "m3",
      tags: [
        { tagId: "t_h", whole: true, stretches: 0 },
        { tagId: "t_b", whole: true, stretches: 1 },
      ],
    },
  ],
  coverage: { visible: 4, indexed: 4 },
};
const byMeeting = tagsByMeeting(vocabulary);
const ids = (list: { id: string }[]) => list.map((m) => m.id);

describe("filterByTags", () => {
  it("narrows nothing until a tag is chosen", () => {
    expect(filterByTags(meetings, byMeeting, [], "all")).toBe(meetings);
  });

  it("matches any or all of the chosen tags, stretches included", () => {
    expect(ids(filterByTags(meetings, byMeeting, ["t_h", "t_b"], "any"))).toEqual(["m1", "m2", "m3"]);
    expect(ids(filterByTags(meetings, byMeeting, ["t_h", "t_b"], "all"))).toEqual(["m3"]);
  });

  it("combines with the room and the search", () => {
    const inRoom = filterMeetingsByRoom(meetings, "id:r1");
    expect(ids(filterByTags(inRoom, byMeeting, ["t_h"], "any"))).toEqual(["m1"]);
    const tagged = filterByTags(meetings, byMeeting, ["t_b"], "any");
    expect(ids(filterMeetingCatalogEntries(tagged, "hiring"))).toEqual(["m3"]);
  });
});

describe("rowChips", () => {
  it("shows three and counts the rest", () => {
    const four = ["a", "b", "c", "d"].map((id) => ({ tag: tag(id, id), whole: true, stretches: 0 }));
    const { shown, rest } = rowChips(four);
    expect(shown).toHaveLength(3);
    expect(rest.map(({ tag }) => tag.label)).toEqual(["d"]);
    expect(rowChips(undefined).shown).toEqual([]);
  });
});

describe("whole-meeting tag requests", () => {
  it("marks an existing tag by id, unmarks it by tag, and colours a new one in the same request", () => {
    expect(wholeTagRequest({ tagId: "t_h", label: "hiring" }, false)).toEqual({
      ops: [{ op: "mark", tag: { id: "t_h", label: "hiring" }, target: { kind: "meeting" } }],
    });
    expect(wholeTagRequest({ tagId: "t_h", label: "hiring" }, true)).toEqual({
      ops: [{ op: "unmark-tag", tagId: "t_h", target: { kind: "meeting" } }],
    });
    expect(wholeTagRequest({ label: "legal", color: "teal", icon: "" }, false)).toEqual({
      ops: [{ op: "mark", tag: { label: "legal" }, target: { kind: "meeting" } }],
      tagStyles: [{ label: "legal", color: "teal", icon: "" }],
    });
  });

  it("ticks the tags on every selected meeting and half-ticks the ones on some", () => {
    expect(wholeTagState(byMeeting, ["m1", "m3"])).toEqual({ selected: ["t_h"], mixed: ["t_b"] });
  });
});

describe("tagging several meetings", () => {
  const picked = [meetings[0], meetings[2]];

  it("adds a tag to the meetings without it, and removes one that is on all of them", () => {
    const add = planBulkTag(picked, byMeeting, { tagId: "t_b", label: "budget" });
    expect(add.remove).toBe(false);
    expect(ids(add.targets)).toEqual(["m1"]);
    const remove = planBulkTag(picked, byMeeting, { tagId: "t_h", label: "hiring" });
    expect(remove.remove).toBe(true);
    expect(ids(remove.targets)).toEqual(["m1", "m3"]);
    expect(remove.request.ops[0]).toEqual({ op: "unmark-tag", tagId: "t_h", target: { kind: "meeting" } });
    expect(ids(planBulkTag(picked, byMeeting, { label: "new", color: "red", icon: "" }).targets)).toEqual(["m1", "m3"]);
  });

  it("sends one request per meeting, one after another, and reports the ones that failed", async () => {
    const order: string[] = [];
    let inFlight = 0;
    const result = await applyEach(["a", "b", "c", "d", "e"], async (id) => {
      inFlight += 1;
      expect(inFlight).toBe(1);
      order.push(id);
      await Promise.resolve();
      inFlight -= 1;
      if (id === "c") {
        throw new Error("busy");
      }
    });
    expect(order).toEqual(["a", "b", "c", "d", "e"]);
    expect(result).toEqual({ done: 4, failed: 1 });
    expect(bulkReport(false, 4, 5)).toBe("Tagged 4 of 5 — 1 failed");
    expect(bulkReport(true, 2, 2)).toBe("Untagged 2 meetings");
  });
});
