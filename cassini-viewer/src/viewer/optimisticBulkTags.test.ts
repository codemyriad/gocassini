import { get } from "svelte/store";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AnnotationError, type AnnotationRequest, type AnnotationBatchRequest, type AnnotationBatchResult, type AnnotationResult, type TagVocabulary } from "./annotations";
import type { MeetingCatalogEntry } from "./catalog";
import { createListTagSession, createWriteQueue } from "./listTags";

const meetings = ["a", "b", "c"].map((id) => ({ id, title: id, dateLabel: "2026-10-15" }));
const pick = { tagId: "t", label: "Project" };
const vocabulary = (): TagVocabulary => ({
  tags: [{ ...pick, namespace: "ns", color: "blue", icon: "", meetings: 0, marks: 0 }],
  meetings: [], coverage: { visible: 3, indexed: 3 },
});
function answer(id: string, on = true): AnnotationResult {
  return {
    meetingId: id, revision: 1, resolved: true, operationId: "op", added: [], removed: [], notFound: [],
    annotations: {
      format: "cassini.annotations.v1", revision: 1, audioOpusSha256: "", tagNamespace: "ns",
      tags: [{ id: "t", label: "Project" }],
      items: on ? [{ id: `i-${id}`, tagId: "t", target: { kind: "meeting" }, actor: { kind: "person", id: "user" }, createdAtUtc: "", operationId: "op" }] : [],
    },
  };
}
const batch = (ids = ["a", "b"], on = true): AnnotationBatchResult => ({ tags: vocabulary().tags, results: ids.map((id) => answer(id, on)) });
const settle = async () => { for (let i = 0; i < 50; i++) await Promise.resolve(); };
function setup() {
  const apply = vi.fn(async (_meeting: MeetingCatalogEntry, _request: AnnotationRequest) => answer("a", false));
  const applyBatch = vi.fn<(request: AnnotationBatchRequest) => Promise<AnnotationBatchResult>>(() => new Promise(() => {}));
  const drained = vi.fn();
  const session = createListTagSession(apply, createWriteQueue(drained), applyBatch);
  session.setConfirmed(vocabulary());
  const on = (id: string) => get(session).vocabulary!.meetings.find((m) => m.meetingId === id)?.tags.some((t) => t.whole) ?? false;
  return { apply, applyBatch, drained, session, on };
}
afterEach(() => vi.useRealTimers());

