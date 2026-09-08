/** SPDX-License-Identifier: AGPL-3.0-only
 * Word playback for the shared Cassini meeting view (D-734).
 *
 * Build once per transcript. Frames within the same word-boundary interval do
 * nothing; crossing a boundary updates only words entering or leaving it.
 * Multiple speakers can be active together. No timing is inferred or extended.
 */
export interface PlaybackWord {
  id: string;
  startMs: number;
  endMs: number;
  blockId?: string;
  order?: number;
  ambiguous?: boolean;
}

export interface WordHighlightIndex {
  words: readonly PlaybackWord[];
  starts: Float64Array;
  prefixEnds: Float64Array;
  boundaries: Float64Array;
}

function upperBound(values: ArrayLike<number>, timeMs: number): number {
  let lo = 0;
  let hi = values.length;
  while (lo < hi) {
    const mid = (lo + hi) >>> 1;
    if (values[mid] <= timeMs) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

export function buildWordHighlightIndex(
  input: readonly PlaybackWord[],
): WordHighlightIndex {
  const unique = new Map(input.map((word) => [word.id, word]));
  const words = [...unique.values()]
    .filter(
      (word) =>
        Number.isFinite(word.startMs) &&
        Number.isFinite(word.endMs) &&
        word.endMs > word.startMs,
    )
    .sort((left, right) => left.startMs - right.startMs);
  const starts = Float64Array.from(words, (word) => word.startMs);
  let maxEnd = Number.NEGATIVE_INFINITY;
  const prefixEnds = Float64Array.from(
    words,
    (word) => (maxEnd = Math.max(maxEnd, word.endMs)),
  );
  const boundaries = Float64Array.from(
    [...new Set(words.flatMap((word) => [word.startMs, word.endMs]))].sort(
      (a, b) => a - b,
    ),
  );
  return { words, starts, prefixEnds, boundaries };
}

export function resolveWordHighlight(
  index: WordHighlightIndex,
  timeMs: number,
  soundingBlockIds?: ReadonlySet<string>,
) {
  const activeIds = new Set<string>();
  const activeByBlock = new Map<string, PlaybackWord[]>();
  // Prefix maxima stop the backwards scan as soon as every earlier word has
  // ended, while still finding a long word spanning several short overlaps.
  for (
    let i = upperBound(index.starts, timeMs) - 1;
    i >= 0 && index.prefixEnds[i] > timeMs;
    i--
  ) {
    const word = index.words[i];
    if (timeMs >= word.endMs) continue;
    if (word.blockId !== undefined) {
      if (soundingBlockIds && !soundingBlockIds.has(word.blockId)) continue;
      const group = activeByBlock.get(word.blockId);
      if (group) group.push(word);
      else activeByBlock.set(word.blockId, [word]);
    } else {
      activeIds.add(word.id);
    }
  }
  for (const group of activeByBlock.values()) {
    if (group.some((word) => word.ambiguous)) {
      // Rewritten runs can duplicate their anchors' intervals. Preserve the
      // existing first-in-display-order choice for that ambiguous overlap
      // (also used for display tokens with rejected source references);
      // independent source timings (including 1 ms overlaps) stay simultaneous.
      const first = group.reduce((a, b) => (a.order ?? 0) <= (b.order ?? 0) ? a : b);
      activeIds.add(first.id);
    } else {
      for (const word of group) activeIds.add(word.id);
    }
  }
  const next = upperBound(index.boundaries, timeMs);
  return {
    activeIds,
    validFromMs:
      next > 0 ? index.boundaries[next - 1] : Number.NEGATIVE_INFINITY,
    validUntilMs:
      next < index.boundaries.length
        ? index.boundaries[next]
        : Number.POSITIVE_INFINITY,
  };
}

/** Per-word subscriptions avoid invalidating every token on each audio frame. */
export function createWordHighlighter() {
  let index = buildWordHighlightIndex([]);
  let activeIds = new Set<string>();
  let validFromMs = Number.POSITIVE_INFINITY;
  let validUntilMs = Number.NEGATIVE_INFINITY;
  let previousSoundingBlockIds: ReadonlySet<string> | undefined;
  const listeners = new Map<string, Set<(active: boolean) => void>>();
  const notify = (id: string, active: boolean) => {
    for (const listener of listeners.get(id) ?? []) listener(active);
  };
  return {
    setWords(words: readonly PlaybackWord[]) {
      index = buildWordHighlightIndex(words);
      validFromMs = Number.POSITIVE_INFINITY;
      validUntilMs = Number.NEGATIVE_INFINITY;
    },
    update(timeMs: number, soundingBlockIds?: ReadonlySet<string>) {
      if (!Number.isFinite(timeMs)) return;
      if (previousSoundingBlockIds === soundingBlockIds && timeMs >= validFromMs && timeMs < validUntilMs) return;
      previousSoundingBlockIds = soundingBlockIds;
      const next = resolveWordHighlight(index, timeMs, soundingBlockIds);
      validFromMs = next.validFromMs;
      validUntilMs = next.validUntilMs;
      for (const id of activeIds)
        if (!next.activeIds.has(id)) notify(id, false);
      for (const id of next.activeIds) if (!activeIds.has(id)) notify(id, true);
      activeIds = next.activeIds;
    },
    subscribe(id: string, listener: (active: boolean) => void) {
      let bucket = listeners.get(id);
      if (!bucket) listeners.set(id, (bucket = new Set()));
      bucket.add(listener);
      listener(activeIds.has(id));
      return () => {
        bucket.delete(listener);
        if (!bucket.size) listeners.delete(id);
      };
    },
  };
}
