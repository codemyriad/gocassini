import { get } from "svelte/store";
import { describe, expect, it, vi } from "vitest";
import { AnnotationError, type AnnotationBatchResult, type TagVocabulary } from "./annotations";
import { createBulkTagSession, withAnnotationBatch } from "./bulkTags";

const tag = { tagId: "new", label: "New", namespace: "ns", color: "blue" as const, icon: "" as const, meetings: 0, marks: 0, changedBy: "", changedAtUtc: "" };
const batch: AnnotationBatchResult = {
  tags: [tag],
  results: ["a", "b"].map((meetingId) => ({
    meetingId, revision: 1, resolved: true, operationId: "op", added: ["i"], removed: [], notFound: [],
    annotations: { format: "cassini.annotations.v1", revision: 1, audioOpusSha256: "audio", tagNamespace: "ns", tags: [{ id: "new", label: "New" }], items: [{ id: "i", tagId: "new", target: { kind: "meeting" }, actor: { kind: "person", id: "alice" }, createdAtUtc: "now", operationId: "op" }] },
  })),
};
const request = { ops: [{ op: "mark" as const, tag: { label: "New" }, target: { kind: "meeting" as const } }] };

describe("bulk list tagging", () => {
  it("sends one request and publishes all results once, after acknowledgment", async () => {
    let finish!: (result: AnnotationBatchResult) => void;
    const apply = vi.fn(() => new Promise<AnnotationBatchResult>((resolve) => { finish = resolve; }));
    const committed = vi.fn();
    const session = createBulkTagSession(apply, committed);
    const done = session.write(["a", "b"], request, false);
    expect(get(session).busy).toBe(true);
    expect(committed).not.toHaveBeenCalled();
    expect(await session.write(["c"], request, false)).toBe(false);
    expect(apply).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ meetingIds: ["a", "b"], requestId: expect.any(String), ...request }));
    finish(batch);
    expect(await done).toBe(true);
    expect(committed).toHaveBeenCalledExactlyOnceWith(batch);
    expect(get(session)).toEqual({ busy: false, retryable: false, report: "Tagged 2 meetings" });
  });

  it("keeps original targets and the request ID after a lost response", async () => {
    const apply = vi.fn().mockRejectedValueOnce(new TypeError("lost response")).mockResolvedValue(batch);
    const committed = vi.fn();
    const session = createBulkTagSession(apply, committed);
    expect(await session.write(["a", "b"], request, false)).toBe(false);
    expect(committed).not.toHaveBeenCalled();
    expect(get(session).retryable).toBe(true);
    expect(await session.write(["c"], request, true)).toBe(false);
    expect(await session.retry()).toBe(true);
    expect(apply.mock.calls[1][0]).toEqual(apply.mock.calls[0][0]);
    expect(committed).toHaveBeenCalledTimes(1);
  });

  it("does not publish partial rows on refusal and permits a corrected selection", async () => {
    const apply = vi.fn().mockRejectedValueOnce(new AnnotationError(404, "not found")).mockResolvedValue(batch);
    const committed = vi.fn();
    const session = createBulkTagSession(apply, committed);
    await session.write(["a", "b"], request, false);
    expect(committed).not.toHaveBeenCalled();
    expect(get(session).retryable).toBe(false);
    await session.write(["a"], request, false);
    expect(apply.mock.calls[1][0].requestId).not.toBe(apply.mock.calls[0][0].requestId);
  });

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
