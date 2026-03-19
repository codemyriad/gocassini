import { describe, expect, it } from "vitest";

import {
  buildDisplayTranscriptFromArtifacts,
  buildReadableTranscriptFromPortable,
  buildTranscriptWordsFromPortable,
} from "./portable";

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

  it("interpolates word timings from multi-word portable transcript items", () => {
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
    expect(timedWords?.[0]).toMatchObject({
      text: "And",
      alignment: "interpolated",
      startMs: 1000,
    });
    expect(timedWords?.at(-1)).toMatchObject({
      text: "it",
      alignment: "interpolated",
      endMs: 5000,
    });
    expect(display.blocks[0]?.timedWordCount).toBe(9);
    expect(display.blocks[0]?.wordCount).toBe(9);
    expect(display.blocks[0]?.timingCoverage).toBe(1);
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

  it("splits synthetic readable blocks around interruptions from other speakers", () => {
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

    expect(chimaBlocks).toHaveLength(2);
    expect(chimaBlocks[0]?.text).toContain("Actually, I was wondering");
    expect(chimaBlocks[0]?.text).not.toContain("It's a pity");
    expect(chimaBlocks[0]?.endMs).toBeLessThanOrEqual(64_837);
    expect(chimaBlocks[1]?.text).toContain("It's a pity, Chris, you're ruining everything.");
    expect(chimaBlocks[1]?.startMs).toBeGreaterThanOrEqual(78_757);
  });

  it("uses exact transcript words when splitting readable blocks around interruptions", () => {
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

    expect(chimaBlocks).toHaveLength(2);
    expect(chimaBlocks[0]?.text).toContain("doing something else.");
    expect(chimaBlocks[0]?.text).not.toContain("It's a pity");
    expect(chimaBlocks[0]?.endMs).toBe(62_021);
    expect(chimaBlocks[1]?.text).toBe("It's a pity, Chris, you're ruining everything.");
    expect(chimaBlocks[1]?.startMs).toBe(77_541);
  });

  it("keeps short fully rewritten cleaned blocks seekable via interpolated timing", () => {
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
        alignment: "interpolated",
        startMs: 127_398,
      },
      {
        text: "sense",
        alignment: "interpolated",
        endMs: 128_197,
      },
    ]);
    expect(wordTokens.every((token) => Number.isInteger(token.startMs) && Number.isInteger(token.endMs))).toBe(true);
    expect(display.blocks[0]?.timedWordCount).toBe(2);
    expect(display.blocks[0]?.wordCount).toBe(2);
    expect(display.blocks[0]?.timingCoverage).toBe(1);
  });

  it("interpolates rewritten cleaned words between exact anchors", () => {
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
          text: "we should buy this now",
          words: [
            { id: "w_1", text: "we", startMs: 1000, endMs: 1300 },
            { id: "w_2", text: "should", startMs: 1300, endMs: 1700 },
            { id: "w_3", text: "buy", startMs: 1700, endMs: 2200 },
            { id: "w_4", text: "this", startMs: 2200, endMs: 2700 },
            { id: "w_5", text: "now", startMs: 2700, endMs: 3200 },
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
          text: "We should purchase this now.",
          sourceSegmentIds: ["seg_1"],
        },
      ],
    };

    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const purchaseToken = display.blocks[0]?.tokens.find((token) => token.text === "purchase");

    expect(purchaseToken).toMatchObject({
      alignment: "interpolated",
      sourceWordIds: [],
      startMs: 1700,
      endMs: 2200,
    });
    expect(display.blocks[0]?.timedWordCount).toBe(5);
    expect(display.blocks[0]?.wordCount).toBe(5);
    expect(display.blocks[0]?.timingCoverage).toBe(1);
  });
});
