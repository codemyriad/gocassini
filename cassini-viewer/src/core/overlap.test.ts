import { describe, expect, it } from "vitest";

import {
  INTERJECTION_MAX_MIDDLE_MS,
  analyzeOverlap,
  buildTranscriptRows,
  buildTurnModel,
  audibleIntervalsOf,
  followRowKeyForBlocks,
  getSoundingBlocks,
  repairTurnFinalWordInflation,
  sortBlocksInReadingOrder,
  type OverlapBlock,
  type TranscriptRow,
} from "./overlap";
import { shreddedDoubleTalkSegments } from "./fixtures/shreddedDoubleTalk";

/** Every block a row puts on the page, chips included, in the order it lays them out. */
function blockIdsIn(row: TranscriptRow<OverlapBlock>): string[] {
  return row.members.flatMap((member) =>
    member.kind === "speech" ? [member.block.id] : member.blocks.map((block) => block.id),
  );
}

/** The interjection chips a row carries. */
function chipsIn(row: TranscriptRow<OverlapBlock>) {
  return row.members.filter((member) => member.kind === "interjection");
}

/**
 * The shape a fully rewritten cleaned block really has: a complete set of word
 * TOKENS carrying no timing at all (alignment "none" — see portable.test.ts,
 * "leaves fully rewritten cleaned blocks untimed at the word level") over
 * canonical words that are timed. Both fields are populated, so choosing the
 * pool by array length picks the untimed one and falls through to the
 * paragraph envelope.
 */
function untimedTokensFor(text: string) {
  return text.split(" ").map((word) => ({ text: word }));
}

/**
 * Fixtures are the shapes the producer really emits, not tidy idealisations.
 *
 * `overlapAndPauseSegments` is the segment list a real ASR run of the
 * overlap-and-pause scenario produced through current main, transcribed from
 * that run (spans in ms, text as emitted). Its ground truth is known from the
 * TTS manifest that generated the audio: Ana speaks CONTINUOUSLY from 1.0 s to
 * 8.23 s as one sentence, and Cara speaks continuously from 23.0 s to 30.1 s as
 * one utterance, with Ben's "Right." and Ana's "Perfect." as backchannels over
 * them. The producer nonetheless emits Ana's and Cara's turns in two halves,
 * cut exactly where the backchannel starts — note 5200/5200 and 26680/26680 —
 * with the interjection wedged between, and the second half opening mid-clause.
 */
const overlapAndPauseSegments: OverlapBlock[] = [
  { id: "s1", speaker: "ana", speakerLabel: "Ana Duarte", startMs: 1020, endMs: 5200 },
  { id: "s2", speaker: "ben", speakerLabel: "Ben Okafor", startMs: 4780, endMs: 5260 },
  { id: "s3", speaker: "ana", speakerLabel: "Ana Duarte", startMs: 5200, endMs: 8240 },
  { id: "s4", speaker: "ben", speakerLabel: "Ben Okafor", startMs: 13_100, endMs: 15_880 },
  { id: "s5", speaker: "ana", speakerLabel: "Ana Duarte", startMs: 18_040, endMs: 19_280 },
  { id: "s6", speaker: "cara", speakerLabel: "Cara Lindqvist", startMs: 23_480, endMs: 26_680 },
  { id: "s7", speaker: "ana", speakerLabel: "Ana Duarte", startMs: 26_560, endMs: 27_240 },
  { id: "s8", speaker: "cara", speakerLabel: "Cara Lindqvist", startMs: 26_680, endMs: 29_360 },
  { id: "s9", speaker: "ben", speakerLabel: "Ben Okafor", startMs: 29_130, endMs: 31_120 },
  { id: "s10", speaker: "cara", speakerLabel: "Cara Lindqvist", startMs: 33_530, endMs: 36_490 },
  { id: "s11", speaker: "ana", speakerLabel: "Ana Duarte", startMs: 37_080, endMs: 38_120 },
];

/** Evenly spaced ordinary words — 240 ms each, the measured archive median. */
function ordinaryWords(startMs: number, count: number, durationMs = 240) {
  return Array.from({ length: count }, (_, index) => ({
    text: `word${index}`,
    startMs: startMs + index * durationMs,
    endMs: startMs + (index + 1) * durationMs,
  }));
}

describe("sortBlocksInReadingOrder", () => {
  it("puts producer-appended wordless segments back where they were said", () => {
    const blocks = [
      { id: "b", startMs: 5000, endMs: 6000 },
      { id: "c", startMs: 9000, endMs: 9500 },
      // The Go producer appends wordless segments after every timed one.
      { id: "a", startMs: 1000, endMs: 2000 },
    ];

    expect(sortBlocksInReadingOrder(blocks).map((block) => block.id)).toEqual(["a", "b", "c"]);
  });

  it("keeps producer order for blocks that start at the same millisecond", () => {
    const blocks = [
      { id: "second", startMs: 1000, endMs: 4000 },
      { id: "first", startMs: 1000, endMs: 2000 },
    ];

    expect(sortBlocksInReadingOrder(blocks).map((block) => block.id)).toEqual(["second", "first"]);
  });

  it("does not mutate the caller's array", () => {
    const blocks = [
      { id: "b", startMs: 5000, endMs: 6000 },
      { id: "a", startMs: 1000, endMs: 2000 },
    ];
    sortBlocksInReadingOrder(blocks);

    expect(blocks.map((block) => block.id)).toEqual(["b", "a"]);
  });
});

describe("split-turn interjections (the A/B/A shape)", () => {
  const analysis = analyzeOverlap(overlapAndPauseSegments);

  it("marks Ben's backchannel as spoken inside Ana's continuing turn", () => {
    expect(analysis.get("s2")?.interrupts).toEqual({
      speakerLabel: "Ana Duarte",
      beforeId: "s1",
      afterId: "s3",
    });
    expect(analysis.get("s3")?.resumes).toEqual({ speakerLabel: "Ben Okafor", blockId: "s2" });
  });

  it("marks Ana's backchannel as spoken inside Cara's continuing turn", () => {
    expect(analysis.get("s7")?.interrupts).toEqual({
      speakerLabel: "Cara Lindqvist",
      beforeId: "s6",
      afterId: "s8",
    });
    expect(analysis.get("s8")?.resumes).toEqual({ speakerLabel: "Ana Duarte", blockId: "s7" });
  });

  it("puts each interrupted turn on one row, with the backchannel at its seam", () => {
    const rows = buildTranscriptRows(overlapAndPauseSegments);
    const hosts = rows.filter((row) => chipsIn(row).length > 0);

    expect(hosts).toHaveLength(2);
    // One paragraph, both halves of it, and the remark inline between them.
    expect(blockIdsIn(hosts[0]!)).toEqual(["s1", "s2", "s3"]);
    expect(hosts[0]?.speakerLabel).toBe("Ana Duarte");
    expect(chipsIn(hosts[0]!).map((chip) => [chip.key, chip.speakerLabel])).toEqual([
      ["s2", "Ben Okafor"],
    ]);
    expect(blockIdsIn(hosts[1]!)).toEqual(["s6", "s7", "s8"]);
    expect(hosts[1]?.speakerLabel).toBe("Cara Lindqvist");
    expect(chipsIn(hosts[1]!).map((chip) => chip.speakerLabel)).toEqual(["Ana Duarte"]);
  });

  it("keeps every block exactly once, in time order, across all rows", () => {
    const ordered = sortBlocksInReadingOrder(overlapAndPauseSegments);
    const rows = buildTranscriptRows(overlapAndPauseSegments);

    expect(rows.flatMap(blockIdsIn)).toEqual(ordered.map((block) => block.id));
  });

  it("does not group an ordinary three-turn exchange", () => {
    // A asks, B answers after a beat, A replies after another beat. Same A/B/A
    // adjacency, but A stopped: the halves are 3.2 s apart.
    const exchange: OverlapBlock[] = [
      { id: "a1", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 2000 },
      { id: "b1", speaker: "ben", speakerLabel: "Ben", startMs: 2300, endMs: 4900 },
      { id: "a2", speaker: "ana", speakerLabel: "Ana", startMs: 5200, endMs: 7000 },
    ];
    const result = analyzeOverlap(exchange);

    expect(result.get("b1")?.interrupts).toBeUndefined();
    expect(result.get("a2")?.resumes).toBeUndefined();
    expect(buildTranscriptRows(exchange).map(blockIdsIn)).toEqual([["a1"], ["b1"], ["a2"]]);
  });

  it("does not group when a long pause separates the speaker's two halves", () => {
    // B lands right at A's boundary, but A only resumes four seconds later, so
    // A did stop talking and these are three turns, not one interrupted turn.
    const paused: OverlapBlock[] = [
      { id: "a1", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 3000 },
      { id: "b1", speaker: "ben", speakerLabel: "Ben", startMs: 2900, endMs: 3400 },
      { id: "a2", speaker: "ana", speakerLabel: "Ana", startMs: 7400, endMs: 9000 },
    ];

    expect(analyzeOverlap(paused).get("b1")?.interrupts).toBeUndefined();
  });

  it("does not group when the speaker took a beat before resuming", () => {
    // Only the seam gate rejects this one: B is short, B overlaps A's tail, and
    // A resumes only 0.1 s after B stops — but a full second of silence sits
    // between A's two halves, so A yielded the floor and took it back.
    const beat: OverlapBlock[] = [
      { id: "a1", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 2000 },
      { id: "b1", speaker: "ben", speakerLabel: "Ben", startMs: 1900, endMs: 2900 },
      { id: "a2", speaker: "ana", speakerLabel: "Ana", startMs: 3000, endMs: 5000 },
    ];

    expect(analyzeOverlap(beat).get("b1")?.interrupts).toBeUndefined();
  });

  it("does not group when the speaker's two halves overlap each other massively", () => {
    // An upper-bound-only seam test passes any NEGATIVE seam, so this grouped:
    // A's second half starts 7 s before its first half ends, and A's halves
    // overlap B for its entire duration. Whatever this is, it is not one
    // continuous turn with a backchannel dropped into a narrow seam.
    const blocks: OverlapBlock[] = [
      { id: "a1", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 10_000 },
      { id: "b", speaker: "ben", speakerLabel: "Ben", startMs: 2000, endMs: 2500 },
      { id: "a2", speaker: "ana", speakerLabel: "Ana", startMs: 3000, endMs: 4000 },
    ];
    const analysis = analyzeOverlap(blocks);

    expect(analysis.get("b")?.interrupts).toBeUndefined();
    expect(analysis.get("a2")?.resumes).toBeUndefined();
    expect(buildTranscriptRows(blocks)).toHaveLength(3);
  });

  it("does not group when the middle block is a full contribution rather than a backchannel", () => {
    const longMiddle: OverlapBlock[] = [
      { id: "a1", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 3000 },
      { id: "b1", speaker: "ben", speakerLabel: "Ben", startMs: 2900, endMs: 6000 },
      { id: "a2", speaker: "ana", speakerLabel: "Ana", startMs: 3100, endMs: 9000 },
    ];

    expect(analyzeOverlap(longMiddle).get("b1")?.interrupts).toBeUndefined();
  });
});

