import { describe, expect, it } from "vitest";

import {
  analyzeOverlap,
  describeOverlap,
  describeResumption,
  formatOverlapDuration,
  groupInterruptedTurns,
  repairTurnFinalWordInflation,
  sortBlocksInReadingOrder,
  type OverlapBlock,
} from "./overlap";

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
