import { describe, expect, it } from "vitest";

import {
  canonicalMeetingKey,
  choosePreferredArtifactCandidate,
  classifyPortableTimingPrecision,
} from "./reprocess-portable-meetings.mjs";

describe("canonicalMeetingKey", () => {
  it("strips opus extensions and stt suffixes", () => {
    expect(canonicalMeetingKey("daily-meeting-2026-03-12--12:29--stt-parakeet-ctc-0.6b.opus")).toBe(
      "daily-meeting-2026-03-12--12:29",
    );
  });
});

describe("choosePreferredArtifactCandidate", () => {
  it("prefers an exact audio match in the same STT family", () => {
    const portable = {
      provenance: {
        speechToText: {
          backend: "sherpa-onnx",
          model: "parakeet-tdt-0.6b-v2-int8",
        },
      },
    };
    const parakeet = {
      path: "/tmp/meeting--stt-parakeet-ctc-0.6b",
      audioMatch: true,
      artifact: {
        manifest: {
          provenance: {
            speechToText: {
              engine: "parakeet-nemo",
              model: "nvidia/parakeet-ctc-0.6b",
            },
          },
        },
        readableTranscript: {},
        displayTranscript: {},
      },
    };
    const whisper = {
      path: "/tmp/meeting--stt-whisper-large-v3",
      audioMatch: true,
      artifact: {
        manifest: {
          provenance: {
            speechToText: {
              engine: "faster-whisper",
              model: "large-v3",
            },
          },
        },
        readableTranscript: {},
        displayTranscript: {},
      },
    };

    expect(choosePreferredArtifactCandidate([whisper, parakeet], portable)).toBe(parakeet);
  });
});

describe("classifyPortableTimingPrecision", () => {
  it("detects exact word timing from single-word transcript items", () => {
    expect(
      classifyPortableTimingPrecision({
        transcript: {
          items: [
            { text: "hello" },
            { text: "world" },
          ],
        },
      }),
    ).toBe("exact-word");
  });

  it("detects uniformly interpolated readable words", () => {
    expect(
      classifyPortableTimingPrecision({
        transcript: {
          items: [{ text: "hello world" }],
        },
        readableTranscript: {
          segments: [
            {
              startMs: 0,
              endMs: 1000,
              words: [
                { text: "hello", startMs: 0, endMs: 500 },
                { text: "world", startMs: 500, endMs: 1000 },
              ],
            },
          ],
        },
      }),
    ).toBe("interpolated-word");
  });

  it("detects non-uniform readable word timings", () => {
    expect(
      classifyPortableTimingPrecision({
        transcript: {
          items: [{ text: "hello world" }],
        },
        readableTranscript: {
          segments: [
            {
              startMs: 0,
              endMs: 1000,
              words: [
                { text: "hello", startMs: 0, endMs: 200 },
                { text: "world", startMs: 600, endMs: 1000 },
              ],
            },
          ],
        },
      }),
    ).toBe("exact-word-readable");
  });
});