describe("span overlap", () => {
  it("reports a genuine floor-change interruption", () => {
    // Ben cuts in 0.23 s before Cara's second half finishes (fixture s8/s9).
    const analysis = analyzeOverlap(overlapAndPauseSegments);

    expect(analysis.get("s9")?.peers.map((peer) => peer.id)).toEqual(["s8"]);
    expect(analysis.get("s9")?.overlapMs).toBe(230);
    expect(analysis.get("s9")?.overlapMsBefore).toBe(230);
    expect(analysis.get("s9")?.overlapMsAfter).toBe(0);
  });

  it("reports a short backchannel fully contained in a long turn", () => {
    const blocks: OverlapBlock[] = [
      {
        id: "long",
        speaker: "ana",
        speakerLabel: "Ana Duarte",
        startMs: 1000,
        endMs: 21_000,
        words: ordinaryWords(1000, 80),
      },
      {
        id: "backchannel",
        speaker: "ben",
        speakerLabel: "Ben Okafor",
        startMs: 8000,
        endMs: 8600,
        words: [{ text: "Right.", startMs: 8000, endMs: 8600 }],
      },
    ];
    const analysis = analyzeOverlap(blocks);

    expect(analysis.get("backchannel")?.containedIn).toBe("long");
    expect(analysis.get("backchannel")?.overlapMs).toBe(600);
    expect(analysis.get("long")?.peers[0]?.speakerLabel).toBe("Ben Okafor");
    // Ana's turn was never split around it, so it reads as its own short turn
    // with each of them named over the other - and no duration either way.
    expect(buildTranscriptRows(blocks).map((row) => [row.speakerLabel, [...row.over]])).toEqual([
      ["Ana Duarte", ["Ben Okafor"]],
      ["Ben Okafor", ["Ana Duarte"]],
    ]);
  });

  it("measures words, not paragraph envelopes, when a speaker's paragraph spans another's", () => {
    // The producer's readable writer groups a speaker's turns into paragraphs
    // that reach across the other speaker's turns. Measured on an archived
    // meeting, these two paragraphs intersect for 14.08 s of envelope while
    // their words strictly alternate and never once sound together.
    const blocks: OverlapBlock[] = [
      {
        id: "ivan",
        speaker: "ivan",
        speakerLabel: "Ivan",
        startMs: 965_000,
        endMs: 983_320,
        words: [...ordinaryWords(965_000, 12), ...ordinaryWords(972_920, 43)],
      },
      {
        id: "chris",
        speaker: "chris",
        speakerLabel: "Chris",
        startMs: 969_240,
        endMs: 1_004_600,
        words: [...ordinaryWords(969_240, 5), ...ordinaryWords(988_280, 68)],
      },
    ];

    expect(analyzeOverlap(blocks).size).toBe(0);
  });

  it("counts time shared by two peers once", () => {
    const blocks: OverlapBlock[] = [
      { id: "a", speaker: "a", speakerLabel: "A", startMs: 0, endMs: 4000 },
      { id: "b", speaker: "b", speakerLabel: "B", startMs: 1000, endMs: 4000 },
      { id: "c", speaker: "c", speakerLabel: "C", startMs: 1000, endMs: 4000 },
    ];
    const analysis = analyzeOverlap(blocks);

    expect(analysis.get("a")?.peers).toHaveLength(2);
    expect(analysis.get("a")?.overlapMs).toBe(3000);
    // Both peers are named on the page rather than counted ("+1").
    expect(buildTranscriptRows(blocks)[0]?.over).toEqual(["B", "C"]);
  });

  it("never treats a speaker as simultaneous with themselves", () => {
    const blocks: OverlapBlock[] = [
      { id: "a1", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 5000 },
      { id: "a2", speaker: "ana", speakerLabel: "Ana", startMs: 2000, endMs: 7000 },
    ];

    expect(analyzeOverlap(blocks).size).toBe(0);
  });

  it("ignores a boundary touch below the credible threshold", () => {
    const blocks: OverlapBlock[] = [
      { id: "a", speaker: "a", speakerLabel: "A", startMs: 0, endMs: 5000 },
      { id: "b", speaker: "b", speakerLabel: "B", startMs: 4900, endMs: 9000 },
    ];

    expect(analyzeOverlap(blocks).size).toBe(0);
  });

  it("reports nothing for a single-speaker meeting", () => {
    const blocks: OverlapBlock[] = [
      { id: "a", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 5000 },
      { id: "b", speaker: "ana", speakerLabel: "Ana", startMs: 5000, endMs: 9000 },
      { id: "c", speaker: "ana", speakerLabel: "Ana", startMs: 9000, endMs: 12_000 },
    ];

    expect(analyzeOverlap(blocks).size).toBe(0);
    // One speaker who never stopped is one paragraph, whatever the producer cut.
    expect(buildTranscriptRows(blocks).map(blockIdsIn)).toEqual([["a", "b", "c"]]);
  });

  it("falls back to the block envelope for a turn with no words at all", () => {
    const blocks: OverlapBlock[] = [
      { id: "wordless", speaker: "ana", speakerLabel: "Ana", startMs: 1000, endMs: 6000, words: [] },
      {
        id: "timed",
        speaker: "ben",
        speakerLabel: "Ben",
        startMs: 5000,
        endMs: 9000,
        words: ordinaryWords(5000, 16),
      },
    ];

    expect(analyzeOverlap(blocks).get("wordless")?.overlapMs).toBe(1000);
  });

  it("accepts unsorted input", () => {
    const shuffled = [...overlapAndPauseSegments].reverse();

    expect(analyzeOverlap(shuffled).get("s2")?.interrupts?.beforeId).toBe("s1");
  });
});

