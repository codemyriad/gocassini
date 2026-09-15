import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { get } from "svelte/store";

import { AnnotationError, type TagJob, type VocabularyTag } from "./annotations";
import {
  BUSY,
  changedLine,
  confirmLine,
  countsLine,
  createJobTracker,
  jobStatus,
  relativeTime,
  tagUpdate,
  type TagAction,
} from "./tagManager";

const NOW = Date.parse("2026-09-11T12:00:00Z");
const ago = (ms: number) => new Date(NOW - ms).toISOString();
const DAY = 864e5;

function tag(over: Partial<VocabularyTag> = {}): VocabularyTag {
  return {
    tagId: "tag_b",
    namespace: "ns",
    label: "budget",
    meetings: 9,
    marks: 14,
    color: "",
    icon: "",
    changedBy: "",
    changedAtUtc: "",
    ...over,
  };
}

function job(over: Partial<TagJob> = {}): TagJob {
  return {
    id: "job_1",
    kind: "delete",
    tagId: "tag_b",
    state: "running",
    total: 9,
    done: 0,
    failed: [],
    actor: "chris",
    startedAtUtc: ago(0),
    ...over,
  };
}

function fakeProvider() {
  return {
    updateTag: vi.fn(async (_tagId: string, _update: object) => ({ tag: tag(), job: null as TagJob | null })),
    mergeTag: vi.fn(async (_tagId: string, _into: string) => job({ kind: "merge", into: "tag_c" })),
    deleteTag: vi.fn(async (_tagId: string) => job()),
    loadTagJob: vi.fn(async () => null as TagJob | null),
  };
}

describe("row lines", () => {
  it("counts meetings and marks", () => {
    expect(countsLine(tag())).toBe("9 meetings · 14 marks");
    expect(countsLine(tag({ meetings: 1, marks: 0 }))).toBe("1 meeting · 0 marks");
  });

  it("says who changed a tag last, and when, only once someone has", () => {
    expect(changedLine(tag(), NOW)).toBe("");
    expect(changedLine(tag({ changedBy: "Priya", changedAtUtc: ago(2 * DAY) }), NOW)).toBe("Changed by Priya · 2 days ago");
  });

  it("tells time relative to now", () => {
    expect(relativeTime(ago(30e3), NOW)).toBe("just now");
    expect(relativeTime(ago(5 * 6e4), NOW)).toBe("5 minutes ago");
    expect(relativeTime(ago(3 * 36e5), NOW)).toBe("3 hours ago");
    expect(relativeTime(ago(1.5 * DAY), NOW)).toBe("yesterday");
    expect(relativeTime(ago(15 * DAY), NOW)).toBe("2 weeks ago");
    expect(relativeTime(ago(-60e3), NOW)).toBe("just now");
  });

  it("confirms with one line about the caller's own meetings", () => {
    expect(confirmLine(9)).toBe("Updates 9 of your meetings. Meetings in rooms you can't open keep it.");
    expect(confirmLine(1)).toMatch(/^Updates one of your meetings\./);
    expect(confirmLine(0)).toBe("None of your meetings use it.");
  });
});

describe("tagUpdate", () => {
  it("sends only what the editor changed", () => {
    const draft = { label: "budget", color: "slate", icon: "" } as const;
    const teal = tag({ color: "teal" });
    expect(tagUpdate(teal, { ...draft, color: "teal" })).toBeNull();
    expect(tagUpdate(teal, { ...draft, label: " budgets ", color: "teal" })).toEqual({ label: "budgets" });
    expect(tagUpdate(teal, { ...draft, color: "red" })).toEqual({ color: "red" });
    expect(tagUpdate(tag({ icon: "star", color: "red" }), { ...draft, color: "red" })).toEqual({ icon: "" });
  });
});

describe("jobStatus", () => {
  const state = (over: Partial<TagJob>, action: TagAction | null = null, lost = false) => ({
    job: job(over),
    action,
    lost,
    notice: "",
  });

  it("belongs to the job's own tag only", () => {
    expect(jobStatus(state({ done: 3 }), "tag_b")?.text).toBe("Updating 3 of 9…");
    expect(jobStatus(state({ done: 3 }), "tag_other")).toBeNull();
    expect(jobStatus(state({ state: "finished", done: 9 }), "tag_b")).toBeNull();
  });

  it("runs a merge or delete again from the job, but a rename only from the action that started it", () => {
    const interrupted = { state: "interrupted" as const };
    expect(jobStatus(state({ ...interrupted, kind: "merge", into: "tag_c" }), "tag_b")?.rerun).toEqual({
      kind: "merge",
      tagId: "tag_b",
      into: "tag_c",
    });
    expect(jobStatus(state({ ...interrupted, kind: "rename" }), "tag_b")?.rerun).toBeNull();
    const rename: TagAction = { kind: "update", tagId: "tag_b", update: { label: "budgets" } };
    expect(jobStatus(state({ ...interrupted, kind: "rename" }, rename), "tag_b")?.rerun).toBe(rename);
  });
});

