import { describe, expect, it } from "vitest";

import {
  buildDisplayTranscriptFromArtifacts,
  describeMeeting,
  describeSpeechToTextVariant,
  preferredPortableTitle,
  describeVariantSuffix,
  buildReadableTranscriptFromPortable,
  buildTranscriptWordsFromPortable,
  portableDefaultSegmentCount,
  parseArgs,
} from "./export-static-meetings.mjs";

describe("describeMeeting", () => {
  it("parses colon-separated legacy meeting ids", () => {
    expect(describeMeeting("daily-meeting--2026-03-05--12:38:29")).toEqual({
      title: "Daily Meeting",
      dateLabel: "2026-03-05 12:38",
    });
  });

  it("parses dash-separated legacy meeting ids", () => {
    expect(describeMeeting("daily-meeting--2026-03-04--12-36-53")).toEqual({
      title: "Daily Meeting",
      dateLabel: "2026-03-04 12:36",
    });
  });

  it("parses double-dash ids that keep date inside title", () => {
    expect(describeMeeting("daily-meeting-2026-03-10--12:30")).toEqual({
      title: "Daily Meeting",
      dateLabel: "2026-03-10 12:30",
    });
  });

  it("parses compact stamped meeting ids", () => {
    expect(describeMeeting("synthetic-pied-piper-v1--20260310T150453")).toEqual({
      title: "Synthetic Pied Piper V1",
      dateLabel: "2026-03-10 15:04",
    });
  });

  it("ignores trailing stt variant suffixes when parsing meeting ids", () => {
    expect(describeMeeting("daily-meeting-2026-03-10--12:30--stt-whisper-large-v3")).toEqual({
      title: "Daily Meeting",
      dateLabel: "2026-03-10 12:30",
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

  it("keeps the plain-id fallback for non-ULID meeting ids", () => {
    expect(describeMeeting("weekly-sync")).toEqual({
      title: "Weekly Sync",
      dateLabel: "weekly-sync",
    });
  });
});

describe("preferredPortableTitle", () => {
  const ulid = "01KKA70QN0ABCDEFGHJKMNPQRS";

  it("uses a real embedded title (e.g. the Talk room name)", () => {
    expect(preferredPortableTitle({ meeting: { title: "Daily Meeting" } }, ulid)).toBe(
      "Daily Meeting",
    );
    expect(preferredPortableTitle({ meeting: { title: "  Silvio-Alex-Chris  " } }, ulid)).toBe(
      "Silvio-Alex-Chris",
    );
  });

  it("rejects packer defaults that echo the meeting id", () => {
    expect(preferredPortableTitle({ meeting: { title: ulid } }, ulid)).toBe("");
    expect(
      preferredPortableTitle({ meeting: { title: ulid } }, `${ulid}--stt-whisper-large-v3`),
    ).toBe("");
    expect(preferredPortableTitle({ meeting: { title: "Cassini Meeting" } }, ulid)).toBe("");
  });

  it("rejects missing or empty titles", () => {
    expect(preferredPortableTitle({ meeting: { title: "   " } }, ulid)).toBe("");
    expect(preferredPortableTitle({ meeting: {} }, ulid)).toBe("");
    expect(preferredPortableTitle({}, ulid)).toBe("");
    expect(preferredPortableTitle(null, ulid)).toBe("");
  });
});

// Pre-existing grab-bag suite: STT variant labels and portable transcript
// grouping, kept under the original suite name.
describe("describeMeeting", () => {
  it("formats whisper STT variants for catalog titles", () => {
    expect(
      describeSpeechToTextVariant({
        provenance: {
          speechToText: {
            backend: "local-whisper",
            engine: "faster-whisper",
            model: "large-v3",
          },
        },
      }),
    ).toBe("Whisper large-v3");
  });

  it("formats non-whisper STT variants for catalog titles", () => {
    expect(
      describeSpeechToTextVariant({
        provenance: {
          speechToText: {
            backend: "http",
            engine: "parakeet-nemo",
            model: "nvidia/parakeet-tdt-0.6b-v2",
          },
        },
      }),
    ).toBe("Parakeet tdt-0.6b-v2");
  });

  it("falls back to the stt suffix when provenance is absent", () => {
    expect(describeVariantSuffix("daily-meeting-2026-03-10--12:30--stt-whisper-small-en")).toBe(
      "Whisper Small En",
    );
  });

  it("groups portable fallback transcript items into multi-word readable blocks", () => {
    const portable = {
      meeting: { durationMs: 10_000 },
      speakers: [{ id: "spk_1", label: "Chris" }],
      transcript: {
        items: [
          { id: "seg_1", speaker: "spk_1", startMs: 0, endMs: 400, text: "okay" },
          { id: "seg_2", speaker: "spk_1", startMs: 400, endMs: 800, text: "this" },
          { id: "seg_3", speaker: "spk_1", startMs: 800, endMs: 1200, text: "looks" },
          { id: "seg_4", speaker: "spk_1", startMs: 1200, endMs: 1600, text: "much" },
          { id: "seg_5", speaker: "spk_1", startMs: 1600, endMs: 2000, text: "better" },
          { id: "seg_6", speaker: "spk_1", startMs: 3600, endMs: 4000, text: "now" },
        ],
      },
    };
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 10_000, sha256: "" },
      speakers: [{ id: "spk_1", label: "Chris" }],
      segments: portable.transcript.items.map((item) => ({
        ...item,
        words: [{ id: `${item.id}:w_0`, text: item.text, startMs: item.startMs, endMs: item.endMs }],
      })),
    };

    const readable = buildReadableTranscriptFromPortable(portable, transcript);

    expect(readable.segments).toHaveLength(1);
    expect(readable.segments[0]).toMatchObject({
      text: "okay this looks much better now",
      sourceSegmentIds: ["seg_1", "seg_2", "seg_3", "seg_4", "seg_5", "seg_6"],
    });
  });

  it("keeps a hard break across large pauses or speaker changes", () => {
    const portable = {
      meeting: { durationMs: 20_000 },
      speakers: [
        { id: "spk_1", label: "Chris" },
        { id: "spk_2", label: "Alex" },
      ],
      transcript: {
        items: [
          { id: "seg_1", speaker: "spk_1", startMs: 0, endMs: 500, text: "Okay," },
          { id: "seg_2", speaker: "spk_1", startMs: 500, endMs: 1000, text: "that works." },
          { id: "seg_3", speaker: "spk_1", startMs: 6000, endMs: 6500, text: "Long pause." },
          { id: "seg_4", speaker: "spk_2", startMs: 7000, endMs: 7600, text: "Other speaker." },
        ],
      },
    };
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 20_000, sha256: "" },
      speakers: portable.speakers,
      segments: portable.transcript.items.map((item) => ({
        ...item,
        words: [{ id: `${item.id}:w_0`, text: item.text, startMs: item.startMs, endMs: item.endMs }],
      })),
    };

    const readable = buildReadableTranscriptFromPortable(portable, transcript);

    expect(readable.segments).toHaveLength(3);
    expect(readable.segments[0]?.text).toBe("Okay, that works.");
    expect(readable.segments[1]?.text).toBe("Long pause.");
    expect(readable.segments[2]?.text).toBe("Other speaker.");
  });

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
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 4_000, sha256: "" },
      speakers: portable.speakers,
      segments: portable.transcript.items.map((item) => ({
        ...item,
        words: [{ id: `${item.id}:w_0`, text: item.text, startMs: item.startMs, endMs: item.endMs }],
      })),
    };

    const readable = buildReadableTranscriptFromPortable(portable, transcript);
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

    const transcript = buildTranscriptWordsFromPortable(portable);
    const readable = buildReadableTranscriptFromPortable(portable, transcript);
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

    const transcript = buildTranscriptWordsFromPortable(portable);
    const readable = buildReadableTranscriptFromPortable(portable, transcript);
    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const wordTokens = display.blocks[0]?.tokens.filter((token) => token.kind === "word") ?? [];

    expect(transcript.segments[0]?.words).toEqual([]);
    expect(display.blocks[0]?.timedWordCount).toBe(9);
    expect(wordTokens[0]).toMatchObject({ text: "And", startMs: 1000, endMs: 1200 });
    expect(wordTokens[5]).toMatchObject({ text: "very", startMs: 2400, endMs: 2800 });
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

    const transcript = buildTranscriptWordsFromPortable(portable);
    const readable = buildReadableTranscriptFromPortable(portable, transcript);
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

    const transcript = buildTranscriptWordsFromPortable(portable);
    const readable = buildReadableTranscriptFromPortable(portable, transcript);
    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const chimaBlocks = display.blocks.filter((block) => block.speaker === "spk_chima");

    expect(chimaBlocks).toHaveLength(2);
    expect(chimaBlocks[0]?.text).toContain("doing something else.");
    expect(chimaBlocks[0]?.text).not.toContain("It's a pity");
    expect(chimaBlocks[0]?.endMs).toBe(62_021);
    expect(chimaBlocks[1]?.text).toBe("It's a pity, Chris, you're ruining everything.");
    expect(chimaBlocks[1]?.startMs).toBe(77_541);
  });

  it("builds a viewer-ready display transcript with timed cleaned tokens", () => {
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "seg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 2400,
          text: "um hello there i think",
          words: [
            { id: "w_1", text: "um", startMs: 1000, endMs: 1100 },
            { id: "w_2", text: "hello", startMs: 1200, endMs: 1500 },
            { id: "w_3", text: "there", startMs: 1520, endMs: 1800 },
            { id: "w_4", text: "i", startMs: 1850, endMs: 1900 },
            { id: "w_5", text: "think", startMs: 1920, endMs: 2200 },
          ],
        },
      ],
    };
    const readable = {
      version: "transcript.readable.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "rseg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 2400,
          text: "Hello there, I think.",
          sourceSegmentIds: ["seg_1"],
        },
      ],
    };

    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);

    expect(display.version).toBe("transcript.display.v1");
    expect(display.blocks).toHaveLength(1);
    expect(display.blocks[0]?.tokens.map((token) => token.text)).toEqual([
      "Hello",
      "there",
      ",",
      "I",
      "think",
      ".",
    ]);
    expect(display.blocks[0]?.tokens[0]).toMatchObject({
      sourceWordIds: ["w_2"],
      startMs: 1200,
      endMs: 1500,
      alignment: "source",
    });
    expect(display.blocks[0]?.timingCoverage).toBe(1);
  });

  it("falls back to time-overlap words when readable sourceSegmentIds are stale", () => {
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 12000 },
      speakers: [{ id: "spk_1", label: "Chris" }],
      segments: [
        "okay",
        "any",
        "work",
        "with",
        "these",
        "comments",
        "is",
        "probably",
        "worth",
        "it",
      ].map((word, index) => ({
        id: `seg_${String(index + 1).padStart(6, "0")}`,
        speaker: "spk_1",
        startMs: 1000 + index * 1000,
        endMs: 1500 + index * 1000,
        text: word,
        words: [
          {
            id: `w_${String(index + 1).padStart(6, "0")}`,
            text: word,
            startMs: 1000 + index * 1000,
            endMs: 1500 + index * 1000,
          },
        ],
      })),
    };
    const readable = {
      version: "transcript.readable.v1",
      media: { src: "meeting.opus", durationMs: 12000 },
      speakers: [{ id: "spk_1", label: "Chris" }],
      segments: [
        {
          id: "rseg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 11000,
          text: "Okay, any work with these comments is probably worth it.",
          sourceSegmentIds: ["seg_000001", "seg_000002"],
        },
      ],
    };

    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const block = display.blocks[0];
    const probablyToken = block.tokens.find((token) => token.text === "probably");

    expect(block.endMs).toBeGreaterThanOrEqual(9000);
    expect(block.timedWordCount).toBe(block.wordCount);
    expect(probablyToken?.startMs).toBeGreaterThanOrEqual(8000);
  });

  it("leaves synonym substitutions untimed instead of assigning fake precision", () => {
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 6000 },
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
      version: "transcript.readable.v1",
      media: { src: "meeting.opus", durationMs: 6000 },
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

    expect(purchaseToken?.alignment).toBe("none");
    expect(purchaseToken?.sourceWordIds).toEqual([]);
    expect(purchaseToken?.startMs).toBeUndefined();
    expect(purchaseToken?.endMs).toBeUndefined();
    expect(display.blocks[0]?.timedWordCount).toBe(4);
    expect(display.blocks[0]?.wordCount).toBe(5);
    expect(display.blocks[0]?.timingCoverage).toBe(0.8);
  });

  it("leaves edge-only rewritten cleaned words untimed without two-sided anchors", () => {
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 6000 },
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
      version: "transcript.readable.v1",
      media: { src: "meeting.opus", durationMs: 6000 },
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
    expect(yeahToken?.startMs).toBe(1000);
    expect(yeahToken?.endMs).toBe(1300);
    expect(yeahToken?.alignment).toBe("source");
    expect(laterToken?.startMs).toBeUndefined();
    expect(laterToken?.endMs).toBeUndefined();
    expect(laterToken?.alignment).toBe("none");
    expect(display.blocks[0]?.timedWordCount).toBe(5);
    expect(display.blocks[0]?.wordCount).toBe(10);
    expect(display.blocks[0]?.timingCoverage).toBe(0.5);
  });

  it("leaves fully rewritten cleaned blocks untimed at the word level", () => {
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
      speakers: [{ id: "spk_1", label: "Silvio" }],
      segments: [
        {
          id: "seg_1",
          speaker: "spk_1",
          startMs: 127398,
          endMs: 128197,
          text: "Mexicans",
          words: [
            { id: "w_1", text: "Mexicans", startMs: 127398, endMs: 128197 },
          ],
        },
      ],
    };
    const readable = {
      version: "transcript.readable.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
      speakers: [{ id: "spk_1", label: "Silvio" }],
      segments: [
        {
          id: "rseg_1",
          speaker: "spk_1",
          startMs: 127398,
          endMs: 128197,
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
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 6000 },
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
      version: "transcript.readable.v1",
      media: { src: "meeting.opus", durationMs: 6000 },
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
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
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
      version: "transcript.readable.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
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

  it("synthesizes source word ids for transcript artifact words that omit them", () => {
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
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
      version: "transcript.readable.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
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

  it("leaves contraction-expansion edge tokens untimed when there is no exact ASR match", () => {
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "seg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 2600,
          text: "we're not done",
          words: [
            { id: "w_1", text: "we're", startMs: 1000, endMs: 1500 },
            { id: "w_2", text: "not", startMs: 1500, endMs: 1900 },
            { id: "w_3", text: "done", startMs: 1900, endMs: 2400 },
          ],
        },
      ],
    };
    const readable = {
      version: "transcript.readable.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "rseg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 2600,
          text: "We are not done.",
          sourceSegmentIds: ["seg_1"],
        },
      ],
    };

    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const weToken = display.blocks[0]?.tokens.find((token) => token.text === "We");
    const areToken = display.blocks[0]?.tokens.find((token) => token.text === "are");

    expect(weToken?.alignment).toBe("none");
    expect(weToken?.startMs).toBeUndefined();
    expect(weToken?.endMs).toBeUndefined();
    expect(areToken?.alignment).toBe("none");
    expect(areToken?.startMs).toBeUndefined();
    expect(areToken?.endMs).toBeUndefined();
    expect(display.blocks[0]?.timedWordCount).toBe(2);
    expect(display.blocks[0]?.wordCount).toBe(4);
    expect(display.blocks[0]?.timingCoverage).toBe(0.5);
  });
});

