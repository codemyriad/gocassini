/**
 * The playhead index: the transcript's highlight state as a lookup instead of a
 * recomputation (D-692).
 *
 * WHY THIS EXISTS. The viewer highlighted the transcript by re-deriving it from
 * scratch on every animation frame. `getSoundingBlocks` walked every display
 * block, rebuilt each block's audible spans from its two token pools, sorted
 * them, merged them, bridged the sub-400 ms gaps, allocated an array per block
 * per pass, and only then asked whether the playhead was inside one. At 60 Hz
 * over the largest published meeting — 104 blocks carrying 7,318 timed tokens —
 * that is roughly 7 million span visits and 50,000 array sorts in an eight
 * second listen. Measured on that meeting it was 840 ms in `mergeIntervals` and
 * 254 ms in `collectAudibleSpans` out of a 6.56 s script budget, with the
 * renderer 93% busy and the page running at about 20 fps.
 *
 * None of that work depends on the playhead. The intervals are a pure function
 * of the blocks, and the blocks are rebuilt — never mutated — only when the
 * transcript itself changes. So they are computed once here, and playback
 * becomes a lookup.
 *
 * WHAT IT BUYS BEYOND THE OBVIOUS. The index also answers "until when is this
 * answer still true", which is the larger win. Highlight state only changes when
 * the playhead crosses a boundary — a word start, a word end, the edge of a
 * bridged silence — and in dense speech that is a few times a second, not sixty.
 * A frame that lands inside the current window can therefore do NOTHING at all:
 * one comparison against `validUntilMs` and out. The reactive fan-out that
 * dominated the profile is not made cheaper, it is not entered.
 *
 * WHAT IT DELIBERATELY DOES NOT DO. It does not re-derive, re-interpret or
 * "improve" any of the D-690 acoustic-evidence rules in overlap.ts. It calls
 * that module's own `soundingIntervalsOf` and reuses `containsPlaybackTime`
 * verbatim — the bridging tolerance and the definition of audible stay over
 * there, with the tests that pin them — and it is itself held to a differential
 * test that sweeps every boundary of every fixture and asserts the index and
 * `getSoundingBlocks` agree exactly, element identity included. If the two ever
 * disagree, the index is wrong.
 *
 * Pure module: no Svelte, no DOM, no I/O.
 */

import {
  soundingIntervalsOf,
  type Interval,
  type OverlapBlock,
  type OverlapTimedSpan,
} from "./overlap";
import { containsPlaybackTime } from "./timing";

/**
 * A block's precomputed highlight material.
 *
 * `intervals` are the bridged audible intervals the ring is drawn from;
 * `timedTokens` are the tokens the word highlight may land on. They come from
 * DIFFERENT predicates over the same tokens and must not be conflated — an
 * interpolated token, or one whose canonical words were rejected, still renders
 * and still seeks and can still be the active word, but it casts no vote on
 * whether its speaker was audible. That distinction is the whole of D-690; see
 * `audibleIntervalsOf`.
 */
interface IndexedBlock<B> {
  readonly block: B;
  /** Bridged audible intervals, ascending and disjoint. */
  readonly intervals: readonly Interval[];
  /**
   * `segment.tokens` filtered to those carrying both bounds, in ARRAY ORDER —
   * the order `getActiveTimedRange` resolves ties by, which is not necessarily
   * time order.
   */
  readonly timedTokens: readonly OverlapTimedSpan[];
  /**
   * True when `timedTokens` is ascending and non-overlapping, so a binary search
   * returns the same token the linear scan would.
   *
   * NOTHING GUARANTEES TOKEN ORDER, so this cannot be assumed. `validateDisplayToken`
   * checks only `startMs <= endMs` per token and never inter-token ordering;
   * `judgedDisplaySegments` sorts words by canonical position, which is not time
   * position; and `resolveInterpolatedSpan` in portable.ts has a reachable
   * fallback that emits an interpolated run spanning BOTH its anchors, so a
   * block can genuinely carry `prev[1000,1200], interp[1000,1200],
   * interp[1200,1400], next[1200,1400]`. At 1100 ms the linear scan returns
   * `prev`; a binary search on start times returns the interpolated duplicate —
   * a different object, identity-compared in the template, so the highlight
   * lands on the wrong word and wears the wrong underline. Hence: check once,
   * here, and fall back to the scan when the check fails.
   */
  readonly monotone: boolean;
}

