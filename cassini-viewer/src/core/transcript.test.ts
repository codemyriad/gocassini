import { describe, expect, it } from "vitest";
import {
  analyzeOverlap,
  buildTranscriptRows,
  getSoundingBlocks,
  repairTurnFinalWordInflation,
  type OverlapBlock,
} from "./overlap";
import {
  buildTranscriptIndex,
  canonicalEvidenceForBlock,
  canonicalWordsForBlock,
  judgedDisplaySegments,
  isLikelyCrosstalkTurn,
  lowConfidenceWordCount,
  getActiveSegment,
  getActiveWord,
  parseTimeHash,
  searchSegments,
  validateDisplayTranscriptV1,
  validateTranscriptWordsV1,
} from "./transcript";

const fixture = validateTranscriptWordsV1({
  version: "transcript.words.v1",
  media: {
    src: "meeting.webm",
    durationMs: 18000,
  },
  speakers: [
    { id: "spk_1", label: "Alice" },
    { id: "spk_2", label: "Bob" },
  ],
  segments: [
    {
      id: "seg_1",
      speaker: "spk_1",
      startMs: 1000,
      endMs: 4500,
      text: "Hello everyone lets start the meeting",
      words: [
        { text: "Hello", startMs: 1000, endMs: 1350 },
        { text: "everyone", startMs: 1400, endMs: 1950 },
        { text: "lets", startMs: 2100, endMs: 2350 },
      ],
    },
    {
      id: "seg_2",
      speaker: "spk_2",
      startMs: 3200,
      endMs: 6200,
      text: "Sorry I am late",
      words: [
        { text: "Sorry", startMs: 3200, endMs: 3500 },
        { text: "I", startMs: 3550, endMs: 3620 },
        { text: "am", startMs: 3630, endMs: 3740 },
        { text: "late", startMs: 3900, endMs: 4300 },
      ],
    },
  ],
});

describe("transcript core", () => {
  it("validates a transcript.words.v1 payload", () => {
    expect(fixture.media.src).toBe("meeting.webm");
    expect(fixture.segments).toHaveLength(2);
  });

  it("validates a transcript.display.v1 payload", () => {
    const display = validateDisplayTranscriptV1({
      version: "transcript.display.v1",
      media: {
        src: "meeting.webm",
        durationMs: 18000,
      },
      speakers: [
        { id: "spk_1", label: "Alice" },
        { id: "spk_2", label: "Bob" },
      ],
      blocks: [
        {
          id: "dseg_1",
          speaker: "spk_1",
          speakerLabel: "Alice",
          startMs: 1000,
          endMs: 4500,
          text: "Hello everyone.",
          sourceSegmentIds: ["seg_1"],
          wordCount: 2,
          timedWordCount: 2,
          timingCoverage: 1,
          tokens: [
            {
              text: "Hello",
              spaceBefore: false,
              kind: "word",
              sourceWordIds: ["seg_1:w0"],
              startMs: 1000,
              endMs: 1350,
              alignment: "source",
            },
            {
              text: "everyone",
              spaceBefore: true,
              kind: "word",
              sourceWordIds: ["seg_1:w1"],
              startMs: 1400,
              endMs: 1950,
              alignment: "source",
            },
            {
              text: ".",
              spaceBefore: false,
              kind: "punctuation",
              sourceWordIds: [],
              alignment: "none",
            },
          ],
        },
      ],
      sourceTranscriptVersion: "transcript.words.v1",
      sourceReadableTranscriptVersion: "transcript.readable.v1",
    });

    expect(display.blocks[0]?.tokens[1]?.text).toBe("everyone");
  });

  it("prefers the latest overlapping segment as active", () => {
    const index = buildTranscriptIndex(fixture);
    expect(getActiveSegment(index, 3400)?.id).toBe("seg_2");
    expect(getActiveWord(getActiveSegment(index, 3400), 3400)?.text).toBe("Sorry");
  });

  it("parses deep-link times and searches segment text", () => {
    const index = buildTranscriptIndex(fixture);
    expect(parseTimeHash("#t=12.5")).toBe(12500);
    expect(searchSegments(index, "late", []).map((segment) => segment.id)).toEqual(["seg_2"]);
  });
});

