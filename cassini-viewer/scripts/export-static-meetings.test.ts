import { describe, expect, it } from "vitest";

import {
  buildDisplayTranscriptFromArtifacts,
  describeMeeting,
  describeSpeechToTextVariant,
  describeVariantSuffix,
  buildReadableTranscriptFromPortable,
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
});
