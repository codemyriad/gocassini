import { afterEach, describe, expect, it, vi } from "vitest";
import { gzipSync } from "node:zlib";

import {
  loadArtifactFromDirectory,
  loadBundledArtifact,
  loadPortableArtifactFromAudioPath,
  loadPortableMeetingSummary,
} from "./loadArtifact";

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

  it("loads bundled artifacts from the app base when opened on a nested route", async () => {
    const calls: string[] = [];
    globalThis.window = {
      location: {
        href: "http://127.0.0.1:8765/preview/",
        protocol: "http:",
      },
    } as Window;
    globalThis.fetch = vi.fn(async (input: string | URL, init?: RequestInit) => {
      const url = String(input);
      calls.push(url);
      if (init?.method === "HEAD") {
        return { ok: true } as Response;
      }
      if (url === "http://127.0.0.1:8765/transcript.words.v1.json") {
        return {
          ok: true,
          json: async () => transcriptFixture,
        } as Response;
      }
      if (url === "http://127.0.0.1:8765/transcript.display.v1.json") {
        return {
          ok: true,
          json: async () => displayFixture,
        } as Response;
      }
      return { ok: false } as Response;
    }) as typeof fetch;

    const artifact = await loadBundledArtifact();

    expect(artifact.transcript.segments).toHaveLength(1);
    expect(calls).toContain("http://127.0.0.1:8765/transcript.words.v1.json");
    expect(calls).not.toContain("http://127.0.0.1:8765/preview/transcript.words.v1.json");
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
    globalThis.fetch = vi.fn(async (input: string | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith(".opus")) {
        expect(init?.headers).toMatchObject({
          Range: "bytes=0-262143",
        });
        return {
          ok: true,
          status: 206,
          headers: new Headers({
            "content-range": "bytes 0-1999/2000",
          }),
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

  it("builds user-facing metadata for portable meetings", async () => {
    globalThis.window = {
      location: {
        href: "http://127.0.0.1:8765/?meeting=daily-meeting-2026-03-10--12:30",
        protocol: "http:",
      },
    } as Window;
    const portableFixture = {
      meeting: {
        id: "mtg_abc123",
        durationMs: 1_046_260,
        createdAtUtc: "2026-03-19T09:43:13Z",
        recordedAtLocal: "2026-03-10T12:30:00",
        processedAtUtc: "2026-03-19T09:43:13Z",
      },
      audio: {
        codec: "opus",
        sampleRate: 48_000,
        channels: 1,
        sha256: "abc123",
      },
      provenance: {
        speechToText: {
          backend: "sherpa-onnx",
          model: "parakeet-tdt-0.6b-v2-int8",
        },
      },
      speakers: [
        { id: "spk_chris", label: "Chris" },
        { id: "spk_alex", label: "Alex" },
      ],
      transcript: {
        items: [
          {
            id: "seg_1",
            speaker: "spk_chris",
            startMs: 1000,
            endMs: 1500,
            text: "hello there",
          },
        ],
      },
    };
    const portableBytes = buildPortableOpusFixture(portableFixture);
    globalThis.fetch = vi.fn(async (input: string | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith(".opus")) {
        expect(init?.headers).toMatchObject({
          Range: "bytes=0-262143",
        });
        return {
          ok: true,
          status: 206,
          headers: new Headers({
            "content-range": "bytes 0-1999/2000",
          }),
          arrayBuffer: async () =>
            portableBytes.buffer.slice(
              portableBytes.byteOffset,
              portableBytes.byteOffset + portableBytes.byteLength,
            ),
        } as Response;
      }
      return { ok: false } as Response;
    }) as typeof fetch;

    const artifact = await loadPortableArtifactFromAudioPath("./daily-meeting-2026-03-10--12:30.opus");
    const meetingSection = artifact.metadata?.sections.find((section) => section.title === "Meeting");
    const processingSection = artifact.metadata?.sections.find((section) => section.title === "Processing");

    expect(meetingSection?.rows).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ label: "Recorded", value: "March 10, 2026 at 12:30 PM" }),
        expect.objectContaining({ label: "Duration", value: "17:26" }),
        expect.objectContaining({ label: "Speakers", values: ["Chris", "Alex"] }),
      ]),
    );
    expect(processingSection?.rows).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ label: "Processed", value: "March 19, 2026 at 9:43 AM UTC" }),
        expect.objectContaining({ label: "Speech to text", value: "sherpa-onnx · parakeet-tdt-0.6b-v2-int8" }),
      ]),
    );
  });

  it("prefers an embedded display transcript from the portable manifest", async () => {
    globalThis.window = {
      location: {
        href: "http://127.0.0.1:8765/?meeting=meeting-a",
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
      displayTranscript: {
        ...displayFixture,
        blocks: [
          {
            ...displayFixture.blocks[0],
            text: "Custom display.",
            tokens: [
              {
                text: "Custom",
                spaceBefore: false,
                kind: "word",
                sourceWordIds: ["w_1"],
                startMs: 1000,
                endMs: 1250,
                alignment: "source",
              },
              {
                text: "display",
                spaceBefore: true,
                kind: "word",
                sourceWordIds: ["w_1"],
                startMs: 1250,
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
            wordCount: 2,
            timedWordCount: 2,
          },
        ],
      },
    };
    const portableBytes = buildPortableOpusFixture(portableFixture);
    globalThis.fetch = vi.fn(async (input: string | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith(".opus")) {
        expect(init?.headers).toMatchObject({
          Range: "bytes=0-262143",
        });
        return {
          ok: true,
          status: 206,
          headers: new Headers({
            "content-range": "bytes 0-1999/2000",
          }),
          arrayBuffer: async () =>
            portableBytes.buffer.slice(
              portableBytes.byteOffset,
              portableBytes.byteOffset + portableBytes.byteLength,
            ),
        } as Response;
      }
      return { ok: false } as Response;
    }) as typeof fetch;

    const artifact = await loadPortableArtifactFromAudioPath("./meeting-a.opus");

    expect(artifact.displayTranscript?.blocks[0]?.text).toBe("Custom display.");
    expect(artifact.displayTranscript?.blocks[0]?.tokens[0]?.text).toBe("Custom");
  });

  it("recovers word timing from portable readable transcript words when display data is absent", async () => {
    globalThis.window = {
      location: {
        href: "http://127.0.0.1:8765/?meeting=meeting-word-timed",
        protocol: "http:",
      },
    } as Window;
    const portableFixture = {
      meeting: {
        durationMs: 5000,
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
            endMs: 5000,
            text: "And I think they'll be very happy with it.",
          },
        ],
      },
      readableTranscript: {
        version: "transcript.readable.v1",
        speakers: [{ id: "spk_1", label: "Alice" }],
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
    const portableBytes = buildPortableOpusFixture(portableFixture);
    globalThis.fetch = vi.fn(async (input: string | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith(".opus")) {
        expect(init?.headers).toMatchObject({
          Range: "bytes=0-262143",
        });
        return {
          ok: true,
          status: 206,
          headers: new Headers({
            "content-range": "bytes 0-1999/2000",
          }),
          arrayBuffer: async () =>
            portableBytes.buffer.slice(
              portableBytes.byteOffset,
              portableBytes.byteOffset + portableBytes.byteLength,
            ),
        } as Response;
      }
      return { ok: false } as Response;
    }) as typeof fetch;

    const artifact = await loadPortableArtifactFromAudioPath("./meeting-word-timed.opus");
    const wordTokens = artifact.displayTranscript?.blocks[0]?.tokens.filter((token) => token.kind === "word") ?? [];

    expect(artifact.timingPrecision.level).toBe("word");
    expect(wordTokens[0]).toMatchObject({ text: "And", startMs: 1000, endMs: 1200 });
    expect(wordTokens[5]).toMatchObject({ text: "very", startMs: 2400, endMs: 2800 });
  });

  it("loads portable meeting summary counts from embedded metadata", async () => {
    globalThis.window = {
      location: {
        href: "http://127.0.0.1:8765/",
        protocol: "http:",
      },
    } as Window;
    const portableFixture = {
      meeting: {
        durationMs: 4200,
      },
      speakers: [
        { id: "spk_1", label: "Alice" },
        { id: "spk_2", label: "Bob" },
      ],
      transcript: {
        items: [
          { id: "seg_1", speaker: "spk_1", startMs: 0, endMs: 1000, text: "hello" },
          { id: "seg_2", speaker: "spk_2", startMs: 1100, endMs: 2000, text: "there" },
        ],
      },
    };
    const portableBytes = buildPortableOpusFixture(portableFixture);
    globalThis.fetch = vi.fn(async (input: string | URL) => {
      const url = String(input);
      if (url.endsWith(".opus")) {
        return {
          ok: true,
          status: 206,
          headers: new Headers({
            "content-range": "bytes 0-1999/2000",
          }),
          arrayBuffer: async () =>
            portableBytes.buffer.slice(
              portableBytes.byteOffset,
              portableBytes.byteOffset + portableBytes.byteLength,
            ),
        } as Response;
      }
      return { ok: false } as Response;
    }) as typeof fetch;

    const summary = await loadPortableMeetingSummary("./meeting.opus");

    expect(summary).toEqual({
      speakerCount: 2,
      segmentCount: 2,
      digestDurationMs: 4200,
    });
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
