import { describe, expect, it } from "vitest";
import type { MeetingCatalogEntry } from "./catalog";
import {
  EMPTY_SELECTION,
  acknowledgeDropped,
  clearSelection,
  countHiddenByView,
  describeSelectionGaps,
  formatSelectionWordCount,
  formatWordCount,
  isSelected,
  reconcileSelection,
  selectedEntries,
  shouldShowSelectionBar,
  summarizeSelection,
  toggleSelected,
  type MeetingSelection,
} from "./selectionModel";

function meeting(
  overrides: Partial<MeetingCatalogEntry> & { id: string },
): MeetingCatalogEntry {
  return {
    title: `Meeting ${overrides.id}`,
    dateLabel: "2026-08-18 14:30",
    audioPath: `${overrides.id}.opus`,
    ...overrides,
  };
}

function selectionOf(...ids: string[]): MeetingSelection {
  return ids.reduce(toggleSelected, EMPTY_SELECTION);
}

describe("toggleSelected", () => {
  it("adds in pick order, because the bundle prints in the order you named", () => {
    expect(selectionOf("c", "a", "b").ids).toEqual(["c", "a", "b"]);
  });

  it("removes without disturbing the order of the rest", () => {
    expect(toggleSelected(selectionOf("c", "a", "b"), "a").ids).toEqual(["c", "b"]);
  });

  it("re-adding puts the meeting back at the end", () => {
    const once = toggleSelected(selectionOf("c", "a", "b"), "c");
    expect(toggleSelected(once, "c").ids).toEqual(["a", "b", "c"]);
  });

  it("never leaves the same meeting in twice", () => {
    // The endpoint answers a duplicate id with 400, so a selection that could
    // hold one would build a request that cannot succeed.
    const twice = toggleSelected(toggleSelected(selectionOf("a"), "a"), "a");
    expect(twice.ids).toEqual(["a"]);
  });

  it("does not touch the picks the shell already reported as lost", () => {
    const withLoss: MeetingSelection = { ids: ["a"], dropped: ["gone"] };
    expect(toggleSelected(withLoss, "b").dropped).toEqual(["gone"]);
  });

  it("expires a loss notice when the selection it belonged to is gone", () => {
    // Every pick was dropped, the bar reported it, and the user picked
    // something else instead. That is a new selection: carrying the notice into
    // it would tell them a meeting they just picked had left the archive.
    const abandoned: MeetingSelection = { ids: [], dropped: ["gone"] };
    expect(toggleSelected(abandoned, "b")).toEqual({ ids: ["b"], dropped: [] });
  });
});

describe("shouldShowSelectionBar", () => {
  // The bar is the only thing that renders the loss notice, so the case where
  // every pick was dropped — a deleted meeting, or a per-caller catalog that
  // momentarily failed closed to empty — has to keep it on screen. Gated on the
  // picks alone, the checkboxes would simply clear and nothing would be said.
  it("stays up for a loss that emptied the selection", () => {
    expect(shouldShowSelectionBar({ ids: [], dropped: ["gone"] })).toBe(true);
  });

  it("is up while anything is picked", () => {
    expect(shouldShowSelectionBar(selectionOf("a"))).toBe(true);
  });

  it("is down when there is neither a pick nor a loss to report", () => {
    expect(shouldShowSelectionBar(EMPTY_SELECTION)).toBe(false);
    expect(shouldShowSelectionBar(acknowledgeDropped({ ids: [], dropped: ["gone"] }))).toBe(false);
  });
});

describe("isSelected / clearSelection", () => {
  it("reports membership", () => {
    const selection = selectionOf("a", "b");
    expect(isSelected(selection, "b")).toBe(true);
    expect(isSelected(selection, "z")).toBe(false);
  });

  it("clearing drops the loss notice along with the picks", () => {
    expect(clearSelection()).toEqual(EMPTY_SELECTION);
  });
});

