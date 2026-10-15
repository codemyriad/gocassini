import { describe, expect, it } from "vitest";
import { type AnnotationBatchResult, type TagVocabulary } from "./annotations";
import { withAnnotationBatch } from "./bulkTags";

const tag = { tagId: "new", label: "New", namespace: "ns", color: "blue" as const, icon: "" as const, meetings: 0, marks: 0, changedBy: "", changedAtUtc: "" };
const batch: AnnotationBatchResult = {
  tags: [tag],
  results: ["a", "b"].map((meetingId) => ({
    meetingId, revision: 1, resolved: true, operationId: "op", added: ["i"], removed: [], notFound: [],
    annotations: { format: "cassini.annotations.v1", revision: 1, audioOpusSha256: "audio", tagNamespace: "ns", tags: [{ id: "new", label: "New" }], items: [{ id: "i", tagId: "new", target: { kind: "meeting" }, actor: { kind: "person", id: "alice" }, createdAtUtc: "now", operationId: "op" }] },
  })),
};

describe("bulk list tagging", () => {
  it("renders a new tag on every row without waiting for a vocabulary GET", () => {
    const before: TagVocabulary = { tags: [], meetings: [], coverage: { visible: 2, indexed: 2 } };
    const after = withAnnotationBatch(before, batch);
    expect(after.meetings.map((meeting) => [meeting.meetingId, meeting.tags[0].tagId])).toEqual([["a", "new"], ["b", "new"]]);
    expect(after.tags).toEqual([{ ...tag, meetings: 2, marks: 2 }]);
    expect(before.tags).toEqual([]);
    expect(withAnnotationBatch(after, batch)).toEqual(after);
  });

  it("preserves other meetings and removes a tag from all batch targets together", () => {
    const before = withAnnotationBatch({ tags: [], meetings: [{ meetingId: "other", tags: [] }], coverage: { visible: 3, indexed: 3 } }, batch);
    const after = withAnnotationBatch(before, { tags: [], results: batch.results.map((result) => ({ ...result, annotations: null })) });
    expect(after.tags).toEqual([]);
    expect(after.meetings).toHaveLength(3);
    expect(after.meetings.every((meeting) => meeting.tags.length === 0)).toBe(true);
  });
});
