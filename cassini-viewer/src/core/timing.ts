export interface TimedRange {
  startMs: number;
  endMs: number;
}

export function getActiveTimedRange<T extends TimedRange>(items: readonly T[], timeMs: number): T | null {
  let winner: T | null = null;
  for (const item of items) {
    if (containsPlaybackTime(item, timeMs)) {
      winner = item;
      continue;
    }
    if (item.startMs <= timeMs) {
      winner = item;
    }
  }
  return winner;
}

export function containsPlaybackTime(range: TimedRange, timeMs: number): boolean {
  if (range.startMs === range.endMs) {
    return timeMs === range.startMs;
  }
  return range.startMs <= timeMs && timeMs < range.endMs;
}
