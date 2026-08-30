import { describe, expect, it } from "vitest";
import {
  buildTranscriptIndex,
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

  // The display-transcript path is what portable meetings always take. Its
  // blocks carry tokens, not words, so the canonical words have to be gathered
  // from the source segments or the badge can never fire in the common case.
  it("judges a display block from the canonical words of its source segments", () => {
    const parsed = validateTranscriptWordsV1({
      version: "transcript.words.v1",
      media: { src: "m.webm", durationMs: 5000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "seg_1",
          speaker: "spk_1",
          startMs: 0,
          endMs: 500,
          text: "okay",
          words: [{ text: "okay", startMs: 0, endMs: 500, lowConfidenceSpeaker: true }],
        },
      ],
    });
    const index = buildTranscriptIndex(parsed);
    const canonicalById = new Map(index.segments.map((s) => [s.id, s]));
    const blockWords = ["seg_1"]
      .map((id) => canonicalById.get(id))
      .filter((s): s is NonNullable<typeof s> => Boolean(s))
      .flatMap((s) => s.words);
    expect(isLikelyCrosstalkTurn(blockWords)).toBe(true);
  });
});
