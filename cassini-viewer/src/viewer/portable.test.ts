import { describe, expect, it } from "vitest";
import { gzipSync } from "node:zlib";
import { createHash } from "node:crypto";

import {
  buildDisplayTranscriptFromArtifacts,
  buildReadableTranscriptFromPortable,
  buildTranscriptWordsFromPortable,
  describeMeeting,
  describeTranscript,
  getDefaultTranscriptId,
  listAvailableTranscripts,
  loadPortableTranscriptBody,
  pickReadableForTranscript,
  sha256HexFallback,
  type PortableMeetingManifest,
  type PortablePayloadRef,
  type PortableTranscriptEntry,
} from "./portable";
import {
  buildTranscriptIndex,
  canonicalWordsForBlock,
  isLikelyCrosstalkTurn,
  validateDisplayTranscriptV1,
  validateTranscriptWordsV1,
} from "../core/transcript";

// Mirrors scripts/export-static-meetings.test.ts — this module keeps an
// identical copy of describeMeeting for in-browser use.
describe("describeMeeting", () => {
  it("parses timestamped meeting ids", () => {
    expect(describeMeeting("daily-meeting--2026-03-05--12:38:29")).toEqual({
      title: "Daily Meeting",
      dateLabel: "2026-03-05 12:38",
    });
  });

  it("derives the date from ULID meeting ids instead of echoing the id", () => {
    // 01KKA70QN0 encodes 2026-03-09T21:11:00Z.
    expect(describeMeeting("01KKA70QN0ABCDEFGHJKMNPQRS")).toEqual({
      title: "Untitled meeting",
      dateLabel: "2026-03-09 21:11",
    });
  });

  it("handles ULID meeting ids with stt variant suffixes", () => {
    expect(describeMeeting("01KKA70QN0ABCDEFGHJKMNPQRS--stt-whisper-large-v3")).toEqual({
      title: "Untitled meeting",
      dateLabel: "2026-03-09 21:11",
    });
  });

  it("keeps the plain-id fallback for ULID lookalikes with implausible timestamps", () => {
    expect(describeMeeting("00000000000000000000000000")).toEqual({
      title: "00000000000000000000000000",
      dateLabel: "00000000000000000000000000",
    });
  });

  it("does not treat lowercase ids as ULIDs", () => {
    // Operator job ids are always uppercase; lowercase stays a plain name.
    expect(describeMeeting("01kka70qn0abcdefghjkmnpqrs")).toEqual({
      title: "01kka70qn0abcdefghjkmnpqrs",
      dateLabel: "01kka70qn0abcdefghjkmnpqrs",
    });
  });

  it("does not treat 25- or 27-char Crockford strings as ULIDs", () => {
    expect(describeMeeting("01KKA70QN0ABCDEFGHJKMNPQR").dateLabel).toBe(
      "01KKA70QN0ABCDEFGHJKMNPQR",
    );
    expect(describeMeeting("01KKA70QN0ABCDEFGHJKMNPQRST").dateLabel).toBe(
      "01KKA70QN0ABCDEFGHJKMNPQRST",
    );
  });
});