describe("legacy turn-final inflation repair", () => {
  /**
   * The archive shape. Ana's turn really ends at 12.0 s, but Parakeet stamped
   * the trailing period at the next acoustic onset and the producer glued it to
   * "everything", so the word claims 12.0 s → 20.5 s. Ben starts speaking at
   * 15.0 s, in what is really silence. Ana's other words are ordinary (240 ms),
   * so her budget is max(1000, 4 × 240) = 1000 ms.
   */
  const fabricated: OverlapBlock[] = [
    {
      id: "ana-turn",
      speaker: "ana",
      speakerLabel: "Ana Duarte",
      startMs: 2000,
      endMs: 20_500,
      words: [...ordinaryWords(2000, 41), { text: "everything.", startMs: 12_000, endMs: 20_500 }],
    },
    {
      id: "ben-turn",
      speaker: "ben",
      speakerLabel: "Ben Okafor",
      startMs: 15_000,
      endMs: 19_000,
      words: ordinaryWords(15_000, 16),
    },
  ];

  it("shows fabricated overlap before the repair, so the fixture is real", () => {
    expect(analyzeOverlap(fabricated).get("ben-turn")?.overlapMs).toBe(3840);
  });

  it("yields no overlap affordance once the inflated word is clipped", () => {
    const repaired = repairTurnFinalWordInflation(fabricated);

    expect(analyzeOverlap(repaired).size).toBe(0);
    expect(buildTranscriptRows(repaired).flatMap((row) => [...row.over])).toEqual([]);
  });

  it("clips the inflated word to start + the speaker's budget and shrinks the envelope", () => {
    const repaired = repairTurnFinalWordInflation(fabricated);
    const words = repaired[0]?.words ?? [];

    expect(words.at(-1)).toMatchObject({ text: "everything.", startMs: 12_000, endMs: 13_000 });
    expect(repaired[0]?.endMs).toBe(13_000);
  });

  it("leaves a genuine backchannel overlapping, punctuation and all", () => {
    // "Right." ends in a full stop but runs for an ordinary 600 ms, so it is
    // not touched and stays simultaneous with the turn it lands inside.
    const genuine: OverlapBlock[] = [
      {
        id: "long",
        speaker: "ana",
        speakerLabel: "Ana",
        startMs: 1000,
        endMs: 21_000,
        words: ordinaryWords(1000, 80),
      },
      {
        id: "backchannel",
        speaker: "ben",
        speakerLabel: "Ben",
        startMs: 8000,
        endMs: 8600,
        words: [{ text: "Right.", startMs: 8000, endMs: 8600 }],
      },
    ];
    const repaired = repairTurnFinalWordInflation(genuine);

    expect(repaired[1]?.words?.at(-1)?.endMs).toBe(8600);
    expect(analyzeOverlap(repaired).get("backchannel")?.overlapMs).toBe(600);
  });

  it("clips a word whose terminal punctuation hides behind a closing quote", () => {
    const blocks: OverlapBlock[] = [
      {
        id: "quoted",
        speaker: "ana",
        speakerLabel: "Ana",
        startMs: 1000,
        endMs: 14_000,
        words: [...ordinaryWords(1000, 20), { text: 'Yeah."', startMs: 5800, endMs: 14_000 }],
      },
    ];

    expect(repairTurnFinalWordInflation(blocks)[0]?.words?.at(-1)?.endMs).toBe(6800);
  });

  it("clips an inflated word that sits in the middle of a display paragraph", () => {
    // The readable writer glues many ASR turns into one paragraph, so most
    // inflated words are interior. Restricting the repair to the last word of a
    // block removed only 12% of the archive's fabricated overlap.
    const blocks: OverlapBlock[] = [
      {
        id: "paragraph",
        speaker: "ana",
        speakerLabel: "Ana",
        startMs: 1000,
        endMs: 30_000,
        words: [
          ...ordinaryWords(1000, 20),
          { text: "dentistry.", startMs: 5800, endMs: 14_000 },
          ...ordinaryWords(14_000, 40),
        ],
      },
    ];

    expect(repairTurnFinalWordInflation(blocks)[0]?.words?.[20]?.endMs).toBe(6800);
  });

  it("does not let a speaker's only word set the budget that judges it", () => {
    // Ben says one thing all meeting: an inflated "Yeah." claiming 2.96 s.
    // Without the guard his median IS 2.96 s, his budget 11.8 s, and the word
    // escapes for ever.
    const blocks: OverlapBlock[] = [
      {
        id: "ana",
        speaker: "ana",
        speakerLabel: "Ana",
        startMs: 0,
        endMs: 12_000,
        words: ordinaryWords(0, 50),
      },
      {
        id: "ben",
        speaker: "ben",
        speakerLabel: "Ben",
        startMs: 12_000,
        endMs: 14_960,
        words: [{ text: "Yeah.", startMs: 12_000, endMs: 14_960 }],
      },
    ];

    expect(repairTurnFinalWordInflation(blocks)[1]?.words?.[0]?.endMs).toBe(13_000);
  });

  it("does not let a speaker's own sentence endings raise the budget that judges them", () => {
    // Ben has plenty of words, but half of them are inflated sentence endings.
    // Counting those in his reference median gives 1620 ms, a 6.5 s budget, and
    // not one of them is ever clipped.
    const words = [];
    for (let index = 0; index < 5; index += 1) {
      words.push({ text: `word${index}`, startMs: index * 4000, endMs: index * 4000 + 240 });
      words.push({ text: `end${index}.`, startMs: index * 4000 + 240, endMs: index * 4000 + 3240 });
    }
    const blocks: OverlapBlock[] = [
      { id: "ben", speaker: "ben", speakerLabel: "Ben", startMs: 0, endMs: 19_240, words },
    ];
    const repaired = repairTurnFinalWordInflation(blocks)[0]?.words ?? [];

    expect(repaired.filter((word) => word.text.endsWith(".")).map((word) => word.endMs)).toEqual([
      1240, 5240, 9240, 13_240, 17_240,
    ]);
  });

  it("falls back to the meeting median when a speaker has too few reference words", () => {
    // Ben has only two words that are not sentence endings, and both happen to
    // be long. Trusting a two-word median would give him a 6 s budget.
    const blocks: OverlapBlock[] = [
      { id: "ana", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 12_000, words: ordinaryWords(0, 50) },
      {
        id: "ben",
        speaker: "ben",
        speakerLabel: "Ben",
        startMs: 12_000,
        endMs: 17_960,
        words: [
          { text: "wellll", startMs: 12_000, endMs: 13_500 },
          { text: "hmmmm", startMs: 13_500, endMs: 15_000 },
          { text: "Yeah.", startMs: 15_000, endMs: 17_960 },
        ],
      },
    ];

    expect(repairTurnFinalWordInflation(blocks)[1]?.words?.at(-1)?.endMs).toBe(16_000);
  });

  it("clips display tokens whose punctuation is tokenised separately", () => {
    const blocks: OverlapBlock[] = [
      {
        id: "tokens",
        speaker: "ana",
        speakerLabel: "Ana",
        startMs: 1000,
        endMs: 14_000,
        tokens: [
          ...ordinaryWords(1000, 20),
          { text: "everything", startMs: 5800, endMs: 14_000 },
          { text: ".", startMs: 5800, endMs: 14_000 },
        ],
      },
    ];
    const repaired = repairTurnFinalWordInflation(blocks);

    expect(repaired[0]?.tokens?.at(-2)?.endMs).toBe(6800);
    expect(repaired[0]?.tokens?.at(-1)?.endMs).toBe(6800);
    expect(repaired[0]?.endMs).toBe(6800);
  });

  it("recognises a sentence-final word whose punctuation token carries no timing", () => {
    const blocks: OverlapBlock[] = [
      {
        id: "tokens",
        speaker: "ana",
        speakerLabel: "Ana",
        startMs: 1000,
        endMs: 14_000,
        tokens: [
          ...ordinaryWords(1000, 20),
          { text: "everything", startMs: 5800, endMs: 14_000 },
          { text: "." },
        ],
      },
    ];

    expect(repairTurnFinalWordInflation(blocks)[0]?.tokens?.at(-2)?.endMs).toBe(6800);
  });

  it("never moves a word start, so every seek target survives the repair", () => {
    const repaired = repairTurnFinalWordInflation(fabricated);

    expect(repaired[0]?.words?.map((word) => word.startMs)).toEqual(
      fabricated[0]?.words?.map((word) => word.startMs),
    );
    expect(repaired[0]?.startMs).toBe(fabricated[0]?.startMs);
  });

  it("does not mutate the blocks, spans or text it was given", () => {
    const before = JSON.stringify(fabricated);
    const repaired = repairTurnFinalWordInflation(fabricated);

    expect(JSON.stringify(fabricated)).toBe(before);
    expect(repaired[0]).not.toBe(fabricated[0]);
    expect(repaired[0]?.words?.map((word) => word.text)).toEqual(
      fabricated[0]?.words?.map((word) => word.text),
    );
    // A block that needed nothing is handed back by identity.
    expect(repaired[1]).toBe(fabricated[1]);
  });

  it("survives an empty meeting and a block with no spans", () => {
    expect(repairTurnFinalWordInflation([])).toEqual([]);
    const bare: OverlapBlock[] = [
      { id: "bare", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 1000 },
    ];
    expect(repairTurnFinalWordInflation(bare)[0]).toBe(bare[0]);
  });
});

describe("provenance: artifacts the producer already bounded", () => {
  /**
   * The fp32 evidence that put the marker in the manifest. "held." runs 1.44 s
   * against Ana's 240 ms median, so the legacy budget — max(1000, 4 × 240) =
   * 1000 ms — would clip it to 1 s. On a modern artifact that end was MEASURED
   * against Ana's own track, and clipping it undoes the producer-side fix.
   */
  const measured: OverlapBlock[] = [
    {
      id: "ana-turn",
      speaker: "ana",
      speakerLabel: "Ana Duarte",
      startMs: 2000,
      endMs: 13_280,
      words: [...ordinaryWords(2000, 41), { text: "held.", startMs: 11_840, endMs: 13_280 }],
    },
  ];

  it("clips the 1.44 s word when nothing vouches for the artifact's word ends", () => {
    const repaired = repairTurnFinalWordInflation(measured);

    expect(repaired[0]?.words?.at(-1)).toMatchObject({ endMs: 12_840 });
    expect(repaired[0]?.endMs).toBe(12_840);
  });

  it("leaves the 1.44 s word alone when the producer bounded ends by audio", () => {
    const repaired = repairTurnFinalWordInflation(measured, { endsBoundedByAudio: true });

    expect(repaired[0]?.words?.at(-1)).toMatchObject({ text: "held.", startMs: 11_840, endMs: 13_280 });
    expect(repaired[0]?.endMs).toBe(13_280);
  });

  it("still returns a fresh array so callers cannot be handed their own input", () => {
    const repaired = repairTurnFinalWordInflation(measured, { endsBoundedByAudio: true });

    expect(repaired).not.toBe(measured);
    expect(repaired).toEqual(measured);
  });

  it("keeps the repair when the marker says the ends were NOT bounded", () => {
    const repaired = repairTurnFinalWordInflation(measured, { endsBoundedByAudio: false });

    expect(repaired[0]?.words?.at(-1)?.endMs).toBe(12_840);
  });
});

describe("untimed display tokens over timed canonical words", () => {
  /**
   * The Ivan/Chris archive shape again, but carrying the tokens a fully
   * rewritten cleanup emits. The paragraphs intersect for 14.08 s of extent
   * while their words strictly alternate; the only timing in the blocks lives
   * on the canonical words.
   */
  const rewritten: OverlapBlock[] = [
    {
      id: "ivan",
      speaker: "ivan",
      speakerLabel: "Ivan",
      startMs: 965_000,
      endMs: 983_320,
      tokens: untimedTokensFor("So the installer finished and the docs went out"),
      words: [...ordinaryWords(965_000, 12), ...ordinaryWords(972_920, 43)],
    },
    {
      id: "chris",
      speaker: "chris",
      speakerLabel: "Chris",
      startMs: 969_240,
      endMs: 1_004_600,
      tokens: untimedTokensFor("Right and then we shipped it"),
      words: [...ordinaryWords(969_240, 5), ...ordinaryWords(988_280, 68)],
    },
  ];

  it("judges audible time on the timed canonical words, not the untimed tokens", () => {
    expect(audibleIntervalsOf(rewritten[0]!)).toEqual([
      { startMs: 965_000, endMs: 967_880 },
      { startMs: 972_920, endMs: 983_240 },
    ]);
    expect(analyzeOverlap(rewritten).size).toBe(0);
  });

  it("lets a repaired canonical word shrink a block whose tokens are untimed", () => {
    const blocks: OverlapBlock[] = [
      {
        id: "ana-turn",
        speaker: "ana",
        speakerLabel: "Ana Duarte",
        startMs: 2000,
        endMs: 20_500,
        tokens: untimedTokensFor("It fixes everything."),
        words: [...ordinaryWords(2000, 41), { text: "everything.", startMs: 12_000, endMs: 20_500 }],
      },
    ];

    expect(repairTurnFinalWordInflation(blocks)[0]?.endMs).toBe(13_000);
  });

  it("keeps display-token timing when it is the only timing there is", () => {
    const block: OverlapBlock = {
      id: "cleaned",
      speaker: "ana",
      speakerLabel: "Ana",
      startMs: 1000,
      endMs: 2000,
      tokens: [{ text: "Hello", startMs: 1000, endMs: 1400 }],
      words: [{ text: "hello" }],
    };

    expect(audibleIntervalsOf(block)).toEqual([{ startMs: 1000, endMs: 1400 }]);
  });
});

