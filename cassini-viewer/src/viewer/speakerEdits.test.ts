import { describe, expect, it } from "vitest";

import {
  applyPending,
  describeSpeakerEditsError,
  emptySpeakerEdits,
  labelProblem,
  phaseText,
  progressNow,
  QUEUED_GRACE_MS,
  remainingText,
  sameEdits,
  speakerEditsBody,
  SpeakerEditsError,
  speakerEditsLocked,
  withoutSplit,
  withSplit,
  type SpeakerEditsState,
} from "./speakerEdits";

const current = new Map([
  ["room~1", "Meeting room laptop · Speaker 1"],
  ["room~2", "Meeting room laptop · Speaker 2"],
  ["room~3", "Meeting room laptop · Speaker 3"],
]);

describe("speaker edits documents", () => {
  it("adds a split once and removes it without touching names or merges", () => {
    const named = { ...emptySpeakerEdits(), merges: [{ from: "room~2", into: "room~1" }], labels: [{ speakerId: "room~1", label: "Mira" }] };
    const split = withSplit(named, "room");
    expect(withSplit(split, "room")).toBe(split);
    expect(split.splits).toEqual([{ speakerId: "room" }]);
    // Undoing keeps the names dormant, so separating again brings them back.
    expect(withoutSplit(split, "room")).toEqual({ ...named, splits: [] });
  });

  it("sends only the three lists, never the format or revision the server stamps", () => {
    const doc = { ...withSplit(emptySpeakerEdits(), "room"), revision: 4 };
    expect(speakerEditsBody(doc)).toEqual({ splits: [{ speakerId: "room" }], merges: [], labels: [] });
  });

  it("folds typed names in, ignoring a name typed back unchanged and dropping a cleared one", () => {
    const doc = { ...withSplit(emptySpeakerEdits(), "room"), labels: [{ speakerId: "room~3", label: "Old" }] };
    const next = applyPending(doc, {
      labels: { "room~1": "  Mira ", "room~2": "Meeting room laptop · Speaker 2", "room~3": "" },
      merges: {},
    }, current);
    expect(next.labels).toEqual([{ speakerId: "room~1", label: "Mira" }]);
  });

  it("merges voices of one device, separates a saved merge, and refuses a chain", () => {
    const doc = { ...withSplit(emptySpeakerEdits(), "room"), merges: [{ from: "room~3", into: "room~1" }] };
    expect(applyPending(doc, { labels: {}, merges: { "room~2": "room~1", "room~3": null } }, current).merges).toEqual([
      { from: "room~2", into: "room~1" },
    ]);
    // room~1 is a target; merging it away would chain room~3 → room~1 → room~2.
    expect(applyPending(doc, { labels: {}, merges: { "room~1": "room~2" } }, current).merges).toEqual([
      { from: "room~3", into: "room~1" },
    ]);
    // Never across devices, never into itself.
    expect(applyPending(emptySpeakerEdits(), { labels: {}, merges: { "room~1": "desk~1", "room~2": "room~2" } }, current).merges).toEqual([]);
  });

  it("compares documents by what they ask for", () => {
    const a = { ...emptySpeakerEdits(), splits: [{ speakerId: "a" }, { speakerId: "b" }], revision: 1 };
    const b = { ...emptySpeakerEdits(), splits: [{ speakerId: "b" }, { speakerId: "a" }], revision: 2 };
    expect(sameEdits(a, b)).toBe(true);
    expect(sameEdits(a, withoutSplit(b, "a"))).toBe(false);
  });

  it("checks names by the annotation label rules", () => {
    expect(labelProblem("Mira")).toBe("");
    expect(labelProblem("x".repeat(65))).toMatch(/at most 64/);
    expect(labelProblem("a\u0007b")).toMatch(/control/);
  });

  it("says each refusal in words, and repeats the operator's reason for an invalid document", () => {
    expect(describeSpeakerEditsError(new SpeakerEditsError(409, "busy"))).toMatch(/being updated/);
    expect(describeSpeakerEditsError(new SpeakerEditsError(400, "invalid", { detail: "label too long" }))).toMatch(/label too long$/);
    expect(describeSpeakerEditsError(new SpeakerEditsError(500, ""))).toBe("HTTP 500");
    // Missing voice separation says who can fix it, and where.
    expect(describeSpeakerEditsError(new SpeakerEditsError(503, "diarization-unavailable"))).toMatch(/administrator can download it in Cassini's Settings/);
    // The operator's refusal for a meeting whose participant audio has gone.
    expect(describeSpeakerEditsError(new SpeakerEditsError(409, "unavailable", { reason: "no-source-audio" }))).toBe(
      "Each participant's own audio was not kept for this recording.",
    );
    expect(describeSpeakerEditsError(new SpeakerEditsError(409, "unavailable", { reason: "later" }))).toMatch(/cannot be changed/);
  });

  it("locks every save on a meeting the operator cannot republish, and only new splits without the diarizer", () => {
    const server = (available: boolean, reason: SpeakerEditsState["reason"]): SpeakerEditsState => ({
      available, reason, revision: 1, appliedRevision: 1, state: available ? "idle" : "unavailable", lastError: "",
      doc: emptySpeakerEdits(), participants: [], report: null,
    });
    expect(speakerEditsLocked(server(true, ""))).toBe(false);
    expect(speakerEditsLocked(server(false, "diarization-unavailable"))).toBe(false);
    for (const reason of ["no-job", "no-source-audio", "no-transcript"] as const) {
      expect(speakerEditsLocked(server(false, reason))).toBe(true);
    }
  });
});

describe("an apply's progress", () => {
  it("counts the operator's elapsed time on locally, and never fills the bar before the recording is republished", () => {
    const progress = { phase: "separating" as const, elapsedMs: 10_000, estimatedMs: 70_000 };
    expect(progressNow(progress, 0)).toEqual({ phase: "separating", remainingMs: 60_000, percent: 14 });
    expect(progressNow(progress, 25_000)).toEqual({ phase: "separating", remainingMs: 35_000, percent: 50 });
    // Past the estimate the time left goes negative and the bar holds.
    expect(progressNow(progress, 90_000)).toEqual({ phase: "separating", remainingMs: -30_000, percent: 95 });
    // A clock that went backwards takes nothing off what the operator said.
    expect(progressNow(progress, -5000).remainingMs).toBe(60_000);
    expect(progressNow({ phase: "updating", elapsedMs: 0, estimatedMs: 0 }, 0).percent).toBe(95);
  });

  it("calls a just-saved attempt starting, and only one still queued after the grace waiting", () => {
    // The save's own answer: queued a moment ago on an operator with nothing else to do.
    const queued = { phase: "queued" as const, elapsedMs: 40, estimatedMs: 75_000 };
    expect(progressNow(queued, 0)).toEqual({ phase: "starting", remainingMs: 74_960, percent: 0 });
    expect(progressNow(queued, QUEUED_GRACE_MS - 41).phase).toBe("starting");
    expect(progressNow(queued, QUEUED_GRACE_MS - 40).phase).toBe("queued");
    expect(progressNow({ ...queued, elapsedMs: 12_000 }, 0).phase).toBe("queued");
    // A running phase is what it is from the first millisecond.
    expect(progressNow({ ...queued, phase: "separating" }, 0).phase).toBe("separating");
  });

  it("says the time left as a rough figure, and almost done once it is up", () => {
    expect(remainingText(50_000)).toBe("about 50 s left");
    expect(remainingText(46_200)).toBe("about 50 s left");
    expect(remainingText(1)).toBe("about 5 s left");
    expect(remainingText(55_000)).toBe("about 55 s left");
    expect(remainingText(55_001)).toBe("about 1 min left");
    expect(remainingText(89_000)).toBe("about 1 min left");
    expect(remainingText(100_000)).toBe("about 2 min left");
    expect(remainingText(0)).toBe("almost done");
    expect(remainingText(-12_000)).toBe("almost done");
  });

  it("names each phase", () => {
    expect(phaseText("starting")).toBe("Starting…");
    expect(phaseText("queued")).toBe("Waiting for other recordings…");
    expect(phaseText("separating")).toBe("Separating voices…");
    expect(phaseText("updating")).toBe("Updating the recording…");
  });
});