describe("speaker attribution provenance", () => {
  const withAttribution = (word: Record<string, unknown>) =>
    validateTranscriptWordsV1({
      version: "transcript.words.v1",
      media: { src: "meeting.webm", durationMs: 5000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "seg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 2000,
          text: "okay",
          words: [{ text: "okay", startMs: 1000, endMs: 2000, ...word }],
        },
      ],
    });

  it("keeps the attribution gap and the low-confidence flag", () => {
    const parsed = withAttribution({ attributionGapDb: 31.7, lowConfidenceSpeaker: true });
    const word = parsed.segments[0].words[0];
    expect(word.attributionGapDb).toBe(31.7);
    expect(word.lowConfidenceSpeaker).toBe(true);
  });

  // The validator rebuilds every word from a whitelist, so a field that is not
  // explicitly carried is silently dropped. That is exactly how the evidence
  // would go missing between the producer and the viewer.
  it("leaves both undefined when the producer did not measure attribution", () => {
    const word = withAttribution({}).segments[0].words[0];
    expect(word.attributionGapDb).toBeUndefined();
    expect(word.lowConfidenceSpeaker).toBeUndefined();
  });

  // A producer that always emits the keys writes null for "not measured".
  // That must degrade to "no evidence", never to a transcript that refuses
  // to load — for both fields, symmetrically.
  it("treats null attribution values as not measured instead of rejecting the transcript", () => {
    const word = withAttribution({
      attributionGapDb: null,
      lowConfidenceSpeaker: null,
    }).segments[0].words[0];
    expect(word.attributionGapDb).toBeUndefined();
    expect(word.lowConfidenceSpeaker).toBeUndefined();
  });

  it("accepts a negative gap, which means the speaker's own microphone won", () => {
    const word = withAttribution({ attributionGapDb: -12.4 }).segments[0].words[0];
    expect(word.attributionGapDb).toBe(-12.4);
  });

  it("rejects a non-numeric gap rather than passing it through", () => {
    expect(() => withAttribution({ attributionGapDb: "loud" })).toThrow(/attributionGapDb/);
  });

  it("rejects a non-boolean flag", () => {
    expect(() => withAttribution({ lowConfidenceSpeaker: "yes" })).toThrow(
      /lowConfidenceSpeaker/,
    );
  });

  it("carries the flag into the built index so components can read it", () => {
    const parsed = withAttribution({ attributionGapDb: 40, lowConfidenceSpeaker: true });
    const index = buildTranscriptIndex(parsed);
    const indexed = index.segments[0].words[0];
    expect(indexed.lowConfidenceSpeaker).toBe(true);
    expect(indexed.attributionGapDb).toBe(40);
  });
});

describe("crosstalk turn detection", () => {
  const w = (text: string, low?: boolean) => ({
    text,
    startMs: 0,
    endMs: 100,
    ...(low === undefined ? {} : { lowConfidenceSpeaker: low }),
  });

  it("counts only the flagged words", () => {
    expect(lowConfidenceWordCount([w("a"), w("b", true), w("c", true)])).toBe(2);
    expect(lowConfidenceWordCount([w("a"), w("b")])).toBe(0);
  });

  it("calls a turn crosstalk only when every word is flagged", () => {
    expect(isLikelyCrosstalkTurn([w("okay", true)])).toBe(true);
    expect(isLikelyCrosstalkTurn([w("okay", true), w("sure", true)])).toBe(true);
    // A real turn that merely overlaps someone louder must not be written off.
    expect(isLikelyCrosstalkTurn([w("okay", true), w("sure")])).toBe(false);
    expect(isLikelyCrosstalkTurn([w("okay"), w("sure")])).toBe(false);
  });

  it("is false for a turn with no words rather than vacuously true", () => {
    expect(isLikelyCrosstalkTurn([])).toBe(false);
  });

});