describe("mixed timing: some display tokens timed, some not", () => {
  /**
   * The reviewer's reproducer, reduced to the two spans that make the point:
   * ONE display token covers the first half-second of a block whose canonical
   * words run for two and a half seconds. Choosing a pool — either pool — is
   * wrong here. Reading the tokens claims the speaker fell silent at 500 ms;
   * reading only the words would throw away timing the reader's highlight hangs
   * off. The block sounded across the whole 0–2500 ms and both pools are
   * evidence for parts of it.
   */
  it("covers the whole passage when one timed token sits over longer canonical words", () => {
    const block: OverlapBlock = {
      id: "one-token",
      speaker: "ana",
      speakerLabel: "Ana Duarte",
      startMs: 0,
      endMs: 2500,
      tokens: [
        { text: "So", startMs: 0, endMs: 500 },
        ...untimedTokensFor("we agreed to ship it on Friday"),
      ],
      words: ordinaryWords(0, 10, 250),
    };

    expect(audibleIntervalsOf(block)).toEqual([{ startMs: 0, endMs: 2500 }]);
  });

  /**
   * A realistic partially rewritten passage, the shape 30 of the 421 display
   * blocks in the nine portable meetings in this repo's export tree really have.
   * Cleanup rewrote the opening of Ana's paragraph, so those tokens carry
   * `alignment: "none"` and no times; the rest aligned word for word and is
   * timed. The canonical words cover the WHOLE paragraph, opening included —
   * they are the ASR's own record of when Ana made a noise.
   *
   * Ben's aside lands at 10.4–11.4 s, inside the opening that only the canonical
   * words account for. Judged on the timed tokens alone, Ana is not speaking
   * there at all: no overlap is reported, and the ring is off her paragraph
   * while she is audibly mid-sentence.
   */
  const partiallyRewritten: OverlapBlock[] = [
    {
      id: "ana-mixed",
      speaker: "ana",
      speakerLabel: "Ana Duarte",
      startMs: 10_000,
      endMs: 18_000,
      tokens: [
        ...untimedTokensFor("Can you all hear me"),
        { text: "let's", startMs: 12_000, endMs: 12_800 },
        { text: "start", startMs: 12_800, endMs: 13_600 },
        { text: "with", startMs: 13_600, endMs: 14_400 },
        { text: "the", startMs: 14_400, endMs: 15_200 },
        { text: "release", startMs: 15_200, endMs: 16_000 },
        { text: "notes", startMs: 16_000, endMs: 18_000 },
      ],
      words: [
        { text: "can", startMs: 10_000, endMs: 10_600 },
        { text: "you", startMs: 10_600, endMs: 11_200 },
        { text: "hear", startMs: 11_200, endMs: 11_600 },
        { text: "me", startMs: 11_600, endMs: 12_000 },
        { text: "lets", startMs: 12_000, endMs: 12_800 },
        { text: "start", startMs: 12_800, endMs: 13_600 },
        { text: "with", startMs: 13_600, endMs: 14_400 },
        { text: "the", startMs: 14_400, endMs: 15_200 },
        { text: "release", startMs: 15_200, endMs: 16_000 },
        { text: "notes", startMs: 16_000, endMs: 18_000 },
      ],
    },
    {
      id: "ben-aside",
      speaker: "ben",
      speakerLabel: "Ben Okafor",
      startMs: 10_400,
      endMs: 11_400,
      words: [
        { text: "loud", startMs: 10_400, endMs: 10_900 },
        { text: "and", startMs: 10_900, endMs: 11_150 },
        { text: "clear", startMs: 11_150, endMs: 11_400 },
      ],
    },
  ];

  it("loses no audible region of a partially rewritten passage", () => {
    expect(audibleIntervalsOf(partiallyRewritten[0]!)).toEqual([
      { startMs: 10_000, endMs: 18_000 },
    ]);
  });

  it("still reports the overlap that lands in the rewritten opening", () => {
    const analysis = analyzeOverlap(partiallyRewritten);

    expect(analysis.get("ana-mixed")?.overlapMs).toBe(1000);
    expect(analysis.get("ana-mixed")?.peers.map((peer) => peer.id)).toEqual(["ben-aside"]);
    expect(buildTranscriptRows(partiallyRewritten).map((row) => [...row.over])).toEqual([
      ["Ben Okafor"],
      ["Ana Duarte"],
    ]);
  });

  it("highlights the passage across its rewritten half as well as its timed half", () => {
    // 10.8 s is inside the rewritten opening, 13.0 s inside the aligned
    // remainder. Both are Ana talking, and the ring has to be on for both.
    for (const timeMs of [10_100, 10_800, 11_900, 13_000, 17_500]) {
      expect(getSoundingBlocks(partiallyRewritten, timeMs).map((block) => block.id)).toContain(
        "ana-mixed",
      );
    }
    expect(getSoundingBlocks(partiallyRewritten, 10_800).map((block) => block.id)).toEqual([
      "ana-mixed",
      "ben-aside",
    ]);
    // And it is genuinely bounded: nobody is sounding before the passage starts.
    expect(getSoundingBlocks(partiallyRewritten, 9000)).toEqual([]);
  });

  it("keeps the canonical coverage when the repair shrinks a mixed block", () => {
    // The block's one timed token stops at 500 ms; its canonical words run on
    // to an inflated 20.5 s that the repair clips back to 13.0 s. The envelope
    // has to follow the CLIPPED canonical end, not the token's, or the repair
    // amputates eight seconds of speech that really happened.
    const blocks: OverlapBlock[] = [
      {
        id: "ana-mixed",
        speaker: "ana",
        speakerLabel: "Ana Duarte",
        startMs: 0,
        endMs: 20_500,
        tokens: [
          { text: "So", startMs: 0, endMs: 500 },
          ...untimedTokensFor("this fixes everything."),
        ],
        words: [...ordinaryWords(0, 41), { text: "everything.", startMs: 12_000, endMs: 20_500 }],
      },
    ];

    expect(repairTurnFinalWordInflation(blocks)[0]?.endMs).toBe(13_000);
  });
});

/**
 * THE TWO POOLS CAN UNDO EACH OTHER'S REPAIR (the reviewer's D-690 blocker).
 *
 * The repair recognises an inflated word by its TERMINAL PUNCTUATION. The LLM
 * cleanup pass is the thing that takes terminal punctuation off — canonical
 * `Yeah.` is shown to the reader as `Yeah` — and the display token keeps the
 * canonical word's times, because portable.ts gives an aligned token the
 * minimum start and maximum end of the words it matched.
 *
 * So the canonical word is a candidate and gets clipped, the token is not a
 * candidate and does not, and the two now disagree by the whole fabricated
 * span. audibleIntervalsOf unions the pools, latestTimedEnd takes the later
 * end, and the inflated word walks straight back in with the invented
 * cross-speaker overlap hanging off it.
 */
describe("cross-pool repair: a clipped canonical word under an unclipped token", () => {
  /** Ten ordinary 240 ms words, so Ana's own median sets a 1000 ms budget. */
  function alignedRun(startMs: number, count: number) {
    return Array.from({ length: count }, (_, index) => ({
      id: `ana-w${index}`,
      text: `word${index}`,
      startMs: startMs + index * 240,
      endMs: startMs + (index + 1) * 240,
    }));
  }

  /** The display token cleanup produced from one canonical word. */
  function tokenFor(word: { id: string; text: string; startMs: number; endMs: number }, text = word.text) {
    return {
      text,
      startMs: word.startMs,
      endMs: word.endMs,
      sourceWordIds: [word.id],
      alignment: "source" as const,
    };
  }

  const canonicalRun = alignedRun(0, 10);
  // The artifact: canonical `Yeah.` stamped at the NEXT onset, 8.5 s long.
  const inflated = { id: "ana-yeah", text: "Yeah.", startMs: 12_000, endMs: 20_500 };

  const blocks: OverlapBlock[] = [
    {
      id: "ana-turn",
      speaker: "ana",
      speakerLabel: "Ana Duarte",
      startMs: 0,
      endMs: 20_500,
      // Cleanup stripped the full stop: the TOKEN reads `Yeah`, so nothing in
      // the token pool marks it as a repair candidate.
      tokens: [...canonicalRun.map((word) => tokenFor(word)), tokenFor(inflated, "Yeah")],
      words: [...canonicalRun, inflated],
    },
    {
      id: "ben-aside",
      speaker: "ben",
      speakerLabel: "Ben Okafor",
      startMs: 15_000,
      endMs: 16_000,
      words: [{ id: "ben-w0", text: "sure", startMs: 15_000, endMs: 16_000 }],
    },
  ];

  it("fabricates the overlap before the repair, so the fixture is real", () => {
    const analysis = analyzeOverlap(blocks);

    expect(analysis.get("ana-turn")?.overlapMs).toBe(1000);
    expect(analysis.get("ben-aside")?.peers.map((peer) => peer.id)).toEqual(["ana-turn"]);
  });

  it("pulls the punctuation-free token back to the canonical word it came from", () => {
    const repaired = repairTurnFinalWordInflation(blocks);

    expect(repaired[0]?.words?.at(-1)?.endMs).toBe(13_000);
    // The token has no punctuation of its own to be judged on. It is bounded
    // because its times were never its own in the first place.
    expect(repaired[0]?.tokens?.at(-1)?.endMs).toBe(13_000);
    expect(repaired[0]?.tokens?.at(-1)?.startMs).toBe(12_000);
    expect(repaired[0]?.endMs).toBe(13_000);
  });

  it("keeps the fabricated span out of the audible evidence", () => {
    const repaired = repairTurnFinalWordInflation(blocks);

    expect(audibleIntervalsOf(repaired[0]!)).toEqual([
      { startMs: 0, endMs: 2400 },
      { startMs: 12_000, endMs: 13_000 },
    ]);
  });

  it("reports no overlap once the token is bounded by its canonical word", () => {
    expect(analyzeOverlap(repairTurnFinalWordInflation(blocks)).size).toBe(0);
  });

  it("takes the ring off Ana through the silence her inflated word invented", () => {
    const repaired = repairTurnFinalWordInflation(blocks);

    expect(getSoundingBlocks(repaired, 15_500).map((block) => block.id)).toEqual(["ben-aside"]);
    expect(getSoundingBlocks(repaired, 12_500).map((block) => block.id)).toEqual(["ana-turn"]);
  });

  it("leaves a token alone when the canonical word under it was never clipped", () => {
    const honest = { id: "ana-yes", text: "Yes.", startMs: 12_000, endMs: 12_600 };
    const repaired = repairTurnFinalWordInflation([
      {
        id: "ana-turn",
        speaker: "ana",
        speakerLabel: "Ana Duarte",
        startMs: 0,
        endMs: 12_600,
        tokens: [...canonicalRun.map((word) => tokenFor(word)), tokenFor(honest, "Yes")],
        words: [...canonicalRun, honest],
      },
    ]);

    expect(repaired[0]?.tokens?.at(-1)?.endMs).toBe(12_600);
    expect(repaired[0]?.endMs).toBe(12_600);
  });

  it("leaves a token alone when it names no canonical word this block carries", () => {
    // A rewritten token names nothing, and a stale one names a word that is not
    // here. Neither can be bounded, and neither is silently dropped.
    const repaired = repairTurnFinalWordInflation([
      {
        id: "ana-turn",
        speaker: "ana",
        speakerLabel: "Ana Duarte",
        startMs: 0,
        endMs: 20_500,
        tokens: [
          ...canonicalRun.map((word) => tokenFor(word)),
          { text: "Yeah", startMs: 12_000, endMs: 20_500, sourceWordIds: ["not-in-this-block"] },
        ],
        words: canonicalRun,
      },
    ]);

    expect(repaired[0]?.tokens?.at(-1)?.endMs).toBe(20_500);
  });
});