describe("reconcileSelection", () => {
  const catalog = [meeting({ id: "a" }), meeting({ id: "b" })];

  it("returns the same object when every pick is still in the catalog", () => {
    // Identity matters: the shell runs this against every catalog it observes,
    // four times a minute in the embedded viewer.
    const selection = selectionOf("a", "b");
    expect(reconcileSelection(selection, catalog)).toBe(selection);
  });

  it("returns the same object when nothing is picked", () => {
    expect(reconcileSelection(EMPTY_SELECTION, catalog)).toBe(EMPTY_SELECTION);
  });

  it("drops a pick the catalog no longer contains and records it", () => {
    const next = reconcileSelection(selectionOf("a", "gone", "b"), catalog);
    expect(next.ids).toEqual(["a", "b"]);
    expect(next.dropped).toEqual(["gone"]);
  });

  it("keeps an earlier loss visible when a later refresh drops nothing", () => {
    const afterLoss = reconcileSelection(selectionOf("a", "gone"), catalog);
    const afterQuietRefresh = reconcileSelection(afterLoss, catalog);
    expect(afterQuietRefresh.dropped).toEqual(["gone"]);
  });

  it("does not record the same loss twice", () => {
    const first = reconcileSelection(selectionOf("gone"), catalog);
    const again = reconcileSelection({ ids: ["gone"], dropped: first.dropped }, catalog);
    expect(again.dropped).toEqual(["gone"]);
  });

  it("empties the selection when the catalog empties, and says so", () => {
    // An archive that really is empty must not leave a bar claiming meetings
    // the bundle endpoint would answer with 404.
    const next = reconcileSelection(selectionOf("a", "b"), []);
    expect(next.ids).toEqual([]);
    expect(next.dropped).toEqual(["a", "b"]);
  });
});

describe("acknowledgeDropped", () => {
  it("clears the notice and keeps the picks", () => {
    const next = acknowledgeDropped({ ids: ["a"], dropped: ["gone"] });
    expect(next).toEqual({ ids: ["a"], dropped: [] });
  });

  it("returns the same object when there is nothing to acknowledge", () => {
    const selection = selectionOf("a");
    expect(acknowledgeDropped(selection)).toBe(selection);
  });
});

describe("selectedEntries", () => {
  const catalog = [meeting({ id: "a" }), meeting({ id: "b" }), meeting({ id: "c" })];

  it("resolves in pick order, not catalog order", () => {
    expect(selectedEntries(selectionOf("c", "a"), catalog).map((entry) => entry.id)).toEqual([
      "c",
      "a",
    ]);
  });

  it("skips an id the catalog does not describe rather than inventing a row", () => {
    expect(selectedEntries(selectionOf("a", "ghost"), catalog).map((entry) => entry.id)).toEqual([
      "a",
    ]);
  });
});

describe("countHiddenByView", () => {
  const roomA = [meeting({ id: "a1", roomId: "r1" }), meeting({ id: "a2", roomId: "r1" })];
  const roomB = [meeting({ id: "b1", roomId: "r2" })];

  it("counts the picks the current narrowing does not show", () => {
    // Selection spans rooms: picking one in each room and then filtering to one
    // room must not read as a bundle of one.
    expect(countHiddenByView(selectionOf("a1", "b1"), roomA)).toBe(1);
  });

  it("is zero when the list shows everything picked", () => {
    expect(countHiddenByView(selectionOf("a1", "a2"), [...roomA, ...roomB])).toBe(0);
  });

  it("is zero when nothing is picked", () => {
    expect(countHiddenByView(EMPTY_SELECTION, [])).toBe(0);
  });
});

describe("summarizeSelection", () => {
  it("separates 'no summary' from 'does not say'", () => {
    const totals = summarizeSelection([
      meeting({ id: "a", hasSummary: true }),
      meeting({ id: "b", hasSummary: false }),
      meeting({ id: "c" }),
    ]);
    expect(totals.withoutSummary).toBe(1);
    expect(totals.summaryUnknown).toBe(1);
  });

  it("sums only the word counts it has, and counts the ones it does not", () => {
    const totals = summarizeSelection([
      meeting({ id: "a", wordCount: 1200 }),
      meeting({ id: "b", wordCount: 800 }),
      meeting({ id: "c" }),
    ]);
    expect(totals.wordCount).toBe(2000);
    expect(totals.meetingsWithWordCount).toBe(2);
    expect(totals.meetingsWithoutWordCount).toBe(1);
  });

  it("treats a zero word count as a measurement, not as an absence", () => {
    const totals = summarizeSelection([meeting({ id: "a", wordCount: 0 })]);
    expect(totals.meetingsWithWordCount).toBe(1);
    expect(totals.meetingsWithoutWordCount).toBe(0);
  });

  it("counts entries with no single-file recording", () => {
    const totals = summarizeSelection([
      meeting({ id: "a" }),
      meeting({ id: "b", audioPath: undefined, artifactPath: "./meetings/b" }),
    ]);
    expect(totals.withoutPortableAudio).toBe(1);
  });
});