export interface PlayheadIndex<B extends OverlapBlock> {
  /**
   * The array this index was built from, kept for identity comparison.
   *
   * `displaySegments` is rebuilt — never mutated in place — and only when
   * `transcriptIndex`, `readableTranscript`, `displayTranscript` or
   * `wordEndsBoundedByAudio` change. So `index.source !== displaySegments` is a
   * sound and complete invalidation test, and needs no deep comparison.
   */
  readonly source: readonly B[];
  readonly blocks: readonly IndexedBlock<B>[];
  /**
   * Every time the highlight state can change, ascending and unique.
   *
   * Between two consecutive entries nothing about the answer moves, which is
   * what lets a frame skip all work.
   */
  readonly changePoints: Float64Array;
}

/** What is lit at a given playhead, and for how long it stays that way. */
export interface PlayheadState<B extends OverlapBlock> {
  /**
   * Sounding blocks, ascending by `startMs`, ties in source order — the same
   * array contract `getSoundingBlocks` has, holding the same object references
   * the caller passed in. Identity matters: the template compares the active
   * token to `segment.tokens` entries with `===`.
   */
  readonly soundingBlocks: readonly B[];
  /**
   * The active token per sounding block, positionally aligned with
   * `soundingBlocks`. `null` where the block is inside a bridged silence, or
   * carries no timed tokens.
   */
  readonly activeTokens: readonly (OverlapTimedSpan | null)[];
  /** Lower bound of the window this state is valid across, inclusive. */
  readonly validFromMs: number;
  /**
   * Upper bound of the window, EXCLUSIVE. `Infinity` past the last boundary.
   * A playhead still inside `[validFromMs, validUntilMs)` has nothing to update.
   */
  readonly validUntilMs: number;
}

/**
 * Precompute every block's highlight material and the transcript's change points.
 *
 * Cost is one pass over the blocks — the same work a single frame used to do —
 * and it is paid once per transcript instead of sixty times a second.
 */
export function buildPlayheadIndex<B extends OverlapBlock>(source: readonly B[]): PlayheadIndex<B> {
  const blocks: IndexedBlock<B>[] = [];
  const boundaries: number[] = [];

  for (const block of source) {
    const intervals = soundingIntervalsOf(block);
    const timedTokens = (block.tokens ?? []).filter(
      (token) => token.startMs !== undefined && token.endMs !== undefined,
    );
    blocks.push({ block, intervals, timedTokens, monotone: isMonotone(timedTokens) });

    for (const interval of intervals) {
      collectBoundaries(interval.startMs, interval.endMs, boundaries);
    }
    for (const token of timedTokens) {
      collectBoundaries(token.startMs as number, token.endMs as number, boundaries);
    }
    // THE EXACT-WORDS PATH NEEDS ITS OWN BOUNDARIES. On the readable and raw
    // transcripts a block carries no display tokens, and the reader can switch
    // the canonical word list on; the highlight then moves word by word through
    // `block.words`. Those moves are invisible in the interval boundaries above,
    // because `bridgeGaps` has already melted a run of consecutive words into
    // ONE interval — so a window built from intervals alone would hold a single
    // word lit for the whole run. Blocks that DO carry tokens are skipped: the
    // exact-words highlight is switched off for them at source
    // (`getActiveDisplayWord` returns null once `tokens` is non-empty), so their
    // canonical words would only add change points nothing can observe, and on
    // the production display path that is every block in the meeting.
    if (timedTokens.length === 0) {
      for (const word of block.words ?? []) {
        if (word.startMs !== undefined && word.endMs !== undefined) {
          collectBoundaries(word.startMs, word.endMs, boundaries);
        }
      }
    }
  }

  boundaries.sort((left, right) => left - right);
  const changePoints = new Float64Array(dedupeSorted(boundaries));
  return { source, blocks, changePoints };
}

/**
 * Resolve the playhead against the index.
 *
 * Allocates only the two small result arrays — one entry per SOUNDING block,
 * typically one or two, never one per rendered block.
 */