/**
 * INTERPOLATED TOKEN TIMING IS NOT ACOUSTIC EVIDENCE (the reviewer's D-690
 * blocker).
 *
 * When cleanup rewrites a run of words, portable.ts spreads the rewritten
 * tokens evenly between the two aligned neighbours around them and marks them
 * `alignment: "interpolated"`. The interval they are spread across is exactly
 * the stretch where this block's own aligned tokens are silent — so it holds
 * the block's pauses, and anything anybody else said in them.
 */
describe("interpolated token spans are excluded from audible evidence", () => {
  /**
   * Ana says one aligned word, cleanup rewrote the middle of her paragraph, and
   * she says one more aligned word 5.6 s later. Ben has the floor for two of
   * those seconds. The interpolated tokens cover the whole gap regardless.
   */
  function anaBlock(alignment: "interpolated" | "source"): OverlapBlock {
    return {
      id: "ana-mixed",
      speaker: "ana",
      speakerLabel: "Ana Duarte",
      startMs: 10_000,
      endMs: 16_400,
      tokens: [
        {
          text: "So",
          startMs: 10_000,
          endMs: 10_400,
          sourceWordIds: ["ana-w0"],
          alignment: "source",
        },
        { text: "the", startMs: 10_400, endMs: 12_266, sourceWordIds: [], alignment },
        { text: "release", startMs: 12_266, endMs: 14_133, sourceWordIds: [], alignment },
        { text: "notes", startMs: 14_133, endMs: 16_000, sourceWordIds: [], alignment },
        {
          text: "shipped",
          startMs: 16_000,
          endMs: 16_400,
          sourceWordIds: ["ana-w1"],
          alignment: "source",
        },
      ],
      words: [
        { id: "ana-w0", text: "So", startMs: 10_000, endMs: 10_400 },
        { id: "ana-w1", text: "shipped", startMs: 16_000, endMs: 16_400 },
      ],
    };
  }

  const benTurn: OverlapBlock = {
    id: "ben-turn",
    speaker: "ben",
    speakerLabel: "Ben Okafor",
    startMs: 12_000,
    endMs: 14_000,
    words: [
      { id: "ben-w0", text: "hang", startMs: 12_000, endMs: 13_000 },
      { id: "ben-w1", text: "on", startMs: 13_000, endMs: 14_000 },
    ],
  };

  it("counts only the aligned neighbours as audible", () => {
    expect(audibleIntervalsOf(anaBlock("interpolated"))).toEqual([
      { startMs: 10_000, endMs: 10_400 },
      { startMs: 16_000, endMs: 16_400 },
    ]);
  });

  it("reports no crosstalk over the speaker the run was spread across", () => {
    expect(analyzeOverlap([anaBlock("interpolated"), benTurn]).size).toBe(0);
  });

  it("keeps the ring off Ana while Ben has the floor, and off the silence too", () => {
    const blocks = [anaBlock("interpolated"), benTurn];

    expect(getSoundingBlocks(blocks, 13_000).map((block) => block.id)).toEqual(["ben-turn"]);
    // 15 s is inside the interpolated run and inside nobody's speech at all.
    expect(getSoundingBlocks(blocks, 15_000)).toEqual([]);
    // The aligned words either side are untouched.
    expect(getSoundingBlocks(blocks, 10_200).map((block) => block.id)).toEqual(["ana-mixed"]);
    expect(getSoundingBlocks(blocks, 16_200).map((block) => block.id)).toEqual(["ana-mixed"]);
  });

  it("is the alignment kind that decides, not the numbers", () => {
    // The identical spans, marked as measured rather than invented, are
    // evidence — and then the overlap is real and must be reported.
    const measured = [anaBlock("source"), benTurn];

    expect(audibleIntervalsOf(measured[0]!)).toEqual([{ startMs: 10_000, endMs: 16_400 }]);
    expect(analyzeOverlap(measured).get("ben-turn")?.overlapMs).toBe(2000);
  });

  it("leaves the interpolated times on the tokens for rendering and seeking", () => {
    // Excluded from the audibility judgement, not deleted: the reader still
    // gets a highlightable, seekable token for every rewritten word.
    const repaired = repairTurnFinalWordInflation([anaBlock("interpolated"), benTurn]);

    expect(repaired[0]?.tokens?.map((token) => token.startMs)).toEqual([
      10_000, 10_400, 12_266, 14_133, 16_000,
    ]);
  });

  it("does not let a synthetic span hold the block envelope open", () => {
    // The repair recomputes a clipped block's envelope from its timed spans.
    // A synthetic span reaching past the clipped canonical end would hold the
    // paragraph open across the silence the clip just removed.
    const aligned = Array.from({ length: 8 }, (_, index) => ({
      id: `ana-w${index}`,
      text: `word${index}`,
      startMs: index * 240,
      endMs: (index + 1) * 240,
    }));
    const repaired = repairTurnFinalWordInflation([
      {
        id: "ana-turn",
        speaker: "ana",
        speakerLabel: "Ana Duarte",
        startMs: 0,
        endMs: 20_500,
        tokens: [
          { text: "rewritten", startMs: 2000, endMs: 20_500, sourceWordIds: [], alignment: "interpolated" as const },
        ],
        words: [...aligned, { id: "ana-yeah", text: "Yeah.", startMs: 12_000, endMs: 20_500 }],
      },
    ]);

    expect(repaired[0]?.endMs).toBe(13_000);
  });

  it("does not let a synthetic span set a speaker's clip budget", () => {
    // An interpolated run is a measurement of the gap cleanup left, not of how
    // long this speaker's words are. Ana's eight aligned 240 ms words must set
    // the budget on their own, or a 1.9 s synthetic span raises it and the
    // inflated word beside them escapes the repair.
    const aligned = Array.from({ length: 8 }, (_, index) => ({
      id: `ana-w${index}`,
      text: `word${index}`,
      startMs: index * 240,
      endMs: (index + 1) * 240,
    }));
    const repaired = repairTurnFinalWordInflation([
      {
        id: "ana-turn",
        speaker: "ana",
        speakerLabel: "Ana Duarte",
        startMs: 0,
        endMs: 28_500,
        tokens: [
          ...aligned.map((word) => ({
            text: word.text,
            startMs: word.startMs,
            endMs: word.endMs,
            sourceWordIds: [word.id],
            alignment: "source" as const,
          })),
          // The rewritten run: eight synthetic 2 s spans. Counted as reference
          // words they drag Ana's median from 240 ms to 1120 ms and the budget
          // from 1.0 s to 4.5 s, and the inflated word survives with 3.5 s of
          // fabricated tail still on it.
          ...Array.from({ length: 8 }, (_, index) => ({
            text: `rewritten${index}`,
            startMs: 2000 + index * 2000,
            endMs: 4000 + index * 2000,
            sourceWordIds: [],
            alignment: "interpolated" as const,
          })),
          { text: "Yeah.", startMs: 20_000, endMs: 28_500, sourceWordIds: [], alignment: "source" as const },
        ],
      },
    ]);

    expect(repaired[0]?.tokens?.at(-1)?.endMs).toBe(21_000);
  });
});

/**
 * A BLOCK WHOSE EVIDENCE WAS REJECTED IS NOT A WORDLESS BLOCK.
 *
 * The extent fallback is there so a genuinely untimed aside still occupies its
 * stretch of tape and can still be reported as simultaneous with somebody. A
 * block whose canonical references resolved and were thrown out as another
 * speaker's is the opposite case: it is not untimed, it is DISCREDITED, and
 * handing it its paragraph extent would rebuild exactly the false overlap and
 * false playback ring the rejection removed.
 */
describe("blocks left with no trustworthy evidence", () => {
  const discredited: OverlapBlock = {
    id: "ana-stale",
    speaker: "ana",
    speakerLabel: "Ana Duarte",
    startMs: 10_000,
    endMs: 14_000,
    referencesRejected: true,
    tokens: [
      // Timed, aligned-looking, and every canonical word behind it rejected.
      { text: "hello", startMs: 10_000, endMs: 14_000, sourceWordsRejected: true },
    ],
  };
  const benTurn: OverlapBlock = {
    id: "ben-turn",
    speaker: "ben",
    speakerLabel: "Ben Okafor",
    startMs: 11_000,
    endMs: 13_000,
    words: [{ text: "carry", startMs: 11_000, endMs: 13_000 }],
  };

  it("has no audible time at all", () => {
    expect(audibleIntervalsOf(discredited)).toEqual([]);
  });

  it("still gives a genuinely wordless block its whole extent", () => {
    // The other side of the same branch: no verdict, no timed spans, so the
    // aside keeps the extent it always had.
    const { referencesRejected: _omitted, tokens: _noTokens, ...wordless } = discredited;

    expect(audibleIntervalsOf(wordless)).toEqual([{ startMs: 10_000, endMs: 14_000 }]);
  });

  it("reports no simultaneous speech and holds no playback ring", () => {
    const blocks = [discredited, benTurn];

    expect(analyzeOverlap(blocks).size).toBe(0);
    expect(getSoundingBlocks(blocks, 12_000).map((block) => block.id)).toEqual(["ben-turn"]);
    expect(getSoundingBlocks(blocks, 10_500)).toEqual([]);
  });

  it("is not groupable as the half of a turn something landed inside", () => {
    // The A/B/A pass runs on EXTENTS, so it is the one place a discredited
    // block could still make a claim. Here the FIRST half of the interrupted
    // turn is the discredited one: "Ana never stopped talking" cannot be said
    // about a block with nothing to say it from.
    const [first, middle, last] = overlapAndPauseSegments;
    const sandwich: OverlapBlock[] = [{ ...first!, referencesRejected: true }, middle!, last!];
    const analysis = analyzeOverlap(sandwich);

    expect(analysis.get("s2")?.interrupts).toBeUndefined();
    expect(analysis.get("s3")?.resumes).toBeUndefined();
    // And no row on the page adopts it as a chip: it reads as its own turn.
    expect(buildTranscriptRows(sandwich).flatMap(chipsIn)).toEqual([]);
  });

  it("still groups the same three turns when all three are real", () => {
    // The positive control: the guard must not have cost the feature.
    const sandwich = overlapAndPauseSegments.slice(0, 3);
    const analysis = analyzeOverlap(sandwich);

    expect(analysis.get("s2")?.interrupts).toMatchObject({ beforeId: "s1", afterId: "s3" });
    expect(buildTranscriptRows(sandwich).map(blockIdsIn)).toEqual([["s1", "s2", "s3"]]);
  });
});