describe("buildReadableTranscriptFromPortable", () => {
  it("accepts historical readable transcripts mislabeled as transcript.words.v1", () => {
    const portable = {
      meeting: { durationMs: 4_000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      transcript: {
        items: [
          { id: "seg_1", speaker: "spk_1", startMs: 1000, endMs: 1400, text: "um" },
          { id: "seg_2", speaker: "spk_1", startMs: 1400, endMs: 1900, text: "hello" },
          { id: "seg_3", speaker: "spk_1", startMs: 1900, endMs: 2400, text: "there" },
        ],
      },
      readableTranscript: {
        version: "transcript.words.v1",
        speakers: [{ id: "spk_1", label: "Alice" }],
        segments: [
          {
            id: "rseg_1",
            speaker: "spk_1",
            startMs: 1000,
            endMs: 2400,
            text: "Hello there.",
            sourceSegmentIds: ["seg_1", "seg_2", "seg_3"],
          },
        ],
      },
    };
    const transcript = {
      version: "transcript.words.v1" as const,
      media: { src: "meeting.opus", durationMs: 4_000, sha256: "" },
      speakers: portable.speakers,
      segments: portable.transcript.items.map((item) => ({
        ...item,
        words: [{ id: `${item.id}:w_0`, text: item.text, startMs: item.startMs, endMs: item.endMs }],
      })),
    };

    const readable = buildReadableTranscriptFromPortable(portable as never, transcript);
    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);

    expect(readable.segments[0]?.text).toBe("Hello there.");
    expect(display.blocks[0]?.text).toBe("Hello there.");
  });

  it("leaves multi-word portable transcript items passage-timed instead of faking word timing", () => {
    const portable = {
      meeting: { durationMs: 10_000 },
      speakers: [{ id: "spk_1", label: "Chris" }],
      transcript: {
        items: [
          {
            id: "seg_1",
            speaker: "spk_1",
            startMs: 1000,
            endMs: 5000,
            text: "And I think they'll be very happy with it.",
          },
        ],
      },
      readableTranscript: {
        version: "transcript.readable.v1",
        speakers: [{ id: "spk_1", label: "Chris" }],
        segments: [
          {
            id: "rseg_1",
            speaker: "spk_1",
            startMs: 1000,
            endMs: 5000,
            text: "And I think they'll be very happy with it.",
            sourceSegmentIds: ["seg_1"],
          },
        ],
      },
    };

    const transcript = buildTranscriptWordsFromPortable(portable as never);
    const readable = buildReadableTranscriptFromPortable(portable as never, transcript);
    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const timedWords = display.blocks[0]?.tokens.filter((token) => token.kind === "word" && token.startMs !== undefined);

    expect(transcript.segments[0]?.words).toEqual([]);
    expect(timedWords).toEqual([]);
    expect(display.blocks[0]?.timedWordCount).toBe(0);
    expect(display.blocks[0]?.wordCount).toBe(9);
    expect(display.blocks[0]?.timingCoverage).toBe(0);
  });

  it("recovers word timings from readable transcript words embedded in portable manifests", () => {
    const portable = {
      meeting: { durationMs: 10_000 },
      speakers: [{ id: "spk_1", label: "Chris" }],
      transcript: {
        items: [
          {
            id: "seg_1",
            speaker: "spk_1",
            startMs: 1000,
            endMs: 5000,
            text: "And I think they'll be very happy with it.",
          },
        ],
      },
      readableTranscript: {
        version: "transcript.readable.v1",
        speakers: [{ id: "spk_1", label: "Chris" }],
        segments: [
          {
            id: "rseg_1",
            speaker: "spk_1",
            startMs: 1000,
            endMs: 5000,
            text: "And I think they'll be very happy with it.",
            words: [
              { text: "And", startMs: 1000, endMs: 1200 },
              { text: "I", startMs: 1200, endMs: 1400 },
              { text: "think", startMs: 1400, endMs: 1800 },
              { text: "they'll", startMs: 1800, endMs: 2200 },
              { text: "be", startMs: 2200, endMs: 2400 },
              { text: "very", startMs: 2400, endMs: 2800 },
              { text: "happy", startMs: 2800, endMs: 3300 },
              { text: "with", startMs: 3300, endMs: 3600 },
              { text: "it.", startMs: 3600, endMs: 4000 },
            ],
          },
        ],
      },
    };

    const transcript = buildTranscriptWordsFromPortable(portable as never);
    const readable = buildReadableTranscriptFromPortable(portable as never, transcript);
    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const wordTokens = display.blocks[0]?.tokens.filter((token) => token.kind === "word") ?? [];

    expect(transcript.segments[0]?.words).toEqual([]);
    expect(display.blocks[0]?.timedWordCount).toBe(9);
    expect(wordTokens[0]).toMatchObject({ text: "And", startMs: 1000, endMs: 1200 });
    expect(wordTokens[5]).toMatchObject({ text: "very", startMs: 2400, endMs: 2800 });
  });

  it("synthesizes source word ids for transcript artifact words that omit them", () => {
    const transcript = {
      version: "transcript.words.v1" as const,
      media: { src: "meeting.opus", durationMs: 4_000, sha256: "" },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          speaker: "spk_1",
          startMs: 1000,
          endMs: 2200,
          text: "hello there",
          words: [
            { text: "hello", startMs: 1000, endMs: 1500 },
            { text: "there", startMs: 1500, endMs: 2000 },
          ],
        },
      ],
    };
    const readable = {
      version: "transcript.readable.v1" as const,
      media: { src: "meeting.opus", durationMs: 4_000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "rseg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 2200,
          text: "Hello there.",
        },
      ],
    };

    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);

    expect(display.blocks[0]?.tokens[0]).toMatchObject({
      text: "Hello",
      sourceWordIds: ["seg_000000:w_0"],
      startMs: 1000,
      endMs: 1500,
      alignment: "source",
    });
    expect(display.blocks[0]?.tokens[1]).toMatchObject({
      text: "there",
      sourceWordIds: ["seg_000000:w_1"],
      startMs: 1500,
      endMs: 2000,
      alignment: "source",
    });
    expect(display.blocks[0]?.timingCoverage).toBe(1);
  });

  it("keeps an interrupted readable block whole instead of splitting it (D-690)", () => {
    const portable = {
      meeting: { durationMs: 110_000 },
      speakers: [
        { id: "spk_chima", label: "chima" },
        { id: "spk_silvio", label: "Silvio" },
      ],
      transcript: {
        items: [
          {
            id: "seg_chima",
            speaker: "spk_chima",
            startMs: 54_981,
            endMs: 83_541,
            text: "Actually, I was wondering if I should have pinged you in the afternoon, and I didn't because I was busy doing something else. It's a pity, Chris, you're ruining everything.",
          },
          {
            id: "seg_silvio",
            speaker: "spk_silvio",
            startMs: 64_837,
            endMs: 78_757,
            text: "Telling Mattia off about homework.",
          },
        ],
      },
      readableTranscript: {
        version: "transcript.readable.v1",
        speakers: [
          { id: "spk_chima", label: "chima" },
          { id: "spk_silvio", label: "Silvio" },
        ],
        segments: [
          {
            id: "readable_000002",
            speaker: "spk_chima",
            startMs: 54_981,
            endMs: 83_541,
            text: "Actually, I was wondering if I should have pinged you in the afternoon, and I didn't because I was busy doing something else. It's a pity, Chris, you're ruining everything.",
            words: [
              "Actually,","I","was","wondering","if","I","should","have","pinged","you","in","the","afternoon,","and","I","didn't","because","I","was","busy","doing","something","else.","It's","a","pity,","Chris,","you're","ruining","everything.",
            ].map((text, index, words) => ({
              text,
              startMs: 54_981 + Math.floor(((83_541 - 54_981) * index) / words.length),
              endMs: 54_981 + Math.floor(((83_541 - 54_981) * (index + 1)) / words.length),
            })),
          },
          {
            id: "readable_000003",
            speaker: "spk_silvio",
            startMs: 64_837,
            endMs: 78_757,
            text: "Telling Mattia off about homework.",
            words: [
              { text: "Telling", startMs: 64_837, endMs: 65_470 },
              { text: "Mattia", startMs: 65_470, endMs: 66_102 },
              { text: "off", startMs: 66_102, endMs: 66_735 },
              { text: "about", startMs: 66_735, endMs: 67_368 },
              { text: "homework.", startMs: 67_368, endMs: 68_001 },
            ],
          },
        ],
      },
    };

    const transcript = buildTranscriptWordsFromPortable(portable as never);
    const readable = buildReadableTranscriptFromPortable(portable as never, transcript);
    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const chimaBlocks = display.blocks.filter((block) => block.speaker === "spk_chima");

    // The readable splitter was deleted in D-690: it never fired on real
    // producer artifacts (which carry no per-segment readable words), it
    // rewrote LLM-cleaned prose by word count, and when it did fire it emitted
    // blocks out of time order. Interruptions are now surfaced by
    // src/core/overlap.ts, which annotates the turn rather than cutting it up.
    expect(chimaBlocks).toHaveLength(1);
    expect(chimaBlocks[0]?.text).toContain("Actually, I was wondering");
    expect(chimaBlocks[0]?.text).toContain("It's a pity, Chris, you're ruining everything.");
    expect(display.blocks.map((block) => block.id)).not.toContain(
      expect.stringContaining("__split_"),
    );
    expect(display.blocks.map((block) => block.startMs)).toEqual(
      [...display.blocks.map((block) => block.startMs)].sort((left, right) => left - right),
    );
  });

  it("keeps an interrupted block whole even with exact transcript words (D-690)", () => {
    const chimaWords = [
      ["Actually,", 54_981, 55_381],
      ["I", 55_381, 55_541],
      ["was", 55_541, 55_701],
      ["wondering", 55_701, 56_182],
      ["if", 56_182, 56_342],
      ["I", 56_342, 56_582],
      ["should", 56_582, 56_822],
      ["have", 56_822, 57_062],
      ["pinged", 57_062, 57_462],
      ["you", 57_462, 57_702],
      ["in", 57_701, 57_941],
      ["the", 57_942, 58_102],
      ["afternoon,", 58_102, 58_742],
      ["and", 58_742, 58_901],
      ["I", 58_901, 59_061],
      ["didn't", 59_061, 59_462],
      ["because", 59_462, 59_861],
      ["I", 59_861, 60_022],
      ["was", 60_022, 60_261],
      ["busy", 60_261, 60_621],
      ["doing", 60_621, 61_021],
      ["something", 61_021, 61_541],
      ["else.", 61_541, 62_021],
      ["It's", 77_541, 77_861],
      ["a", 77_861, 78_021],
      ["pity,", 78_021, 78_502],
      ["Chris,", 78_501, 79_061],
      ["you're", 79_061, 79_542],
      ["ruining", 79_542, 80_102],
      ["everything.", 80_102, 80_742],
    ];
    const portable = {
      meeting: { durationMs: 110_000 },
      speakers: [
        { id: "spk_chima", label: "chima" },
        { id: "spk_silvio", label: "Silvio" },
      ],
      transcript: {
        items: [
          ...chimaWords.map(([text, startMs, endMs], index) => ({
            id: `seg_chima_${index}`,
            speaker: "spk_chima",
            startMs,
            endMs,
            text,
          })),
          {
            id: "seg_silvio",
            speaker: "spk_silvio",
            startMs: 64_837,
            endMs: 78_757,
            text: "Telling Mattia off about homework.",
          },
        ],
      },
      readableTranscript: {
        version: "transcript.readable.v1",
        speakers: [
          { id: "spk_chima", label: "chima" },
          { id: "spk_silvio", label: "Silvio" },
        ],
        segments: [
          {
            id: "readable_000002",
            speaker: "spk_chima",
            startMs: 54_981,
            endMs: 83_541,
            text: "Actually, I was wondering if I should have pinged you in the afternoon, and I didn't because I was busy doing something else. It's a pity, Chris, you're ruining everything.",
            sourceSegmentIds: chimaWords.map((_, index) => `seg_chima_${index}`),
            words: [
              "Actually,","I","was","wondering","if","I","should","have","pinged","you","in","the","afternoon,","and","I","didn't","because","I","was","busy","doing","something","else.","It's","a","pity,","Chris,","you're","ruining","everything.",
            ].map((text, index, words) => ({
              text,
              startMs: 54_981 + Math.floor(((83_541 - 54_981) * index) / words.length),
              endMs: 54_981 + Math.floor(((83_541 - 54_981) * (index + 1)) / words.length),
            })),
          },
          {
            id: "readable_000003",
            speaker: "spk_silvio",
            startMs: 64_837,
            endMs: 78_757,
            text: "Telling Mattia off about homework.",
            sourceSegmentIds: ["seg_silvio"],
          },
        ],
      },
    };

    const transcript = buildTranscriptWordsFromPortable(portable as never);
    const readable = buildReadableTranscriptFromPortable(portable as never, transcript);
    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const chimaBlocks = display.blocks.filter((block) => block.speaker === "spk_chima");

    // See the note on the previous test: the splitter was deleted in D-690, so
    // exact transcript words no longer cut the block in two either.
    expect(chimaBlocks).toHaveLength(1);
    expect(chimaBlocks[0]?.text).toContain("doing something else.");
    expect(chimaBlocks[0]?.text).toContain("It's a pity, Chris, you're ruining everything.");
    expect(chimaBlocks[0]?.startMs).toBe(54_981);
    expect(chimaBlocks[0]?.endMs).toBe(80_742);
  });

  it("leaves fully rewritten cleaned blocks untimed at the word level", () => {
    const transcript = {
      version: "transcript.words.v1" as const,
      media: { src: "meeting.opus", durationMs: 4_000, sha256: "" },
      speakers: [{ id: "spk_1", label: "Silvio" }],
      segments: [
        {
          id: "seg_1",
          speaker: "spk_1",
          startMs: 127_398,
          endMs: 128_197,
          text: "Mexicans",
          words: [
            { id: "w_1", text: "Mexicans", startMs: 127_398, endMs: 128_197 },
          ],
        },
      ],
    };
    const readable = {
      version: "transcript.readable.v1" as const,
      media: { src: "meeting.opus", durationMs: 4_000 },
      speakers: [{ id: "spk_1", label: "Silvio" }],
      segments: [
        {
          id: "rseg_1",
          speaker: "spk_1",
          startMs: 127_398,
          endMs: 128_197,
          text: "Makes sense.",
          sourceSegmentIds: ["seg_1"],
        },
      ],
    };

    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const wordTokens = display.blocks[0]?.tokens.filter((token) => token.kind === "word") ?? [];

    expect(wordTokens).toMatchObject([
      {
        text: "Makes",
        alignment: "none",
      },
      {
        text: "sense",
        alignment: "none",
      },
    ]);
    expect(wordTokens.every((token) => token.startMs === undefined && token.endMs === undefined)).toBe(true);
    expect(display.blocks[0]?.timedWordCount).toBe(0);
    expect(display.blocks[0]?.wordCount).toBe(2);
    expect(display.blocks[0]?.timingCoverage).toBe(0);
  });

  it("leaves inserted cleaned words untimed when the ASR gap has no lexical overlap", () => {
    const transcript = {
      version: "transcript.words.v1" as const,
      media: { src: "meeting.opus", durationMs: 6_000, sha256: "" },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "seg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 3500,
          text: "we should buy now",
          words: [
            { id: "w_1", text: "we", startMs: 1000, endMs: 1300 },
            { id: "w_2", text: "should", startMs: 1300, endMs: 1700 },
            { id: "w_3", text: "buy", startMs: 1700, endMs: 2200 },
            { id: "w_4", text: "now", startMs: 2700, endMs: 3200 },
          ],
        },
      ],
    };
    const readable = {
      version: "transcript.readable.v1" as const,
      media: { src: "meeting.opus", durationMs: 6_000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "rseg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 3500,
          text: "We should buy it now.",
          sourceSegmentIds: ["seg_1"],
        },
      ],
    };

    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const insertedToken = display.blocks[0]?.tokens.find((token) => token.text === "it");

    expect(insertedToken?.alignment).toBe("none");
    expect(insertedToken?.sourceWordIds).toEqual([]);
    expect(insertedToken?.startMs).toBeUndefined();
    expect(insertedToken?.endMs).toBeUndefined();
    expect(display.blocks[0]?.timedWordCount).toBe(4);
    expect(display.blocks[0]?.wordCount).toBe(5);
    expect(display.blocks[0]?.timingCoverage).toBe(0.8);
  });

  it("leaves paraphrased runs untimed when exact anchor overlap is weak", () => {
    const transcript = {
      version: "transcript.words.v1" as const,
      media: { src: "meeting.opus", durationMs: 4_000, sha256: "" },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "seg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 2600,
          text: "to it if they need to",
          words: [
            { id: "w_1", text: "to", startMs: 1000, endMs: 1200 },
            { id: "w_2", text: "it", startMs: 1200, endMs: 1400 },
            { id: "w_3", text: "if", startMs: 1400, endMs: 1600 },
            { id: "w_4", text: "they", startMs: 1600, endMs: 1800 },
            { id: "w_5", text: "need", startMs: 1800, endMs: 2000 },
            { id: "w_6", text: "to", startMs: 2000, endMs: 2200 },
          ],
        },
      ],
    };
    const readable = {
      version: "transcript.readable.v1" as const,
      media: { src: "meeting.opus", durationMs: 4_000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "rseg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 2600,
          text: "To approach it using greenfield to determine.",
          sourceSegmentIds: ["seg_1"],
        },
      ],
    };

    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const usingToken = display.blocks[0]?.tokens.find((token) => token.text === "using");
    const greenfieldToken = display.blocks[0]?.tokens.find((token) => token.text === "greenfield");

    expect(usingToken?.alignment).toBe("none");
    expect(usingToken?.startMs).toBeUndefined();
    expect(usingToken?.endMs).toBeUndefined();
    expect(greenfieldToken?.alignment).toBe("none");
    expect(greenfieldToken?.startMs).toBeUndefined();
    expect(greenfieldToken?.endMs).toBeUndefined();
  });

  it("leaves edge-only rewritten cleaned words untimed without two-sided anchors", () => {
    const transcript = {
      version: "transcript.words.v1" as const,
      media: { src: "meeting.opus", durationMs: 6_000, sha256: "" },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "seg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 3000,
          text: "yeah because it was fine",
          words: [
            { id: "w_1", text: "yeah", startMs: 1000, endMs: 1300 },
            { id: "w_2", text: "because", startMs: 1300, endMs: 1700 },
            { id: "w_3", text: "it", startMs: 1700, endMs: 1900 },
            { id: "w_4", text: "was", startMs: 1900, endMs: 2200 },
            { id: "w_5", text: "fine", startMs: 2200, endMs: 2600 },
          ],
        },
      ],
    };
    const readable = {
      version: "transcript.readable.v1" as const,
      media: { src: "meeting.opus", durationMs: 6_000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "rseg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 3000,
          text: "Please publish this. Yeah, because it was fine. Later extras.",
          sourceSegmentIds: ["seg_1"],
        },
      ],
    };

    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const wordTokens = display.blocks[0]?.tokens.filter((token) => token.kind === "word") ?? [];
    const publishToken = wordTokens.find((token) => token.text === "publish");
    const yeahToken = wordTokens.find((token) => token.text === "Yeah");
    const laterToken = wordTokens.find((token) => token.text === "Later");

    expect(publishToken?.startMs).toBeUndefined();
    expect(publishToken?.endMs).toBeUndefined();
    expect(publishToken?.alignment).toBe("none");
    expect(yeahToken).toMatchObject({
      startMs: 1000,
      endMs: 1300,
      alignment: "source",
    });
    expect(laterToken?.startMs).toBeUndefined();
    expect(laterToken?.endMs).toBeUndefined();
    expect(laterToken?.alignment).toBe("none");
    expect(display.blocks[0]?.timedWordCount).toBe(5);
    expect(display.blocks[0]?.wordCount).toBe(10);
    expect(display.blocks[0]?.timingCoverage).toBe(0.5);
  });
});

