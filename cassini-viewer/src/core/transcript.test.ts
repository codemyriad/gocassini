import { describe, expect, it } from "vitest";
import {
  buildTranscriptIndex,
  canonicalWordsForBlock,
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
      sourceSegmentIds: [],
      tokens: [{ sourceWordIds: ["seg_000000:w_0"] }],
    });
    expect(words.map((word) => word.id)).toEqual(["seg_000000:w_0"]);
    expect(words[0]?.attributionGapDb).toBe(31.7);
  });

  it("falls back to the source segments when tokens carry no word alignment", () => {
    const words = canonicalWordsForBlock(index, {
      sourceSegmentIds: ["seg_000000"],
      tokens: [{ sourceWordIds: [] }],
    });
    expect(words.map((word) => word.id)).toEqual(["seg_000000:w_0"]);
    expect(isLikelyCrosstalkTurn(words)).toBe(true);
  });

  it("returns no words when neither tokens nor segment ids resolve", () => {
    const words = canonicalWordsForBlock(index, {
      sourceSegmentIds: ["seg_missing"],
      tokens: [{ sourceWordIds: ["seg_missing:w_0"] }],
    });
    expect(words).toEqual([]);
    expect(isLikelyCrosstalkTurn(words)).toBe(false);
  });
});
