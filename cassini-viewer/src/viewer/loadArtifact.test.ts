import { afterEach, describe, expect, it, vi } from "vitest";
import { gzipSync } from "node:zlib";

import { loadArtifactFromDirectory, loadPortableArtifactFromAudioPath } from "./loadArtifact";

const transcriptFixture = {
  version: "transcript.words.v1",
  media: {
    src: "meeting.opus",
    durationMs: 3000,
  },
  speakers: [{ id: "spk_1", label: "Alice" }],
  segments: [
    {
      id: "seg_1",
      speaker: "spk_1",
      startMs: 1000,
      endMs: 1500,
      text: "hello",
      words: [{ id: "w_1", text: "hello", startMs: 1000, endMs: 1500 }],
    },
  ],
};

const displayFixture = {
  version: "transcript.display.v1",
  media: {
    src: "meeting.opus",
    durationMs: 3000,
  },
  speakers: [{ id: "spk_1", label: "Alice" }],
  blocks: [
    {
      id: "dseg_1",
      speaker: "spk_1",
      speakerLabel: "Alice",
      startMs: 1000,
      endMs: 1500,
      text: "Hello.",
      sourceSegmentIds: ["seg_1"],
      wordCount: 1,
      timedWordCount: 1,
      timingCoverage: 1,
      tokens: [
        {
          text: "Hello",
          spaceBefore: false,
          kind: "word",
          sourceWordIds: ["w_1"],
          startMs: 1000,
          endMs: 1500,
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
};

describe("loadArtifactFromDirectory", () => {
  const originalWindow = globalThis.window;
  const originalFetch = globalThis.fetch;

  afterEach(() => {
    globalThis.window = originalWindow;
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  it("loads display transcript from the document-relative artifact path", async () => {
    const calls: string[] = [];
    globalThis.window = {
      location: {
        href: "http://127.0.0.1:8765/?meeting=daily-meeting--2026-03-05--12:38:29",
        protocol: "http:",
      },
    } as Window;
    globalThis.fetch = vi.fn(async (input: string | URL, init?: RequestInit) => {
      const url = String(input);
      calls.push(url);
      if (init?.method === "HEAD") {
        return { ok: true } as Response;
      }
      if (url.endsWith("/transcript.words.v1.json")) {
        return {
          ok: true,
          json: async () => transcriptFixture,
        } as Response;
      }
      if (url.endsWith("/transcript.display.v1.json")) {
        return {
          ok: true,
          json: async () => displayFixture,
        } as Response;
      }
      return { ok: false } as Response;
    }) as typeof fetch;

    const artifact = await loadArtifactFromDirectory("./meetings/demo-meeting");

    expect(artifact.displayTranscript?.blocks).toHaveLength(1);
    expect(calls).toContain("http://127.0.0.1:8765/meetings/demo-meeting/transcript.display.v1.json");
    expect(calls).not.toContain(
      "http://127.0.0.1:8765/meetings/demo-meeting/meetings/demo-meeting/transcript.display.v1.json",
    );
  });

  it("loads a portable opus meeting directly from its audio path", async () => {
    globalThis.window = {
      location: {
        href: "http://127.0.0.1:8765/?meeting=daily-meeting-2026-03-18--12:30",
        protocol: "http:",
      },
    } as Window;
    const portableFixture = {
      meeting: {
        durationMs: 3000,
      },
      audio: {
        sha256: "abc123",
      },
      speakers: [{ id: "spk_1", label: "Alice" }],
      transcript: {
        items: [
          {
            id: "seg_1",
            speaker: "spk_1",
            startMs: 1000,
            endMs: 1500,
            text: "hello there",
          },
        ],
      },
    };
    const portableBytes = buildPortableOpusFixture(portableFixture);
    globalThis.fetch = vi.fn(async (input: string | URL) => {
      const url = String(input);
      if (url.endsWith(".opus")) {
        return {
          ok: true,
          arrayBuffer: async () =>
            portableBytes.buffer.slice(
              portableBytes.byteOffset,
              portableBytes.byteOffset + portableBytes.byteLength,
            ),
        } as Response;
      }
      return { ok: false } as Response;
    }) as typeof fetch;

    const artifact = await loadPortableArtifactFromAudioPath("./daily-meeting-2026-03-18--12:30.opus");

    expect(artifact.audioSrc).toBe("http://127.0.0.1:8765/daily-meeting-2026-03-18--12:30.opus");
    expect(artifact.transcript.segments).toHaveLength(1);
    expect(artifact.transcript.segments[0]?.text).toBe("hello there");
    expect(artifact.readableTranscript?.segments).toHaveLength(1);
    expect(artifact.displayTranscript?.blocks).toHaveLength(1);
  });
});

function buildPortableOpusFixture(manifest: object): Uint8Array {
  const rawJson = Buffer.from(JSON.stringify(manifest), "utf8");
  const compressed = gzipSync(rawJson);
  const encoded = compressed
    .toString("base64")
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/g, "");
  const tags = {
    CASSINI_PAYLOAD_CHUNK_COUNT: "1",
    CASSINI_PAYLOAD_000: encoded,
  };
  const opusHead = buildOpusHeadPacket();
  const opusTags = buildOpusTagsPacket(tags);
  return concatenateBytes([
    buildOggPage(opusHead, 0),
    buildOggPage(opusTags, 1),
  ]);
}

function buildOpusHeadPacket(): Uint8Array {
  const packet = new Uint8Array(19);
  packet.set(new TextEncoder().encode("OpusHead"), 0);
  packet[8] = 1;
  packet[9] = 1;
  const view = new DataView(packet.buffer);
  view.setUint16(10, 0, true);
  view.setUint32(12, 48_000, true);
  view.setInt16(16, 0, true);
  packet[18] = 0;
  return packet;
}

function buildOpusTagsPacket(tags: Record<string, string>): Uint8Array {
  const encoder = new TextEncoder();
  const vendor = encoder.encode("cassini-viewer-test");
  const comments = Object.entries(tags).map(([key, value]) => encoder.encode(`${key}=${value}`));
  const totalLength =
    8 +
    4 +
    vendor.length +
    4 +
    comments.reduce((sum, comment) => sum + 4 + comment.length, 0);
  const packet = new Uint8Array(totalLength);
  packet.set(encoder.encode("OpusTags"), 0);
  const view = new DataView(packet.buffer);
  let offset = 8;
  view.setUint32(offset, vendor.length, true);
  offset += 4;
  packet.set(vendor, offset);
  offset += vendor.length;
  view.setUint32(offset, comments.length, true);
  offset += 4;
  for (const comment of comments) {
    view.setUint32(offset, comment.length, true);
    offset += 4;
    packet.set(comment, offset);
    offset += comment.length;
  }
  return packet;
}

function buildOggPage(packet: Uint8Array, sequenceNumber: number): Uint8Array {
  const segmentLengths: number[] = [];
  let remaining = packet.length;
  while (remaining >= 255) {
    segmentLengths.push(255);
    remaining -= 255;
  }
  segmentLengths.push(remaining);

  const page = new Uint8Array(27 + segmentLengths.length + packet.length);
  page.set(new TextEncoder().encode("OggS"), 0);
  page[4] = 0;
  page[5] = 0;
  const view = new DataView(page.buffer);
  view.setUint32(14, 1, true);
  view.setUint32(18, sequenceNumber, true);
  page[26] = segmentLengths.length;
  page.set(segmentLengths, 27);
  page.set(packet, 27 + segmentLengths.length);
  return page;
}

function concatenateBytes(chunks: Uint8Array[]): Uint8Array {
  const totalLength = chunks.reduce((sum, chunk) => sum + chunk.length, 0);
  const output = new Uint8Array(totalLength);
  let offset = 0;
  for (const chunk of chunks) {
    output.set(chunk, offset);
    offset += chunk.length;
  }
  return output;
}