function encodeTranscriptBodyAsTags(
  bodyJSON: unknown,
  prefix: string,
  chunkSize = 4096,
): { tags: Record<string, string>; payloadRef: PortablePayloadRef } {
  const json = JSON.stringify(bodyJSON);
  const raw = new TextEncoder().encode(json);
  const gzip = gzipSync(raw);
  const b64url = Buffer.from(gzip)
    .toString("base64")
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
  const chunks: string[] = [];
  for (let offset = 0; offset < b64url.length; offset += chunkSize) {
    chunks.push(b64url.slice(offset, offset + chunkSize));
  }
  const tags: Record<string, string> = {};
  chunks.forEach((chunk, index) => {
    tags[`${prefix}${String(index).padStart(3, "0")}`] = chunk;
  });
  const sha256 = createHash("sha256").update(raw).digest("hex");
  return {
    tags,
    payloadRef: {
      prefix,
      chunkCount: chunks.length,
      sha256,
      rawBytes: raw.byteLength,
      gzipBytes: gzip.byteLength,
      mime: "application/vnd.cassini.transcript-words+json",
      encoding: "base64url+gzip+utf8json",
    },
  };
}

describe("loadPortableTranscriptBody (v2 transport)", () => {
  it("round-trips a transcript body through the chunk set and verifies sha256", async () => {
    const body = {
      format: "cassini.words.v1",
      language: "en",
      wordCount: 2,
      items: [
        { speaker: "speaker_0", startMs: 0, endMs: 400, text: "hello" },
        { speaker: "speaker_0", startMs: 400, endMs: 800, text: "world" },
      ],
    };
    const { tags, payloadRef } = encodeTranscriptBodyAsTags(body, "CASSINI_TX_PARAKEET_PAYLOAD_");

    const decoded = await loadPortableTranscriptBody(tags, payloadRef);

    expect(decoded).toEqual(body);
  });

  it("has a pure sha256 fallback for HTTP origins without WebCrypto subtle", () => {
    const raw = new TextEncoder().encode("portable transcript body");
    expect(sha256HexFallback(raw)).toBe(
      createHash("sha256").update(raw).digest("hex"),
    );
  });

  it("throws when sha256 in payloadRef does not match the decoded body", async () => {
    const body = { format: "cassini.words.v1", wordCount: 0, items: [] };
    const { tags, payloadRef } = encodeTranscriptBodyAsTags(body, "CASSINI_TX_CANARY_PAYLOAD_");
    const tampered: PortablePayloadRef = {
      ...payloadRef,
      sha256: "0".repeat(64),
    };

    await expect(loadPortableTranscriptBody(tags, tampered)).rejects.toThrow(/sha256 mismatch/);
  });

  it("throws when a numbered chunk is missing from the tag map", async () => {
    const body = { format: "cassini.words.v1", wordCount: 0, items: [] };
    const { tags, payloadRef } = encodeTranscriptBodyAsTags(body, "CASSINI_TX_PARAKEET_PAYLOAD_");
    delete tags[`${payloadRef.prefix}000`];

    await expect(loadPortableTranscriptBody(tags, payloadRef)).rejects.toThrow(
      /missing transcript payload chunk/,
    );
  });

  it("rejects payloadRef with non-positive chunkCount", async () => {
    const ref: PortablePayloadRef = {
      prefix: "CASSINI_TX_BAD_PAYLOAD_",
      chunkCount: 0,
      sha256: "0".repeat(64),
      rawBytes: 0,
      gzipBytes: 0,
      mime: "application/json",
      encoding: "base64url+gzip+utf8json",
    };
    await expect(loadPortableTranscriptBody({}, ref)).rejects.toThrow(/invalid chunkCount/);
  });
});

