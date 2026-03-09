import { describe, expect, it } from "vitest";
import {
  buildTranscriptIndex,
  getActiveSegment,
  getActiveWord,
  parseTimeHash,
  searchSegments,
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