describe("containment", () => {
  it("does not claim a whole turn happened during a peer it merely sits inside", () => {
    // Ben's paragraph is bracketed by Ana's, but only 0.2 s of their words
    // sound together — the rest of Ben's turn lands in Ana's silences.
    const blocks: OverlapBlock[] = [
      {
        id: "ana",
        speaker: "ana",
        speakerLabel: "Ana Duarte",
        startMs: 1000,
        endMs: 30_000,
        words: [...ordinaryWords(1000, 20), ...ordinaryWords(14_800, 64)],
      },
      {
        id: "ben",
        speaker: "ben",
        speakerLabel: "Ben Okafor",
        startMs: 5600,
        endMs: 14_720,
        words: ordinaryWords(5600, 38),
      },
    ];
    const analysis = analyzeOverlap(blocks);

    // Ben's extent sits wholly inside Ana's, but 0.2 s of his 9.1 s of speech
    // sounds at the same time as hers — the rest lands in her silences.
    expect(analysis.get("ben")?.overlapMs).toBe(200);
    expect(analysis.get("ben")?.containedIn).toBeUndefined();
    expect(buildTranscriptRows(blocks).map((row) => [row.speakerLabel, [...row.over]])).toEqual([
      ["Ana Duarte", ["Ben Okafor"]],
      ["Ben Okafor", ["Ana Duarte"]],
    ]);
  });

  it("still names the turn a genuine backchannel happened during", () => {
    const blocks: OverlapBlock[] = [
      {
        id: "long",
        speaker: "ana",
        speakerLabel: "Ana Duarte",
        startMs: 1000,
        endMs: 21_000,
        words: ordinaryWords(1000, 80),
      },
      {
        id: "backchannel",
        speaker: "ben",
        speakerLabel: "Ben Okafor",
        startMs: 8000,
        endMs: 8600,
        words: [{ text: "Right.", startMs: 8000, endMs: 8600 }],
      },
    ];

    expect(analyzeOverlap(blocks).get("backchannel")?.containedIn).toBe("long");
  });

  it("names the containing turn even when both blocks start on the same millisecond", () => {
    // Sorting puts the SHORTER block first, so extent containment never fired
    // in this direction at all.
    const blocks: OverlapBlock[] = [
      {
        id: "long",
        speaker: "ana",
        speakerLabel: "Ana Duarte",
        startMs: 1000,
        endMs: 21_000,
        words: ordinaryWords(1000, 80),
      },
      {
        id: "backchannel",
        speaker: "ben",
        speakerLabel: "Ben Okafor",
        startMs: 1000,
        endMs: 1600,
        words: [{ text: "Right.", startMs: 1000, endMs: 1600 }],
      },
    ];

    expect(analyzeOverlap(blocks).get("backchannel")?.containedIn).toBe("long");
  });
});

