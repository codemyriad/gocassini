import { get } from "svelte/store";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  AnnotationError,
  type AnnotationItem,
  type AnnotationRequest,
  type AnnotationResult,
  type AnnotationsDocument,
  type MeetingAnnotations,
  type VocabularyTag,
} from "../../viewer/annotations";
import {
  createMarksSession,
  markRequest,
  moveRequest,
  PREPARING_RETRY_MS,
  removeRequest,
  stretchRequest,
  untagMeetingRequest,
  viewMarks,
  type MarksState,
} from "./session";

const item = (id: string, tagId: string, target: AnnotationItem["target"]): AnnotationItem => ({
  id,
  tagId,
  target,
  createdAtUtc: "2026-09-11T10:00:00Z",
  actor: { kind: "user", id: "ana" },
  operationId: "op",
});

const doc: AnnotationsDocument = {
  format: "cassini.annotations.v1",
  revision: 3,
  audioOpusSha256: "",
  tagNamespace: "",
  tags: [
    { id: "t-hiring", label: "hiring" },
    { id: "t-budget", label: "budget" },
  ],
  items: [
    item("i1", "t-hiring", { kind: "time-range", startMs: 5000, endMs: 9000 }),
    item("i2", "t-budget", { kind: "time-range", startMs: 1000, endMs: 6000 }),
    item("i3", "t-budget", { kind: "meeting" }),
  ],
};

const meeting = (resolved: boolean | null = true, annotations: AnnotationsDocument | null = doc): MeetingAnnotations => ({
  meetingId: "m1",
  revision: 3,
  annotations,
  resolved,
});

const result = (annotations = doc): AnnotationResult => ({
  ...meeting(true, annotations),
  operationId: "op2",
  added: [],
  removed: [],
  notFound: [],
});

const vocab = (tagId: string, label: string, color: VocabularyTag["color"]): VocabularyTag => ({
  tagId,
  namespace: "",
  label,
  meetings: 1,
  marks: 1,
  color,
  icon: "",
  changedBy: "",
  changedAtUtc: "",
});

describe("the requests a meeting view sends", () => {
  it("marks a stretch with a tag that exists", () => {
    expect(stretchRequest({ tagId: "t-hiring", label: "hiring" }, 1200.4, 5400)).toEqual({
      ops: [
        { op: "mark", tag: { id: "t-hiring", label: "hiring" }, target: { kind: "time-range", startMs: 1200, endMs: 5400 } },
      ],
    });
  });

  it("sends a new tag's colour in the same request", () => {
    expect(stretchRequest({ label: "risk", color: "red", icon: "" }, 0, 900)).toEqual({
      ops: [{ op: "mark", tag: { label: "risk" }, target: { kind: "time-range", startMs: 0, endMs: 900 } }],
      tagStyles: [{ label: "risk", color: "red", icon: "" }],
    });
  });

  it("moves a stretch in one request, so it is never missing between two", () => {
    expect(moveRequest("i1", { id: "t-hiring", label: "hiring" }, 4000, 9500)).toEqual({
      ops: [
        { op: "unmark", itemId: "i1" },
        { op: "mark", tag: { id: "t-hiring", label: "hiring" }, target: { kind: "time-range", startMs: 4000, endMs: 9500 } },
      ],
    });
  });

  it("removes marks by item", () => {
    expect(removeRequest(["i1", "i2"])).toEqual({
      ops: [
        { op: "unmark", itemId: "i1" },
        { op: "unmark", itemId: "i2" },
      ],
    });
  });

  it("tags and untags the whole meeting", () => {
    expect(markRequest({ tagId: "t-budget", label: "budget" }, { kind: "meeting" })).toEqual({
      ops: [{ op: "mark", tag: { id: "t-budget", label: "budget" }, target: { kind: "meeting" } }],
    });
    expect(untagMeetingRequest("t-budget")).toEqual({
      ops: [{ op: "unmark-tag", tagId: "t-budget", target: { kind: "meeting" } }],
    });
  });
});

