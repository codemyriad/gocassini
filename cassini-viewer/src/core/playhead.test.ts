import { describe, expect, it } from "vitest";

import { getSoundingBlocks, type OverlapBlock } from "./overlap";
import { buildPlayheadIndex, resolvePlayhead } from "./playhead";
import { getActiveTimedRange } from "./timing";

/**
 * The index exists to give the same answers faster, so almost everything here is
 * differential: build the index, sweep the playhead, and assert it agrees with
 * the code it replaces. A test that only checked the index against its own idea
 * of the rules would drift away from overlap.ts the first time D-690 learns
 * something new.
 */

function token(
  text: string,
  startMs: number,
  endMs: number,
  extra: Record<string, unknown> = {},
): Record<string, unknown> {
  return { text, startMs, endMs, ...extra };
}

/**
 * Blocks shaped like the ones the producer really emits, including the awkward
 * ones: a bridged sub-400 ms silence, a wordless block living on its extent, a
 * discredited block with no audible time at all, tokens whose canonical words
 * were rejected, an interpolated run, a zero-length token, and — the case that
 * makes a naive binary search wrong — an interpolated pair spanning both of its
 * anchors, so the block's tokens are neither ascending nor disjoint.
 */
function fixtures(): OverlapBlock[] {
  return [
    {
      id: "ana-open",
      speaker: "ana",
      startMs: 1000,
      endMs: 2400,
      tokens: [
        token("Right,", 1000, 1240),
        token("so", 1240, 1400),
        // 300 ms of silence — under HIGHLIGHT_BRIDGE_MS, so the ring holds.
        token("the", 1700, 1900),
        token("plan.", 1900, 2400),
      ],
      words: [],
    },
    {
      id: "ben-interject",
      speaker: "ben",
      startMs: 1850,
      endMs: 2100,
      tokens: [token("Sure.", 1850, 2100)],
      words: [],
    },
    {
      id: "cara-gap",
      speaker: "cara",
      startMs: 3000,
      endMs: 5000,
      tokens: [
        token("One", 3000, 3200),
        // 800 ms — past the bridge, so the ring genuinely goes out here.
        token("more.", 4000, 5000),
      ],
      words: [],
    },
    {
      id: "wordless-aside",
      speaker: "dana",
      startMs: 5200,
      endMs: 5900,
      tokens: [],
      words: [],
    },
    {
      id: "discredited",
      speaker: "erin",
      startMs: 6000,
      endMs: 6800,
      referencesRejected: true,
      tokens: [token("Ghost.", 6000, 6800, { sourceWordsRejected: true })],
      words: [],
    },
    {
      id: "interpolated-run",
      speaker: "finn",
      startMs: 7000,
      endMs: 8000,
      tokens: [
        token("Anchor", 7000, 7200, { alignment: "source" }),
        token("rewritten", 7200, 7600, { alignment: "interpolated" }),
        token("tail.", 7600, 8000, { alignment: "source" }),
      ],
      words: [],
    },
    {
      id: "non-monotone",
      speaker: "gil",
      startMs: 9000,
      endMs: 9400,
      tokens: [
        // The resolveInterpolatedSpan fallback: the interpolated pair spans both
        // anchors, so array order and time order disagree.
        token("prev", 9000, 9200, { alignment: "source" }),
        token("a", 9000, 9200, { alignment: "interpolated" }),
        token("b", 9200, 9400, { alignment: "interpolated" }),
        token("next", 9200, 9400, { alignment: "source" }),
      ],
      words: [],
    },
    {
      id: "point-token",
      speaker: "hana",
      startMs: 10_000,
      endMs: 10_400,
      tokens: [token("clipped", 10_000, 10_000), token("kept.", 10_100, 10_400)],
      words: [],
    },
    {
      id: "canonical-only",
      speaker: "ivan",
      startMs: 11_000,
      endMs: 11_800,
      tokens: [],
      words: [token("Untimed", 11_000, 11_300), token("display.", 11_300, 11_800)],
    },
  ] as unknown as OverlapBlock[];
}

/**
 * Every instant worth asking about: each boundary in the fixture, the
 * millisecond either side of it, the midpoints between them, and a spread of
 * whole milliseconds across the whole range. Sub-millisecond probes are included
 * on purpose even though the real playhead is written as whole milliseconds —
 * if the window logic is only correct for integers, it should be the fixture
 * that says so, not a coincidence.
 */