function makeTranscriptEntry(
  overrides: Partial<PortableTranscriptEntry> & { id: string },
): PortableTranscriptEntry {
  return {
    role: "raw-asr",
    format: "cassini.words.v1",
    payloadRef: {
      prefix: `CASSINI_TX_${overrides.id.toUpperCase()}_PAYLOAD_`,
      chunkCount: 1,
      sha256: "0".repeat(64),
    },
    ...overrides,
  };
}

describe("listAvailableTranscripts / describeTranscript", () => {
  it("returns a synthetic single-entry list for v1 manifests", () => {
    const list = listAvailableTranscripts({} as PortableMeetingManifest);
    expect(list).toEqual([
      { id: "default", role: "asr", label: "Transcript", description: "", isDefault: true },
    ]);
    expect(getDefaultTranscriptId({} as PortableMeetingManifest)).toBe("default");
  });

  it("labels v2 transcripts from the transcript id, not the engine name", () => {
    const manifest: PortableMeetingManifest = {
      version: 2,
      transcripts: [
        makeTranscriptEntry({ id: "parakeet" }),
        makeTranscriptEntry({ id: "canary", default: true }),
      ],
      provenance: {
        speechToText: {
          parakeet: { engine: "sherpa-onnx", model: "parakeet-tdt-0.6b-v2-int8" },
          canary: { engine: "sherpa-onnx", model: "canary-1b-v2" },
        },
      } as unknown,
    };
    const list = listAvailableTranscripts(manifest);
    // Labels come from the transcript id so they stay distinct even when both
    // entries share an engine — this is the parakeet+canary demo case.
    expect(list.map((entry) => entry.label)).toEqual(["Parakeet", "Canary"]);
    // Engine/model/backend land in description for the tooltip.
    expect(list[0]?.description).toContain("sherpa-onnx");
    expect(list[0]?.description).toContain("parakeet-tdt-0.6b-v2-int8");
    expect(list[1]?.description).toContain("canary-1b-v2");
    expect(getDefaultTranscriptId(manifest)).toBe("canary");
  });

  it("uses the v2 multi-transcript shape for compressed-integrity v3 manifests", () => {
    const manifest: PortableMeetingManifest = {
      version: 3,
      integrity: {
        matchPolicy: "exact-opus-audio-v1",
        opusAudioSha256: "a".repeat(64),
      },
      transcripts: [makeTranscriptEntry({ id: "parakeet", default: true })],
    };
    expect(listAvailableTranscripts(manifest).map((entry) => entry.id)).toEqual(["parakeet"]);
    expect(getDefaultTranscriptId(manifest)).toBe("parakeet");
  });

  it("keeps labels unique when two transcripts share engine, backend, and model fields", () => {
    // Regression: an older version of describeTranscript labeled by engine
    // first, so two sherpa-onnx transcripts both rendered as "sherpa-onnx".
    const manifest: PortableMeetingManifest = {
      version: 2,
      transcripts: [
        makeTranscriptEntry({ id: "tx-a" }),
        makeTranscriptEntry({ id: "tx-b" }),
      ],
      provenance: {
        speechToText: {
          "tx-a": { engine: "shared", backend: "shared", model: "shared" },
          "tx-b": { engine: "shared", backend: "shared", model: "shared" },
        },
      } as unknown,
    };
    const labels = listAvailableTranscripts(manifest).map((entry) => entry.label);
    expect(new Set(labels).size).toBe(2);
    expect(labels).toEqual(["Tx A", "Tx B"]);
  });

  it("humanizes transcript ids that contain hyphens and underscores", () => {
    const entry = makeTranscriptEntry({ id: "whisper-large-v3_en" });
    const descriptor = describeTranscript(entry, { version: 2 } as PortableMeetingManifest, false);
    expect(descriptor.label).toBe("Whisper Large V3 En");
    expect(descriptor.description).toBe("");
  });
});

