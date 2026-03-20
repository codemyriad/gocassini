import { describe, expect, it } from "vitest";

import {
  buildPortableManifestFromArtifact,
  canonicalPortableMeetingName,
  flattenPortableTranscriptItems,
  inferRecordedAtLocal,
} from "./pack-portable-artifacts.mjs";

describe("flattenPortableTranscriptItems", () => {
  it("preserves exact word timing when source transcript has words", () => {
    expect(
      flattenPortableTranscriptItems({
        segments: [
          {
            speaker: "spk_1",
            words: [
              { text: "hello", startMs: 100, endMs: 200 },
              { text: "team", startMs: 220, endMs: 320 },
            ],
          },
        ],
      }),
    ).toEqual([
      { speaker: "spk_1", text: "hello", startMs: 100, endMs: 200 },
      { speaker: "spk_1", text: "team", startMs: 220, endMs: 320 },
    ]);
  });
});

describe("buildPortableManifestFromArtifact", () => {
  it("builds a portable manifest that keeps exact transcript items", () => {
    const artifact = {
      rootDir: "/tmp/daily-meeting-2026-03-12--12:29--stt-parakeet-ctc-0.6b",
      audioPath: "/tmp/meeting.opus",
      manifest: {
        generatedAt: "2026-03-18T12:00:00Z",
        source: {
          basename: "daily-meeting-2026-03-12--12:29.mkv",
        },
        provenance: {
          speechToText: {
            backend: "sherpa-onnx",
            model: "parakeet-tdt-0.6b-v2-int8",
          },
        },
      },
      transcript: {
        version: "transcript.words.v1",
        media: { src: "meeting.opus", durationMs: 1000 },
        speakers: [{ id: "spk_1", label: "Chris" }],
        segments: [
          {
            speaker: "spk_1",
            startMs: 100,
            endMs: 320,
            text: "hello team",
            words: [
              { text: "hello", startMs: 100, endMs: 200 },
              { text: "team", startMs: 220, endMs: 320 },
            ],
          },
        ],
      },
      readableTranscript: null,
      displayTranscript: null,
    };

    const manifest = buildPortableManifestFromArtifact(artifact, "/tmp/output.opus", {
      pcmSha256: "abc123",
      sampleRate: 48000,
      channels: 1,
      sampleCount: 48000,
      durationMs: 1000,
    });

    expect(manifest.transcript.items).toEqual([
      { speaker: "spk_1", text: "hello", startMs: 100, endMs: 200 },
      { speaker: "spk_1", text: "team", startMs: 220, endMs: 320 },
    ]);
    expect(manifest.readableTranscript?.version).toBe("transcript.readable.v1");
    expect(manifest.displayTranscript?.version).toBe("transcript.display.v1");
    expect(manifest.meeting.recordedAtLocal).toBe("2026-03-12T12:29:00");
    expect(manifest.meeting.processedAtUtc).toBe("2026-03-18T12:00:00Z");
  });
});

describe("inferRecordedAtLocal", () => {
  it("parses the meeting timestamp from common Cassini names", () => {
    expect(inferRecordedAtLocal("daily-meeting-2026-03-10--12:30.mkv")).toBe("2026-03-10T12:30:00");
    expect(inferRecordedAtLocal("daily-meeting--2026-03-05--12:38:29.opus")).toBe("2026-03-05T12:38:29");
    expect(inferRecordedAtLocal("demo--20260310T123045")).toBe("2026-03-10T12:30:45");
  });
});

describe("canonicalPortableMeetingName", () => {
  it("strips the bundle suffix from meeting artifact directories", () => {
    expect(canonicalPortableMeetingName("/tmp/daily-meeting-2026-03-12--12:29.meeting")).toBe(
      "daily-meeting-2026-03-12--12:29",
    );
  });

  it("leaves plain directory names unchanged", () => {
    expect(canonicalPortableMeetingName("/tmp/daily-meeting-2026-03-12--12:29")).toBe(
      "daily-meeting-2026-03-12--12:29",
    );
  });
});