function probeTimes(blocks: OverlapBlock[]): number[] {
  const edges = new Set<number>([0]);
  for (const block of blocks) {
    edges.add(block.startMs);
    edges.add(block.endMs);
    for (const pool of [block.tokens ?? [], block.words ?? []]) {
      for (const span of pool) {
        if (span.startMs !== undefined) edges.add(span.startMs);
        if (span.endMs !== undefined) edges.add(span.endMs);
      }
    }
  }
  const probes = new Set<number>();
  const sorted = [...edges].sort((left, right) => left - right);
  for (const edge of sorted) {
    for (const offset of [-401, -400, -399, -2, -1, 0, 1, 2, 399, 400, 401]) {
      probes.add(edge + offset);
    }
    probes.add(edge + 0.5);
  }
  for (let time = 0; time <= 13_000; time += 37) {
    probes.add(time);
  }
  return [...probes].filter((time) => time >= 0).sort((left, right) => left - right);
}

/** What the viewer computed per frame before the index existed. */
function referenceActiveToken(block: OverlapBlock, timeMs: number) {
  const tokens = block.tokens ?? [];
  if (tokens.length === 0) {
    return null;
  }
  return getActiveTimedRange(
    tokens.filter(
      (candidate): candidate is typeof candidate & { startMs: number; endMs: number } =>
        candidate.startMs !== undefined && candidate.endMs !== undefined,
    ),
    timeMs,
  );
}