describe("pickReadableForTranscript", () => {
  const manifest: PortableMeetingManifest = {
    version: 2,
    readableTranscripts: [
      {
        id: "readable-paired-canary",
        role: "readable-cleanup",
        format: "cassini.readable.v1",
        sourceTranscriptId: "canary",
        payloadRef: { prefix: "X_", chunkCount: 1, sha256: "0".repeat(64) },
      },
      {
        id: "readable-default",
        role: "readable-cleanup",
        format: "cassini.readable.v1",
        default: true,
        payloadRef: { prefix: "Y_", chunkCount: 1, sha256: "0".repeat(64) },
      },
    ],
  };

  it("matches sourceTranscriptId", () => {
    expect(pickReadableForTranscript(manifest, "canary")?.id).toBe("readable-paired-canary");
  });

  it("falls back to the default-flagged entry", () => {
    expect(pickReadableForTranscript(manifest, "parakeet")?.id).toBe("readable-default");
  });

  it("returns null when no readable transcripts are present", () => {
    expect(pickReadableForTranscript({} as PortableMeetingManifest, "anything")).toBeNull();
  });
});

// Attribution provenance rides on the raw-asr items of a portable manifest
// (optional attributionGapDb / lowConfidenceSpeaker keys; null = not
// measured). The re-projection into canonical words must keep it, or every
// meeting opened from a .opus loses the crosstalk evidence.
describe("buildTranscriptWordsFromPortable attribution carry", () => {
  const manifestWithItem = (item: Record<string, unknown>) => ({
    meeting: { durationMs: 5000 },
    speakers: [
      { id: "spk_ana", label: "Ana" },
      { id: "spk_ben", label: "Ben" },
    ],
    transcript: {
      items: [
        { speaker: "spk_ana", startMs: 0, endMs: 500, text: "we" },
        item,
      ],
    },
  });

  it("copies attributionGapDb and lowConfidenceSpeaker from an item onto its word", () => {
    const transcript = buildTranscriptWordsFromPortable(
      manifestWithItem({
        speaker: "spk_ben",
        startMs: 900,
        endMs: 1400,
        text: "yeah",
        attributionGapDb: 31.7,
        lowConfidenceSpeaker: true,
      }) as never,
    );
    expect(transcript.segments[1]?.words[0]).toMatchObject({
      text: "yeah",
      attributionGapDb: 31.7,
      lowConfidenceSpeaker: true,
    });
    // The unflagged item's word carries neither key.
    expect(transcript.segments[0]?.words[0]).not.toHaveProperty("attributionGapDb");
    expect(transcript.segments[0]?.words[0]).not.toHaveProperty("lowConfidenceSpeaker");
  });

  it("round-trips a flagged item into an index the crosstalk judgement fires on", () => {
    const transcript = validateTranscriptWordsV1(
      buildTranscriptWordsFromPortable(
        manifestWithItem({
          speaker: "spk_ben",
          startMs: 900,
          endMs: 1400,
          text: "yeah",
          attributionGapDb: 31.7,
          lowConfidenceSpeaker: true,
        }) as never,
      ) as unknown,
    );
    const index = buildTranscriptIndex(transcript);
    expect(isLikelyCrosstalkTurn(index.segments[1]!.words)).toBe(true);
    expect(isLikelyCrosstalkTurn(index.segments[0]!.words)).toBe(false);
  });

  it("treats null attribution values as not measured and still validates", () => {
    const transcript = buildTranscriptWordsFromPortable(
      manifestWithItem({
        speaker: "spk_ben",
        startMs: 900,
        endMs: 1400,
        text: "yeah",
        attributionGapDb: null,
        lowConfidenceSpeaker: null,
      }) as never,
    );
    const word = transcript.segments[1]?.words[0];
    expect(word).not.toHaveProperty("attributionGapDb");
    expect(word).not.toHaveProperty("lowConfidenceSpeaker");
    expect(() => validateTranscriptWordsV1(transcript as unknown)).not.toThrow();
  });

  it("drops non-finite or non-numeric gaps so the validator never sees them", () => {
    for (const gap of [Number.NaN, Number.POSITIVE_INFINITY, "loud"]) {
      const transcript = buildTranscriptWordsFromPortable(
        manifestWithItem({
          speaker: "spk_ben",
          startMs: 900,
          endMs: 1400,
          text: "yeah",
          attributionGapDb: gap,
        }) as never,
      );
      expect(transcript.segments[1]?.words[0]).not.toHaveProperty("attributionGapDb");
      expect(() => validateTranscriptWordsV1(transcript as unknown)).not.toThrow();
    }
  });

  it("does not fabricate flagged words for multi-word items", () => {
    // Multi-word spans stay word-less (no fabricated timings), flagged or not.
    const transcript = buildTranscriptWordsFromPortable(
      manifestWithItem({
        speaker: "spk_ben",
        startMs: 900,
        endMs: 1400,
        text: "yeah sure",
        attributionGapDb: 31.7,
        lowConfidenceSpeaker: true,
      }) as never,
    );
    expect(transcript.segments[1]?.words).toEqual([]);
  });
});