describe("a meeting's marks session", () => {
  afterEach(() => vi.useRealTimers());

  it("is off without a loader", async () => {
    const session = createMarksSession(() => {});
    await session.open(null, null);
    expect(get(session).status).toBe("off");
    expect(await session.write(removeRequest(["i1"]))).toBe(false);
  });

  it("loads, writes, takes the answer's marks and says the tags changed", async () => {
    const changed: AnnotationResult[] = [];
    const sent: AnnotationRequest[] = [];
    const session = createMarksSession((next) => changed.push(next));
    const answer = result({ ...doc, items: doc.items.slice(1) });
    await session.open(async () => meeting(), async (request) => (sent.push(request), answer));
    expect(get(session).status).toBe("ready");

    expect(await session.write(removeRequest(["i1"]))).toBe(true);
    expect(sent).toEqual([removeRequest(["i1"])]);
    expect(changed).toEqual([answer]);
    expect(get(session).annotations).toBe(answer);
    expect(get(session).busy).toBe(false);
  });

  it("shows a refused write inline and keeps the marks it had", async () => {
    const changed = vi.fn();
    const session = createMarksSession(changed);
    await session.open(async () => meeting(), async () => {
      throw new AnnotationError(409, "unresolved");
    });
    expect(await session.write(removeRequest(["i1"]))).toBe(false);
    expect(get(session)).toMatchObject({ error: "Remove the marks that can't be placed first.", busy: false });
    expect(get(session).annotations?.annotations).toBe(doc);
    expect(changed).not.toHaveBeenCalled();
  });

  it("says tags are being prepared on a 503, and tries again", async () => {
    vi.useFakeTimers();
    const load = vi
      .fn<() => Promise<MeetingAnnotations>>()
      .mockRejectedValueOnce(new AnnotationError(503, ""))
      .mockResolvedValueOnce(meeting());
    const session = createMarksSession(() => {});
    await session.open(load, null);
    expect(get(session)).toMatchObject({ status: "preparing", error: "" });
    await vi.advanceTimersByTimeAsync(PREPARING_RETRY_MS);
    expect(get(session).status).toBe("ready");
  });

  it("ignores an answer for a meeting it has since left", async () => {
    let finish: (value: MeetingAnnotations) => void = () => {};
    const session = createMarksSession(() => {});
    const first = session.open(() => new Promise((resolve) => (finish = resolve)), null);
    await session.open(async () => meeting(true, null), null);
    finish(meeting());
    await first;
    expect(get(session).annotations?.annotations).toBeNull();
  });
});

describe("what the view draws", () => {
  const state = (annotations: MeetingAnnotations, newColors = {}): MarksState => ({
    status: "ready",
    annotations,
    error: "",
    busy: false,
    newColors,
  });

  it("places stretches by time, side by side where they overlap, in the tag's colour", () => {
    const view = viewMarks(state(meeting()), [vocab("t-hiring", "hiring", "teal")]);
    expect(view.placed.map((mark) => [mark.item.id, mark.column, mark.startMs, mark.endMs])).toEqual([
      ["i2", 0, 1000, 6000],
      ["i1", 1, 5000, 9000],
    ]);
    expect(view.placed[1]!.color).toBe("teal");
    expect(view.whole.map((look) => look.tag.label)).toEqual(["budget"]);
    expect(view.lost).toEqual([]);
  });

  it("uses a new tag's chosen colour until the vocabulary has it", () => {
    const view = viewMarks(state(meeting(), { hiring: "pink" }), []);
    expect(view.placed.find((mark) => mark.tag.label === "hiring")!.color).toBe("pink");
  });

  it("never places marks made against other audio, and keeps the whole-meeting tags", () => {
    const view = viewMarks(state(meeting(false)), []);
    expect(view.placed).toEqual([]);
    expect(view.lost.map((lost) => lost.id).sort()).toEqual(["i1", "i2"]);
    expect(view.whole).toHaveLength(1);
  });

  it("draws nothing for a meeting with no marks yet", () => {
    expect(viewMarks(state(meeting(null, null)), [])).toEqual({ whole: [], placed: [], lost: [] });
  });
});