describe("parseArgs", () => {
  it("returns null recordingsBaseUrl when flag is absent", () => {
    const { recordingsBaseUrl } = parseArgs([]);
    expect(recordingsBaseUrl).toBeNull();
  });

  it("parses --recordings-base-url and normalises trailing slash", () => {
    const { recordingsBaseUrl } = parseArgs([
      "--recordings-base-url",
      "https://view.meetings.codemyriad.io/main",
    ]);
    expect(recordingsBaseUrl).toBe("https://view.meetings.codemyriad.io/main/");
  });

  it("preserves trailing slash when already present", () => {
    const { recordingsBaseUrl } = parseArgs([
      "--recordings-base-url",
      "https://view.meetings.codemyriad.io/main/",
    ]);
    expect(recordingsBaseUrl).toBe("https://view.meetings.codemyriad.io/main/");
  });

  it("throws when --recordings-base-url has no value", () => {
    expect(() => parseArgs(["--recordings-base-url"])).toThrow(
      "missing value for --recordings-base-url",
    );
  });

  it("parses --recordings-base-url alongside --source-dir and --output-dir", () => {
    const { recordingsBaseUrl, sourceDir, outputDir } = parseArgs([
      "--source-dir",
      "/tmp/source",
      "--output-dir",
      "/tmp/out",
      "--recordings-base-url",
      "https://view.meetings.codemyriad.io/main/",
    ]);
    expect(recordingsBaseUrl).toBe("https://view.meetings.codemyriad.io/main/");
    expect(sourceDir).toContain("/tmp/source");
    expect(outputDir).toContain("/tmp/out");
  });
});

