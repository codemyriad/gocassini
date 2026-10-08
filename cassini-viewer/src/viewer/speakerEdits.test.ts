import { describe, expect, it } from "vitest";

import {
  applyPending,
  describeSpeakerEditsError,
  emptySpeakerEdits,
  labelProblem,
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
