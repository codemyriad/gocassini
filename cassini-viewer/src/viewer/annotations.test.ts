import { describe, expect, it } from "vitest";

import {
  findByLabel,
  groupByTag,
  matchTags,
  moveStretchOps,
  splitByTarget,
  tagsByMeeting,
  timeRange,
  type AnnotationItem,
  type AnnotationTarget,
  type AnnotationsDocument,
  type VocabularyTag,
} from "./annotations";

function item(id: string, tagId: string, target: AnnotationTarget): AnnotationItem {
  return {
    id,
    tagId,
    target,
    createdAtUtc: "2026-09-10T11:00:00Z",
    actor: { kind: "person", id: "alice" },
    operationId: "op_1",
  };
}

const doc: AnnotationsDocument = {
  format: "cassini.annotations.v1",
  revision: 3,
  audioOpusSha256: "digest",
  tagNamespace: "urn:uuid:ns",
  tags: [
    { id: "tag_b", label: "budget" },
    { id: "tag_h", label: "hiring" },
    { id: "tag_unused", label: "unused" },
  ],
  items: [
    item("mk_1", "tag_h", { kind: "meeting" }),
    item("mk_3", "tag_h", timeRange(9000, 12000)),
    item("mk_2", "tag_h", timeRange(1000, 2000)),
    item("mk_4", "tag_b", timeRange(500, 700)),
    item("mk_5", "tag_missing", { kind: "meeting" }),
  ],
};

function vocab(tagId: string, label: string, meetings: number): VocabularyTag {
  return { tagId, namespace: "ns", label, meetings, marks: meetings, color: "", icon: "", changedBy: "", changedAtUtc: "" };
}

describe("groupByTag", () => {
  it("keeps a tag's whole-meeting mark apart from its stretches, which run in time order", () => {
    const groups = groupByTag(doc);
    expect(groups.map((group) => group.tag.id)).toEqual(["tag_b", "tag_h"]);
    expect(groups[1].whole?.id).toBe("mk_1");
    expect(groups[1].stretches.map((mark) => mark.id)).toEqual(["mk_2", "mk_3"]);
  });

  it("reads a meeting with no annotations as no tags", () => {
    expect(groupByTag(null)).toEqual([]);
  });
});

describe("splitByTarget", () => {
  it("lists a tag under both when it marks the whole meeting and stretches of it", () => {
    const { whole, stretches } = splitByTarget(doc);
    expect(whole.map((group) => group.tag.label)).toEqual(["hiring"]);
    expect(stretches.map((group) => group.tag.label)).toEqual(["budget", "hiring"]);
  });
});

describe("tagsByMeeting", () => {
  it("joins each meeting's tags to the vocabulary, whole-meeting tags first", () => {
    const joined = tagsByMeeting({
      tags: [vocab("tag_h", "hiring", 3), vocab("tag_b", "budget", 1)],
      meetings: [
        {
          meetingId: "m1",
          tags: [
            { tagId: "tag_b", whole: false, stretches: 2 },
            { tagId: "tag_h", whole: true, stretches: 0 },
            { tagId: "tag_gone", whole: true, stretches: 0 },
          ],
        },
      ],
      coverage: { visible: 1, indexed: 1 },
    });
    expect(joined.get("m1")?.map(({ tag, whole, stretches }) => [tag.label, whole, stretches])).toEqual([
      ["hiring", true, 0],
      ["budget", false, 2],
    ]);
    expect(joined.get("m2")).toBeUndefined();
  });
});

describe("matchTags and findByLabel", () => {
  const tags = [vocab("t1", "rehire", 9), vocab("t2", "hiring", 1), vocab("t3", "HR", 5), vocab("t4", "budget", 2)];

  it("puts prefix matches first, then the most used", () => {
    expect(matchTags(tags, " H").map((tag) => tag.label)).toEqual(["HR", "hiring", "rehire"]);
    expect(matchTags(tags, "").map((tag) => tag.label)).toEqual(["rehire", "HR", "budget", "hiring"]);
  });

  it("matches a label as the operator does: trimmed and case-insensitive", () => {
    expect(findByLabel(tags, "  Hiring ")?.tagId).toBe("t2");
    expect(findByLabel(tags, "hir")).toBeUndefined();
  });
});

describe("timeRange and moveStretchOps", () => {
  it("rounds to whole milliseconds and refuses an empty range", () => {
    expect(timeRange(-4, 1000.6)).toEqual({ kind: "time-range", startMs: 0, endMs: 1001 });
    expect(() => timeRange(500, 500)).toThrow(RangeError);
  });

  it("moves a stretch as one batch: unmark the old, mark the new", () => {
    expect(moveStretchOps("mk_2", { id: "tag_h", label: "hiring" }, 1500, 2500)).toEqual([
      { op: "unmark", itemId: "mk_2" },
      {
        op: "mark",
        tag: { id: "tag_h", label: "hiring" },
        target: { kind: "time-range", startMs: 1500, endMs: 2500 },
      },
    ]);
  });
});
