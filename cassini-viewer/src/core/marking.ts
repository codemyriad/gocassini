import { formatClockTime } from "./transcript";

// Marking a stretch of a transcript (D-746). A selection is a span of words;
// it becomes [startMs, endMs) only when it is written.

export interface TimedWord {
  id: string;
  startMs: number;
  endMs: number;
}

// Inclusive indexes into a list of words in time order.
export interface WordSpan {
  from: number;
  to: number;
}

export const PAUSE_MS = 400;

export function wordsByTime(
  parts: Iterable<{ id: string; startMs?: number; endMs?: number }>,
): TimedWord[] {
  const words: TimedWord[] = [];
  for (const { id, startMs, endMs } of parts) {
    if (startMs !== undefined && endMs !== undefined) {
      words.push({ id, startMs, endMs });
    }
  }
  return words.sort((a, b) => a.startMs - b.startMs);
}

function firstStartingAt(words: readonly TimedWord[], ms: number): number {
  let low = 0;
  let high = words.length;
  while (low < high) {
    const mid = (low + high) >> 1;
    if (words[mid]!.startMs < ms) {
      low = mid + 1;
    } else {
      high = mid;
    }
  }
  return low;
}

// The words a written stretch covers: those that start inside it.
export function spanForRange(
  words: readonly TimedWord[],
  startMs: number,
  endMs: number,
): WordSpan | null {
  const from = firstStartingAt(words, startMs);
  const to = firstStartingAt(words, endMs) - 1;
  return to >= from ? { from, to } : null;
}

// What a drag between two moments grabs, in either direction. It is never
// empty, so the selection cannot collapse as the pointer crosses its start.
export function spanForDrag(words: readonly TimedWord[], aMs: number, bMs: number): WordSpan | null {
  if (words.length === 0) {
    return null;
  }
  const low = Math.min(aMs, bMs);
  let from = firstStartingAt(words, low);
  if (from > 0 && words[from - 1]!.endMs > low) {
    from -= 1;
  }
  from = Math.min(from, words.length - 1);
  return { from, to: Math.max(from, firstStartingAt(words, Math.max(aMs, bMs)) - 1) };
}

// What is written: the start of the first word to the end of the last.
export function rangeOfSpan(words: readonly TimedWord[], span: WordSpan): { startMs: number; endMs: number } {
  const startMs = words[span.from]!.startMs;
  return { startMs, endMs: Math.max(words[span.to]!.endMs, startMs + 1) };
}

// Moves one end by a word, or with toPause to the next pause, never past the other end.
export function nudge<S extends WordSpan>(
  words: readonly TimedWord[],
  span: S,
  edge: "from" | "to",
  step: 1 | -1,
  toPause = false,
): S {
  const last = words.length - 1;
  const pauseBefore = (i: number) => words[i]!.startMs - words[i - 1]!.endMs >= PAUSE_MS;
  const stops =
    edge === "from"
      ? (i: number) => i <= 0 || i > last || pauseBefore(i)
      : (i: number) => i < 0 || i >= last || pauseBefore(i + 1);
  let index = span[edge] + step;
  while (toPause && !stops(index)) {
    index += step;
  }
  return edge === "from"
    ? { ...span, from: Math.min(Math.max(index, 0), span.to) }
    : { ...span, to: Math.min(Math.max(index, span.from), last) };
}

// Side by side where ranges overlap: each takes the first column free at its start.
export function stackColumns(ranges: readonly { startMs: number; endMs: number }[]): number[] {
  const order = ranges
    .map((_, index) => index)
    .sort((a, b) => ranges[a]!.startMs - ranges[b]!.startMs || ranges[b]!.endMs - ranges[a]!.endMs);
  const ends: number[] = [];
  const columns: number[] = [];
  for (const index of order) {
    let column = 0;
    while (ends[column] !== undefined && ends[column]! > ranges[index]!.startMs) {
      column += 1;
    }
    columns[index] = column;
    ends[column] = ranges[index]!.endMs;
  }
  return columns;
}

// The turn under a moment on the rail: one sounding then, or else the nearest.
export function rowAt<R extends { startMs: number; endMs: number }>(
  rows: readonly R[],
  ms: number,
): R | undefined {
  let best: R | undefined;
  let bestDistance = Number.POSITIVE_INFINITY;
  for (const row of rows) {
    const distance = Math.max(row.startMs - ms, ms - row.endMs, 0);
    if (distance < bestDistance) {
      best = row;
      bestDistance = distance;
    }
  }
  return best;
}

export function railTicks(durationMs: number): number[] {
  if (!(durationMs > 0)) {
    return [];
  }
  const minute = 60_000;
  const step = durationMs <= 15 * minute ? 2 * minute : durationMs <= 40 * minute ? 5 * minute : 10 * minute;
  const ticks = [0];
  for (let tick = step; tick < durationMs - step * 0.45; tick += step) {
    ticks.push(tick);
  }
  ticks.push(durationMs);
  return ticks;
}

export function formatPreciseTime(ms: number): string {
  return `${formatClockTime(ms)}.${Math.floor((Math.max(0, ms) % 1000) / 100)}`;
}