describe("createJobTracker", () => {
  let provider: ReturnType<typeof fakeProvider>;
  let changed: ReturnType<typeof vi.fn>;
  let tracker: ReturnType<typeof createJobTracker>;

  beforeEach(() => {
    vi.useFakeTimers();
    provider = fakeProvider();
    changed = vi.fn();
    tracker = createJobTracker(provider, changed);
  });

  afterEach(() => {
    tracker.stop();
    vi.useRealTimers();
  });

  it("sends each action to its route", async () => {
    await tracker.run({ kind: "update", tagId: "tag_b", update: { color: "red", icon: "star" } });
    await tracker.run({ kind: "merge", tagId: "tag_b", into: "tag_c" });
    await tracker.run({ kind: "delete", tagId: "tag_b" });
    expect(provider.updateTag).toHaveBeenCalledWith("tag_b", { color: "red", icon: "star" });
    expect(provider.mergeTag).toHaveBeenCalledWith("tag_b", "tag_c");
    expect(provider.deleteTag).toHaveBeenCalledWith("tag_b");
    expect(changed).toHaveBeenCalledTimes(3);
  });

  it("follows a job about once a second until it ends, then says the vocabulary changed", async () => {
    provider.loadTagJob
      .mockResolvedValueOnce(job({ done: 3 }))
      .mockResolvedValueOnce(job({ state: "finished", done: 9 }));
    await tracker.run({ kind: "delete", tagId: "tag_b" });
    expect(jobStatus(get(tracker), "tag_b")?.text).toBe("Updating 0 of 9…");
    expect(changed).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(1000);
    expect(jobStatus(get(tracker), "tag_b")?.text).toBe("Updating 3 of 9…");

    await vi.advanceTimersByTimeAsync(1000);
    expect(jobStatus(get(tracker), "tag_b")).toBeNull();
    expect(changed).toHaveBeenCalledTimes(2);

    await vi.advanceTimersByTimeAsync(5000);
    expect(provider.loadTagJob).toHaveBeenCalledTimes(2);
  });

  it("names the meetings that could not be updated, and runs the same action again", async () => {
    const merge: TagAction = { kind: "merge", tagId: "tag_b", into: "tag_c" };
    const failed = [
      { meeting: "Daily standup — 11 Sept", error: "conflict" },
      { meeting: "Pricing workshop", error: "conflict" },
    ];
    provider.loadTagJob.mockResolvedValueOnce(job({ kind: "merge", into: "tag_c", state: "finished", done: 9, failed }));
    await tracker.run(merge);
    await vi.advanceTimersByTimeAsync(1000);

    const status = jobStatus(get(tracker), "tag_b");
    expect(status).toEqual({
      text: "2 couldn't be updated",
      failed: ["Daily standup — 11 Sept", "Pricing workshop"],
      rerun: merge,
    });
    await tracker.run(status!.rerun!);
    expect(provider.mergeTag).toHaveBeenCalledTimes(2);
    expect(provider.mergeTag).toHaveBeenLastCalledWith("tag_b", "tag_c");
  });

  it("offers to run again when the job vanishes mid-run, as it does when the server restarts", async () => {
    await tracker.run({ kind: "delete", tagId: "tag_b" });
    await vi.advanceTimersByTimeAsync(1000);

    expect(get(tracker).lost).toBe(true);
    expect(jobStatus(get(tracker), "tag_b")).toMatchObject({
      text: "Stopped before it finished.",
      rerun: { kind: "delete", tagId: "tag_b" },
    });
    expect(changed).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(5000);
    expect(provider.loadTagJob).toHaveBeenCalledTimes(1);
  });

  it("offers to run again when the job was interrupted", async () => {
    provider.loadTagJob.mockResolvedValueOnce(job({ state: "interrupted", done: 4 }));
    await tracker.run({ kind: "delete", tagId: "tag_b" });
    await vi.advanceTimersByTimeAsync(1000);
    expect(jobStatus(get(tracker), "tag_b")).toMatchObject({ text: "Stopped before it finished.", rerun: { kind: "delete" } });
  });

  it("hands back the tag that already has the label, without a notice", async () => {
    provider.updateTag.mockRejectedValueOnce(new AnnotationError(409, "label-exists", "tag_c"));
    const error = await tracker.run({ kind: "update", tagId: "tag_b", update: { label: "budgets" } });
    expect(error).toMatchObject({ code: "label-exists", tagId: "tag_c" });
    expect(get(tracker).notice).toBe("");
    expect(changed).not.toHaveBeenCalled();
  });

  it("says a change is already running, and shows it", async () => {
    provider.deleteTag.mockRejectedValueOnce(new AnnotationError(409, "busy"));
    provider.loadTagJob.mockResolvedValueOnce(job({ id: "job_0", tagId: "tag_h", done: 2 }));
    await tracker.run({ kind: "delete", tagId: "tag_b" });
    await vi.advanceTimersByTimeAsync(0);
    expect(get(tracker).notice).toBe(BUSY);
    expect(jobStatus(get(tracker), "tag_h")?.text).toBe("Updating 2 of 9…");
  });

  it("on open, picks up a job still running and leaves a finished one alone", async () => {
    provider.loadTagJob.mockResolvedValueOnce(job({ state: "finished" }));
    await tracker.refresh();
    expect(get(tracker).job).toBeNull();

    provider.loadTagJob.mockResolvedValueOnce(job({ done: 5 })).mockResolvedValueOnce(job({ state: "finished" }));
    await tracker.refresh();
    expect(jobStatus(get(tracker), "tag_b")?.text).toBe("Updating 5 of 9…");
    await vi.advanceTimersByTimeAsync(1000);
    expect(changed).toHaveBeenCalledTimes(1);
  });

  it("keeps following a running job through a colour change", async () => {
    await tracker.run({ kind: "delete", tagId: "tag_b" });
    await tracker.run({ kind: "update", tagId: "tag_h", update: { color: "red" } });
    expect(get(tracker).job?.id).toBe("job_1");
    provider.loadTagJob.mockResolvedValueOnce(job({ done: 1 }));
    await vi.advanceTimersByTimeAsync(1000);
    expect(provider.loadTagJob).toHaveBeenCalledTimes(1);
  });
});
