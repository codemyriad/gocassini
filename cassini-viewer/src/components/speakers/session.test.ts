import { get } from "svelte/store";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { emptySpeakerEdits, SpeakerEditsError, type SpeakerEditsState } from "../../viewer/speakerEdits";
import { createSpeakersSession, recordingBehind, sinceAnswer } from "./session";

function state(overrides: Partial<SpeakerEditsState> = {}): SpeakerEditsState {
  return {
    available: true, reason: "", revision: 1, appliedRevision: 1, state: "idle", lastError: "",
    doc: { ...emptySpeakerEdits(), revision: 1 }, participants: [{ id: "room", label: "Room" }], report: null,
    ...overrides,
  };
}

const labels = new Map([["room~1", "Room · Speaker 1"]]);

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("speakers session", () => {
  it("stays off without both calls, and read-only when the operator will not answer", async () => {
    const session = createSpeakersSession();
    await session.open(async () => state(), null);
    expect(get(session).status).toBe("off");
    const notFound = vi.fn(async (): Promise<SpeakerEditsState> => { throw new SpeakerEditsError(404, "not-found"); });
    await session.open(notFound, async () => state());
    expect(get(session).status).toBe("failed");
    // Not found is final: asking again would change nothing.
    await vi.advanceTimersByTimeAsync(120_000);
    expect(notFound).toHaveBeenCalledTimes(1);
  });

  it("tries a failed first read again, less and less often, until the operator answers", async () => {
    const load = vi.fn(async (): Promise<SpeakerEditsState> => { throw new Error("Failed to fetch"); });
    const session = createSpeakersSession({ pollMs: 1000 });
    await session.open(load, async () => state());
    expect(get(session).status).toBe("failed");

    await vi.advanceTimersByTimeAsync(1000);
    expect(load).toHaveBeenCalledTimes(2);
    // The next wait is twice as long.
    await vi.advanceTimersByTimeAsync(1999);
    expect(load).toHaveBeenCalledTimes(2);
    load.mockResolvedValueOnce(state());
    await vi.advanceTimersByTimeAsync(1);
    expect(load).toHaveBeenCalledTimes(3);
    expect(get(session).status).toBe("ready");

    // Closing stops a retry that was waiting.
    load.mockRejectedValueOnce(new Error("Failed to fetch"));
    await session.open(load, async () => state());
    session.close();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(load).toHaveBeenCalledTimes(4);
  });

  it("sends the split with the revision it was made from, then polls until it is applied", async () => {
    const load = vi.fn(async () => state());
    const save = vi.fn(async () => state({ revision: 2, state: "applying", doc: { ...emptySpeakerEdits(), revision: 2, splits: [{ speakerId: "room" }] } }));
    const session = createSpeakersSession({ pollMs: 3000 });
    session.showing(1);
    await session.open(load, save);
    // The recording was loaded before the session opened; opening keeps it.
    expect(get(session).shownRevision).toBe(1);

    await session.separate("room", labels);
    expect(save).toHaveBeenCalledExactlyOnceWith(1, { splits: [{ speakerId: "room" }], merges: [], labels: [] });
    expect(get(session).server?.state).toBe("applying");

    load.mockResolvedValueOnce(state({ revision: 2, state: "applying" }));
    await vi.advanceTimersByTimeAsync(3000);
    expect(load).toHaveBeenCalledTimes(2);

    load.mockResolvedValueOnce(state({ revision: 2, appliedRevision: 2 }));
    await vi.advanceTimersByTimeAsync(3000);
    expect(load).toHaveBeenCalledTimes(3);
    expect(get(session).server?.appliedRevision).toBe(2);
    // The recording on screen is still the one loaded before the apply.
    expect(recordingBehind(get(session))).toBe(true);

    await vi.advanceTimersByTimeAsync(10_000);
    expect(load).toHaveBeenCalledTimes(3);
    session.showing(2);
    expect(recordingBehind(get(session))).toBe(false);
  });

  it("counts the time since the operator said how far it is, once a second, only while it applies", async () => {
    const progress = { phase: "separating" as const, elapsedMs: 0, estimatedMs: 70_000 };
    const load = vi.fn(async () => state({ revision: 2, state: "applying", progress: { ...progress, elapsedMs: 3000 } }));
    const save = vi.fn(async () => state({ revision: 2, state: "applying", progress }));
    const session = createSpeakersSession({ pollMs: 3000 });
    await session.open(async () => state(), save);
    // Nothing to count while idle.
    expect(vi.getTimerCount()).toBe(0);

    await session.separate("room", labels);
    expect(sinceAnswer(get(session))).toBe(0);
    await vi.advanceTimersByTimeAsync(999);
    expect(sinceAnswer(get(session))).toBe(0);
    await vi.advanceTimersByTimeAsync(1);
    expect(sinceAnswer(get(session))).toBe(1000);
    await vi.advanceTimersByTimeAsync(1000);
    expect(sinceAnswer(get(session))).toBe(2000);

    // A fresh answer brings the operator's own count, and the local one starts again from it.
    await session.open(load, save);
    expect(get(session).server?.progress?.elapsedMs).toBe(3000);
    expect(sinceAnswer(get(session))).toBe(0);
    // One ticker, however many answers arrived.
    await vi.advanceTimersByTimeAsync(3000);
    expect(load).toHaveBeenCalledTimes(2);
    expect(vi.getTimerCount()).toBe(2);

    // Applied: the count stops with the polling.
    load.mockResolvedValueOnce(state({ revision: 2, appliedRevision: 2, progress: null }));
    await vi.advanceTimersByTimeAsync(3000);
    expect(get(session).server?.state).toBe("idle");
    expect(vi.getTimerCount()).toBe(0);
  });

  it("counts nothing for an operator that does not say how far it is", async () => {
    const session = createSpeakersSession();
    await session.open(async () => state(), async () => state({ revision: 2, state: "applying" }));
    await session.separate("room", labels);
    // Only the poll is waiting.
    expect(vi.getTimerCount()).toBe(1);
    session.close();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("tells a recording older than what the operator applied by the edits it was published with", () => {
    const voices = { splits: [{ speakerId: "room", voices: ["room~1", "room~2"], inconclusive: false }], inconclusive: [] };
    const behind = (shownRevision: number | null, server: Partial<SpeakerEditsState>) =>
      recordingBehind({ shownRevision, server: state(server) });
    // A copy read before the apply finished, still cached in the tab: it
    // carries no edits, or older ones.
    expect(behind(0, { appliedRevision: 2, report: voices })).toBe(true);
    expect(behind(1, { appliedRevision: 2, report: voices })).toBe(true);
    expect(behind(2, { appliedRevision: 2, report: voices })).toBe(false);
    // Undoing every split republishes the original speakers, with no record.
    expect(behind(0, { appliedRevision: 3, report: { splits: [], inconclusive: [] } })).toBe(false);
    expect(behind(2, { appliedRevision: 3, report: { splits: [], inconclusive: [] } })).toBe(true);
    // So does a split where only one voice was found.
    const single = { splits: [{ speakerId: "room", voices: [], inconclusive: true }], inconclusive: ["room"] };
    expect(behind(0, { appliedRevision: 1, report: single })).toBe(false);
    // Nothing is said while an apply runs, or before a recording is loaded.
    expect(behind(1, { revision: 3, appliedRevision: 2, state: "applying", report: voices })).toBe(false);
    expect(behind(null, { appliedRevision: 2, report: voices })).toBe(false);
  });

  it("saves every typed name and pick in one request, and clears them once saved", async () => {
    const doc = { ...emptySpeakerEdits(), revision: 3, splits: [{ speakerId: "room" }] };
    const save = vi.fn(async () => state({ revision: 4, state: "applying" }));
    const session = createSpeakersSession();
    await session.open(async () => state({ revision: 3, appliedRevision: 3, doc }), save);
    session.setLabel("room~1", "Mira");
    session.setMerge("room~2", "room~1");

    await session.saveEdits(labels);
    expect(save).toHaveBeenCalledExactlyOnceWith(3, {
      splits: [{ speakerId: "room" }],
      merges: [{ from: "room~2", into: "room~1" }],
      labels: [{ speakerId: "room~1", label: "Mira" }],
    });
    expect(get(session).pending).toEqual({ labels: {}, merges: {} });
    session.close();
  });

  it("retries a failed apply by saving the same document again", async () => {
    const doc = { ...emptySpeakerEdits(), revision: 2, splits: [{ speakerId: "room" }] };
    const save = vi.fn(async () => state({ revision: 3, state: "applying" }));
    const session = createSpeakersSession();
    await session.open(async () => state({ revision: 2, state: "failed", lastError: "boom", doc }), save);
    await session.retry();
    expect(save).toHaveBeenCalledExactlyOnceWith(2, { splits: [{ speakerId: "room" }], merges: [], labels: [] });
    session.close();
  });

  it("keeps the edits and shows what someone else saved after a revision conflict", async () => {
    const load = vi.fn(async () => state());
    const session = createSpeakersSession();
    await session.open(load, async () => { throw new SpeakerEditsError(409, "revision-conflict", { revision: 5 }); });
    load.mockResolvedValueOnce(state({ revision: 5, appliedRevision: 5 }));
    session.setLabel("room~1", "Mira");

    expect(await session.saveEdits(labels)).toBe(false);
    await vi.waitFor(() => expect(get(session).server?.revision).toBe(5));
    expect(get(session).error).toMatch(/Someone else changed/);
    expect(get(session).pending.labels).toEqual({ "room~1": "Mira" });
  });
});
