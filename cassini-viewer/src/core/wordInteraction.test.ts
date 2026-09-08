import { describe, expect, it } from "vitest";
import { buildTranscriptIndex, judgedDisplaySegments, type JudgedDisplaySegment } from "./transcript";
import { buildDisplayTranscriptFromArtifacts } from "../viewer/portable";
import { repairTurnFinalWordInflation } from "./overlap";
import { tokensPreserveText, transcriptWordParts } from "./wordInteraction";
import type { DisplayTranscriptToken, TranscriptWordsV1 } from "./types";

const transcript: TranscriptWordsV1 = {
  version: "transcript.words.v1",
  media: { src: "synthetic.wav", durationMs: 10_000 },
  speakers: [{ id: "maya", label: "Maya" }],
  segments: [{
    id: "turn", speaker: "maya", startMs: 0, endMs: 2200,
    text: "Um, the old plan works.",
    words: ["Um,", "the", "old", "plan", "works."].map((text, i) => ({
      id: `w${i}`, text, startMs: i * 400, endMs: i * 400 + 300,
    })),
  }],
};
const index = buildTranscriptIndex(transcript);
function block(text: string, tokens: DisplayTranscriptToken[] = []): JudgedDisplaySegment {
  return {
    id: "turn", speaker: "maya", speakerLabel: "Maya", startMs: 0, endMs: 2200,
    text, tokens, words: index.segments[0]!.words, sourceSegmentIds: ["turn"],
  };
}
const textOf = (parts: ReturnType<typeof transcriptWordParts>) => parts.map((p) => p.prefix + p.text).join("");

describe("word interaction projection", () => {
  it("preserves cleaned display prose and timing rather than replacing it with canonical words", () => {
    const readable = {
      version: "transcript.readable.v1" as const,
      media: transcript.media, speakers: transcript.speakers,
      segments: [{ id: "clean", speaker: "maya", startMs: 0, endMs: 2200,
        text: "The revised plan works, thankfully.", sourceSegmentIds: ["turn"] }],
    };
    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const judged = judgedDisplaySegments(index, display)[0]!;
    const parts = transcriptWordParts(judged);
    expect(textOf(parts)).toBe(readable.segments[0]!.text);
    expect(parts.some((part) => part.text === "Um")).toBe(false);
    expect(parts.find((part) => part.text === "The")?.startMs).toBe(400);
    expect(parts.find((part) => part.text === "revised")?.alignment).toBe("none");
    expect(parts.find((part) => part.text === "thankfully")?.startMs).toBeUndefined();
  });

  it("uses token timing even when source references were rejected, without restoring acoustic votes", () => {
    const display = buildDisplayTranscriptFromArtifacts(transcript, null);
    const target = display.blocks[0]!;
    target.speaker = "other-speaker";
    target.speakerLabel = "Jonah";
    const judged = judgedDisplaySegments(index, display)[0]!;
    expect(judged.words).toHaveLength(0);
    const parts = transcriptWordParts(judged);
    expect(textOf(parts)).toBe(target.text);
    expect(parts.find((part) => part.text === "plan")?.startMs).toBe(1200);
    expect(judged.words).toHaveLength(0);
  });

  it("keeps exact raw prose including punctuation, whitespace and Unicode", () => {
    const raw = block(" «Élodie»,\t你好！ ");
    raw.words = [
      { ...index.segments[0]!.words[0]!, text: "Élodie", startMs: 100, endMs: 500 },
      { ...index.segments[0]!.words[1]!, text: "你好", startMs: 600, endMs: 900 },
    ];
    const parts = transcriptWordParts(raw);
    expect(textOf(parts)).toBe(raw.text);
    expect(parts.filter((part) => part.startMs !== undefined).map((part) => part.text)).toEqual(["Élodie", "你好"]);
  });

  it("does not guess raw timing when the prose and canonical words disagree", () => {
    const raw = block("The revised plan works.");
    const parts = transcriptWordParts(raw);
    expect(textOf(parts)).toBe(raw.text);
    expect(parts.every((part) => part.startMs === undefined)).toBe(true);
  });

  it("rejects generated tokens that would lose characters or literal whitespace in readable prose", () => {
    for (const text of ["Use _ to separate.", "_name and x_.", "Um,\tthe\nold  plan works."]) {
      const readable = { version: "transcript.readable.v1" as const,
        media: transcript.media, speakers: transcript.speakers,
        segments: [{ id: "clean", speaker: "maya", startMs: 0, endMs: 2200, text, sourceSegmentIds: ["turn"] }] };
      const tokens = buildDisplayTranscriptFromArtifacts(transcript, readable).blocks[0]!.tokens;
      expect(tokensPreserveText(text, tokens)).toBe(false);
      const parts = transcriptWordParts(block(text));
      expect(textOf(parts)).toBe(text);
      if (text.startsWith("Um,")) {
        expect(parts.filter((part) => part.startMs !== undefined)).toHaveLength(5);
      }
    }
  });

  it("does not invent timing for invalid or untimed tokens, but preserves zero-duration seek targets", () => {
    const raw = block("Estimated zero unknown", [
      { text: "Estimated", kind: "word", spaceBefore: false, sourceWordIds: [], startMs: 10, endMs: 20, alignment: "interpolated" },
      { text: "zero", kind: "word", spaceBefore: true, sourceWordIds: [], startMs: 30, endMs: 30 },
      { text: "unknown", kind: "word", spaceBefore: true, sourceWordIds: [], startMs: 40, endMs: NaN },
    ]);
    const parts = transcriptWordParts(raw);
    expect(textOf(parts)).toBe(raw.text);
    expect(parts.map((part) => part.startMs)).toEqual([10, 30, undefined]);
  });

  it("uses repaired ends without moving starts, and respects producer audio bounds", () => {
    const display = buildDisplayTranscriptFromArtifacts(transcript, null);
    const source = judgedDisplaySegments(index, display);
    const lastWord = source[0]!.words.at(-1)!;
    lastWord.endMs = 9500;
    const lastToken = source[0]!.tokens.find((token) => token.text === "works")!;
    lastToken.endMs = 9500;
    const repaired = transcriptWordParts(repairTurnFinalWordInflation(source)[0]!);
    const bounded = transcriptWordParts(repairTurnFinalWordInflation(source, { endsBoundedByAudio: true })[0]!);
    expect(repaired.find((part) => part.text === "works")?.startMs).toBe(1600);
    expect(repaired.find((part) => part.text === "works")!.endMs!).toBeLessThan(9500);
    expect(bounded.find((part) => part.text === "works")?.endMs).toBe(9500);
  });
});
