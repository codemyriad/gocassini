import { describe, expect, it } from "vitest";

import {
  formatPreciseTime,
  nudge,
  railTicks,
  rangeOfSpan,
  rowAt,
  spanForDrag,
  spanForRange,
  stackColumns,
  wordsByTime,
  type TimedWord,
} from "./marking";

// Eight words: two sentences with a 900 ms pause between words 3 and 4, and
// a 500 ms one between 5 and 6.
const words: TimedWord[] = [
  [0, 300],
  [320, 600],
  [620, 900],
  [920, 1200],
  [2100, 2400],
  [2420, 2700],
  [3200, 3500],
  [3520, 3800],
].map(([startMs, endMs], index) => ({ id: `w${index}`, startMs: startMs!, endMs: endMs! }));

describe("words in time order", () => {
  it("keeps only timed parts and sorts them by start, stably", () => {
    expect(
      wordsByTime([
        { id: "b", startMs: 500, endMs: 700 },
        { id: "untimed" },
        { id: "a", startMs: 100, endMs: 300 },
        { id: "c", startMs: 500, endMs: 900 },
      ]).map((word) => word.id),
    ).toEqual(["a", "b", "c"]);
  });
});

describe("a drag on the rail", () => {
  it("follows the pointer from where it started", () => {
    expect(spanForDrag(words, 250, 2500)).toEqual({ from: 0, to: 5 });
    expect(spanForDrag(words, 250, 3000)).toEqual({ from: 0, to: 5 });
    expect(spanForDrag(words, 250, 3300)).toEqual({ from: 0, to: 6 });
    // Word 0 ends at 300, exclusive, so a drag from there starts on word 1.
    expect(spanForDrag(words, 300, 2500)).toEqual({ from: 1, to: 5 });
  });

  it("swaps its ends when it crosses back over the start", () => {
    expect(spanForDrag(words, 2500, 3600)).toEqual({ from: 5, to: 7 });
    expect(spanForDrag(words, 2500, 700)).toEqual({ from: 2, to: 5 });
  });

  it("never collapses near its start, and never snaps to a block", () => {
    for (const pointer of [2500, 2499, 2501, 2450]) {
      const span = spanForDrag(words, 2500, pointer)!;
      expect(span.to - span.from).toBeLessThanOrEqual(1);
      expect(span.from).toBe(5);
    }
    // In a pause, it holds the next word rather than nothing.
    expect(spanForDrag(words, 1500, 1600)).toEqual({ from: 4, to: 4 });
  });

  it("holds the word the pointer started inside", () => {
    expect(spanForDrag(words, 450, 700)).toEqual({ from: 1, to: 2 });
  });

  it("stays inside the meeting past its last word", () => {
    expect(spanForDrag(words, 5000, 6000)).toEqual({ from: 7, to: 7 });
    expect(spanForDrag([], 0, 10)).toBeNull();
  });
});

describe("what is written", () => {
  it("runs from the start of the first word to the end of the last", () => {
    expect(rangeOfSpan(words, { from: 1, to: 4 })).toEqual({ startMs: 320, endMs: 2400 });
  });

  it("is never empty, even for a word with no length", () => {
    expect(rangeOfSpan([{ id: "x", startMs: 50, endMs: 50 }], { from: 0, to: 0 })).toEqual({
      startMs: 50,
      endMs: 51,
    });
  });

  it("selects the same words again when read back", () => {
    const range = rangeOfSpan(words, { from: 2, to: 5 });
    expect(spanForRange(words, range.startMs, range.endMs)).toEqual({ from: 2, to: 5 });
  });

  it("covers nothing when a stored range falls between words", () => {
    expect(spanForRange(words, 1300, 2000)).toBeNull();
  });
});

describe("nudging a marker", () => {
  const span = { from: 2, to: 5, itemId: "kept" };

  it("moves one end by one word", () => {
    expect(nudge(words, span, "from", -1)).toEqual({ from: 1, to: 5, itemId: "kept" });
    expect(nudge(words, span, "to", 1)).toEqual({ from: 2, to: 6, itemId: "kept" });
  });

  it("never crosses the other end or leaves the meeting", () => {
    expect(nudge(words, { from: 3, to: 3 }, "from", 1)).toEqual({ from: 3, to: 3 });
    expect(nudge(words, { from: 3, to: 3 }, "to", -1)).toEqual({ from: 3, to: 3 });
    expect(nudge(words, { from: 0, to: 3 }, "from", -1)).toEqual({ from: 0, to: 3 });
    expect(nudge(words, { from: 0, to: 7 }, "to", 1)).toEqual({ from: 0, to: 7 });
  });

  it("jumps to the next pause with Shift", () => {
    // The start moves to a word that follows a pause.
    expect(nudge(words, { from: 0, to: 7 }, "from", 1, true).from).toBe(4);
    expect(nudge(words, { from: 4, to: 7 }, "from", 1, true).from).toBe(6);
    expect(nudge(words, { from: 6, to: 7 }, "from", -1, true).from).toBe(4);
    expect(nudge(words, { from: 3, to: 7 }, "from", -1, true).from).toBe(0);
    // The end moves to a word a pause follows.
    expect(nudge(words, { from: 0, to: 0 }, "to", 1, true).to).toBe(3);
    expect(nudge(words, { from: 0, to: 3 }, "to", 1, true).to).toBe(5);
    expect(nudge(words, { from: 0, to: 5 }, "to", 1, true).to).toBe(7);
    expect(nudge(words, { from: 0, to: 7 }, "to", -1, true).to).toBe(5);
  });
});

describe("stacking brackets", () => {
  it("puts overlapping ranges side by side and reuses a column once it is free", () => {
    expect(
      stackColumns([
        { startMs: 0, endMs: 100 },
        { startMs: 50, endMs: 200 },
        { startMs: 100, endMs: 150 },
        { startMs: 60, endMs: 70 },
        { startMs: 300, endMs: 400 },
      ]),
    ).toEqual([0, 1, 0, 2, 0]);
  });

  it("gives the longer of two ranges that start together the outer column", () => {
    expect(
      stackColumns([
        { startMs: 0, endMs: 50 },
        { startMs: 0, endMs: 500 },
      ]),
    ).toEqual([1, 0]);
  });
});

describe("the turn under a click", () => {
  const rows = [
    { key: "a", startMs: 0, endMs: 1000 },
    { key: "b", startMs: 3000, endMs: 5000 },
  ];

  it("is the one sounding, or else the nearest", () => {
    expect(rowAt(rows, 500)?.key).toBe("a");
    expect(rowAt(rows, 1900)?.key).toBe("a");
    expect(rowAt(rows, 2100)?.key).toBe("b");
    expect(rowAt([], 0)).toBeUndefined();
  });
});

describe("rail labels", () => {
  it("steps by a size that suits the meeting and always ends on its length", () => {
    expect(railTicks(527_000)).toEqual([0, 120_000, 240_000, 360_000, 527_000]);
    expect(railTicks(30_000)).toEqual([0, 30_000]);
    expect(railTicks(30 * 60_000)).toEqual([0, 5, 10, 15, 20, 25, 30].map((m) => m * 60_000));
    expect(railTicks(0)).toEqual([]);
  });

  it("prints a marker's time to a tenth of a second", () => {
    expect(formatPreciseTime(449_340)).toBe("7:29.3");
    expect(formatPreciseTime(0)).toBe("0:00.0");
  });
});