describe("playhead index", () => {
  it("agrees with getSoundingBlocks at every boundary, by identity and order", () => {
    const blocks = fixtures();
    const index = buildPlayheadIndex(blocks);

    for (const timeMs of probeTimes(blocks)) {
      const expected = getSoundingBlocks(blocks, timeMs);
      const actual = resolvePlayhead(index, timeMs).soundingBlocks;

      expect(actual.length, `count at ${timeMs}ms`).toBe(expected.length);
      for (let position = 0; position < expected.length; position += 1) {
        // toBe, not toEqual: the template identity-compares tokens, so an index
        // that rebuilt or cloned blocks would pass a structural check and still
        // extinguish every highlight.
        expect(actual[position], `block ${position} at ${timeMs}ms`).toBe(expected[position]);
      }
    }
  });

  it("picks the same active token the per-frame scan picked", () => {
    const blocks = fixtures();
    const index = buildPlayheadIndex(blocks);

    for (const timeMs of probeTimes(blocks)) {
      const state = resolvePlayhead(index, timeMs);
      state.soundingBlocks.forEach((block, position) => {
        expect(state.activeTokens[position], `${block.id} at ${timeMs}ms`).toBe(
          referenceActiveToken(block, timeMs),
        );
      });
    }
  });

  it("resolves a non-monotone block the way array order does, not time order", () => {
    const blocks = fixtures();
    const index = buildPlayheadIndex(blocks);
    // Both `prev` and the interpolated `a` cover 9100 ms. The scan takes the
    // first in array order; a binary search on start times would take the last.
    const state = resolvePlayhead(index, 9100);
    const position = state.soundingBlocks.findIndex((block) => block.id === "non-monotone");

    expect(position).toBeGreaterThanOrEqual(0);
    expect(state.activeTokens[position]?.text).toBe("prev");
    expect(state.activeTokens[position]).toBe(
      referenceActiveToken(
        blocks.find((block) => block.id === "non-monotone")!,
        9100,
      ),
    );
  });

  it("keeps a discredited block dark and a wordless aside lit", () => {
    const blocks = fixtures();
    const index = buildPlayheadIndex(blocks);

    const overGhost = resolvePlayhead(index, 6400).soundingBlocks.map((block) => block.id);
    expect(overGhost).not.toContain("discredited");

    const overAside = resolvePlayhead(index, 5500).soundingBlocks.map((block) => block.id);
    expect(overAside).toEqual(["wordless-aside"]);
  });

  it("holds the ring across a bridged silence but drops it across a real gap", () => {
    const blocks = fixtures();
    const index = buildPlayheadIndex(blocks);

    // 300 ms of silence inside ana-open is bridged.
    expect(resolvePlayhead(index, 1500).soundingBlocks.map((block) => block.id)).toContain(
      "ana-open",
    );
    // 800 ms inside cara-gap is not.
    expect(resolvePlayhead(index, 3500).soundingBlocks.map((block) => block.id)).not.toContain(
      "cara-gap",
    );
  });

  it("rings both speakers while they genuinely overlap", () => {
    const blocks = fixtures();
    const index = buildPlayheadIndex(blocks);

    expect(resolvePlayhead(index, 1900).soundingBlocks.map((block) => block.id)).toEqual([
      "ana-open",
      "ben-interject",
    ]);
  });

  it("reports a validity window nothing changes inside", () => {
    const blocks = fixtures();
    const index = buildPlayheadIndex(blocks);
    const probes = probeTimes(blocks);

    for (const timeMs of probes) {
      const state = resolvePlayhead(index, timeMs);
      expect(state.validFromMs, `window floor at ${timeMs}ms`).toBeLessThanOrEqual(timeMs);
      expect(state.validUntilMs, `window ceiling at ${timeMs}ms`).toBeGreaterThan(timeMs);

      // Anything the caller could ask inside this window must get the same
      // answer, or skipping the recompute would freeze a stale highlight.
      for (const inside of probes) {
        if (inside < state.validFromMs || inside >= state.validUntilMs) {
          continue;
        }
        const expected = getSoundingBlocks(blocks, inside);
        expect(state.soundingBlocks.length, `${timeMs}ms window vs ${inside}ms`).toBe(
          expected.length,
        );
        for (let position = 0; position < expected.length; position += 1) {
          expect(state.soundingBlocks[position], `${timeMs}ms window vs ${inside}ms`).toBe(
            expected[position],
          );
        }
      }
    }
  });

  it("skips work for most whole milliseconds of a dense transcript", () => {
    const blocks = fixtures();
    const index = buildPlayheadIndex(blocks);

    // The point of the window is that playback crosses far fewer boundaries than
    // it renders frames. Over the fixture's span, count how many whole
    // milliseconds open a new window.
    let crossings = 0;
    let validUntilMs = -1;
    for (let timeMs = 0; timeMs <= 12_000; timeMs += 1) {
      if (timeMs >= validUntilMs) {
        validUntilMs = resolvePlayhead(index, timeMs).validUntilMs;
        crossings += 1;
      }
    }
    expect(crossings).toBeLessThanOrEqual(index.changePoints.length + 1);
    // At 60 Hz those 12 s would be 720 frames; the window admits far fewer.
    expect(crossings).toBeLessThan(60);
  });

  it("opens a window at every canonical word of a block with no display tokens", () => {
    const blocks = fixtures();
    const index = buildPlayheadIndex(blocks);
    const canonical = blocks.find((block) => block.id === "canonical-only")!;

    // The two words run back to back, so bridgeGaps melts them into ONE
    // interval and the interval edges alone would keep the first word lit
    // across both. The exact-words highlight moves on at 11300, so the window
    // must end there.
    const first = resolvePlayhead(index, 11_100);
    expect(first.validUntilMs).toBeLessThanOrEqual(11_300);
    expect(getActiveTimedRange(canonical.words!, 11_100)?.text).toBe("Untimed");
    expect(getActiveTimedRange(canonical.words!, 11_400)?.text).toBe("display.");
  });

  it("does not spend change points on canonical words the reader can never see", () => {
    // Every block on the production display path carries tokens, and
    // getActiveDisplayWord refuses to run once tokens exist — so the canonical
    // pool must not inflate the change points that decide how often a frame
    // does work.
    const withWords = [
      {
        id: "display-block",
        speaker: "ana",
        startMs: 0,
        endMs: 1000,
        tokens: [token("One", 0, 500), token("two.", 500, 1000)],
        // Canonical words at different boundaries than the tokens.
        words: [
          token("One", 0, 130),
          token("two", 130, 640),
          token(".", 640, 1000),
        ],
      },
    ] as unknown as OverlapBlock[];

    expect([...buildPlayheadIndex(withWords).changePoints]).toEqual([0, 500, 1000]);
  });

  it("invalidates on array identity, which is how displaySegments changes", () => {
    const blocks = fixtures();
    const index = buildPlayheadIndex(blocks);

    expect(index.source).toBe(blocks);
    expect(buildPlayheadIndex([...blocks]).source).not.toBe(blocks);
  });

  it("handles an empty transcript without special-casing at the call site", () => {
    const index = buildPlayheadIndex([]);
    const state = resolvePlayhead(index, 1234);

    expect(state.soundingBlocks).toEqual([]);
    expect(state.activeTokens).toEqual([]);
    expect(state.validUntilMs).toBe(Number.POSITIVE_INFINITY);
  });
});
