import { get } from "svelte/store";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AnnotationError, markRequest, moveStretchOps, removeRequest, untagMeetingRequest, WHOLE_MEETING,
  type AnnotationItem, type AnnotationRequest, type AnnotationResult, type MeetingAnnotations } from "../../viewer/annotations";
import { createMarksSession } from "./session";

const range = (startMs = 1000, endMs = 2000) => ({ kind: "time-range" as const, startMs, endMs });
const item = (id: string, start = 1000): AnnotationItem => ({ id, tagId: "t1", target: range(start, start + 1000),
  actor: { kind: "person", id: "ana" }, createdAtUtc: "now", operationId: "op1" });
const answer = (items: AnnotationItem[] = [], revision = 1): AnnotationResult => ({
  meetingId: "m1", revision, stateToken: `epoch:${revision}`, resolved: true,
  operationId: "op1", added: items.map(({ id }) => id), removed: [], notFound: [],
  annotations: { format: "cassini.annotations.v1", revision, audioOpusSha256: "audio", tagNamespace: "ns",
    tags: items.length ? [{ id: "t1", label: "Focus", color: "red", icon: "" }] : [], items },
});
const pick = { label: "Focus", color: "red" as const, icon: "" as const };
const settle = async () => { for (let n = 0; n < 30; n++) await Promise.resolve(); };

async function held(initial: MeetingAnnotations = answer()) {
  const calls: AnnotationRequest[] = [];
  const responses: { resolve: (answer: AnnotationResult) => void; reject: (error: unknown) => void }[] = [];
  const changed = vi.fn();
  const session = createMarksSession(changed);
  await session.open(async () => initial, (request) => {
    calls.push(request);
    return new Promise((resolve, reject) => responses.push({ resolve, reject }));
  });
  return { session, calls, responses, changed };
}

