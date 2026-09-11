import { describe, expect, it } from "vitest";

import { findStops, pageBlocks } from "./find";
import type { TranscriptRow } from "./overlap";
import { buildTranscriptIndex } from "./transcript";
import type { TranscriptWordPart } from "./wordInteraction";

const index = buildTranscriptIndex({
  version: "transcript.words.v1",
  media: { src: "a.opus", durationMs: 10_000 },
  speakers: [
    { id: "ana", label: "Ana" },
    { id: "ben", label: "Ben" },
  ],
  segments: [
    { id: "s1", speaker: "ana", startMs: 0, endMs: 2000, text: "The office budget is eighteen.", words: [] },
    { id: "s2", speaker: "ben", startMs: 900, endMs: 1100, text: "Right.", words: [] },
    { id: "s3", speaker: "ana", startMs: 3000, endMs: 5000, text: "Offices at full strength.", words: [] },
  ],
});

type Block = { id: string; startMs: number; sourceSegmentIds: string[] };
const b1: Block = { id: "b1", startMs: 0, sourceSegmentIds: ["s1"] };
const b2: Block = { id: "b2", startMs: 900, sourceSegmentIds: ["s2"] };
// Cleaned wording that lost the word the canonical segment matched on.
const b3: Block = { id: "b3", startMs: 3000, sourceSegmentIds: ["s3"] };

const rows: TranscriptRow<Block>[] = [
  {
    key: "b1",
    speaker: "ana",
    speakerLabel: "Ana",
    startMs: 0,
    endMs: 2000,
    over: [],
    members: [
      { kind: "speech", key: "b1", block: b1 },
      { kind: "interjection", key: "b2", speakerLabel: "Ben", blocks: [b2] },
    ],
  },
  { key: "b3", speaker: "ana", speakerLabel: "Ana", startMs: 3000, endMs: 5000, over: [], members: [{ kind: "speech", key: "b3", block: b3 }] },
];

const part = (id: string, text: string, startMs?: number): TranscriptWordPart => ({
  id,
  text,
  prefix: " ",
  ...(startMs === undefined ? {} : { startMs, endMs: startMs + 200 }),
});

const parts = new Map<string, TranscriptWordPart[]>([
  ["b1", [part("b1:0", "The", 0), part("b1:1", "office", 300), part("b1:2", "budget", 600), part("b1:3", "Office.")]],
  ["b2", [part("b2:0", "Right.", 900)]],
  ["b3", [part("b3:0", "Both", 3000), part("b3:1", "sites", 3300)]],
]);

const ids = (query: string) => findStops(index, rows, parts, query).map((stop) => stop.id);

describe("find in the open meeting", () => {
  it("reads blocks in page order, interjections at their seams", () => {
    expect(pageBlocks(rows).map((block) => block.id)).toEqual(["b1", "b2", "b3"]);
  });

  it("stops on every word that carries a term, in page order", () => {
    expect(findStops(index, rows, parts, "  OFFICE ")).toEqual([
      { id: "b1:1", ms: 300 },
      // Untimed: it stops where its block starts.
      { id: "b1:3", ms: 0 },
      { id: "b3:0", ms: 3000 },
    ]);
  });

  it("matches blocks on their canonical words, so reworded blocks are still found", () => {
    // "offices" was said in s3; b3's wording dropped it, so b3 stops on its first word.
    expect(ids("strength")).toEqual(["b3:0"]);
  });

  it("needs every term in the same canonical segment, as the filter does", () => {
    expect(ids("office budget")).toEqual(["b1:1", "b1:2", "b1:3"]);
    expect(ids("office right")).toEqual([]);
  });

  it("has no stops for a blank query or a meeting with no index", () => {
    expect(ids("   ")).toEqual([]);
    expect(findStops(null, rows, parts, "office")).toEqual([]);
  });
});
