import { describe, expect, it } from "vitest";
import {
  buildWordHighlightIndex,
  createWordHighlighter,
  resolveWordHighlight,
  type PlaybackWord,
} from "./wordHighlight";

describe("exact-word playback", () => {
  it("matches measured spans across unsorted, simultaneous and nested words", () => {
    const words = [
      { id: "long-host", startMs: 900, endMs: 2400 },
      { id: "reply", startMs: 1450, endMs: 1700 },
      { id: "first", startMs: 0, endMs: 350 },
      { id: "second", startMs: 350, endMs: 700 },
      { id: "overlap", startMs: 600, endMs: 1100 },
      { id: "reply-next", startMs: 1700, endMs: 1920 },
      { id: "one-millisecond-overlap", startMs: 699, endMs: 850 },
      { id: "zero-duration", startMs: 1200, endMs: 1200 },
    ];
    const index = buildWordHighlightIndex(words);
    const points = words.flatMap((word) => [
      word.startMs - 1,
      word.startMs,
      word.startMs + 1,
      word.endMs - 1,
      word.endMs,
      word.endMs + 1,
    ]);
    for (const now of [...points, -1, 2800, 1500, 1900, 2500, 0, 620]) {
      const state = resolveWordHighlight(index, now);
      const expected = words
        .filter((word) => word.startMs <= now && now < word.endMs)
        .map((word) => word.id)
        .sort();
      expect([...state.activeIds].sort(), `at ${now} ms`).toEqual(expected);
      expect(state.validFromMs).toBeLessThanOrEqual(now);
      expect(state.validUntilMs).toBeGreaterThan(now);
      const midpoint = Math.min(now + 0.25, (now + state.validUntilMs) / 2);
      expect(
        [...resolveWordHighlight(index, midpoint).activeIds].sort(),
      ).toEqual(expected);
    }
  });

  it("does not invent a highlight in gaps or after the recording", () => {
    const index = buildWordHighlightIndex([
      { id: "one", startMs: 300, endMs: 600 },
      { id: "two", startMs: 900, endMs: 1200 },
      { id: "invalid", startMs: 1300, endMs: Number.NaN },
    ]);
    for (const now of [0, 299, 600, 750, 1200, 9000]) {
      expect(resolveWordHighlight(index, now).activeIds.size).toBe(0);
    }
    expect(resolveWordHighlight(index, 900).activeIds).toEqual(
      new Set(["two"]),
    );
  });

  it("keeps ambiguous interpolation in display order without hiding real source overlaps", () => {
    const index = buildWordHighlightIndex([
      { id: "prev", blockId: "rewritten", order: 0, startMs: 9000, endMs: 9200 },
      { id: "a", blockId: "rewritten", order: 1, startMs: 9000, endMs: 9200, ambiguous: true },
      { id: "b", blockId: "rewritten", order: 2, startMs: 9200, endMs: 9400, ambiguous: true },
      { id: "next", blockId: "rewritten", order: 3, startMs: 9200, endMs: 9400 },
      { id: "source1", blockId: "other", order: 0, startMs: 9000, endMs: 9101 },
      { id: "source2", blockId: "other", order: 1, startMs: 9100, endMs: 9200 },
    ]);
    expect(resolveWordHighlight(index, 9100).activeIds).toEqual(new Set(["prev", "source1", "source2"]));
    expect(resolveWordHighlight(index, 9300).activeIds).toEqual(new Set(["b"]));
  });

  it("respects acoustic eligibility even when display spans extend through silence or rejected references", () => {
    const highlighter = createWordHighlighter();
    highlighter.setWords([{ id: "ghost", blockId: "rejected", startMs: 6000, endMs: 6800 }]);
    const changes: boolean[] = [];
    highlighter.subscribe("ghost", (active) => changes.push(active));
    highlighter.update(6400, new Set());
    expect(changes).toEqual([false]);
    // Acoustic boundaries can change inside one long display token's span.
    highlighter.update(6450, new Set(["rejected"]));
    highlighter.update(6460, new Set());
    expect(changes).toEqual([false, true, false]);
  });

  it("notifies only changed words rather than all words or every animation frame", () => {
    const highlighter = createWordHighlighter();
    const words: PlaybackWord[] = Array.from({ length: 10_000 }, (_, i) => ({
      id: String(i),
      startMs: i * 500,
      endMs: i * 500 + 500,
    }));
    const changes: [string, boolean][] = [];
    for (const word of words)
      highlighter.subscribe(word.id, (active) =>
        changes.push([word.id, active]),
      );
    changes.length = 0;
    highlighter.setWords(words);
    for (let frame = 0; frame < 500; frame++) highlighter.update(frame);
    expect(changes).toEqual([["0", true]]);
    highlighter.update(500);
    expect(changes).toEqual([
      ["0", true],
      ["0", false],
      ["1", true],
    ]);
    for (let frame = 0; frame < 100; frame++) highlighter.update(500);
    expect(changes).toHaveLength(3);
  });

  it("handles backward seeks, transcript replacement and newly mounted duplicate targets", () => {
    const highlighter = createWordHighlighter();
    const words = [
      { id: "first", startMs: 0, endMs: 500 },
      { id: "later", startMs: 1000, endMs: 1500 },
    ];
    const states = new Map<string, boolean>();
    for (const word of words)
      highlighter.subscribe(word.id, (active) => states.set(word.id, active));
    highlighter.setWords(words);
    highlighter.update(1200);
    expect(states.get("later")).toBe(true);
    highlighter.update(20);
    expect(states.get("later")).toBe(false);
    expect(states.get("first")).toBe(true);
    const duplicates: boolean[] = [];
    const unsubscribe = highlighter.subscribe("first", (active) =>
      duplicates.push(active),
    );
    expect(duplicates).toEqual([true]);
    highlighter.setWords([]);
    highlighter.update(20);
    expect([...states.values()]).toEqual([false, false]);
    expect(duplicates).toEqual([true, false]);
    unsubscribe();
    highlighter.setWords(words);
    highlighter.update(20);
    expect(duplicates).toEqual([true, false]);
  });

  it("refreshes timing when a transcript switch reuses the same visible word IDs", () => {
    const highlighter = createWordHighlighter();
    const changes: boolean[] = [];
    highlighter.subscribe("same-id", (active) => changes.push(active));
    highlighter.setWords([{ id: "same-id", startMs: 0, endMs: 500 }]);
    highlighter.update(100);
    highlighter.setWords([{ id: "same-id", startMs: 1000, endMs: 1500 }]);
    highlighter.update(100);
    highlighter.update(1200);
    expect(changes).toEqual([false, true, false, true]);
  });
});