describe("optimistic annotation intents", () => {
  afterEach(() => vi.useRealTimers());

  it("keeps add → move → remove responsive and resolves every temporary item and tag ID", async () => {
    const { session, calls, responses } = await held();
    session.write(markRequest(pick, range()), "stretch");
    let visible = get(session).annotations!.annotations!;
    const original = visible.items[0].id;
    expect(visible.tags[0]).toMatchObject({ label: "Focus", color: "red", icon: "" });
    session.write({ ops: moveStretchOps(original, visible.tags[0], 3000, 4000) }, "stretch");
    visible = get(session).annotations!.annotations!;
    expect(visible.items[0].target).toEqual(range(3000, 4000));
    const moved = visible.items[0].id;
    session.write(removeRequest([moved]), "stretch");
    expect(get(session).annotations!.annotations!.items).toEqual([]);
    await settle();
    expect(calls).toHaveLength(1);
    responses.shift()!.resolve(answer([item("server-added")], 2));
    await settle();
    expect(calls[1]).toMatchObject({ stateToken: "epoch:2", ops: [
      { op: "unmark", itemId: "server-added" },
      { op: "mark", tag: { id: "t1", label: "Focus" }, target: range(3000, 4000) },
    ] });
    expect(get(session).annotations!.annotations!.items).toEqual([]);
    responses.shift()!.resolve(answer([item("server-moved", 3000)], 3));
    await settle();
    expect(calls[2]).toMatchObject({ stateToken: "epoch:3", ops: [{ op: "unmark", itemId: "server-moved" }] });
    responses.shift()!.resolve(answer([], 4));
    await session.whenIdle();
    expect(get(session)).toMatchObject({ saving: false, retryable: false, busy: false });
  });

  it("keeps a selected optimistic item ID stable when it is acknowledged", async () => {
    const { session, responses } = await held();
    session.write(markRequest(pick, range()));
    const local = get(session).annotations!.annotations!.items[0].id;
    await settle();
    responses.shift()!.resolve(answer([item("server-item")], 2));
    await session.whenIdle();
    expect(get(session).annotations!.annotations!.items[0].id).toBe(local);
  });

  it("resolves a fast whole-tag removal and preserves an existing range", async () => {
    const { session, calls, responses } = await held(answer([item("existing")]));
    session.write(markRequest({ tagId: "t1", label: "Focus" }, WHOLE_MEETING));
    session.write(untagMeetingRequest("t1"));
    expect(get(session).annotations!.annotations!.items).toEqual([item("existing")]);
    await settle();
    responses.shift()!.resolve(answer([item("existing"), { ...item("whole"), target: WHOLE_MEETING }], 2));
    await settle();
    expect(get(session).annotations!.annotations!.items).toEqual([item("existing")]);
    expect(calls[1].ops).toEqual([{ op: "unmark-tag", tagId: "t1", target: WHOLE_MEETING }]);
    responses.shift()!.resolve(answer([item("existing")], 3));
    await session.whenIdle();
  });

  it("deduplicates an existing mark without changing its UI identity", async () => {
    const { session, responses } = await held(answer([item("existing")]));
    session.write(markRequest(pick, range()));
    expect(get(session).annotations!.annotations!.items).toHaveLength(1);
    await settle();
    responses.shift()!.resolve(answer([item("existing")]));
    await session.whenIdle();
    expect(get(session).annotations!.annotations!.items[0].id).toBe("existing");
  });

  it("rolls back a rejected creation and refuses its dependent move, while saving an independent edit", async () => {
    const { session, calls, responses } = await held();
    session.write(markRequest(pick, range()));
    const visible = get(session).annotations!.annotations!;
    session.write({ ops: moveStretchOps(visible.items[0].id, visible.tags[0], 3000, 4000) });
    session.write(markRequest(pick, WHOLE_MEETING));
    await settle();
    responses.shift()!.reject(new AnnotationError(400, "invalid range"));
    await settle();
    expect(calls).toHaveLength(2);
    expect(calls[1].ops).toEqual([{ op: "mark", tag: { id: undefined, label: "Focus" }, target: WHOLE_MEETING }]);
    expect(get(session).annotations!.annotations!.items.map(({ target }) => target)).toEqual([WHOLE_MEETING]);
    expect(get(session).error).toContain("changed or was removed");
    responses.shift()!.resolve(answer([{ ...item("whole"), target: WHOLE_MEETING }], 2));
    await session.whenIdle();
  });

  it("retains later intent behind an unknown outcome and replays the exact receipt on retry", async () => {
    vi.useFakeTimers();
    const calls: AnnotationRequest[] = [];
    let attempts = 0;
    const session = createMarksSession(() => {});
    await session.open(async () => answer(), async (request) => {
      calls.push(request);
      if (attempts++ < 2) throw new TypeError("response lost after commit");
      return request.ops[0].op === "mark" ? answer([item("server-item")], 2) : answer([], 3);
    });
    session.write(markRequest(pick, range()));
    session.write(removeRequest([get(session).annotations!.annotations!.items[0].id]));
    await vi.runAllTimersAsync();
    await session.whenIdle();
    expect(calls).toHaveLength(2);
    expect(get(session)).toMatchObject({ retryable: true, saving: true });
    expect(get(session).annotations!.annotations!.items).toEqual([]);
    expect(session.retryWrite()).toBe(true);
    await session.whenIdle();
    expect(calls).toHaveLength(4);
    expect(calls[0]).toEqual(calls[1]);
    expect(calls[0]).toEqual(calls[2]);
    expect(calls[3].ops).toEqual([{ op: "unmark", itemId: "server-item" }]);
    expect(get(session)).toMatchObject({ retryable: false, saving: false, error: "" });
  });

  it("refreshes after conflict and never resurrects a concurrently removed range", async () => {
    const initial = answer([item("existing")]);
    const load = vi.fn().mockResolvedValueOnce(initial).mockResolvedValue(answer([], 2));
    const apply = vi.fn().mockRejectedValue(new AnnotationError(409, "annotations changed"));
    const session = createMarksSession(() => {});
    await session.open(load, apply);
    session.write({ ops: moveStretchOps("existing", initial.annotations!.tags[0], 3000, 4000) });
    const visible = get(session).annotations!.annotations!;
    session.write({ ops: moveStretchOps(visible.items[0].id, visible.tags[0], 5000, 6000) });
    await session.whenIdle();
    expect(apply).toHaveBeenCalledTimes(1);
    expect(load).toHaveBeenCalledTimes(2);
    expect(get(session).annotations!.annotations!.items).toEqual([]);
    expect(get(session).error).toContain("changed or was removed");
  });

  it("keeps pending edits when suspended and resumed before a response", async () => {
    const { session, responses, changed } = await held();
    session.write(markRequest(pick, range()));
    session.suspend();
    await session.resume();
    expect(get(session).annotations!.annotations!.items).toHaveLength(1);
    await settle();
    responses.shift()!.resolve(answer([item("server-item")], 2));
    await session.whenIdle();
    expect(changed).toHaveBeenCalledTimes(1);
    expect(get(session).saving).toBe(false);
  });

  it("does not replace a newer confirmed document with an old receipt", async () => {
    const { session, responses } = await held();
    session.write(markRequest(pick, range()));
    await settle();
    session.receive({ ...answer([item("server-item"), item("later-item", 5000)], 4), sync: { state: "pending", desired: 4, confirmed: 1 } });
    responses.shift()!.resolve({ ...answer([item("server-item")], 2), sync: { state: "pending", desired: 2, confirmed: 1 } });
    await session.whenIdle();
    expect(get(session).annotations!.annotations!.items).toHaveLength(2);
  });

  it("does not optimistically draw ranges against different audio", async () => {
    const { session, calls } = await held({ ...answer([item("existing")]), resolved: false });
    expect(session.write(markRequest(pick, range()))).toBe(false);
    expect(get(session).error).toContain("can't be placed");
    expect(calls).toEqual([]);
  });
});