describe("playback highlighting", () => {
  const paragraphs: OverlapBlock[] = [
    {
      id: "ivan",
      speaker: "ivan",
      speakerLabel: "Ivan",
      startMs: 965_000,
      endMs: 983_320,
      words: [...ordinaryWords(965_000, 12), ...ordinaryWords(972_920, 43)],
    },
    {
      id: "chris",
      speaker: "chris",
      speakerLabel: "Chris",
      startMs: 969_240,
      endMs: 1_004_600,
      words: [...ordinaryWords(969_240, 5), ...ordinaryWords(988_280, 68)],
    },
  ];

  it("highlights only the speaker who is really sounding inside two overlapping extents", () => {
    // 975 s falls inside both paragraph extents. Only Ivan's words are there.
    expect(getSoundingBlocks(paragraphs, 975_000).map((block) => block.id)).toEqual(["ivan"]);
    // 990 s is inside Ivan's extent too, but Ivan stopped at 983.24 s.
    expect(getSoundingBlocks(paragraphs, 990_000).map((block) => block.id)).toEqual(["chris"]);
  });

  it("highlights both speakers while both are genuinely audible", () => {
    const together: OverlapBlock[] = [
      {
        id: "ana",
        speaker: "ana",
        speakerLabel: "Ana",
        startMs: 1000,
        endMs: 10_000,
        words: ordinaryWords(1000, 37),
      },
      {
        id: "ben",
        speaker: "ben",
        speakerLabel: "Ben",
        startMs: 3000,
        endMs: 3500,
        words: [{ text: "Right.", startMs: 3000, endMs: 3500 }],
      },
    ];

    expect(getSoundingBlocks(together, 2999).map((block) => block.id)).toEqual(["ana"]);
    expect(getSoundingBlocks(together, 3200).map((block) => block.id)).toEqual(["ana", "ben"]);
    expect(getSoundingBlocks(together, 3500).map((block) => block.id)).toEqual(["ana"]);
  });

  it("orders the sounding blocks by start time rather than input order", () => {
    const shuffled: OverlapBlock[] = [
      {
        id: "ben",
        speaker: "ben",
        speakerLabel: "Ben",
        startMs: 3000,
        endMs: 3500,
        words: [{ text: "Right.", startMs: 3000, endMs: 3500 }],
      },
      {
        id: "ana",
        speaker: "ana",
        speakerLabel: "Ana",
        startMs: 1000,
        endMs: 10_000,
        words: ordinaryWords(1000, 37),
      },
    ];

    expect(getSoundingBlocks(shuffled, 3200).map((block) => block.id)).toEqual(["ana", "ben"]);
  });

  it("returns nothing when nobody is sounding", () => {
    expect(getSoundingBlocks(paragraphs, 985_000)).toEqual([]);
  });

  it("keeps a wordless block highlightable across its whole extent", () => {
    const wordless: OverlapBlock[] = [
      { id: "aside", speaker: "ana", speakerLabel: "Ana", startMs: 1000, endMs: 6000, words: [] },
    ];

    expect(getSoundingBlocks(wordless, 4000).map((block) => block.id)).toEqual(["aside"]);
  });

  it("holds the highlight across the gaps between a paragraph's own words", () => {
    const gappy: OverlapBlock[] = [
      {
        id: "ana",
        speaker: "ana",
        speakerLabel: "Ana",
        startMs: 1000,
        endMs: 9000,
        words: [
          { text: "So", startMs: 1000, endMs: 1240 },
          // A 300 ms breath, then the paragraph continues.
          { text: "anyway", startMs: 1540, endMs: 1900 },
          // Three seconds of real silence: the speaker has stopped.
          { text: "right.", startMs: 4900, endMs: 5300 },
        ],
      },
    ];

    expect(getSoundingBlocks(gappy, 1400).map((block) => block.id)).toEqual(["ana"]);
    expect(getSoundingBlocks(gappy, 3000)).toEqual([]);
  });
});
describe("the turn model on a shredded transcript", () => {
  /**
   * Ground truth, from the scenario and the TTS manifest that rendered the
   * audio (`harness/scenarios/overlap-and-pause.v1.json`):
   *
   *   Cara  41.00–48.98  one sentence
   *   Ben   43.20–48.96  a different, competing sentence
   *
   * Neither is backchannelling; both are producing whole sentences at the same
   * time. The producer emitted that as 31 alternating fragments of one to three
   * words. Every assertion below is about the structure a renderer is handed,
   * not about what a helper returns.
   */
  const model = buildTurnModel(shreddedDoubleTalkSegments);

  /** What one turn says, as the reader would read it. */
  function spoken(turn: { blocks: readonly OverlapBlock[] }): string {
    return turn.blocks.flatMap((block) => (block.words ?? []).map((word) => word.text)).join(" ");
  }

  function turnsBetween(startMs: number, endMs: number) {
    return model.turns.filter((turn) => turn.startMs >= startMs && turn.startMs < endMs);
  }

  it("yields exactly one coherent turn per speaker across the double talk", () => {
    const collision = turnsBetween(40_000, 50_000);

    expect(collision.map((turn) => turn.speakerLabel)).toEqual(["Cara Lindqvist", "Ben Okafor"]);
    // Cara's sentence, whole and in order — the producer had shredded it into
    // 16 fragments ("f the" / "final" / "sign" / "off").
    expect(spoken(collision[0]!)).toBe(
      "So the only thing I still need from you is f the final sign off on the wording, because once it goes out, we cannot quietly edit it afterwards.",
    );
    expect(collision[0]?.blocks).toHaveLength(16);
    // Ben's competing sentence, whole and in order, from his 15 fragments.
    expect(spoken(collision[1]!)).toBe(
      "Right, but hold on. I thought we agreed the wording was already settled last week when we went through it.",
    );
    expect(collision[1]?.blocks).toHaveLength(15);
    expect(collision.map((turn) => turn.rejoined)).toEqual([true, true]);
  });

  it("classifies the double talk as two competing turns, neither inside the other", () => {
    const collision = turnsBetween(40_000, 50_000);

    expect(collision.map((turn) => turn.interjections)).toEqual([[], []]);
    expect(collision.map((turn) => turn.interjectionOf)).toEqual([undefined, undefined]);
    const stretch = model.simultaneous.find(
      (candidate) => candidate.turnKeys[0] === collision[0]?.key,
    );
    expect(stretch?.kind).toBe("competing");
    expect(stretch?.speakerLabels).toEqual(["Cara Lindqvist", "Ben Okafor"]);
  });

  it("measures the collision between the turns rather than between the fragments", () => {
    // 5.1 s of Cara's turn and Ben's are on the recording at once, across nine
    // separate stretches of tape. Measured fragment by fragment most of those
    // 31 pairs fall under the 150 ms credibility floor and would be discarded.
    const stretch = model.simultaneous[0]!;

    expect(stretch.speakerLabels).toEqual(["Cara Lindqvist", "Ben Okafor"]);
    expect(stretch.totalMs).toBeGreaterThan(5000);
    expect(stretch.totalMs).toBeLessThan(5300);
    expect(stretch.intervals.length).toBeGreaterThan(1);
    // The intervals are the tape itself: ascending, disjoint, and inside the
    // stretch both speakers were talking.
    expect(stretch.intervals.every((interval) => interval.endMs > interval.startMs)).toBe(true);
    expect(
      stretch.intervals.every(
        (interval, index) => index === 0 || interval.startMs >= stretch.intervals[index - 1]!.endMs,
      ),
    ).toBe(true);
    expect(stretch.intervals[0]!.startMs).toBeGreaterThanOrEqual(43_260);
    expect(stretch.intervals.at(-1)!.endMs).toBeLessThanOrEqual(48_860);
  });

  it("classifies the genuine backchannels as interjections inside the turn they landed in", () => {
    const ana = model.turns[0]!;
    const cara = model.turnsByKey.get("seg_000005")!;

    expect(ana.speakerLabel).toBe("Ana Duarte");
    expect(ana.interjections.map((inner) => [inner.speakerLabel, spoken(inner)])).toEqual([
      ["Ben Okafor", "Right."],
    ]);
    expect(ana.interjections[0]?.interjectionOf).toBe("seg_000000");
    expect(ana.interjections[0]?.interjectionSeam).toEqual({
      beforeId: "seg_000000",
      afterId: "seg_000002",
    });
    expect(cara.interjections.map((inner) => [inner.speakerLabel, spoken(inner)])).toEqual([
      ["Ana Duarte", "Perfect."],
    ]);
    // And the sentence the backchannel cut in half is whole again: before the
    // fix this turn's second paragraph opened mid-clause, on "link in the
    // channel".
    expect(spoken(cara)).toBe(
      "can take that one. I will write it this afternoon and post the link in the channel well before the stand-up tomorrow.",
    );
    expect(
      model.simultaneous.filter((stretch) => stretch.kind === "interjection").map((stretch) => stretch.speakerLabels),
    ).toEqual([
      ["Cara Lindqvist", "Ana Duarte"],
      ["Ana Duarte", "Ben Okafor"],
    ]);
  });

  it("leaves the clean sequential turns alone", () => {
    const clean = ["seg_000003", "seg_000004", "seg_000009", "seg_000010", "seg_000042", "seg_000043"];

    for (const key of clean) {
      const turn = model.turnsByKey.get(key)!;
      expect(turn.blocks.map((block) => block.id)).toEqual([key]);
      expect(turn.rejoined).toBe(false);
      expect(turn.interjections).toEqual([]);
      expect(turn.interjectionOf).toBeUndefined();
    }
  });

  it("does not re-join across a real floor change", () => {
    // Ben takes the floor off Cara at 29.1 s and Cara comes back at 33.5 s,
    // 4.2 s later. That is a new turn, not the old one resuming, and Ben's
    // interruption is a turn of its own rather than something inside hers.
    const caraTurns = model.turns.filter((turn) => turn.speakerLabel === "Cara Lindqvist");

    expect(caraTurns.map((turn) => turn.key)).toEqual([
      "seg_000005",
      "seg_000009",
      "seg_000011",
      "seg_000043",
    ]);
    const benInterruption = model.turnsByKey.get("seg_000008")!;
    expect(benInterruption.blocks.map((block) => block.id)).toEqual(["seg_000008"]);
    expect(benInterruption.interjectionOf).toBeUndefined();
    expect(
      model.simultaneous.find((stretch) => stretch.turnKeys.includes("seg_000008"))?.kind,
    ).toBe("competing");
  });

  it("breaks a re-joined turn at a genuine pause in the speaker's own speech", () => {
    // The same shredded double talk, except Cara falls silent for 1.5 s in the
    // middle of it while Ben talks on. Her own words either side of the hole are
    // 1516 ms apart — past a breath by any measure, and past the producer's own
    // 1500 ms segment-gap threshold — so she yielded the floor and took it back,
    // and her turn must not be sewn together across it. Ben's fragments are
    // untouched, so his turn must stay whole: the break belongs to her alone.
    const silent = new Set(["seg_000027", "seg_000029", "seg_000031", "seg_000033"]);
    const paused = buildTurnModel(
      shreddedDoubleTalkSegments.filter((block) => !silent.has(block.id)),
    );
    const collision = paused.turns.filter(
      (turn) => turn.startMs >= 40_000 && turn.startMs < 50_000,
    );

    expect(collision.map((turn) => [turn.speakerLabel, turn.key])).toEqual([
      ["Cara Lindqvist", "seg_000011"],
      ["Ben Okafor", "seg_000012"],
      ["Cara Lindqvist", "seg_000035"],
    ]);
    expect(spoken(collision[0]!)).toBe(
      "So the only thing I still need from you is f the final sign off on the wording,",
    );
    expect(spoken(collision[2]!)).toBe("cannot quietly edit it afterwards.");
  });

  it("keeps every block exactly once, with its own id, words and order", () => {
    const everyTurn = [...model.turns, ...model.turns.flatMap((turn) => turn.interjections)];
    const seen = everyTurn.flatMap((turn) => turn.blocks.map((block) => block.id));

    expect([...seen].sort()).toEqual(shreddedDoubleTalkSegments.map((block) => block.id).sort());
    expect(seen).toHaveLength(shreddedDoubleTalkSegments.length);
    // A turn never reorders or rewrites what is inside it: each block is the
    // very object the caller passed in, and blocks run in time order.
    const byId = new Map(shreddedDoubleTalkSegments.map((block) => [block.id, block]));
    expect(everyTurn.every((turn) => turn.blocks.every((block) => byId.get(block.id) === block))).toBe(
      true,
    );
    expect(
      everyTurn.every((turn) =>
        turn.blocks.every(
          (block, index) => index === 0 || block.startMs >= turn.blocks[index - 1]!.startMs,
        ),
      ),
    ).toBe(true);
    // And every block can be taken back to the turn it belongs to.
    expect(model.turnKeyByBlockId.get("seg_000025")).toBe("seg_000011");
    expect(model.turnKeyByBlockId.get("seg_000001")).toBe("seg_000001");
    expect(model.turnKeyByBlockId.size).toBe(shreddedDoubleTalkSegments.length);
  });

  it("re-joins a legacy artifact only after the inflated word end is repaired", () => {
    // The pre-fix producer stamped a trailing punctuation token at the NEXT
    // acoustic onset, so "wording," runs 1.6 s past where Cara stopped saying
    // it. That fabricated tail swallows her next fragment and the seam between
    // her own two halves goes to -1.4 s: unrepaired, her turn falls apart at
    // exactly the wrong place.
    const legacy = shreddedDoubleTalkSegments.map((block) =>
      block.id === "seg_000025"
        ? {
            ...block,
            endMs: 47_000,
            words: (block.words ?? []).map((word, index) =>
              index === (block.words ?? []).length - 1 ? { ...word, endMs: 47_000 } : word,
            ),
          }
        : block,
    );
    const caraTurns = (blocks: OverlapBlock[]) =>
      buildTurnModel(blocks)
        .turns.filter(
          (turn) =>
            turn.speakerLabel === "Cara Lindqvist" &&
            turn.startMs >= 40_000 &&
            turn.startMs < 50_000,
        )
        .map((turn) => turn.key);

    expect(caraTurns(legacy)).toEqual(["seg_000011", "seg_000027"]);
    expect(caraTurns(repairTurnFinalWordInflation(legacy))).toEqual(["seg_000011"]);
    // This fixture's own ends are already measured, so the repair is a no-op on
    // it and the marker changes nothing: the turns come out the same either way.
    expect(caraTurns(repairTurnFinalWordInflation(shreddedDoubleTalkSegments))).toEqual([
      "seg_000011",
    ]);
    expect(
      caraTurns(
        repairTurnFinalWordInflation(shreddedDoubleTalkSegments, { endsBoundedByAudio: true }),
      ),
    ).toEqual(["seg_000011"]);
  });

  it("takes no notice of the order the producer handed the blocks over in", () => {
    const shuffled = buildTurnModel([...shreddedDoubleTalkSegments].reverse());

    expect(shuffled.turns.map((turn) => turn.key)).toEqual(model.turns.map((turn) => turn.key));
    expect(shuffled.turns.map((turn) => turn.blocks.map((block) => block.id))).toEqual(
      model.turns.map((turn) => turn.blocks.map((block) => block.id)),
    );
  });

  it("maps every sounding fragment to its stable rendered row", () => {
    const rows = buildTranscriptRows(shreddedDoubleTalkSegments);
    const byId = new Map(shreddedDoubleTalkSegments.map((block) => [block.id, block]));

    // A late fragment of Cara's shredded sentence follows the same row as its
    // opening fragment, while Ben's competing sentence follows Ben's row.
    expect(followRowKeyForBlocks(rows, [byId.get("seg_000025")!])).toBe("seg_000011");
    expect(followRowKeyForBlocks(rows, [byId.get("seg_000030")!])).toBe("seg_000012");
    // Ben's short "Right" is rendered as a chip inside Ana's row.
    expect(followRowKeyForBlocks(rows, [byId.get("seg_000001")!])).toBe("seg_000000");
    // If two top-level turns sound together, follow the earlier reading row.
    expect(
      followRowKeyForBlocks(rows, [byId.get("seg_000030")!, byId.get("seg_000025")!]),
    ).toBe("seg_000011");
  });
});

describe("turn re-joining", () => {
  it("joins a speaker's own blocks wherever their speech runs on, and only there", () => {
    // Two of Ana's blocks abut, a third opens a second later. The seam decides
    // both, and nobody else's blocks are consulted: it is a question about
    // whether ANA stopped.
    const blocks: OverlapBlock[] = [
      { id: "a1", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 1000 },
      { id: "a2", speaker: "ana", speakerLabel: "Ana", startMs: 1000, endMs: 2000 },
      { id: "a3", speaker: "ana", speakerLabel: "Ana", startMs: 3000, endMs: 4000 },
    ];
    const model = buildTurnModel(blocks);

    expect(model.turns.map((turn) => turn.blocks.map((block) => block.id))).toEqual([
      ["a1", "a2"],
      ["a3"],
    ]);
    expect(model.turns.map((turn) => turn.rejoined)).toEqual([true, false]);
    expect(model.turns[0]?.audibleMs).toBe(2000);
  });

  it("joins across the alternation, which is where a neighbour-only pass does nothing", () => {
    // The production shape: no two ADJACENT blocks in the sorted list share a
    // speaker, so merging neighbours would merge nothing. The grouping has to
    // walk each speaker's own blocks.
    const blocks: OverlapBlock[] = [
      { id: "a1", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 500 },
      { id: "b1", speaker: "ben", speakerLabel: "Ben", startMs: 400, endMs: 900 },
      { id: "a2", speaker: "ana", speakerLabel: "Ana", startMs: 500, endMs: 1000 },
      { id: "b2", speaker: "ben", speakerLabel: "Ben", startMs: 900, endMs: 1400 },
      { id: "a3", speaker: "ana", speakerLabel: "Ana", startMs: 1000, endMs: 1500 },
      { id: "b3", speaker: "ben", speakerLabel: "Ben", startMs: 1400, endMs: 1900 },
    ];
    const model = buildTurnModel(blocks);

    expect(
      blocks.every((block, index) => index === 0 || block.speaker !== blocks[index - 1]!.speaker),
    ).toBe(true);
    expect(model.turns.map((turn) => [turn.speakerLabel, turn.blocks.map((b) => b.id)])).toEqual([
      ["Ana", ["a1", "a2", "a3"]],
      ["Ben", ["b1", "b2", "b3"]],
    ]);
  });

  it("leaves a rapid but non-overlapping exchange in chronological order", () => {
    const blocks: OverlapBlock[] = [
      { id: "a1", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 200 },
      { id: "b1", speaker: "ben", speakerLabel: "Ben", startMs: 250, endMs: 450 },
      { id: "a2", speaker: "ana", speakerLabel: "Ana", startMs: 500, endMs: 700 },
      { id: "b2", speaker: "ben", speakerLabel: "Ben", startMs: 750, endMs: 950 },
      { id: "a3", speaker: "ana", speakerLabel: "Ana", startMs: 1000, endMs: 1200 },
    ];

    expect(buildTranscriptRows(blocks).flatMap(blockIdsIn)).toEqual([
      "a1",
      "b1",
      "a2",
      "b2",
      "a3",
    ]);
  });

  it("does not split a backchannel over a few milliseconds of timestamp noise", () => {
    const blocks: OverlapBlock[] = [
      { id: "a1", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 1000 },
      { id: "b", speaker: "ben", speakerLabel: "Ben", startMs: 1010, endMs: 1090 },
      { id: "a2", speaker: "ana", speakerLabel: "Ana", startMs: 1100, endMs: 2000 },
    ];
    const rows = buildTranscriptRows(blocks);

    expect(rows).toHaveLength(1);
    expect(blockIdsIn(rows[0]!)).toEqual(["a1", "b", "a2"]);
  });
});

