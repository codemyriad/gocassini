import { afterEach, describe, expect, it, vi } from "vitest";

import {
  AnnotationError,
  retryDelay,
  tagsByMeeting,
  type MeetingAnnotations,
  type TagVocabulary,
  type VocabularyTag,
} from "./annotations";
import { filterMeetingCatalogEntries, type MeetingCatalogEntry } from "./catalog";
import {
  applyEach,
  bulkReport,
  createTagLoader,
  createWriteQueue,
  filterByTags,
  planBulkTag,
  wholeTagState,
  withMeetingResult,
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

describe("whole-meeting tags", () => {
  it("ticks the tags on every selected meeting and half-ticks the ones on some", () => {
    expect(wholeTagState(byMeeting, ["m1", "m3"])).toEqual({ selected: ["t_h"], mixed: ["t_b"] });
  });

  it("takes a meeting's tags from a write's answer, before the vocabulary reloads", () => {
    const answer: MeetingAnnotations = {
      meetingId: "m1",
      revision: 4,
      resolved: true,
      annotations: {
        format: "cassini.annotations.v1",
        revision: 4,
        audioOpusSha256: "",
        tagNamespace: "ns",
        tags: [
          { id: "t_h", label: "hiring" },
          { id: "t_b", label: "budget" },
        ],
        items: [
          { id: "i1", tagId: "t_b", target: { kind: "meeting" }, createdAtUtc: "", actor: { kind: "user", id: "ana" }, operationId: "op" },
        ],
      },
    };
    const next = tagsByMeeting(withMeetingResult(vocabulary, answer));
    expect(next.get("m1")?.map(({ tag, whole }) => [tag.tagId, whole])).toEqual([["t_b", true]]);
    expect(next.get("m3")).toEqual(byMeeting.get("m3"));
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

describe("writing tags", () => {
  const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

  it("queues a pick made while a write is in flight, keeps going past a failure, and reloads once", async () => {
    const drained = vi.fn();
    const enqueue = createWriteQueue(drained);
    const order: string[] = [];
    let finishFirst!: () => void;
    void enqueue(() => new Promise<void>((resolve) => (order.push("first"), (finishFirst = resolve))));
    void enqueue(async () => {
      order.push("second");
      throw new Error("busy");
    });
    const last = enqueue(async () => void order.push("third"));
    await settle();
    expect(order).toEqual(["first"]);
    finishFirst();
    await last;
    expect(order).toEqual(["first", "second", "third"]);
    expect(drained).toHaveBeenCalledTimes(1);
  });
});

describe("loading the vocabulary", () => {
  afterEach(() => {
    vi.useRealTimers();
  });
  const settle = () => new Promise((resolve) => setTimeout(resolve, 0));
  const deferred = () => {
    let resolve!: (value: TagVocabulary) => void;
    const promise = new Promise<TagVocabulary>((done) => (resolve = done));
    return { promise, resolve };
  };

  it("sends one request at a time, and one more after a write so the answer cannot predate it", async () => {
    const first = deferred();
    const load = vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(vocabulary);
    const loaded = vi.fn();
    const loader = createTagLoader(load, loaded);
    void loader.reload();
    void loader.reload();
    expect(load).toHaveBeenCalledTimes(1);
    void loader.reload(true);
    first.resolve(vocabulary);
    await settle();
    expect(load).toHaveBeenCalledTimes(2);
    expect(loaded).toHaveBeenCalledTimes(2);
  });

  it("retries while the index builds or the budget is spent, backing off, and gives up on anything else", async () => {
    expect([0, 1, 2, 10].map((attempt) => retryDelay(new AnnotationError(503, ""), attempt))).toEqual([
      2_000, 4_000, 8_000, 60_000,
    ]);
    expect(retryDelay(new AnnotationError(429, ""), 0)).toBe(2_000);
    expect(retryDelay(new AnnotationError(502, ""), 0)).toBeNull();
    expect(retryDelay(new Error("offline"), 0)).toBeNull();

    vi.useFakeTimers();
    const load = vi.fn().mockRejectedValueOnce(new AnnotationError(503, "")).mockResolvedValue(vocabulary);
    const loaded = vi.fn();
    const loader = createTagLoader(load, loaded);
    await loader.reload();
    expect(loaded).toHaveBeenLastCalledWith(null);
    await vi.advanceTimersByTimeAsync(2_000);
    expect(load).toHaveBeenCalledTimes(2);
    expect(loaded).toHaveBeenLastCalledWith(vocabulary);

    const stopped = createTagLoader(vi.fn().mockRejectedValue(new AnnotationError(503, "")), vi.fn());
    await stopped.reload();
    stopped.stop();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(vi.getTimerCount()).toBe(0);
  });
});