describe("formatWordCount / formatSelectionWordCount", () => {
  it("groups thousands the same way regardless of the browser locale", () => {
    expect(formatWordCount(12405)).toBe("12,405 words");
  });

  it("says an unmeasured meeting is unmeasured rather than short", () => {
    expect(formatWordCount(undefined)).toBe("Length not recorded");
  });

  it("marks a total that is missing some of its parts as a floor", () => {
    const totals = summarizeSelection([
      meeting({ id: "a", wordCount: 1200 }),
      meeting({ id: "b" }),
    ]);
    expect(formatSelectionWordCount(totals)).toBe("At least 1,200 words");
  });

  it("states a complete total plainly", () => {
    const totals = summarizeSelection([
      meeting({ id: "a", wordCount: 1200 }),
      meeting({ id: "b", wordCount: 800 }),
    ]);
    expect(formatSelectionWordCount(totals)).toBe("2,000 words");
  });

  it("says nothing is known when nothing is", () => {
    expect(formatSelectionWordCount(summarizeSelection([meeting({ id: "a" })]))).toBe(
      "Length not recorded",
    );
  });
});

describe("describeSelectionGaps", () => {
  it("says nothing about a selection with nothing missing", () => {
    const totals = summarizeSelection([
      meeting({ id: "a", hasSummary: true, wordCount: 10 }),
      meeting({ id: "b", hasSummary: true, wordCount: 20 }),
    ]);
    expect(describeSelectionGaps(totals)).toEqual([]);
  });

  it("states a missing summary as missing", () => {
    const totals = summarizeSelection([
      meeting({ id: "a", hasSummary: false, wordCount: 10 }),
      meeting({ id: "b", hasSummary: false, wordCount: 20 }),
    ]);
    expect(describeSelectionGaps(totals)).toEqual([
      "2 of these have no summary. Their transcripts are complete; only the summary section is missing.",
    ]);
  });

  it("never counts an unknown summary as a missing one", () => {
    // The whole point of the third state: an archive published before the index
    // recorded summaries says nothing, and stating a gap that is not there is
    // as wrong as hiding one.
    const totals = summarizeSelection([
      meeting({ id: "a", wordCount: 10 }),
      meeting({ id: "b", wordCount: 20 }),
    ]);
    const gaps = describeSelectionGaps(totals);
    expect(gaps).toEqual([
      "2 of these do not record whether they have a summary. The bundle carries one wherever the recording holds it.",
    ]);
    expect(gaps.join(" ")).not.toContain("have no summary");
  });

  it("reports the two summary states as two separate claims", () => {
    const totals = summarizeSelection([
      meeting({ id: "a", hasSummary: false, wordCount: 10 }),
      meeting({ id: "b", wordCount: 20 }),
      meeting({ id: "c", hasSummary: true, wordCount: 30 }),
    ]);
    expect(describeSelectionGaps(totals)).toEqual([
      "One of these has no summary. Its transcript is complete; only the summary section is missing.",
      "One of these does not record whether it has a summary. The bundle carries one if the recording holds it.",
    ]);
  });

  it("states that a pre-single-file meeting fails the whole bundle, not just itself", () => {
    // It is not a "may": a meeting with no portable recording is not in the
    // operator's readable set, so the request for the SET answers 404 and the
    // reader is left with an error about availability. Hedging it, or scoping
    // it to the one meeting, sends them looking for the wrong thing.
    const totals = summarizeSelection([
      meeting({ id: "a", hasSummary: true, wordCount: 10 }),
      meeting({
        id: "b",
        hasSummary: true,
        wordCount: 20,
        audioPath: undefined,
        artifactPath: "./meetings/b",
      }),
    ]);
    expect(describeSelectionGaps(totals)).toEqual([
      "One of these predates the single-file format — it is marked in the list. The bundle is read from that file, so Prepare will fail for the whole selection until you unpick it.",
    ]);
    expect(describeSelectionGaps({ ...totals, withoutPortableAudio: 2 })[0]).toContain(
      "will fail for the whole selection until you unpick them",
    );
  });

  it("says the total is a floor when part of it is unknown", () => {
    const totals = summarizeSelection([
      meeting({ id: "a", hasSummary: true, wordCount: 10 }),
      meeting({ id: "b", hasSummary: true }),
    ]);
    expect(describeSelectionGaps(totals)).toEqual([
      "One of these does not record its length, so the total is a floor.",
    ]);
  });

  it("does not call a wholly unmeasured total a floor — there is no total to floor", () => {
    const totals = summarizeSelection([meeting({ id: "a", hasSummary: true })]);
    expect(describeSelectionGaps(totals)).toEqual([]);
  });
});
