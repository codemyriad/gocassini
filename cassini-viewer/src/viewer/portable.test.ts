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

  it("does not fabricate word timings from multi-word portable transcript items", () => {
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
});
