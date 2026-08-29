import { describe, expect, it } from "vitest";

import {
  containsPlaybackTime,
  getActiveTimedRange,
  getLatestStartingActiveTimedRange,
} from "./timing";

describe("timing helpers", () => {
  it("treats shared boundaries as belonging to the later range", () => {
    const words = [
      { id: "w1", startMs: 1000, endMs: 1200 },
      { id: "w2", startMs: 1200, endMs: 1450 },
      { id: "w3", startMs: 1450, endMs: 1700 },
    ];

    expect(getActiveTimedRange(words, 1199)?.id).toBe("w1");
    expect(getActiveTimedRange(words, 1200)?.id).toBe("w2");
    expect(getActiveTimedRange(words, 1450)?.id).toBe("w3");
  });

  it("goes inactive outside all timed ranges", () => {
    const words = [
      { id: "w1", startMs: 1000, endMs: 1200 },
      { id: "w2", startMs: 1200, endMs: 1450 },
    ];

    expect(getActiveTimedRange(words, 1450)).toBeNull();
    expect(getActiveTimedRange(words, 2000)).toBeNull();
  });

  it("goes inactive across gaps between timed ranges", () => {
    const words = [
      { id: "w1", startMs: 1000, endMs: 1200 },
      { id: "w2", startMs: 1400, endMs: 1450 },
    ];

    expect(getActiveTimedRange(words, 1300)).toBeNull();
  });

  it("supports zero-length point ranges", () => {
    const markers = [{ id: "m1", startMs: 5000, endMs: 5000 }];

    expect(containsPlaybackTime(markers[0], 4999)).toBe(false);
    expect(containsPlaybackTime(markers[0], 5000)).toBe(true);
    expect(containsPlaybackTime(markers[0], 5001)).toBe(false);
  });

  it("prefers the latest-starting range during a nested overlap", () => {
    const blocks = [
      { id: "long-turn", startMs: 1000, endMs: 10_000 },
      { id: "interjection", startMs: 3000, endMs: 3500 },
    ];

    expect(getLatestStartingActiveTimedRange(blocks, 2999)?.id).toBe("long-turn");
    expect(getLatestStartingActiveTimedRange(blocks, 3200)?.id).toBe("interjection");
    expect(getLatestStartingActiveTimedRange(blocks, 3500)?.id).toBe("long-turn");
  });

  it("selects by start time rather than input order", () => {
    const blocks = [
      { id: "interjection", startMs: 3000, endMs: 3500 },
      { id: "long-turn", startMs: 1000, endMs: 10_000 },
    ];

    expect(getLatestStartingActiveTimedRange(blocks, 3200)?.id).toBe("interjection");
  });
});