describe("turn classification", () => {
  it("keeps a turn that weaves through the host's speech out of the host", () => {
    // Both gates have to be able to answer on their own, and here only the
    // structural one can. Ben's OWN continuous speech is 0.9 s — well inside
    // the backchannel bound, shorter than the fixture's real "Perfect." — but
    // he says it in three goes and Ana restarts between every one of them.
    // Somebody who is acknowledging your turn lands in one gap in it; somebody
    // holding the floor makes you keep restarting, which is what this is.
    const blocks: OverlapBlock[] = [
      { id: "a1", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 1000 },
      { id: "b1", speaker: "ben", speakerLabel: "Ben", startMs: 1000, endMs: 1300 },
      { id: "a2", speaker: "ana", speakerLabel: "Ana", startMs: 1300, endMs: 1600 },
      { id: "b2", speaker: "ben", speakerLabel: "Ben", startMs: 1600, endMs: 1900 },
      { id: "a3", speaker: "ana", speakerLabel: "Ana", startMs: 1900, endMs: 2200 },
      { id: "b3", speaker: "ben", speakerLabel: "Ben", startMs: 2200, endMs: 2500 },
      { id: "a4", speaker: "ana", speakerLabel: "Ana", startMs: 2500, endMs: 4000 },
    ];
    const model = buildTurnModel(blocks);

    expect(model.turns.map((turn) => [turn.speakerLabel, turn.blocks.length])).toEqual([
      ["Ana", 4],
      ["Ben", 3],
    ]);
    expect(model.turns.map((turn) => turn.interjections)).toEqual([[], []]);
    // Under the duration bound alone Ben would have been demoted to a
    // backchannel inside Ana's turn.
    expect(model.turns[1]?.audibleMs).toBeLessThan(INTERJECTION_MAX_MIDDLE_MS);
  });

  it("keeps a long contribution out of the turn it landed in, single gap or not", () => {
    // The other gate answering on its own. Ben lands in exactly ONE gap in
    // Ana's speech — structurally he looks like a backchannel — but he holds
    // the floor for four seconds while doing it. Somebody speaking for four
    // seconds is making a contribution, and demoting it to an aside inside
    // Ana's turn would lose a real turn off the page.
    const blocks: OverlapBlock[] = [
      { id: "a1", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 5000 },
      { id: "ben", speaker: "ben", speakerLabel: "Ben", startMs: 3000, endMs: 7000 },
      { id: "a2", speaker: "ana", speakerLabel: "Ana", startMs: 5000, endMs: 10_000 },
    ];
    const model = buildTurnModel(blocks);

    expect(model.turns.map((turn) => [turn.speakerLabel, turn.blocks.map((b) => b.id)])).toEqual([
      ["Ana", ["a1", "a2"]],
      ["Ben", ["ben"]],
    ]);
    expect(model.turns.map((turn) => turn.interjections)).toEqual([[], []]);
    expect(model.simultaneous.map((stretch) => stretch.kind)).toEqual(["competing"]);
    expect(model.turns[1]?.audibleMs).toBeGreaterThan(INTERJECTION_MAX_MIDDLE_MS);
  });

  it("measures the contribution on the whole run, not on the fragments it arrived in", () => {
    // The generalisation that makes the double talk come out right, isolated.
    // Ben's three fragments are 1.2 s each — every one of them is inside the
    // backchannel bound on its own — and they land in a single gap in Ana's
    // speech, so the structural test passes them too. Only reading them as the
    // 3.6 s of continuous speech they are keeps his turn a turn.
    const blocks: OverlapBlock[] = [
      { id: "a1", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 5000 },
      { id: "b1", speaker: "ben", speakerLabel: "Ben", startMs: 3000, endMs: 4200 },
      { id: "b2", speaker: "ben", speakerLabel: "Ben", startMs: 4300, endMs: 5500 },
      { id: "a2", speaker: "ana", speakerLabel: "Ana", startMs: 5000, endMs: 10_000 },
      { id: "b3", speaker: "ben", speakerLabel: "Ben", startMs: 5600, endMs: 6800 },
    ];
    const model = buildTurnModel(blocks);

    expect(
      blocks.every((block) => block.endMs - block.startMs <= INTERJECTION_MAX_MIDDLE_MS || block.speaker === "ana"),
    ).toBe(true);
    expect(model.turns.map((turn) => [turn.speakerLabel, turn.blocks.map((b) => b.id)])).toEqual([
      ["Ana", ["a1", "a2"]],
      ["Ben", ["b1", "b2", "b3"]],
    ]);
    expect(model.turns.map((turn) => turn.interjections)).toEqual([[], []]);
    expect(model.turns[1]?.audibleMs).toBe(3600);
  });

  it("only folds in a remark that landed at the seam the turn was cut at", () => {
    // Nesting undoes a SPLIT. Ben's remark here lands deep inside Ana's second
    // fragment, 700 ms clear of either edge of it, so it is not what cut her
    // turn — the producer would have cut around it if it had been. It is
    // reported as two people speaking at once instead of being folded in.
    const blocks: OverlapBlock[] = [
      { id: "a1", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 1000 },
      { id: "a2", speaker: "ana", speakerLabel: "Ana", startMs: 1300, endMs: 2000 },
      { id: "ben", speaker: "ben", speakerLabel: "Ben", startMs: 1400, endMs: 1600 },
      { id: "a3", speaker: "ana", speakerLabel: "Ana", startMs: 2300, endMs: 5000 },
    ];
    const model = buildTurnModel(blocks);

    expect(model.turns.map((turn) => [turn.speakerLabel, turn.blocks.map((b) => b.id)])).toEqual([
      ["Ana", ["a1", "a2", "a3"]],
      ["Ben", ["ben"]],
    ]);
    expect(model.turns[0]?.interjections).toEqual([]);
    expect(model.simultaneous.map((stretch) => stretch.kind)).toEqual(["competing"]);
  });

  it("keeps a backchannel inside a backchannel as a turn of its own, not a lost block", () => {
    // Ana's turn is split around Ben's, and Ben's is split around Cara's. Ben
    // is an interjection inside Ana; Cara's best host is Ben, who is himself an
    // interjection. Two levels of nesting is not a shape a reader can parse,
    // and a renderer that only draws a turn's own interjections would drop Cara
    // off the page altogether.
    const blocks: OverlapBlock[] = [
      { id: "ana1", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 1000 },
      { id: "ben1", speaker: "ben", speakerLabel: "Ben", startMs: 1000, endMs: 1100 },
      { id: "cara", speaker: "cara", speakerLabel: "Cara", startMs: 1100, endMs: 1200 },
      { id: "ben2", speaker: "ben", speakerLabel: "Ben", startMs: 1200, endMs: 1300 },
      { id: "ana2", speaker: "ana", speakerLabel: "Ana", startMs: 1300, endMs: 4000 },
    ];
    const model = buildTurnModel(blocks);

    expect(model.turns.map((turn) => [turn.speakerLabel, turn.blocks.map((block) => block.id)])).toEqual([
      ["Ana", ["ana1", "ana2"]],
      ["Cara", ["cara"]],
    ]);
    expect(model.turns[0]?.interjections.map((inner) => inner.blocks.map((block) => block.id))).toEqual([
      ["ben1", "ben2"],
    ]);
    expect(model.turnKeyByBlockId.size).toBe(blocks.length);
  });

  it("gives a block with no defensible audible time a turn of its own", () => {
    // A block whose canonical references resolved and were all rejected has no
    // evidence it sounded at all. It cannot be re-joined into somebody's turn
    // ("she never stopped" is a claim about sound), it cannot be an
    // interjection, and it cannot host one.
    const blocks: OverlapBlock[] = [
      { id: "a1", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 2000 },
      { id: "ghost", speaker: "ben", speakerLabel: "Ben", startMs: 1900, endMs: 2100, referencesRejected: true },
      { id: "a2", speaker: "ana", speakerLabel: "Ana", startMs: 2000, endMs: 4000 },
    ];
    const model = buildTurnModel(blocks);

    expect(model.turns.map((turn) => turn.blocks.map((block) => block.id))).toEqual([
      ["a1", "a2"],
      ["ghost"],
    ]);
    expect(model.turns[0]?.interjections).toEqual([]);
    expect(model.simultaneous).toEqual([]);
  });

  it("does not let a real block join a discredited one's turn either", () => {
    // The same rule from the other side. The discredited block comes FIRST, and
    // Ana's real block opens the instant its extent ends. Joining them would
    // hand the discredited block a place inside a turn that sounded, which is
    // exactly the standing it was denied.
    const blocks: OverlapBlock[] = [
      { id: "ghost", speaker: "ana", speakerLabel: "Ana", startMs: 0, endMs: 2000, referencesRejected: true },
      { id: "ben", speaker: "ben", speakerLabel: "Ben", startMs: 1900, endMs: 2100 },
      { id: "a2", speaker: "ana", speakerLabel: "Ana", startMs: 2000, endMs: 4000 },
    ];
    const model = buildTurnModel(blocks);

    expect(model.turns.map((turn) => turn.blocks.map((block) => block.id))).toEqual([
      ["ghost"],
      ["ben"],
      ["a2"],
    ]);
    expect(model.turnsByKey.get("ghost")?.audibleMs).toBe(0);
    expect(model.turnsByKey.get("a2")?.audibleMs).toBe(2000);
  });
});
