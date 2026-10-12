import { get } from "svelte/store";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { emptySpeakerEdits, SpeakerEditsError, type SpeakerEditsState } from "../../viewer/speakerEdits";
import {
  createSpeakersSession,
  recordingBehind,
  reloadDue,
  segmentationBehind,
  sinceAnswer,
  SPEAKER_POLL_MS,
  SPEAKER_RETRY_MS,
  speakerOverlayFor,
} from "./session";

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
    const session = createSpeakersSession({ retryMs: 1000 });
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

  it("keeps a name typed while the save was on its way", async () => {
    let answer!: (value: SpeakerEditsState) => void;
    const save = vi.fn(() => new Promise<SpeakerEditsState>((resolve) => { answer = resolve; }));
    const session = createSpeakersSession();
    await session.open(async () => state(), save);
    session.setLabel("room~1", "Mira");
    session.setLabel("room~2", "Leo");

    const saving = session.saveEdits(labels);
    // Typed after Save was pressed: not in the document sent.
    session.setLabel("room~1", "Mirabel");
    session.setMerge("room~3", "room~2");
    answer(state({ revision: 2, state: "applying" }));
    expect(await saving).toBe(true);

    expect(save.mock.calls[0][1].labels).toEqual([
      { speakerId: "room~1", label: "Mira" },
      { speakerId: "room~2", label: "Leo" },
    ]);
    expect(get(session).pending).toEqual({ labels: { "room~1": "Mirabel" }, merges: { "room~3": "room~2" } });
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

describe("too many changes", () => {
  it("says when to try again and keeps every typed name for then", async () => {
    const save = vi
      .fn()
      .mockRejectedValueOnce(new SpeakerEditsError(429, "rate-limited", { retryAfterMs: 125_000 }))
      .mockResolvedValueOnce(state({ revision: 2, state: "applying" }));
    const session = createSpeakersSession();
    await session.open(async () => state(), save);
    session.setLabel("room~1", "Mira");

    expect(await session.saveEdits(labels)).toBe(false);
    expect(get(session).error).toBe("Too many changes in a short time. Try again in 3 min.");
    expect(get(session).pending.labels).toEqual({ "room~1": "Mira" });

    // Later, the same Save sends them.
    expect(await session.saveEdits(labels)).toBe(true);
    expect(save.mock.calls[1][1].labels).toEqual([{ speakerId: "room~1", label: "Mira" }]);
    expect(get(session).pending.labels).toEqual({});
    session.close();
  });
});

describe("how often the operator is asked", () => {
  it("asks once a second while it applies, and waits longer after a failed first read", async () => {
    expect(SPEAKER_POLL_MS).toBe(1000);
    expect(SPEAKER_RETRY_MS).toBe(3000);
    const load = vi.fn(async (): Promise<SpeakerEditsState> => { throw new Error("Failed to fetch"); });
    const save = vi.fn(async () => state({ revision: 2, appliedRevision: 1, state: "applying" }));
    const session = createSpeakersSession();
    await session.open(load, save);
    await vi.advanceTimersByTimeAsync(2999);
    expect(load).toHaveBeenCalledTimes(1);
    load.mockResolvedValue(state());
    await vi.advanceTimersByTimeAsync(1);
    expect(load).toHaveBeenCalledTimes(2);

    // Idle: nothing to ask.
    await vi.advanceTimersByTimeAsync(10_000);
    expect(load).toHaveBeenCalledTimes(2);

    load.mockResolvedValue(state({ revision: 2, appliedRevision: 1, state: "applying" }));
    await session.saveEdits(labels);
    await vi.advanceTimersByTimeAsync(999);
    expect(load).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(load).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(1000);
    expect(load).toHaveBeenCalledTimes(4);

    // Applied: the polling stops.
    load.mockResolvedValue(state({ revision: 2, appliedRevision: 2 }));
    await vi.advanceTimersByTimeAsync(1000);
    expect(load).toHaveBeenCalledTimes(5);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(load).toHaveBeenCalledTimes(5);
    session.close();
  });
});

describe("saved names shown before the recording has them", () => {
  const named = { ...emptySpeakerEdits(), splits: [{ speakerId: "room" }], labels: [{ speakerId: "room~1", label: "Mira" }] };

  it("shows a save on the recording on screen until a recording with it is loaded", async () => {
    const load = vi.fn(async () => state({ revision: 2, appliedRevision: 1, state: "applying", doc: { ...named, revision: 2 } }));
    const save = vi.fn(async () => state({ revision: 2, appliedRevision: 1, state: "applying", doc: { ...named, revision: 2 } }));
    const session = createSpeakersSession();
    session.showing(1);
    await session.open(async () => state(), save);
    expect(speakerOverlayFor(get(session))).toBeNull();

    session.setLabel("room~1", "Mira");
    await session.saveEdits(labels);
    // At once, from the save's own answer: no poll, no recording read.
    expect(speakerOverlayFor(get(session))?.labels).toEqual([{ speakerId: "room~1", label: "Mira" }]);

    // Applied, and the recording on screen still the one from before.
    load.mockResolvedValue(state({ revision: 2, appliedRevision: 2, doc: { ...named, revision: 2 } }));
    await session.open(load, save);
    expect(speakerOverlayFor(get(session))?.labels).toEqual([{ speakerId: "room~1", label: "Mira" }]);

    // A recording published with revision 2 (or later) has the names itself.
    session.showing(2);
    expect(speakerOverlayFor(get(session))).toBeNull();
    session.showing(3);
    expect(speakerOverlayFor(get(session))).toBeNull();
    // Nothing is shown before a recording is.
    session.showing(null);
    expect(speakerOverlayFor(get(session))).toBeNull();
    session.close();
  });

  it("shows nothing for a meeting whose last apply failed", async () => {
    const renamed = { ...named, labels: [{ speakerId: "room~1", label: "Mira Lind" }] };
    const session = createSpeakersSession();
    session.showing(1);
    // Revision 3 failed; a session opened now knows of nothing applied after
    // the recording on screen, so it shows the recording as it is.
    await session.open(
      async () => state({ revision: 3, appliedRevision: 1, state: "failed", lastError: "boom", doc: { ...renamed, revision: 3 } }),
      async () => state(),
    );
    expect(speakerOverlayFor(get(session))).toBeNull();
    session.close();
  });

  it("drops a failed save even within one session, keeping the last applied names", async () => {
    const renamed = { ...named, labels: [{ speakerId: "room~1", label: "Mira Lind" }] };
    const load = vi.fn(async () => state({ revision: 2, appliedRevision: 2, doc: { ...named, revision: 2 } }));
    const save = vi.fn(async () => state({ revision: 3, appliedRevision: 2, state: "applying", doc: { ...renamed, revision: 3 } }));
    const session = createSpeakersSession({ pollMs: 1000 });
    session.showing(1);
    await session.open(load, save);
    await session.saveEdits(labels);
    expect(speakerOverlayFor(get(session))?.labels[0]?.label).toBe("Mira Lind");

    load.mockResolvedValue(state({ revision: 3, appliedRevision: 2, state: "failed", lastError: "boom", doc: { ...renamed, revision: 3 } }));
    await vi.advanceTimersByTimeAsync(1000);
    expect(get(session).server?.state).toBe("failed");
    expect(speakerOverlayFor(get(session))?.labels[0]?.label).toBe("Mira");

    // With nothing applied since the recording on screen, nothing is shown.
    session.showing(2);
    expect(speakerOverlayFor(get(session))).toBeNull();
    session.close();
  });
});

describe("segmentationBehind", () => {
  const report = (...voices: string[][]) => ({
    splits: voices.map((ids, i) => ({ speakerId: `dev${i}`, voices: ids, inconclusive: ids.length < 2 })),
    inconclusive: voices.flatMap((ids, i) => (ids.length < 2 ? [`dev${i}`] : [])),
  });
  const behind = (shownRevision: number, shown: string[], server: Partial<SpeakerEditsState>) =>
    segmentationBehind({ shownRevision, server: state(server) }, new Set(shown));

  it("needs the republished recording only when a device is split, or made one again, since", () => {
    // Split since: the recording on screen has no voices.
    expect(behind(0, ["dev0"], { appliedRevision: 1, report: report(["dev0~1", "dev0~2"]) })).toBe(true);
    // Names or merges since: the recording on screen has the same voices.
    expect(behind(1, ["dev0~1", "dev0~2"], { revision: 2, appliedRevision: 2, report: report(["dev0~1", "dev0~2"]) })).toBe(false);
    // Made one person again.
    expect(behind(1, ["dev0~1", "dev0~2"], { revision: 2, appliedRevision: 2, report: { splits: [], inconclusive: [] } })).toBe(true);
    // Only one voice found: nothing changed in the recording.
    expect(behind(0, [], { appliedRevision: 1, report: report(["dev0~1"]) })).toBe(false);
    // Up to date, or still applying.
    expect(behind(2, [], { revision: 2, appliedRevision: 2, report: { splits: [], inconclusive: [] } })).toBe(false);
    expect(behind(0, [], { revision: 2, appliedRevision: 1, state: "applying", report: report(["dev0~1", "dev0~2"]) })).toBe(false);
  });
});

describe("reloadDue", () => {
  const shown = new Set(["dev0~1", "dev0~2"]);
  const split = { splits: [{ speakerId: "dev0", voices: ["dev0~1", "dev0~2"], inconclusive: false }], inconclusive: [] };
  const due = (shownRevision: number, server: Partial<SpeakerEditsState>) =>
    reloadDue({ shownRevision, server: state(server) }, shown);

  it("reads the recording again for a summary rewritten after names or merges", () => {
    // A name saved since: the voices are the same, the summary is new.
    expect(due(1, { revision: 2, appliedRevision: 2, report: { ...split, summary: "regenerated" } })).toBe(true);
    // No summary rewritten, or it could not be: the overlay is enough.
    for (const summary of ["unchanged", "stale", "none"] as const) {
      expect(due(1, { revision: 2, appliedRevision: 2, report: { ...split, summary } })).toBe(false);
    }
    // Once a recording with it is on screen, or while still applying.
    expect(due(2, { revision: 2, appliedRevision: 2, report: { ...split, summary: "regenerated" } })).toBe(false);
    expect(due(1, { revision: 2, appliedRevision: 1, state: "applying", report: { ...split, summary: "regenerated" } })).toBe(false);
  });

  // Which summary the operator published, against the one on screen: what
  // the last apply did to it is not enough.
  const summaryDue = (shownRevision: number, shownSummary: string, server: Partial<SpeakerEditsState>) =>
    reloadDue({ shownRevision, shownSummary, server: state(server) }, new Set());
  const none = { splits: [], inconclusive: [] };

  it("reads the recording again when undoing a name puts the build's summary back", () => {
    // A device renamed at revision 1, its summary rewritten and read; the name
    // taken away at 2. The words are cut as before, and the summary is the
    // build's again.
    const restored = { ...none, summary: "restored" as const, summarySha256: "" };
    expect(summaryDue(1, "renamed", { revision: 2, appliedRevision: 2, report: restored })).toBe(true);
    // Once the recording with the build's summary is on screen: nothing.
    expect(summaryDue(0, "", { revision: 2, appliedRevision: 2, report: restored })).toBe(false);
  });

  it("still reads a rewritten summary after a later apply that left it alone", () => {
    // Revision 2 rewrote the summary while the reader listened, so the
    // recording on screen is revision 1's; revision 3 changed nobody's words.
    const unchanged = { ...none, summary: "unchanged" as const, summarySha256: "rewritten-at-2" };
    expect(summaryDue(1, "summary-at-1", { revision: 3, appliedRevision: 3, report: unchanged })).toBe(true);
    expect(summaryDue(2, "rewritten-at-2", { revision: 3, appliedRevision: 3, report: unchanged })).toBe(false);
    // A summary that could not be rewritten is the one already on screen.
    expect(summaryDue(2, "rewritten-at-2", { revision: 3, appliedRevision: 3, report: { ...unchanged, summary: "stale" } })).toBe(false);
  });

  it("keeps the summary on screen with the recording, across opening the session again", async () => {
    const session = createSpeakersSession();
    session.showing(1, "renamed");
    const restored = { ...none, summary: "restored" as const, summarySha256: "" };
    await session.open(async () => state({ revision: 2, appliedRevision: 2, report: restored }), async () => state());
    expect(get(session).shownSummary).toBe("renamed");
    expect(reloadDue(get(session), new Set())).toBe(true);
    session.showing(0);
    expect(reloadDue(get(session), new Set())).toBe(false);
    session.close();
  });
});

describe("segmentationBehind after a merge is taken back", () => {
  const found = {
    splits: [{ speakerId: "dev0", voices: ["dev0~1", "dev0~2", "dev0~3"], inconclusive: false }],
    inconclusive: [],
  };
  const doc = (merges: { from: string; into: string }[]) => ({ ...emptySpeakerEdits(), splits: [{ speakerId: "dev0" }], merges, revision: 3 });
  const behind = (shown: string[], merges: { from: string; into: string }[], extra: Partial<SpeakerEditsState> = {}) =>
    segmentationBehind(
      { shownRevision: 2, server: state({ revision: 3, appliedRevision: 3, report: found, doc: doc(merges), ...extra }) },
      new Set(shown),
    );

  it("needs the republished recording for a voice that is a different person again", () => {
    // On screen voice 3 is merged into voice 1; the published recording has it
    // on its own again, which no overlay can cut back out of voice 1.
    expect(behind(["dev0~1", "dev0~2"], [])).toBe(true);
    // Still merged in the published recording: nothing to read.
    expect(behind(["dev0~1", "dev0~2"], [{ from: "dev0~3", into: "dev0~1" }])).toBe(false);
    // Merged since: the overlay merges it on screen.
    expect(behind(["dev0~1", "dev0~2", "dev0~3"], [{ from: "dev0~3", into: "dev0~1" }])).toBe(false);
    // A merge into a voice with no words is not applied, so the voice stays.
    expect(behind(["dev0~1", "dev0~2"], [{ from: "dev0~3", into: "dev0~9" }])).toBe(true);
  });

  it("does not guess when the document is not the one applied", () => {
    expect(behind(["dev0~1", "dev0~2"], [], { revision: 4 })).toBe(false);
  });
});

describe("the overlay puts default names back", () => {
  it("carries the original roster, so a name taken away is not left on screen", async () => {
    const session = createSpeakersSession();
    session.showing(2);
    await session.open(
      async () => state({ revision: 3, appliedRevision: 3, doc: { ...emptySpeakerEdits(), splits: [{ speakerId: "room" }], revision: 3 } }),
      async () => state(),
    );
    expect(speakerOverlayFor(get(session))).toEqual({ labels: [], merges: [], base: [{ speakerId: "room", label: "Room" }] });
    session.close();
  });
});