// The display-transcript path is what portable meetings always take. Its
// blocks carry tokens, not words, so the canonical words must be recovered
// through canonicalWordsForBlock — the exact function MeetingView judges each
// block with — or the badge can never fire in the common case.
describe("canonicalWordsForBlock", () => {
  const parsed = validateTranscriptWordsV1({
    version: "transcript.words.v1",
    media: { src: "m.opus", durationMs: 5000 },
    speakers: [{ id: "spk_1", label: "Alice" }],
    segments: [
      {
        id: "seg_000000",
        speaker: "spk_1",
        startMs: 0,
        endMs: 500,
        text: "okay",
        words: [
          {
            id: "seg_000000:w_0",
            text: "okay",
            startMs: 0,
            endMs: 500,
            attributionGapDb: 31.7,
            lowConfidenceSpeaker: true,
          },
        ],
      },
      {
        id: "seg_000001",
        speaker: "spk_1",
        startMs: 600,
        endMs: 1100,
        text: "sure",
        words: [{ id: "seg_000001:w_0", text: "sure", startMs: 600, endMs: 1100 }],
      },
    ],
  });
  const index = buildTranscriptIndex(parsed);

  it("resolves display tokens' sourceWordIds to canonical indexed words", () => {
    const words = canonicalWordsForBlock(index, {
      speaker: "spk_1",
      speakerLabel: "Alice",
      startMs: 0,
      endMs: 500,
      // Producer-style segment ids that do NOT exist in the canonical index —
      // the portable shape, where only the token mapping can find the words.
      sourceSegmentIds: ["seg_1"],
      tokens: [
        { sourceWordIds: ["seg_000000:w_0"] },
        { sourceWordIds: [] }, // punctuation token
      ],
    });
    expect(words.map((word) => word.id)).toEqual(["seg_000000:w_0"]);
    expect(words[0]?.attributionGapDb).toBe(31.7);
    expect(isLikelyCrosstalkTurn(words)).toBe(true);
  });

  it("counts a word once when several tokens reference it, in canonical order", () => {
    // Cleanup may reference the words in any order it likes; the block is
    // judged on when they were SPOKEN, so they come back in canonical order.
    const words = canonicalWordsForBlock(index, {
      speaker: "spk_1",
      speakerLabel: "Alice",
      startMs: 0,
      endMs: 1100,
      sourceSegmentIds: [],
      tokens: [
        { sourceWordIds: ["seg_000001:w_0"] },
        { sourceWordIds: ["seg_000001:w_0", "seg_000000:w_0"] },
      ],
    });
    expect(words.map((word) => word.id)).toEqual(["seg_000000:w_0", "seg_000001:w_0"]);
  });

  it("keeps the canonical words the rewritten half of a block aligned to nothing", () => {
    // The mixed shape: cleanup kept "sure" word for word and rewrote the rest,
    // so only one token names a canonical word. Returning that one word alone
    // discarded "okay" — half a second of speech the block really covers, and
    // half a second the overlap analysis and the playback ring are judged on.
    const words = canonicalWordsForBlock(index, {
      speaker: "spk_1",
      speakerLabel: "Alice",
      startMs: 0,
      endMs: 1100,
      sourceSegmentIds: ["seg_000000", "seg_000001"],
      tokens: [{ sourceWordIds: [] }, { sourceWordIds: ["seg_000001:w_0"] }],
    });
    expect(words.map((word) => word.id)).toEqual(["seg_000000:w_0", "seg_000001:w_0"]);
  });

  it("keeps a token-mapped word its block's segment ids can never reach", () => {
    // The portable shape: baked display blocks carry `sourceSegmentIds: []`, so
    // the token mapping is the only route to the canonical words. Unioning the
    // two mappings must not cost anything when one of them is empty.
    const words = canonicalWordsForBlock(index, {
      speaker: "spk_1",
      speakerLabel: "Alice",
      startMs: 0,
      endMs: 500,
      sourceSegmentIds: [],
      tokens: [{ sourceWordIds: ["seg_000000:w_0"] }],
    });
    expect(words.map((word) => word.id)).toEqual(["seg_000000:w_0"]);
    expect(words[0]?.attributionGapDb).toBe(31.7);
  });

  it("falls back to the source segments when tokens carry no word alignment", () => {
    const words = canonicalWordsForBlock(index, {
      speaker: "spk_1",
      speakerLabel: "Alice",
      startMs: 0,
      endMs: 500,
      sourceSegmentIds: ["seg_000000"],
      tokens: [{ sourceWordIds: [] }],
    });
    expect(words.map((word) => word.id)).toEqual(["seg_000000:w_0"]);
    expect(isLikelyCrosstalkTurn(words)).toBe(true);
  });

  it("returns no words when neither tokens nor segment ids resolve", () => {
    const words = canonicalWordsForBlock(index, {
      speaker: "spk_1",
      speakerLabel: "Alice",
      startMs: 0,
      endMs: 500,
      sourceSegmentIds: ["seg_missing"],
      tokens: [{ sourceWordIds: ["seg_missing:w_0"] }],
    });
    expect(words).toEqual([]);
    expect(isLikelyCrosstalkTurn(words)).toBe(false);
  });
});

/**
 * RESOLVABLE IS NOT COMPATIBLE (the reviewer's D-690 blocker).
 *
 * The portable path re-projects `transcript.items[]` into one SYNTHETIC segment
 * per WORD and names them `seg_%06d` (portable.ts) — the same shape the Go
 * producer names its real, many-word segments. A display block cleaned against
 * a producer pack therefore carries block-level `sourceSegmentIds` that RESOLVE
 * on the portable path, against whichever single word happens to sit at that
 * ordinal: any speaker, anywhere in the meeting.
 *
 * These words are what src/core/overlap.ts measures a block's audible spans
 * from, so one stale id is enough to put somebody else's speech into this
 * block's evidence — and the viewer then draws a simultaneous-speech badge on a
 * turn where only one person was talking.
 */