describe("shared optimistic single and bulk tagging", () => {
  it("publishes the whole selection immediately and preserves a later single inverse through acknowledgment", async () => {
    const { session, applyBatch, apply, on, drained } = setup();
    let finish!: (value: AnnotationBatchResult) => void;
    applyBatch.mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    session.toggleBulk(meetings.slice(0, 2), pick);
    expect([on("a"), on("b")]).toEqual([true, true]);
    expect(get(session).vocabulary!.tags[0]).toMatchObject({ meetings: 2, marks: 2 });
    expect(applyBatch).not.toHaveBeenCalled();
    session.toggle(meetings[0], pick);
    expect([on("a"), on("b")]).toEqual([false, true]);
    await settle();
    expect(apply).not.toHaveBeenCalled();
    finish(batch());
    await settle();
    expect([on("a"), on("b")]).toEqual([false, true]);
    expect(applyBatch).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ meetingIds: ["a", "b"] }));
    expect(apply.mock.calls[0][1]).toMatchObject({ ops: [{ op: "unmark-tag", tagId: "t", target: { kind: "meeting" } }] });
    expect(drained).toHaveBeenCalledTimes(1);
  });

  it("calculates targets after an earlier single write and captures selection at click time", async () => {
    const { session, apply, applyBatch, on } = setup();
    apply.mockResolvedValue(answer("a"));
    applyBatch.mockResolvedValue(batch(["b"]));
    session.toggle(meetings[0], pick);
    const selected = meetings.slice(0, 2);
    session.toggleBulk(selected, pick);
    selected.splice(0, 2, meetings[2]);
    expect([on("a"), on("b"), on("c")]).toEqual([true, true, false]);
    await settle();
    expect(applyBatch.mock.calls[0][0].meetingIds).toEqual(["b"]);
  });

  it("shares one temporary identity and resolves it for a queued bulk removal", async () => {
    const { session, applyBatch, on } = setup();
    session.setConfirmed({ ...vocabulary(), tags: [] });
    applyBatch.mockResolvedValueOnce(batch()).mockResolvedValueOnce(batch(["a", "b"], false));
    session.toggleBulk(meetings.slice(0, 2), { label: "Project", color: "blue", icon: "" });
    const optimistic = get(session).vocabulary!;
    expect(optimistic.tags).toHaveLength(1);
    expect(optimistic.meetings.every((m) => m.tags[0].tagId === optimistic.tags[0].tagId)).toBe(true);
    session.toggleBulk(meetings.slice(0, 2), { tagId: optimistic.tags[0].tagId, label: "Project" });
    expect([on("a"), on("b")]).toEqual([false, false]);
    await settle();
    expect(applyBatch.mock.calls[1][0]).toMatchObject({ ops: [{ op: "unmark-tag", tagId: "t", target: { kind: "meeting" } }] });
    expect([on("a"), on("b")]).toEqual([false, false]);
  });

  it("can create a temporarily identified tag for a later selection after the original batch is rejected", async () => {
    const { session, applyBatch, on } = setup();
    session.setConfirmed({ ...vocabulary(), tags: [] });
    applyBatch.mockRejectedValueOnce(new AnnotationError(404, "not found")).mockResolvedValueOnce(batch(["c"]));
    session.toggleBulk(meetings.slice(0, 2), { label: "Project", color: "blue", icon: "" });
    const temporary = get(session).vocabulary!.tags[0];
    session.toggleBulk([meetings[2]], { tagId: temporary.tagId, label: temporary.label });
    await settle();
    expect(applyBatch.mock.calls[1][0]).toMatchObject({
      meetingIds: ["c"], ops: [{ op: "mark", tag: { label: "Project" }, target: { kind: "meeting" } }],
      tagStyles: [{ label: "Project", color: "blue", icon: "" }],
    });
    expect([on("a"), on("b"), on("c")]).toEqual([false, false, true]);
  });

  it("rolls back an entire rejected batch while preserving a newer independent edit", async () => {
    const { session, applyBatch, on } = setup();
    applyBatch.mockRejectedValueOnce(new AnnotationError(404, "not found")).mockResolvedValueOnce(batch(["c"]));
    session.toggleBulk(meetings.slice(0, 2), pick);
    session.toggleBulk([meetings[2]], pick);
    await settle();
    expect([on("a"), on("b"), on("c")]).toEqual([false, false, true]);
    expect(get(session).notice).toContain("Could not tag 2 meetings");
    expect(get(session).retryable).toBe(false);
  });

  it("retains uncertain batches, retries the exact payload, and keeps deferred edits ahead of new clicks", async () => {
    vi.useFakeTimers();
    const { session, applyBatch, apply, on } = setup();
    applyBatch.mockRejectedValue(new TypeError("lost response"));
    session.toggleBulk(meetings.slice(0, 2), pick);
    session.toggle(meetings[0], pick);
    await vi.runAllTimersAsync();
    expect(get(session).retryable).toBe(true);
    expect(apply).not.toHaveBeenCalled();
    expect([on("a"), on("b")]).toEqual([false, true]);
    const request = applyBatch.mock.calls[0][0];
    applyBatch.mockResolvedValueOnce(batch()).mockResolvedValueOnce(batch(["a"]));
    expect(session.retry()).toBe(true);
    expect(session.retry()).toBe(false);
    session.toggleBulk(meetings.slice(0, 2), pick);
    await vi.runAllTimersAsync();
    expect(applyBatch.mock.calls[1][0]).toBe(request);
    expect(applyBatch.mock.calls[2][0]).toBe(request);
    expect(applyBatch.mock.calls[3][0].meetingIds).toEqual(["a"]);
    expect([on("a"), on("b")]).toEqual([true, true]);
    expect(get(session).retryable).toBe(false);
  });

  it.each([503, 429])("bounds %s retries and preserves the original request for manual recovery", async (status) => {
    vi.useFakeTimers();
    const { session, applyBatch } = setup();
    applyBatch.mockRejectedValue(new AnnotationError(status, "retry later"));
    session.toggleBulk(meetings.slice(0, 2), pick);
    await vi.runAllTimersAsync();
    expect(applyBatch).toHaveBeenCalledTimes(6);
    expect(get(session).retryable).toBe(true);
    applyBatch.mockResolvedValue(batch());
    session.retry();
    await vi.runAllTimersAsync();
    expect(applyBatch.mock.calls.every(([request]) => request === applyBatch.mock.calls[0][0])).toBe(true);
    expect(get(session).retryable).toBe(false);
  });

  it("preserves stretch counts while optimistically adding and removing whole-meeting tags", () => {
    const { session } = setup();
    session.setConfirmed({ ...vocabulary(), tags: [{ ...vocabulary().tags[0], meetings: 1, marks: 2 }], meetings: [{ meetingId: "a", tags: [{ tagId: "t", whole: false, stretches: 2 }] }] });
    session.toggleBulk(meetings.slice(0, 2), pick);
    expect(get(session).vocabulary!.tags[0]).toMatchObject({ meetings: 2, marks: 4 });
    session.toggleBulk(meetings.slice(0, 2), pick);
    expect(get(session).vocabulary!.tags[0]).toMatchObject({ meetings: 1, marks: 2 });
    expect(get(session).vocabulary!.meetings[0].tags[0]).toMatchObject({ whole: false, stretches: 2 });
  });

  it("settles a redundant inverse after rejection without another request or a stuck saving message", async () => {
    const { session, applyBatch, on } = setup();
    applyBatch.mockRejectedValueOnce(new AnnotationError(404, "not found"));
    session.toggleBulk(meetings.slice(0, 2), pick);
    session.toggleBulk(meetings.slice(0, 2), pick);
    await settle();
    expect(applyBatch).toHaveBeenCalledTimes(1);
    expect([on("a"), on("b")]).toEqual([false, false]);
    expect(get(session).bulkReport).toBe("Untagged 2 meetings");
  });

  it("rejects oversized selections without publishing optimistic rows or scheduling writes", async () => {
    const { session, applyBatch } = setup();
    expect(session.toggleBulk(Array.from({ length: 101 }, (_, i) => ({ ...meetings[0], id: `${i}` })), pick)).toBe(false);
    expect(get(session).vocabulary!.meetings).toEqual([]);
    expect(get(session).bulkReport).toContain("at most 100");
    await settle();
    expect(applyBatch).not.toHaveBeenCalled();
  });
});
