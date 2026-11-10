import { describe, expect, it } from "vitest";
import { get } from "svelte/store";

import { durationFormat, LL } from "../i18n";

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
      get(LL).viewer.meeting.speaker_edits.reason_no_source_audio(),
    );
    // A rerun that was never published: who can fix it, and where.
    expect(describeSpeakerEditsError(new SpeakerEditsError(409, "unavailable", { reason: "unpublished-rebuild" }))).toMatch(
      /not published\. An administrator can run it again from Cassini Admin\.$/,
    );
    expect(describeSpeakerEditsError(new SpeakerEditsError(409, "unavailable", { reason: "later" }))).toMatch(/cannot be changed/);
    // Too many saves: when to try again, in whole minutes, never "0 min".
    const limited = (retryAfterMs?: number) => describeSpeakerEditsError(new SpeakerEditsError(429, "rate-limited", { retryAfterMs }));
    const t = get(LL).viewer.meeting.speaker_edits;
    expect(limited(125_000)).toBe(t.error_rate_limited_in({ minutes: "3" }));
    expect(limited(60_000)).toBe(t.error_rate_limited_in({ minutes: "1" }));
    expect(limited(0)).toBe(t.error_rate_limited_in({ minutes: "1" }));
    expect(limited(undefined)).toBe(t.error_rate_limited());
    expect(t.error_rate_limited_in({ minutes: "3" })).toBe("Too many changes in a short time. Try again in 3 min.");
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
    expect(progressNow(queued, 0)).toEqual({ phase: "starting", remainingMs: 75_000, percent: 0 });
    // The wait is not work: none of the estimate is used up until the
    // attempt starts, when the operator counts its elapsed time from zero.
    expect(progressNow({ ...queued, elapsedMs: 4000 }, 900)).toEqual({ phase: "starting", remainingMs: 75_000, percent: 0 });
    expect(progressNow({ ...queued, phase: "updating", elapsedMs: 100 }, 0).remainingMs).toBe(74_900);
    expect(progressNow(queued, QUEUED_GRACE_MS - 41).phase).toBe("starting");
    expect(progressNow(queued, QUEUED_GRACE_MS - 40).phase).toBe("queued");
    expect(progressNow({ ...queued, elapsedMs: 12_000 }, 0).phase).toBe("queued");
    // A running phase is what it is from the first millisecond.
    expect(progressNow({ ...queued, phase: "separating" }, 0).phase).toBe("separating");
  });

  it("says the time left as a rough figure, and almost done once it is up", () => {
    const t = get(LL).viewer.meeting.speaker_edits;
    expect(remainingText(50_000)).toBe(t.time_left({ duration: durationFormat({ seconds: 50 }) }));
    expect(remainingText(46_200)).toBe(t.time_left({ duration: durationFormat({ seconds: 50 }) }));
    expect(remainingText(1)).toBe(t.time_left({ duration: durationFormat({ seconds: 5 }) }));
    expect(remainingText(55_000)).toBe(t.time_left({ duration: durationFormat({ seconds: 55 }) }));
    expect(remainingText(55_001)).toBe(t.time_left({ duration: durationFormat({ minutes: 1 }) }));
    expect(remainingText(89_000)).toBe(t.time_left({ duration: durationFormat({ minutes: 1 }) }));
    expect(remainingText(100_000)).toBe(t.time_left({ duration: durationFormat({ minutes: 2 }) }));
    expect(remainingText(0)).toBe(t.almost_done());
    expect(remainingText(-12_000)).toBe(t.almost_done());
    // The English itself, the same in every runtime.
    expect(remainingText(50_000)).toBe("about 50 secs left");
  });

  it("names each phase", () => {
    const t = get(LL).viewer.meeting.speaker_edits;
    expect(phaseText("starting")).toBe(t.phase_starting());
    expect(phaseText("queued")).toBe(t.phase_queued());
    expect(phaseText("separating")).toBe(t.phase_separating());
    expect(phaseText("updating")).toBe(t.phase_updating());
    expect(t.phase_queued()).toBe("Waiting for other recordings…");
  });
});
