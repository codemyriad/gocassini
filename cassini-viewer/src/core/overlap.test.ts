import { describe, expect, it } from "vitest";

import {
  analyzeOverlap,
  audibleIntervalsOf,
  describeOverlap,
  describeResumption,
  formatOverlapDuration,
  getSoundingBlocks,
  groupInterruptedTurns,
  repairTurnFinalWordInflation,
  sortBlocksInReadingOrder,
  type OverlapBlock,
} from "./overlap";

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

  it("groups each interrupted turn into one reading row, keeping all three ids in order", () => {
    const rows = groupInterruptedTurns(sortBlocksInReadingOrder(overlapAndPauseSegments), analysis);
    const grouped = rows.filter((row) => row.interrupted);

    expect(grouped).toHaveLength(2);
    expect(grouped[0]?.members.map((member) => member.id)).toEqual(["s1", "s2", "s3"]);
    expect(grouped[0]?.speakerLabel).toBe("Ana Duarte");
    expect(grouped[0]?.interjectorLabel).toBe("Ben Okafor");
    expect(grouped[0]?.interjectionId).toBe("s2");
    expect(grouped[1]?.members.map((member) => member.id)).toEqual(["s6", "s7", "s8"]);
    expect(grouped[1]?.speakerLabel).toBe("Cara Lindqvist");
    expect(grouped[1]?.interjectorLabel).toBe("Ana Duarte");
  });

  it("keeps every block exactly once, in time order, across all rows", () => {
    const ordered = sortBlocksInReadingOrder(overlapAndPauseSegments);
    const rows = groupInterruptedTurns(ordered, analysis);

    expect(rows.flatMap((row) => row.members.map((member) => member.id))).toEqual(
      ordered.map((block) => block.id),
    );
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
    expect(groupInterruptedTurns(exchange, result).every((row) => !row.interrupted)).toBe(true);
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
    expect(groupInterruptedTurns(blocks, analysis)).toHaveLength(3);
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
    expect(describeOverlap(analysis.get("backchannel"))?.badge).toBe("0.6 s during Ana Duarte");
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
    expect(describeOverlap(analysis.get("a"))?.badge).toBe("3.0 s with B +1");
    expect(describeOverlap(analysis.get("a"))?.detail).toContain("Those voices are on the recording at once.");
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
    expect(groupInterruptedTurns(blocks, analyzeOverlap(blocks))).toHaveLength(3);
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
    expect(describeOverlap(analyzeOverlap(repaired).get("ben-turn"))).toBeNull();
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

describe("copy", () => {
  it("says who and how long for a partial overlap", () => {
    const analysis = analyzeOverlap(overlapAndPauseSegments);
    const description = describeOverlap(analysis.get("s9"));

    expect(description?.badge).toBe("0.2 s with Cara Lindqvist");
    expect(description?.detail).toContain("Cara Lindqvist (0.2 s)");
    expect(description?.detail).toContain("Both voices are on the recording at once.");
  });

  it("says 'during' for a turn that landed inside a continuing turn", () => {
    const analysis = analyzeOverlap(overlapAndPauseSegments);

    expect(describeOverlap(analysis.get("s2"))?.badge).toBe("0.4 s during Ana Duarte");
    expect(describeOverlap(analysis.get("s2"))?.detail).toContain("continues after it");
  });

  it("names the interjector on the half that resumes", () => {
    const analysis = analyzeOverlap(overlapAndPauseSegments);

    expect(describeResumption(analysis.get("s3"))?.badge).toBe("continues past Ben Okafor");
    expect(describeResumption(analysis.get("s2"))).toBeNull();
  });

  it("drops the decimal past ten seconds", () => {
    expect(formatOverlapDuration(600)).toBe("0.6 s");
    expect(formatOverlapDuration(9949)).toBe("9.9 s");
    expect(formatOverlapDuration(12_400)).toBe("12 s");
  });

  it("says nothing when there is nothing to say", () => {
    expect(describeOverlap(null)).toBeNull();
    expect(describeOverlap(undefined)).toBeNull();
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
    expect(describeOverlap(analysis.get("ben-aside"))?.badge).toBe("1.0 s during Ana Duarte");
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
    expect(groupInterruptedTurns(sandwich, analysis).map((row) => row.interrupted)).toEqual([
      false,
      false,
      false,
    ]);
  });

  it("still groups the same three turns when all three are real", () => {
    // The positive control: the guard must not have cost the feature.
    const sandwich = overlapAndPauseSegments.slice(0, 3);
    const analysis = analyzeOverlap(sandwich);

    expect(analysis.get("s2")?.interrupts).toMatchObject({ beforeId: "s1", afterId: "s3" });
    expect(groupInterruptedTurns(sandwich, analysis).map((row) => row.interrupted)).toEqual([true]);
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
    expect(describeOverlap(analysis.get("ben"))?.badge).toBe("0.2 s with Ana Duarte");
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