export function resolvePlayhead<B extends OverlapBlock>(
  index: PlayheadIndex<B>,
  timeMs: number,
): PlayheadState<B> {
  const sounding: { entry: IndexedBlock<B>; position: number }[] = [];
  for (let position = 0; position < index.blocks.length; position += 1) {
    const entry = index.blocks[position]!;
    if (entry.intervals.some((interval) => containsPlaybackTime(interval, timeMs))) {
      sounding.push({ entry, position });
    }
  }
  // Ascending startMs, ties by source order — `Array.prototype.sort` is stable
  // and `position` ascends, so the explicit tiebreak only documents it. The
  // order is load-bearing: `activeSegments[0]` is the auto-scroll anchor, and
  // following the latest-starting turn instead would bounce the page every time
  // a one-word backchannel opened inside a long turn.
  sounding.sort(
    (left, right) =>
      left.entry.block.startMs - right.entry.block.startMs || left.position - right.position,
  );

  const soundingBlocks: B[] = [];
  const activeTokens: (OverlapTimedSpan | null)[] = [];
  for (const { entry } of sounding) {
    soundingBlocks.push(entry.block);
    activeTokens.push(activeTokenIn(entry, timeMs));
  }

  return {
    soundingBlocks,
    activeTokens,
    validFromMs: floorChangePoint(index.changePoints, timeMs),
    validUntilMs: nextChangePoint(index.changePoints, timeMs),
  };
}

/**
 * The token the word highlight lands on, matching `getActiveTimedRange` exactly:
 * the FIRST token in array order containing the playhead, which for abutting
 * spans is the later one (ends are exclusive) and for overlapping spans is the
 * earlier ARRAY POSITION, not the earlier start.
 */
function activeTokenIn<B>(entry: IndexedBlock<B>, timeMs: number): OverlapTimedSpan | null {
  const tokens = entry.timedTokens;
  if (tokens.length === 0) {
    return null;
  }
  if (!entry.monotone) {
    for (const token of tokens) {
      if (containsPlaybackTime(token as Interval, timeMs)) {
        return token;
      }
    }
    return null;
  }
  // Ascending and disjoint: the only candidate is the last token starting at or
  // before the playhead.
  let low = 0;
  let high = tokens.length - 1;
  let candidate = -1;
  while (low <= high) {
    const middle = (low + high) >> 1;
    if ((tokens[middle]!.startMs as number) <= timeMs) {
      candidate = middle;
      low = middle + 1;
    } else {
      high = middle - 1;
    }
  }
  if (candidate < 0) {
    return null;
  }
  const token = tokens[candidate]!;
  return containsPlaybackTime(token as Interval, timeMs) ? token : null;
}

/**
 * Times at which an interval's membership can flip.
 *
 * A half-open `[start, end)` flips exactly at its two edges. A ZERO-LENGTH span
 * is different: `containsPlaybackTime` matches it only on exact equality, so it
 * is lit for an instant and dark immediately after, and `start + 1` is the
 * conservative next edge — the playhead is written as whole milliseconds
 * (`Math.round(currentTime * 1000)`), so no representable playhead falls
 * strictly between. Over-supplying boundaries only costs a redundant recompute;
 * under-supplying would freeze a stale highlight, so the bias is deliberate.
 * An inverted span matches nothing and contributes nothing.
 */
function collectBoundaries(startMs: number, endMs: number, into: number[]): void {
  if (!Number.isFinite(startMs) || !Number.isFinite(endMs) || endMs < startMs) {
    return;
  }
  into.push(startMs);
  into.push(endMs === startMs ? startMs + 1 : endMs);
}

function dedupeSorted(values: readonly number[]): number[] {
  const unique: number[] = [];
  for (const value of values) {
    if (unique.length === 0 || unique[unique.length - 1] !== value) {
      unique.push(value);
    }
  }
  return unique;
}

/** Greatest change point at or below `timeMs`; `-Infinity` before the first. */
function floorChangePoint(points: Float64Array, timeMs: number): number {
  let low = 0;
  let high = points.length - 1;
  let found = Number.NEGATIVE_INFINITY;
  while (low <= high) {
    const middle = (low + high) >> 1;
    if (points[middle]! <= timeMs) {
      found = points[middle]!;
      low = middle + 1;
    } else {
      high = middle - 1;
    }
  }
  return found;
}

/** Smallest change point strictly above `timeMs`; `Infinity` past the last. */
function nextChangePoint(points: Float64Array, timeMs: number): number {
  let low = 0;
  let high = points.length - 1;
  let found = Number.POSITIVE_INFINITY;
  while (low <= high) {
    const middle = (low + high) >> 1;
    if (points[middle]! > timeMs) {
      found = points[middle]!;
      high = middle - 1;
    } else {
      low = middle + 1;
    }
  }
  return found;
}

function isMonotone(tokens: readonly OverlapTimedSpan[]): boolean {
  for (let position = 1; position < tokens.length; position += 1) {
    if ((tokens[position]!.startMs as number) < (tokens[position - 1]!.endMs as number)) {
      return false;
    }
  }
  return true;
}
