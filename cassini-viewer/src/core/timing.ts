export interface TimedRange {
  startMs: number;
  endMs: number;
}

export function getActiveTimedRange<T extends TimedRange>(items: readonly T[], timeMs: number): T | null {
  for (const item of items) {
    if (containsPlaybackTime(item, timeMs)) {
      return item;
    }
  }
  return null;
}

/**
 * EVERY range containing the playhead, earliest start first.
 *
 * Replaces the old single "latest-starting wins" pick, which was a workaround
 * from a time when the viewer could not represent two structures being active
 * at once: during an overlap the later-starting block stole the highlight and
 * then abandoned it, so an interrupting turn took the ring, lost it partway
 * through, and was never highlighted again despite still running — while its
 * tokens stayed clickable and seeked the reader into the middle of somebody
 * else's highlighted paragraph.
 *
 * Now that simultaneity is a first-class concept, both the surrounding turn and
 * the turn nested inside it are highlighted honestly for as long as each is
 * really sounding. Callers that need a single anchor (scroll-into-view) take
 * the first element, which is the turn that has held the floor longest.
 */
export function getActiveTimedRanges<T extends TimedRange>(
  items: readonly T[],
  timeMs: number,
): T[] {
  return items
    .filter((item) => containsPlaybackTime(item, timeMs))
    .sort((left, right) => left.startMs - right.startMs);
}

export function containsPlaybackTime(range: TimedRange, timeMs: number): boolean {
  if (range.startMs === range.endMs) {
    return timeMs === range.startMs;
  }
  return range.startMs <= timeMs && timeMs < range.endMs;
}
