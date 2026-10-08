import { get } from "svelte/store";
import { describe, expect, it, vi } from "vitest";
import { createWriteQueue } from "../../viewer/listTags";
import { AnnotationError, markRequest, WHOLE_MEETING, type AnnotationRequest, type AnnotationResult } from "../../viewer/annotations";
import { createMeetingMarksSessions } from "./sessions";

const answer = (meetingId = "m1", revision = 1): AnnotationResult => ({
  meetingId, revision, stateToken: `epoch:${revision}`, resolved: true,
  annotations: null, operationId: "op", added: [], removed: [], notFound: [],
});
const settle = async () => { for (let n = 0; n < 30; n++) await Promise.resolve(); };
const request = markRequest({ label: "Focus", color: "red", icon: "" }, WHOLE_MEETING);

describe("meeting annotation ownership in the shell", () => {
  it("retains a session through closing, another meeting, and reopening before its response", async () => {
    const changed = vi.fn();
    const drained = vi.fn();
    const registry = createMeetingMarksSessions(createWriteQueue(drained), changed);
    let finish!: (value: AnnotationResult) => void;
    const load = vi.fn(async () => answer());
    const apply = vi.fn(() => new Promise<AnnotationResult>((resolve) => { finish = resolve; }));
    const first = registry.activate("m1", "First", load, apply)!;
    await settle();
    first.write(request);
    registry.activate("", "", null, null);
    const other = registry.activate("m2", "Second", async () => answer("m2"), null)!;
    await settle();
    expect(get(other).annotations?.meetingId).toBe("m2");
    const reopened = registry.activate("m1", "First", load, apply)!;
    expect(reopened).toBe(first);
    expect(get(reopened).annotations?.annotations?.items).toHaveLength(1);
    expect(load).toHaveBeenCalledTimes(1);
    finish(answer("m1", 2));
    await first.whenIdle();
    expect(changed).toHaveBeenCalledTimes(1);
    expect(drained).toHaveBeenCalledTimes(1);
    expect(get(reopened).saving).toBe(false);
    registry.stop();
  });

  it("orders overlay writes with list/bulk writes and compiles against their latest token", async () => {
    const drained = vi.fn();
    const schedule = createWriteQueue(drained);
    const registry = createMeetingMarksSessions(schedule, () => {});
    const order: string[] = [];
    const apply = vi.fn(async (sent: AnnotationRequest) => {
      order.push("overlay");
      expect(sent.stateToken).toBe("epoch:2");
      return answer("m1", 3);
    });
    const session = registry.activate("m1", "First", async () => answer(), apply)!;
    await settle();
    let finish!: () => void;
    const bulk = schedule(async () => {
      order.push("bulk");
      await new Promise<void>((resolve) => { finish = resolve; });
      registry.receive(answer("m1", 2));
    });
    session.write(request);
    await settle();
    expect(apply).not.toHaveBeenCalled();
    expect(get(session).saving).toBe(true);
    finish();
    await bulk;
    await session.whenIdle();
    expect(order).toEqual(["bulk", "overlay"]);
    expect(drained).toHaveBeenCalledTimes(1);
    registry.stop();
  });

  it("surfaces an annotation failure after the overlay closes and permits dismissal", async () => {
    const registry = createMeetingMarksSessions(createWriteQueue(() => {}), () => {});
    const session = registry.activate("m1", "First", async () => answer(), async () => {
      throw new AnnotationError(400, "range is invalid");
    })!;
    await settle();
    session.write(request, "stretch");
    registry.activate("", "", null, null);
    await session.whenIdle();
    expect(get(registry)).toEqual([{ meetingId: "m1", title: "First", message: "range is invalid", retryable: false }]);
    registry.dismiss("m1");
    expect(get(registry)).toEqual([]);
    registry.stop();
  });
});
