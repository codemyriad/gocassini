import type { TranscriptRow } from "./overlap";
import { filterDisplaySegmentsByQuery } from "./transcript";
import type { TranscriptIndex } from "./types";
import type { TranscriptWordPart } from "./wordInteraction";

export interface FindStop {
  id: string;
  ms: number;
}

export function pageBlocks<B>(rows: readonly TranscriptRow<B>[]): B[] {
  return rows.flatMap((row) =>
    row.members.flatMap((member) => (member.kind === "speech" ? [member.block] : [...member.blocks])),
  );
}

// Where find stops, in page order. A block matches on its canonical words, as
// the filter does; inside it, the words that carry a term are the stops, and a
// block whose cleaned wording lost every term stops on its first word. An
// untimed word sits at its block's start.
export function findStops<B extends { id: string; startMs: number; sourceSegmentIds: readonly string[] }>(
  index: TranscriptIndex | null,
  rows: readonly TranscriptRow<B>[],
  partsByBlock: ReadonlyMap<string, readonly TranscriptWordPart[]>,
  query: string,
): FindStop[] {
  const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (!index || terms.length === 0) {
    return [];
  }
  return filterDisplaySegmentsByQuery(index, pageBlocks(rows), query).flatMap((block) => {
    const parts = partsByBlock.get(block.id) ?? [];
    const hits = parts.filter((part) => terms.some((term) => part.text.toLowerCase().includes(term)));
    return (hits.length > 0 ? hits : parts.slice(0, 1)).map((part) => ({ id: part.id, ms: part.startMs ?? block.startMs }));
  });
}