describe("portableDefaultSegmentCount (v2 .opus segmentCount fallback)", () => {
  // A v2 portable main manifest carries per-transcript metadata in transcripts[]
  // (wordCount); the actual items live in separate CASSINI_TX_* chunk sets the
  // exporter does not decode. So buildTranscriptWordsFromPortable sees no inline
  // items and yields 0 segments — the catalog must fall back to wordCount, or it
  // would publish segmentCount: 0 for every fresh v2 .opus.
  const v2Manifest = {
    kind: "cassini-portable-meeting",
    version: 2,
    speakers: [{ id: "spk_0", label: "Alice" }],
    transcripts: [
      { id: "raw-asr", role: "speech-to-text", default: true, wordCount: 42 },
      { id: "cleaned", role: "cleaned", default: false, wordCount: 40 },
    ],
  };

  it("buildTranscriptWordsFromPortable yields 0 segments for a v2 main manifest (the bug condition)", () => {
    expect(buildTranscriptWordsFromPortable(v2Manifest).segments).toHaveLength(0);
  });

  it("uses the default transcript's wordCount as the segment count", () => {
    expect(portableDefaultSegmentCount(v2Manifest)).toBe(42);
  });

  it("the catalog fallback (segments.length || portableDefaultSegmentCount) avoids segmentCount: 0", () => {
    const transcript = buildTranscriptWordsFromPortable(v2Manifest);
    const segmentCount = transcript.segments?.length || portableDefaultSegmentCount(v2Manifest);
    expect(segmentCount).toBe(42);
  });

  it("falls back to the first transcript when none is marked default", () => {
    expect(
      portableDefaultSegmentCount({
        transcripts: [
          { id: "a", wordCount: 7 },
          { id: "b", wordCount: 9 },
        ],
      }),
    ).toBe(7);
  });

  it("returns 0 when there are no transcripts or no numeric wordCount", () => {
    expect(portableDefaultSegmentCount({})).toBe(0);
    expect(portableDefaultSegmentCount({ transcripts: [] })).toBe(0);
    expect(portableDefaultSegmentCount({ transcripts: [{ id: "a" }] })).toBe(0);
  });
});