describe("canonicalWordsForBlock rejects resolvable but incompatible references", () => {
  // The portable projection: one synthetic segment per word, ids by ordinal.
  const portableIndex = buildTranscriptIndex(
    validateTranscriptWordsV1({
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 700_000 },
      speakers: [
        { id: "spk_alice", label: "Alice" },
        { id: "spk_bob", label: "Bob" },
      ],
      segments: [
        {
          id: "seg_000000",
          speaker: "spk_alice",
          startMs: 0,
          endMs: 500,
          text: "okay",
          words: [{ id: "seg_000000:w_0", text: "okay", startMs: 0, endMs: 500 }],
        },
        {
          // Bob, speaking INSIDE Alice's block extent. A stale id landing here
          // is the dangerous case: the block spans do intersect, so the sweep
          // compares them and the foreign word decides the answer.
          id: "seg_000001",
          speaker: "spk_bob",
          startMs: 1000,
          endMs: 2500,
          text: "actually",
          words: [{ id: "seg_000001:w_0", text: "actually", startMs: 1000, endMs: 2500 }],
        },
        {
          // Alice again, after Bob — the survivor in a partially stale token.
          id: "seg_000003",
          speaker: "spk_alice",
          startMs: 2600,
          endMs: 3000,
          text: "sure",
          words: [{ id: "seg_000003:w_0", text: "sure", startMs: 2600, endMs: 3000 }],
        },
        {
          // Alice again, ten minutes later — resolvable, same speaker, but
          // nowhere near the block that names it.
          id: "seg_000002",
          speaker: "spk_alice",
          startMs: 600_000,
          endMs: 600_500,
          text: "anyway",
          words: [{ id: "seg_000002:w_0", text: "anyway", startMs: 600_000, endMs: 600_500 }],
        },
      ],
    }),
  );

  const aliceBlock = {
    id: "d_alice",
    speaker: "spk_alice",
    speakerLabel: "Alice",
    startMs: 0,
    endMs: 3000,
    // The stale producer id, alongside the token mapping that really is Alice's.
    sourceSegmentIds: ["seg_000001"],
    tokens: [{ sourceWordIds: ["seg_000000:w_0"] }],
  };

  const bobBlock: OverlapBlock = {
    id: "d_bob",
    speaker: "spk_bob",
    speakerLabel: "Bob",
    startMs: 1000,
    endMs: 2500,
    words: [{ id: "seg_000001:w_0", text: "actually", startMs: 1000, endMs: 2500 }],
  };

  function overlapBlocks(): OverlapBlock[] {
    return [
      {
        id: aliceBlock.id,
        speaker: aliceBlock.speaker,
        speakerLabel: aliceBlock.speakerLabel,
        startMs: aliceBlock.startMs,
        endMs: aliceBlock.endMs,
        words: canonicalWordsForBlock(portableIndex, aliceBlock),
      },
      bobBlock,
    ];
  }

  it("does not take another speaker's word from a stale but resolvable id", () => {
    expect(canonicalWordsForBlock(portableIndex, aliceBlock).map((word) => word.id)).toEqual([
      "seg_000000:w_0",
    ]);
  });

  it("invents the crosstalk when the reference is trusted, so the fixture is real", () => {
    // The same block judged WITHOUT the compatibility check: Bob's word joins
    // Alice's evidence and the two blocks now share 1.5 s that never happened.
    const unchecked: OverlapBlock[] = [
      {
        ...bobBlock,
        id: aliceBlock.id,
        speaker: aliceBlock.speaker,
        speakerLabel: aliceBlock.speakerLabel,
        startMs: aliceBlock.startMs,
        endMs: aliceBlock.endMs,
        words: [
          { id: "seg_000000:w_0", text: "okay", startMs: 0, endMs: 500 },
          { id: "seg_000001:w_0", text: "actually", startMs: 1000, endMs: 2500 },
        ],
      },
      bobBlock,
    ];

    expect(analyzeOverlap(unchecked).get("d_alice")?.overlapMs).toBe(1500);
  });

  it("reports no simultaneous speech between the two blocks", () => {
    expect(analyzeOverlap(overlapBlocks()).size).toBe(0);
  });

  it("keeps the playback ring off the block the stale id pointed into", () => {
    expect(getSoundingBlocks(overlapBlocks(), 2000).map((block) => block.id)).toEqual(["d_bob"]);
    expect(getSoundingBlocks(overlapBlocks(), 250).map((block) => block.id)).toEqual(["d_alice"]);
  });

  it("rejects a same-speaker reference from elsewhere in the meeting", () => {
    const drifted = {
      ...aliceBlock,
      sourceSegmentIds: ["seg_000002"],
    };

    expect(canonicalWordsForBlock(portableIndex, drifted).map((word) => word.id)).toEqual([
      "seg_000000:w_0",
    ]);
    // And the consequence: the block does not light up ten minutes later.
    const blocks: OverlapBlock[] = [
      {
        id: drifted.id,
        speaker: drifted.speaker,
        speakerLabel: drifted.speakerLabel,
        startMs: drifted.startMs,
        endMs: drifted.endMs,
        words: canonicalWordsForBlock(portableIndex, drifted),
      },
    ];
    expect(getSoundingBlocks(blocks, 600_200)).toEqual([]);
  });

  it("rejects a mixed-identity pair whose labels name different people", () => {
    // One side has an id, the other only a label — the readable writer leaves
    // the field off — so the IDS cannot be compared at all. The labels can, and
    // they plainly disagree. The block carries a word of its own as well, so
    // this is a real block being judged, not the wordless-extent fallback.
    const labelOnlyBlock = {
      id: "d_labelled",
      speakerLabel: "Alice",
      startMs: 0,
      endMs: 2500,
      sourceSegmentIds: ["seg_000001"],
      tokens: [{ sourceWordIds: ["seg_000000:w_0"] }],
    };

    expect(canonicalWordsForBlock(portableIndex, labelOnlyBlock).map((word) => word.id)).toEqual([
      "seg_000000:w_0",
    ]);

    const blocks: OverlapBlock[] = [
      {
        id: labelOnlyBlock.id,
        speakerLabel: labelOnlyBlock.speakerLabel,
        startMs: labelOnlyBlock.startMs,
        endMs: labelOnlyBlock.endMs,
        words: canonicalWordsForBlock(portableIndex, labelOnlyBlock),
      },
      bobBlock,
    ];

    expect(analyzeOverlap(blocks).size).toBe(0);
    expect(getSoundingBlocks(blocks, 2000).map((block) => block.id)).toEqual(["d_bob"]);
  });

  it("takes a mixed-identity pair whose labels name the same person", () => {
    const labelOnlyBlock = {
      id: "d_labelled",
      speakerLabel: "Alice",
      startMs: 0,
      endMs: 500,
      sourceSegmentIds: ["seg_000000"],
      tokens: [] as Array<{ sourceWordIds: readonly string[] }>,
    };

    expect(canonicalWordsForBlock(portableIndex, labelOnlyBlock).map((word) => word.id)).toEqual([
      "seg_000000:w_0",
    ]);
  });

  it("reads the unknown-speaker placeholder as a missing answer, not a disagreement", () => {
    // A block with no speaker at all falls back to "Unknown speaker"; so does a
    // segment. Treating that as a different name from "Alice" would throw away
    // the block's own words on the strength of an absent field.
    const unlabelledBlock = {
      id: "d_unknown",
      speakerLabel: "Unknown speaker",
      startMs: 0,
      endMs: 500,
      sourceSegmentIds: ["seg_000000"],
      tokens: [] as Array<{ sourceWordIds: readonly string[] }>,
    };

    expect(canonicalWordsForBlock(portableIndex, unlabelledBlock).map((word) => word.id)).toEqual([
      "seg_000000:w_0",
    ]);
  });

  /**
   * The projection MeetingView actually renders and judges from. This is where
   * the two halves are joined, so it is where taking the tokens straight off
   * the block would put the rejected evidence back — the exact wiring that
   * makes the compatibility check worth anything.
   */
  it("hands the viewer a projection whose tokens went through the same filter", () => {
    const display = validateDisplayTranscriptV1({
      version: "transcript.display.v1",
      media: { src: "meeting.opus", durationMs: 700_000 },
      speakers: [
        { id: "spk_alice", label: "Alice audio" },
        { id: "spk_bob", label: "Bob" },
      ],
      blocks: [
        {
          id: "d_alice",
          speaker: "spk_alice",
          speakerLabel: "Alice audio",
          startMs: 0,
          endMs: 3000,
          text: "Okay actually",
          sourceSegmentIds: [],
          wordCount: 2,
          timedWordCount: 2,
          timingCoverage: 1,
          tokens: [
            {
              text: "Okay",
              spaceBefore: false,
              kind: "word",
              sourceWordIds: ["seg_000000:w_0"],
              startMs: 0,
              endMs: 500,
              alignment: "source",
            },
            {
              text: "actually",
              spaceBefore: true,
              kind: "word",
              sourceWordIds: ["seg_000001:w_0"],
              startMs: 1000,
              endMs: 2500,
              alignment: "source",
            },
          ],
        },
      ],
    });

    const segments = judgedDisplaySegments(portableIndex, display);

    expect(segments).toHaveLength(1);
    expect(segments[0]?.words.map((word) => word.id)).toEqual(["seg_000000:w_0"]);
    expect(segments[0]?.tokens[1]).toMatchObject({ sourceWordsRejected: true });
    // The device suffix is still stripped off the rendered name.
    expect(segments[0]?.speakerLabel).toBe("Alice");

    const blocks: OverlapBlock[] = [...segments, bobBlock];
    expect(analyzeOverlap(blocks).size).toBe(0);
    expect(getSoundingBlocks(blocks, 2000).map((block) => block.id)).toEqual(["d_bob"]);
  });

  it("still takes a source segment that really is this block's own", () => {
    // The guard must not cost the JSON-directory path anything: same speaker,
    // inside the extent, so the words come through as they always did.
    const honest = {
      ...aliceBlock,
      sourceSegmentIds: ["seg_000000"],
      tokens: [{ sourceWordIds: [] }],
    };

    expect(canonicalWordsForBlock(portableIndex, honest).map((word) => word.id)).toEqual([
      "seg_000000:w_0",
    ]);
  });

  /**
   * REJECTING THE WORD IS NOT ENOUGH — the token that named it carries its own
   * times, and audibleIntervalsOf unions the token pool with the word pool. So
   * the stale reference simply fabricates the same overlap through the other
   * pool, and holds the playback ring through it, unless the token loses its
   * acoustic vote as well.
   *
   * Same fixture: Alice's block spans 0–3000 and Bob really does speak inside
   * it, so the two block extents intersect and the sweep genuinely compares
   * them. Cleanup timed Alice's token off Bob's word, so the token reads
   * 1000–2500 — Bob's span exactly.
   */
  describe("the token that named the rejected word", () => {
    const staleTokenBlock = {
      ...aliceBlock,
      sourceSegmentIds: [],
      tokens: [
        { text: "okay", startMs: 0, endMs: 500, sourceWordIds: ["seg_000000:w_0"], alignment: "source" },
        // Aligned, timed, and pointing at somebody else's word.
        { text: "actually", startMs: 1000, endMs: 2500, sourceWordIds: ["seg_000001:w_0"], alignment: "source" },
      ],
    };

    function judged(): OverlapBlock[] {
      const evidence = canonicalEvidenceForBlock(portableIndex, staleTokenBlock);
      return [
        {
          id: staleTokenBlock.id,
          speaker: staleTokenBlock.speaker,
          speakerLabel: staleTokenBlock.speakerLabel,
          startMs: staleTokenBlock.startMs,
          endMs: staleTokenBlock.endMs,
          tokens: evidence.tokens,
          words: evidence.words,
        },
        bobBlock,
      ];
    }

    it("fabricates the overlap when the token votes, so the fixture is real", () => {
      // The tokens exactly as the display transcript carries them — which is
      // what MeetingView used to pass through.
      const unchecked: OverlapBlock[] = [
        {
          id: staleTokenBlock.id,
          speaker: staleTokenBlock.speaker,
          speakerLabel: staleTokenBlock.speakerLabel,
          startMs: staleTokenBlock.startMs,
          endMs: staleTokenBlock.endMs,
          tokens: staleTokenBlock.tokens,
          words: canonicalWordsForBlock(portableIndex, staleTokenBlock),
        },
        bobBlock,
      ];

      expect(analyzeOverlap(unchecked).get("d_alice")?.overlapMs).toBe(1500);
      expect(getSoundingBlocks(unchecked, 2000).map((block) => block.id)).toEqual([
        "d_alice",
        "d_bob",
      ]);
    });

    it("marks the token rather than dropping it, so it still renders and seeks", () => {
      const evidence = canonicalEvidenceForBlock(portableIndex, staleTokenBlock);

      expect(evidence.tokens).toHaveLength(2);
      expect(evidence.tokens[1]).toMatchObject({
        text: "actually",
        startMs: 1000,
        endMs: 2500,
        sourceWordsRejected: true,
      });
      // The honest token is handed back untouched.
      expect(evidence.tokens[0]).not.toHaveProperty("sourceWordsRejected");
      // And the artifact's own tokens are never mutated.
      expect(staleTokenBlock.tokens[1]).not.toHaveProperty("sourceWordsRejected");
    });

    it("reports no simultaneous speech once the token loses its vote", () => {
      expect(analyzeOverlap(judged()).size).toBe(0);
    });

    it("keeps the playback ring off the block through the stale token's span", () => {
      expect(getSoundingBlocks(judged(), 2000).map((block) => block.id)).toEqual(["d_bob"]);
      // Alice's own word still holds the ring where it really sounds.
      expect(getSoundingBlocks(judged(), 250).map((block) => block.id)).toEqual(["d_alice"]);
    });

    /**
     * PARTLY STALE IS STALE. A token's times are the minimum start and the
     * maximum end of ALL the canonical words it matched, so a rejected word
     * contaminates whichever end it happened to sit at — here the START, since
     * Bob's word runs 1000–2500 and Alice's survivor only opens at 2600.
     *
     * Keeping the token on the strength of the survivor and trusting the repair
     * to bound it fails twice over: boundTokensBySourceWords only ever lowers
     * the END, and on an artifact carrying `endsBoundedByAudio` the bounding
     * pass does not run at all. Both are covered below.
     */
    describe("a token with one rejected source and one accepted one", () => {
      const partlyStale = {
        ...staleTokenBlock,
        tokens: [
          {
            text: "actually sure",
            // min(Bob 1000, Alice 2600) … max(Bob 2500, Alice 3000)
            startMs: 1000,
            endMs: 3000,
            sourceWordIds: ["seg_000001:w_0", "seg_000003:w_0"],
            alignment: "source",
          },
        ],
      };

      function pipeline(tokens: unknown, endsBoundedByAudio: boolean): OverlapBlock[] {
        return repairTurnFinalWordInflation(
          [
            {
              id: partlyStale.id,
              speaker: partlyStale.speaker,
              speakerLabel: partlyStale.speakerLabel,
              startMs: partlyStale.startMs,
              endMs: partlyStale.endMs,
              tokens: tokens as OverlapBlock["tokens"],
              words: canonicalEvidenceForBlock(portableIndex, partlyStale).words,
            },
            bobBlock,
          ],
          { endsBoundedByAudio },
        );
      }

      it("takes only the accepted word, and still marks the token", () => {
        const evidence = canonicalEvidenceForBlock(portableIndex, partlyStale);

        expect(evidence.words.map((word) => word.id)).toEqual(["seg_000003:w_0"]);
        expect(evidence.tokens[0]).toMatchObject({
          text: "actually sure",
          startMs: 1000,
          endMs: 3000,
          sourceWordsRejected: true,
        });
        expect(evidence.referencesRejected).toBe(true);
      });

      for (const endsBoundedByAudio of [false, true]) {
        const marker = `endsBoundedByAudio: ${endsBoundedByAudio}`;

        it(`leaks the rejected word's start when the token still votes (${marker})`, () => {
          // The token exactly as the display transcript carries it. The legacy
          // bound cannot save this even when it runs: it would pull the END to
          // 3000, which it already is, and the start stays on Bob's word.
          const leaked = pipeline(partlyStale.tokens, endsBoundedByAudio);

          expect(analyzeOverlap(leaked).get("d_alice")?.overlapMs).toBe(1500);
          expect(getSoundingBlocks(leaked, 2000).map((block) => block.id)).toEqual([
            "d_alice",
            "d_bob",
          ]);
        });

        it(`reports nothing once the whole token loses its vote (${marker})`, () => {
          const judged = pipeline(
            canonicalEvidenceForBlock(portableIndex, partlyStale).tokens,
            endsBoundedByAudio,
          );

          expect(analyzeOverlap(judged).size).toBe(0);
          expect(getSoundingBlocks(judged, 2000).map((block) => block.id)).toEqual(["d_bob"]);
          // Alice's own accepted word still sounds where it really did.
          expect(getSoundingBlocks(judged, 2800).map((block) => block.id)).toEqual(["d_alice"]);
        });
      }
    });

    /**
     * A BLOCK WHOSE EVIDENCE WAS REJECTED IS NOT A WORDLESS BLOCK. The extent
     * fallback exists so a genuinely untimed aside still occupies its stretch
     * of tape. Applying it to a block whose references resolved and were thrown
     * out would hand the whole paragraph back as audible time and rebuild the
     * exact overlap and playback ring the rejection removed — the rejection
     * would have bought nothing.
     */
    describe("a block left with no trustworthy evidence at all", () => {
      // Only a block-level source-segment reference, and it is Bob's. There is
      // no token here to carry the verdict, which is why the block carries it.
      const segmentOnly = {
        id: "d_alice",
        speaker: "spk_alice",
        speakerLabel: "Alice",
        startMs: 0,
        endMs: 3000,
        sourceSegmentIds: ["seg_000001"],
        tokens: [] as Array<{ sourceWordIds: readonly string[] }>,
      };

      it("says so on the block, not on a token", () => {
        const evidence = canonicalEvidenceForBlock(portableIndex, segmentOnly);

        expect(evidence.words).toEqual([]);
        expect(evidence.tokens).toEqual([]);
        expect(evidence.referencesRejected).toBe(true);
      });

      for (const endsBoundedByAudio of [false, true]) {
        const marker = `endsBoundedByAudio: ${endsBoundedByAudio}`;

        it(`falls back to the paragraph extent while it looks wordless (${marker})`, () => {
          // The same block WITHOUT the verdict — indistinguishable from a
          // genuinely untimed aside, and the extent covers all of Bob's turn.
          const wordless = repairTurnFinalWordInflation(
            [
              {
                id: segmentOnly.id,
                speaker: segmentOnly.speaker,
                speakerLabel: segmentOnly.speakerLabel,
                startMs: segmentOnly.startMs,
                endMs: segmentOnly.endMs,
                words: [],
              },
              bobBlock,
            ],
            { endsBoundedByAudio },
          );

          expect(analyzeOverlap(wordless).get("d_alice")?.overlapMs).toBe(1500);
          expect(getSoundingBlocks(wordless, 2000).map((block) => block.id)).toEqual([
            "d_alice",
            "d_bob",
          ]);
        });

        it(`keeps no extent once the verdict is carried (${marker})`, () => {
          const evidence = canonicalEvidenceForBlock(portableIndex, segmentOnly);
          const judged = repairTurnFinalWordInflation(
            [
              {
                id: segmentOnly.id,
                speaker: segmentOnly.speaker,
                speakerLabel: segmentOnly.speakerLabel,
                startMs: segmentOnly.startMs,
                endMs: segmentOnly.endMs,
                words: evidence.words,
                referencesRejected: evidence.referencesRejected,
              },
              bobBlock,
            ],
            { endsBoundedByAudio },
          );

          expect(analyzeOverlap(judged).size).toBe(0);
          expect(getSoundingBlocks(judged, 2000).map((block) => block.id)).toEqual(["d_bob"]);
          expect(getSoundingBlocks(judged, 250)).toEqual([]);
        });
      }

      it("is not groupable as the interjection in an A/B/A sandwich", () => {
        // The producer's commonest shape: Bob's turn cut in half around a block
        // that landed inside it. "Bob never stopped and somebody spoke over
        // him" is a claim about sound, and this middle block has none.
        const evidence = canonicalEvidenceForBlock(portableIndex, segmentOnly);
        const sandwich: OverlapBlock[] = [
          {
            id: "d_bob_first",
            speaker: "spk_bob",
            speakerLabel: "Bob",
            startMs: 1000,
            endMs: 2500,
            words: [{ id: "seg_000001:w_0", text: "actually", startMs: 1000, endMs: 2500 }],
          },
          {
            id: "d_alice",
            speaker: "spk_alice",
            speakerLabel: "Alice",
            startMs: 2400,
            endMs: 2600,
            words: evidence.words,
            referencesRejected: evidence.referencesRejected,
          },
          {
            id: "d_bob_second",
            speaker: "spk_bob",
            speakerLabel: "Bob",
            startMs: 2500,
            endMs: 4000,
            words: [{ id: "seg_000003:w_0", text: "anyway", startMs: 2500, endMs: 4000 }],
          },
        ];
        const analysis = analyzeOverlap(sandwich);

        expect(analysis.get("d_alice")?.interrupts).toBeUndefined();
        expect(analysis.get("d_bob_second")?.resumes).toBeUndefined();
        // And the page does not adopt it as a chip inside Bob's paragraph: it
        // reads as a turn of its own, next to the one Bob never stopped.
        const rows = buildTranscriptRows(sandwich);
        expect(
          rows.map((row) => row.members.map((member) => member.kind === "speech" ? member.block.id : member.key)),
        ).toEqual([["d_bob_first", "d_bob_second"], ["d_alice"]]);
      });
    });

    it("leaves a token that names nothing, and one whose ids resolve to nothing", () => {
      // The two cases that must NOT be silenced: a rewritten token with no
      // references at all, and a token whose references are simply absent from
      // this index — 15% of the aligned tokens in the export tree.
      const unjudgeable = {
        ...staleTokenBlock,
        tokens: [
          { text: "rewritten", startMs: 100, endMs: 300, sourceWordIds: [], alignment: "none" },
          {
            text: "elsewhere",
            startMs: 300,
            endMs: 500,
            sourceWordIds: ["some-other-transcript:w_9"],
            alignment: "source",
          },
        ],
      };
      const evidence = canonicalEvidenceForBlock(portableIndex, unjudgeable);

      expect(evidence.tokens).toBe(unjudgeable.tokens);
      expect(evidence.tokens.every((token) => !("sourceWordsRejected" in token))).toBe(true);
    });
  });

  it("drops a token-named word that belongs to another speaker too", () => {
    // Baked display transcripts persist sourceWordIds, so a re-transcribed
    // canonical index can leave those stale in exactly the same way.
    const staleToken = {
      ...aliceBlock,
      sourceSegmentIds: [],
      tokens: [{ sourceWordIds: ["seg_000000:w_0", "seg_000001:w_0"] }],
    };

    expect(canonicalWordsForBlock(portableIndex, staleToken).map((word) => word.id)).toEqual([
      "seg_000000:w_0",
    ]);
  });
});