// The JSON-directory shape: canonical words already carry ids and flags, and
// readable segments reference the REAL canonical segment ids. The display
// judgement must fire here through the same canonicalWordsForBlock mapping the
// portable path uses.
describe("display judgement over the JSON-directory artifacts", () => {
  it("carries per-word flags from canonical words into the display-block judgement", () => {
    const rawTranscript = {
      version: "transcript.words.v1",
      media: { src: "meeting.webm", durationMs: 10_000 },
      speakers: [
        { id: "spk_ana", label: "Ana" },
        { id: "spk_ben", label: "Ben" },
      ],
      segments: [
        {
          id: "seg_000000",
          speaker: "spk_ana",
          startMs: 0,
          endMs: 2000,
          text: "hello there ben",
          words: [
            { id: "seg_000000:w_0", text: "hello", startMs: 0, endMs: 500, attributionGapDb: -22.4 },
            { id: "seg_000000:w_1", text: "there", startMs: 500, endMs: 1200, attributionGapDb: -19.1 },
            { id: "seg_000000:w_2", text: "ben", startMs: 1200, endMs: 2000, attributionGapDb: -25.0 },
          ],
        },
        {
          id: "seg_000001",
          speaker: "spk_ben",
          startMs: 2600,
          endMs: 3600,
          text: "budget approved",
          words: [
            {
              id: "seg_000001:w_0",
              text: "budget",
              startMs: 2600,
              endMs: 3100,
              attributionGapDb: 14.2,
              lowConfidenceSpeaker: true,
            },
            {
              id: "seg_000001:w_1",
              text: "approved",
              startMs: 3100,
              endMs: 3600,
              attributionGapDb: 15.8,
              lowConfidenceSpeaker: true,
            },
          ],
        },
      ],
    };
    const rawReadable = {
      version: "transcript.readable.v1",
      media: { src: "meeting.webm", durationMs: 10_000 },
      speakers: rawTranscript.speakers,
      sourceTranscriptVersion: "transcript.words.v1",
      segments: [
        {
          id: "r_seg_000000",
          speaker: "spk_ana",
          startMs: 0,
          endMs: 2000,
          text: "Hello there, Ben.",
          sourceSegmentIds: ["seg_000000"],
        },
        {
          id: "r_seg_000001",
          speaker: "spk_ben",
          startMs: 2600,
          endMs: 3600,
          text: "Budget approved.",
          sourceSegmentIds: ["seg_000001"],
        },
      ],
    };

    const display = validateDisplayTranscriptV1(
      buildDisplayTranscriptFromArtifacts(rawTranscript as never, rawReadable as never) as unknown,
    );
    const index = buildTranscriptIndex(validateTranscriptWordsV1(rawTranscript));

    const judged = display.blocks.map((block) => ({
      speaker: block.speaker,
      words: canonicalWordsForBlock(index, block).length,
      crosstalk: isLikelyCrosstalkTurn(canonicalWordsForBlock(index, block)),
    }));
    expect(judged).toEqual([
      { speaker: "spk_ana", words: 3, crosstalk: false },
      { speaker: "spk_ben", words: 2, crosstalk: true },
    ]);
  });
});
