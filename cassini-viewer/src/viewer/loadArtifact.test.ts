import { afterEach, describe, expect, it, vi } from "vitest";
import { gzipSync } from "node:zlib";
import { createHash } from "node:crypto";

import {
  loadArtifactFromDirectory,
  loadBundledArtifact,
  loadPortableArtifactFromAudioPath,
  loadPortableMeetingSummary,
  switchPortableTranscript,
  type LoadedArtifact,
} from "./loadArtifact";
import {
  buildDisplayTranscriptFromArtifacts,
  buildReadableTranscriptFromPortable,
  buildTranscriptWordsFromPortable,
} from "./portable";
import { canonicalWordsForBlock, isLikelyCrosstalkTurn } from "../core/transcript";
import {
  analyzeOverlap,
  buildTranscriptRows,
  repairTurnFinalWordInflation,
  sortBlocksInReadingOrder,
} from "../core/overlap";
import type { TranscriptWordsV1 } from "../core/types";

const OPUS_AUDIO_SHA256 = "a".repeat(64);

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
      if (url.endsWith("/summary.md")) {
        return {
          ok: true,
          text: async () => "# Meeting Summary\n",
        } as Response;
      }
      return { ok: false } as Response;
    }) as typeof fetch;

    const artifact = await loadArtifactFromDirectory("./meetings/demo-meeting");

    expect(artifact.displayTranscript?.blocks).toHaveLength(1);
    expect(artifact.summary).toBe("# Meeting Summary\n");
    expect(calls).toContain("http://127.0.0.1:8765/meetings/demo-meeting/transcript.display.v1.json");
    expect(calls).toContain("http://127.0.0.1:8765/meetings/demo-meeting/summary.md");
    expect(calls).not.toContain(
      "http://127.0.0.1:8765/meetings/demo-meeting/meetings/demo-meeting/transcript.display.v1.json",
    );
  });

  it("returns null summary when no summary file is present", async () => {
    globalThis.window = {
      location: {
        href: "http://127.0.0.1:8765/?meeting=daily-meeting--2026-03-05--12:38:29",
        protocol: "http:",
      },
    } as Window;
    globalThis.fetch = vi.fn(async (input: string | URL, init?: RequestInit) => {
      const url = String(input);
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

    expect(artifact.summary).toBeNull();
  });

  it("treats 404 summary responses as missing summaries", async () => {
    globalThis.window = {
      location: {
        href: "http://127.0.0.1:8765/?meeting=daily-meeting--2026-03-05--12:38:29",
        protocol: "http:",
      },
    } as Window;
    globalThis.fetch = vi.fn(async (input: string | URL, init?: RequestInit) => {
      const url = String(input);
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
      if (url.endsWith("/summary.md")) {
        return {
          ok: false,
          status: 404,
        } as Response;
      }
      return { ok: false } as Response;
    }) as typeof fetch;

    const artifact = await loadArtifactFromDirectory("./meetings/demo-meeting");

    expect(artifact.summary).toBeNull();
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
    const manifest = {
      kind: "cassini-portable-meeting",
      version: 1,
      profile: "ogg-opus",
      meeting: {
        durationMs: 3000,
      },
      audio: {
        container: "ogg",
        codec: "opus",
        sampleRate: 48_000,
        channels: 1,
        sampleCount: 144_000,
        durationMs: 3000,
      },
      integrity: { opusAudioSha256: OPUS_AUDIO_SHA256 },
      speakers: [{ id: "spk_1", label: "Alice" }],
    };
    const rawTranscript = {
      format: "cassini.words.v1",
      wordCount: 1,
      items: [{
        id: "seg_1",
        speaker: "spk_1",
        startMs: 1000,
        endMs: 1500,
        text: "hello",
      }],
    };
    const portableBytes = buildPortableOpusFixture({ manifest, rawTranscript });
    globalThis.fetch = vi.fn(async (input: string | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith(".opus")) {
        expect(init?.headers).toMatchObject({
          Range: "bytes=0-1048575",
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
    expect(artifact.transcript.segments[0]?.text).toBe("hello");
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
      integrity: { opusAudioSha256: OPUS_AUDIO_SHA256 },
      provenance: {
        speechToText: {
          "raw-asr": {
            backend: "sherpa-onnx",
            model: "parakeet-tdt-0.6b-v2-int8",
          },
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
            text: "hello",
          },
        ],
      },
    };
    const { transcript: rawTranscript, ...manifest } = portableFixture;
    const portableBytes = buildPortableOpusFixture({ manifest, rawTranscript });
    globalThis.fetch = vi.fn(async (input: string | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith(".opus")) {
        expect(init?.headers).toMatchObject({
          Range: "bytes=0-1048575",
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
      integrity: { opusAudioSha256: OPUS_AUDIO_SHA256 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      transcript: {
        items: [
          {
            id: "seg_1",
            speaker: "spk_1",
            startMs: 1000,
            endMs: 1500,
            text: "hello",
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
    const {
      transcript: rawTranscript,
      displayTranscript,
      ...manifest
    } = portableFixture;
    const portableBytes = buildPortableOpusFixture({ manifest, rawTranscript, displayTranscript });
    globalThis.fetch = vi.fn(async (input: string | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith(".opus")) {
        expect(init?.headers).toMatchObject({
          Range: "bytes=0-1048575",
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
      integrity: { opusAudioSha256: OPUS_AUDIO_SHA256 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      transcript: {
        items: [
          { speaker: "spk_1", startMs: 1000, endMs: 1200, text: "And" },
          { speaker: "spk_1", startMs: 1200, endMs: 1400, text: "I" },
          { speaker: "spk_1", startMs: 1400, endMs: 1800, text: "think" },
          { speaker: "spk_1", startMs: 1800, endMs: 2200, text: "they'll" },
          { speaker: "spk_1", startMs: 2200, endMs: 2400, text: "be" },
          { speaker: "spk_1", startMs: 2400, endMs: 2800, text: "very" },
          { speaker: "spk_1", startMs: 2800, endMs: 3300, text: "happy" },
          { speaker: "spk_1", startMs: 3300, endMs: 3600, text: "with" },
          { speaker: "spk_1", startMs: 3600, endMs: 4000, text: "it." },
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
    const {
      transcript: rawTranscript,
      readableTranscript,
      ...manifest
    } = portableFixture;
    const portableBytes = buildPortableOpusFixture({ manifest, rawTranscript, readableTranscript });
    globalThis.fetch = vi.fn(async (input: string | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith(".opus")) {
        expect(init?.headers).toMatchObject({
          Range: "bytes=0-1048575",
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
    const { transcript: rawTranscript, ...manifest } = portableFixture;
    const portableBytes = buildPortableOpusFixture({ manifest, rawTranscript });
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

describe("switchPortableTranscript", () => {
  const originalWindow = globalThis.window;
  const originalFetch = globalThis.fetch;

  afterEach(() => {
    globalThis.window = originalWindow;
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  function buildDualTranscriptFixture() {
    const parakeetBody = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 3000, sha256: "abc" },
      speakers: [{ id: "spk_1", label: "Alice" }],
      items: [
        { id: "seg_1", speaker: "spk_1", startMs: 1000, endMs: 1400, text: "parakeet" },
      ],
    };
    const canaryBody = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 3000, sha256: "abc" },
      speakers: [{ id: "spk_1", label: "Alice" }],
      items: [
        { id: "seg_1", speaker: "spk_1", startMs: 1000, endMs: 1400, text: "canary" },
      ],
    };
    const parakeetPayload = encodeBodyForOpusTags(parakeetBody, "CASSINI_TX_PARAKEET_PAYLOAD_");
    const canaryPayload = encodeBodyForOpusTags(canaryBody, "CASSINI_TX_CANARY_PAYLOAD_");
    const displayBody = (text: string) => ({
      ...displayFixture,
      blocks: [{ ...displayFixture.blocks[0], text }],
    });
    const parakeetDisplay = encodeBodyForOpusTags(
      displayBody("Parakeet display."),
      "CASSINI_TX_DISPLAY_PARAKEET_PAYLOAD_",
    );
    const canaryDisplay = encodeBodyForOpusTags(
      displayBody("Canary display."),
      "CASSINI_TX_DISPLAY_CANARY_PAYLOAD_",
    );
    const indexManifest = {
      version: 1,
      meeting: { durationMs: 3000 },
      integrity: { opusAudioSha256: OPUS_AUDIO_SHA256 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      transcripts: [
        {
          id: "parakeet",
          role: "raw-asr",
          format: "cassini.words.v1",
          payloadRef: parakeetPayload.payloadRef,
        },
        {
          id: "canary",
          role: "raw-asr",
          default: true,
          format: "cassini.words.v1",
          payloadRef: canaryPayload.payloadRef,
        },
      ],
      readableTranscripts: [
        {
          id: "display-parakeet",
          role: "display",
          format: "transcript.display.v1",
          sourceTranscriptId: "parakeet",
          payloadRef: parakeetDisplay.payloadRef,
        },
        {
          id: "display-canary",
          role: "display",
          default: true,
          format: "transcript.display.v1",
          sourceTranscriptId: "canary",
          payloadRef: canaryDisplay.payloadRef,
        },
      ],
      provenance: {
        speechToText: {
          parakeet: { engine: "Parakeet" },
          canary: { engine: "Canary" },
        },
      },
    };
    const extraTags = {
      ...parakeetPayload.tags,
      ...canaryPayload.tags,
      ...parakeetDisplay.tags,
      ...canaryDisplay.tags,
    };
    return buildPortableOpusFixture({ manifest: indexManifest, extraTags });
  }

  function mockFetchReturning(bytes: Uint8Array) {
    return vi.fn(async () => ({
      ok: true,
      status: 206,
      headers: new Headers({ "content-range": "bytes 0-1999/2000" }),
      arrayBuffer: async () =>
        bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength),
    }) as Response) as typeof fetch;
  }

  it("loads the producer-default transcript and exposes available transcripts", async () => {
    globalThis.window = {
      location: { href: "http://127.0.0.1:8765/?meeting=portable-fixture", protocol: "http:" },
    } as Window;
    globalThis.fetch = mockFetchReturning(buildDualTranscriptFixture());

    const artifact = await loadPortableArtifactFromAudioPath("./portable-fixture.opus");

    expect(artifact.currentTranscriptId).toBe("canary");
    expect(artifact.availableTranscripts.map((t) => t.id)).toEqual(["parakeet", "canary"]);
    expect(artifact.transcript.segments[0]?.text).toBe("canary");
    expect(artifact.displayTranscript?.blocks[0]?.text).toBe("Canary display.");
  });

  it("switches to an alternate transcript and caches the body on round-trip", async () => {
    globalThis.window = {
      location: { href: "http://127.0.0.1:8765/?meeting=portable-fixture-switch", protocol: "http:" },
    } as Window;
    const fixture = buildDualTranscriptFixture();
    const fetchMock = mockFetchReturning(fixture);
    globalThis.fetch = fetchMock;

    await loadPortableArtifactFromAudioPath("./portable-fixture-switch.opus");
    const fetchCountAfterLoad = (fetchMock as unknown as { mock: { calls: unknown[] } }).mock.calls
      .length;

    const switched = await switchPortableTranscript("./portable-fixture-switch.opus", "parakeet");
    expect(switched.currentTranscriptId).toBe("parakeet");
    expect(switched.transcript.segments[0]?.text).toBe("parakeet");
    expect(switched.displayTranscript?.blocks[0]?.text).toBe("Parakeet display.");
    // No additional fetch — body decoded from cached tags map.
    expect((fetchMock as unknown as { mock: { calls: unknown[] } }).mock.calls.length).toBe(
      fetchCountAfterLoad,
    );

    const back = await switchPortableTranscript("./portable-fixture-switch.opus", "canary");
    expect(back.currentTranscriptId).toBe("canary");
    expect(back.transcript.segments[0]?.text).toBe("canary");
    expect(back.displayTranscript?.blocks[0]?.text).toBe("Canary display.");
  });

  it("throws for an unknown transcript id", async () => {
    globalThis.window = {
      location: { href: "http://127.0.0.1:8765/?meeting=portable-fixture-unknown", protocol: "http:" },
    } as Window;
    globalThis.fetch = mockFetchReturning(buildDualTranscriptFixture());

    await loadPortableArtifactFromAudioPath("./portable-fixture-unknown.opus");

    await expect(
      switchPortableTranscript("./portable-fixture-unknown.opus", "whisper"),
    ).rejects.toThrow(/no transcript with id "whisper"/);
  });

  it("throws if called before the meeting is loaded", async () => {
    globalThis.window = {
      location: { href: "http://127.0.0.1:8765/", protocol: "http:" },
    } as Window;
    await expect(
      switchPortableTranscript("./never-loaded.opus", "parakeet"),
    ).rejects.toThrow(/before the portable meeting was loaded/);
  });

  it("surfaces a sha256 mismatch on the alternate body so the UI can show it", async () => {
    globalThis.window = {
      location: { href: "http://127.0.0.1:8765/?meeting=portable-fixture-sha", protocol: "http:" },
    } as Window;
    // Build a fixture where the alternate (parakeet) payloadRef carries a
    // bogus sha256 so the integrity check fails when we try to switch to it.
    const parakeetBody = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 3000, sha256: "abc" },
      speakers: [{ id: "spk_1", label: "Alice" }],
      items: [{ id: "seg_1", speaker: "spk_1", startMs: 1000, endMs: 1400, text: "parakeet" }],
    };
    const canaryBody = {
      ...parakeetBody,
      items: [{ id: "seg_1", speaker: "spk_1", startMs: 1000, endMs: 1400, text: "canary" }],
    };
    const parakeetPayload = encodeBodyForOpusTags(parakeetBody, "CASSINI_TX_PARAKEET_PAYLOAD_");
    const canaryPayload = encodeBodyForOpusTags(canaryBody, "CASSINI_TX_CANARY_PAYLOAD_");
    const manifest = {
      version: 1,
      meeting: { durationMs: 3000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      transcripts: [
        {
          id: "parakeet",
          role: "raw-asr",
          format: "cassini.words.v1",
          payloadRef: { ...parakeetPayload.payloadRef, sha256: "0".repeat(64) },
        },
        {
          id: "canary",
          role: "raw-asr",
          default: true,
          format: "cassini.words.v1",
          payloadRef: canaryPayload.payloadRef,
        },
      ],
    };
    const fixture = buildPortableOpusFixture({
      manifest,
      extraTags: {
        ...parakeetPayload.tags,
        ...canaryPayload.tags,
      },
    });
    globalThis.fetch = mockFetchReturning(fixture);

    await loadPortableArtifactFromAudioPath("./portable-fixture-sha.opus");

    await expect(
      switchPortableTranscript("./portable-fixture-sha.opus", "parakeet"),
    ).rejects.toThrow(/sha256 mismatch/);
  });

  it("rejects a portable file whose main manifest digest is stale", async () => {
    globalThis.window = {
      location: { href: "http://127.0.0.1:8765/?meeting=portable-main-sha", protocol: "http:" },
    } as Window;
    const fixture = buildPortableOpusFixture({
      manifest: {
        version: 1,
        meeting: { durationMs: 3000 },
        transcripts: [],
      },
      extraTags: { CASSINI_PAYLOAD_SHA256: "0".repeat(64) },
    });
    globalThis.fetch = mockFetchReturning(fixture);

    await expect(loadPortableArtifactFromAudioPath("./portable-main-sha.opus")).rejects.toThrow(
      /portable manifest sha256 mismatch/,
    );
  });

  it("rejects an unsupported portable format tag", async () => {
    globalThis.window = {
      location: { href: "http://127.0.0.1:8765/?meeting=unsupported-format", protocol: "http:" },
    } as Window;
    const fixture = buildPortableOpusFixture({
      manifest: { meeting: { durationMs: 3000 } },
      rawTranscript: { items: [] },
      extraTags: { CASSINI_FORMAT: "org.example.unsupported/9" },
    });
    globalThis.fetch = mockFetchReturning(fixture);

    await expect(loadPortableArtifactFromAudioPath("./unsupported-format.opus")).rejects.toThrow(
      /unsupported CASSINI_FORMAT/,
    );
  });

  it("rejects unsupported manifest shapes", async () => {
    globalThis.window = {
      location: { href: "http://127.0.0.1:8765/?meeting=unsupported-manifest", protocol: "http:" },
    } as Window;
    const fixture = buildPortableOpusFixture({
      manifest: {
        version: 7,
        meeting: { durationMs: 3000 },
        transcripts: [],
      },
    });
    globalThis.fetch = mockFetchReturning(fixture);

    await expect(loadPortableArtifactFromAudioPath("./unsupported-manifest.opus")).rejects.toThrow(
      /unsupported manifest version 7/,
    );
  });
});

// ---------------------------------------------------------------------------
// Parity: loose-file path vs packed `.opus` path (D-430)
//
// The viewer can load a meeting two ways:
//   - loose files: loadArtifactFromDirectory reads transcript.words.v1.json
//     (segments[]) + transcript.display.v1.json + transcript.readable.v1.json
//     (a dev-only affordance);
//   - packed `.opus`: loadPortableArtifactFromAudioPath reads the portable
//     manifest, whose transcript lives as a flat items[] list, and reprojects
//     items[] -> segments[] via buildTranscriptWordsFromPortable.
//
// `.opus` is the single durable published artifact, so the two paths must
// produce identical normalized output. This block guards the segments[] ->
// portable items[] reprojection: it builds ONE canonical source transcript,
// feeds it to both paths, and asserts the LoadedArtifact projections that the
// viewer renders (transcript words/segments, display timing, readable text,
// the speaker set, and the meeting metadata sections) match between them.
//
// Path-dependent fields are EXPECTED to differ and are normalized out before
// comparison: audioSrc / media.src (the loose path resolves them against the
// transcript URL, the portable path against the resolved `.opus` URL) and
// metadata.sourceKind / metadata.rawJson (different artifact kinds). These are
// asserted separately so the divergence stays intentional and documented.
// ---------------------------------------------------------------------------
describe("loose-file vs packed `.opus` parity", () => {
  const originalWindow = globalThis.window;
  const originalFetch = globalThis.fetch;

  afterEach(() => {
    globalThis.window = originalWindow;
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  // Canonical source. Single-word segments with the same id scheme the portable
  // reprojection synthesizes (seg_NNNNNN / seg_NNNNNN:w_0) so the round trip is
  // id-stable: buildTranscriptWordsFromPortable emits one segment per item and
  // assigns these ids when items carry none (as the producer's items do).
  const sourceTranscript: TranscriptWordsV1 = {
    version: "transcript.words.v1",
    media: { src: "meeting.opus", durationMs: 6000, sha256: OPUS_AUDIO_SHA256 },
    speakers: [
      { id: "spk_1", label: "Alice" },
      { id: "spk_2", label: "Bob" },
    ],
    segments: [
      { id: "seg_000000", speaker: "spk_1", startMs: 0, endMs: 600, text: "Hello", words: [{ id: "seg_000000:w_0", text: "Hello", startMs: 0, endMs: 600 }] },
      { id: "seg_000001", speaker: "spk_1", startMs: 600, endMs: 1200, text: "everyone", words: [{ id: "seg_000001:w_0", text: "everyone", startMs: 600, endMs: 1200 }] },
      { id: "seg_000002", speaker: "spk_1", startMs: 1200, endMs: 1800, text: "today", words: [{ id: "seg_000002:w_0", text: "today", startMs: 1200, endMs: 1800 }] },
      { id: "seg_000003", speaker: "spk_2", startMs: 2000, endMs: 2600, text: "Thanks", words: [{ id: "seg_000003:w_0", text: "Thanks", startMs: 2000, endMs: 2600 }] },
      { id: "seg_000004", speaker: "spk_2", startMs: 2600, endMs: 3200, text: "Alice", words: [{ id: "seg_000004:w_0", text: "Alice", startMs: 2600, endMs: 3200 }] },
    ],
  };

  // Derive readable + display ONCE from the source, with both builders the
  // viewer itself uses, then feed the identical artifacts to both load paths.
  const sharedReadable = buildReadableTranscriptFromPortable(
    { speakers: sourceTranscript.speakers as unknown[] },
    sourceTranscript,
  );
  const sharedDisplay = buildDisplayTranscriptFromArtifacts(sourceTranscript, sharedReadable);

  // Mirrors the producer's flattenPortableTranscriptItems (see
  // scripts/pack-portable-artifacts.mjs / portable_meeting.go): each timed word
  // becomes a word-level item carrying its segment's speaker; id-less, so the
  // viewer re-synthesizes ids on read.
  function flattenPortableTranscriptItems(transcript: TranscriptWordsV1) {
    const items: Array<{ speaker: string; startMs: number; endMs: number; text: string }> = [];
    for (const segment of transcript.segments) {
      const words = Array.isArray(segment.words) ? segment.words : [];
      for (const word of words) {
        const text = (word.text ?? "").trim();
        if (!text) {
          continue;
        }
        items.push({ speaker: segment.speaker ?? "", startMs: word.startMs, endMs: word.endMs, text });
      }
    }
    return items;
  }

  function setWindow(href: string) {
    globalThis.window = {
      location: { href, protocol: "http:" },
    } as Window;
  }

  async function loadViaLooseFiles(): Promise<LoadedArtifact> {
    globalThis.fetch = vi.fn(async (input: string | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === "HEAD") {
        return { ok: true } as Response;
      }
      if (url.endsWith("/transcript.words.v1.json")) {
        return { ok: true, json: async () => sourceTranscript } as Response;
      }
      if (url.endsWith("/transcript.display.v1.json")) {
        return { ok: true, json: async () => sharedDisplay } as Response;
      }
      if (url.endsWith("/transcript.readable.v1.json")) {
        return { ok: true, json: async () => sharedReadable } as Response;
      }
      // No summary.md, manifest.json, captions, or chapters.
      return { ok: false, status: 404 } as Response;
    }) as typeof fetch;
    return loadArtifactFromDirectory("./meetings/parity-meeting");
  }

  async function loadViaPackedOpus(): Promise<LoadedArtifact> {
    const portableManifest = {
      meeting: { durationMs: sourceTranscript.media.durationMs },
      integrity: { opusAudioSha256: sourceTranscript.media.sha256 },
      speakers: sourceTranscript.speakers,
      transcript: { items: flattenPortableTranscriptItems(sourceTranscript) },
      readableTranscript: sharedReadable,
      displayTranscript: sharedDisplay,
    };
    const {
      transcript: rawTranscript,
      readableTranscript,
      displayTranscript,
      ...manifest
    } = portableManifest;
    const portableBytes = buildPortableOpusFixture({
      manifest,
      rawTranscript,
      readableTranscript,
      displayTranscript,
    });
    globalThis.fetch = vi.fn(async (input: string | URL) => {
      const url = String(input);
      if (url.endsWith(".opus")) {
        return {
          ok: true,
          status: 206,
          headers: new Headers({ "content-range": "bytes 0-1999/2000" }),
          arrayBuffer: async () =>
            portableBytes.buffer.slice(
              portableBytes.byteOffset,
              portableBytes.byteOffset + portableBytes.byteLength,
            ),
        } as Response;
      }
      return { ok: false, status: 404 } as Response;
    }) as typeof fetch;
    return loadPortableArtifactFromAudioPath("./parity-meeting.opus");
  }

  // Strips path-dependent media.src so the shape comparison is about content,
  // not the URL the loader happened to resolve.
  function stripMediaSrc<T extends { media?: { src?: string } }>(value: T): T {
    if (!value || !value.media) {
      return value;
    }
    return { ...value, media: { ...value.media, src: undefined } };
  }

  it("reprojects items[] -> segments[] so the packed `.opus` matches the loose files", async () => {
    setWindow("http://127.0.0.1:8765/?meeting=parity-meeting");
    const loose = await loadViaLooseFiles();
    setWindow("http://127.0.0.1:8765/?meeting=parity-meeting");
    const packed = await loadViaPackedOpus();

    // Sanity: the loose path read what we served, and the portable reprojection
    // really did rebuild the segments (so this isn't a vacuous comparison).
    expect(loose.transcript.segments).toHaveLength(sourceTranscript.segments.length);
    expect(packed.transcript.segments).toHaveLength(sourceTranscript.segments.length);
    expect(packed.transcript).toEqual(buildTranscriptWordsFromPortable(
      {
        meeting: { durationMs: sourceTranscript.media.durationMs },
        integrity: { opusAudioSha256: sourceTranscript.media.sha256 },
        speakers: sourceTranscript.speakers as unknown[],
        transcript: { items: flattenPortableTranscriptItems(sourceTranscript) },
      },
      packed.audioSrc,
    ));

    // Transcript words/segments (ids, text, timing, per-word timing) — modulo
    // the path-dependent media.src.
    expect(stripMediaSrc(packed.transcript)).toEqual(stripMediaSrc(loose.transcript));

    // Speaker set.
    expect(packed.transcript.speakers).toEqual(loose.transcript.speakers);

    // Readable text and segmentation.
    expect(stripMediaSrc(packed.readableTranscript!)).toEqual(stripMediaSrc(loose.readableTranscript!));

    // Display timing (blocks, tokens, per-token start/end + alignment).
    expect(stripMediaSrc(packed.displayTranscript!)).toEqual(stripMediaSrc(loose.displayTranscript!));

    // Derived timing precision classification.
    expect(packed.timingPrecision).toEqual(loose.timingPrecision);

    // The transcript index the viewer renders from.
    expect(stripMediaSrc(packed.index.transcript)).toEqual(stripMediaSrc(loose.index.transcript));
    expect(packed.index.transcript.media.durationMs).toBe(loose.index.transcript.media.durationMs);

    // Meeting metadata: the human-facing sections (Meeting / Processing /
    // Technical) must agree. sourceKind and rawJson legitimately differ between
    // an artifact-directory and a portable-opus load, so they are excluded.
    expect(packed.metadata?.sections).toEqual(loose.metadata?.sections);

    // Path-dependent fields differ by construction; assert that explicitly so
    // the divergence is intentional rather than an accidental regression.
    expect(loose.metadata?.sourceKind).toBe("artifact-directory");
    expect(packed.metadata?.sourceKind).toBe("portable-opus");
    // Loose path resolves media.src against the served transcript's directory;
    // packed path resolves the `.opus` against the document URL.
    expect(loose.audioSrc).toBe("http://127.0.0.1:8765/meetings/parity-meeting/meeting.opus");
    expect(packed.audioSrc).toBe("http://127.0.0.1:8765/parity-meeting.opus");
  });
});

// ---------------------------------------------------------------------------
// Attribution provenance over the packed `.opus` path (D-683).
//
// The production shape this guards: the Go packer flattens transcript words
// into id-LESS per-word items (optionally carrying attributionGapDb /
// lowConfidenceSpeaker; null = not measured), while the packed readable
// transcript's sourceSegmentIds still name the PRODUCER's original segment
// ids. The viewer re-projects items[] into synthetic seg_%06d segments — one
// per item — so those readable ids resolve to nothing, and the crosstalk badge
// can only fire if the display tokens' sourceWordIds path
// (canonicalWordsForBlock, the mapping MeetingView judges each block with)
// finds the flagged canonical words.
// ---------------------------------------------------------------------------
describe("portable attribution end-to-end (crosstalk badge judgement)", () => {
  const originalWindow = globalThis.window;
  const originalFetch = globalThis.fetch;

  afterEach(() => {
    globalThis.window = originalWindow;
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  function serveOpus(portableBytes: Uint8Array) {
    globalThis.fetch = vi.fn(async (input: string | URL) => {
      const url = String(input);
      if (url.endsWith(".opus")) {
        return {
          ok: true,
          status: 206,
          headers: new Headers({ "content-range": "bytes 0-1999/2000" }),
          arrayBuffer: async () =>
            portableBytes.buffer.slice(
              portableBytes.byteOffset,
              portableBytes.byteOffset + portableBytes.byteLength,
            ),
        } as Response;
      }
      return { ok: false } as Response;
    }) as typeof fetch;
  }

  it("judges a flagged portable interjection as crosstalk through the real display mapping", async () => {
    globalThis.window = {
      location: { href: "http://127.0.0.1:8765/?meeting=attributed", protocol: "http:" },
    } as Window;
    const portableFixture = {
      meeting: { durationMs: 4000 },
      integrity: { opusAudioSha256: OPUS_AUDIO_SHA256 },
      speakers: [
        { id: "spk_ana", label: "Ana" },
        { id: "spk_ben", label: "Ben" },
      ],
      transcript: {
        items: [
          // Id-less word-level items, exactly as the Go packer writes them.
          { speaker: "spk_ana", startMs: 0, endMs: 500, text: "we" },
          { speaker: "spk_ana", startMs: 500, endMs: 1000, text: "should" },
          { speaker: "spk_ana", startMs: 1000, endMs: 1500, text: "ship" },
          { speaker: "spk_ana", startMs: 1500, endMs: 2000, text: "it" },
          {
            speaker: "spk_ben",
            startMs: 2500,
            endMs: 2900,
            text: "yeah",
            attributionGapDb: 31.7,
            lowConfidenceSpeaker: true,
          },
        ],
      },
      readableTranscript: {
        version: "transcript.readable.v1",
        speakers: [
          { id: "spk_ana", label: "Ana" },
          { id: "spk_ben", label: "Ben" },
        ],
        segments: [
          {
            id: "readable_000000",
            speaker: "spk_ana",
            startMs: 0,
            endMs: 2000,
            text: "We should ship it.",
            // Producer segment ids: they resolve to NOTHING in the
            // re-projected canonical index (seg_000000..seg_000004, one per
            // word item).
            sourceSegmentIds: ["seg_0"],
          },
          {
            id: "readable_000001",
            speaker: "spk_ben",
            startMs: 2500,
            endMs: 2900,
            text: "Yeah.",
            sourceSegmentIds: ["seg_1"],
          },
        ],
      },
    };
    const { transcript: rawTranscript, readableTranscript, ...manifest } = portableFixture;
    serveOpus(buildPortableOpusFixture({ manifest, rawTranscript, readableTranscript }));

    const artifact = await loadPortableArtifactFromAudioPath("./attributed.opus");

    // The flag survived into the canonical index the viewer judges on.
    const flaggedWords = artifact.index.segments.flatMap((segment) =>
      segment.words.filter((word) => word.lowConfidenceSpeaker),
    );
    expect(flaggedWords).toHaveLength(1);
    expect(flaggedWords[0]).toMatchObject({ text: "yeah", attributionGapDb: 31.7 });

    // Fixture realism: the display blocks' sourceSegmentIds really do NOT
    // resolve against the canonical index — only the token path can find the
    // words, so a regression back to segment-only judgement fails this test.
    const canonicalIds = new Set(artifact.index.segments.map((segment) => segment.id));
    const blocks = artifact.displayTranscript?.blocks ?? [];
    expect(blocks.length).toBeGreaterThan(0);
    for (const block of blocks) {
      for (const sourceSegmentId of block.sourceSegmentIds) {
        expect(canonicalIds.has(sourceSegmentId)).toBe(false);
      }
    }

    // MeetingView's judgement over the REAL display blocks the loader built.
    const judged = blocks.map((block) => ({
      speaker: block.speaker,
      crosstalk: isLikelyCrosstalkTurn(canonicalWordsForBlock(artifact.index, block)),
    }));
    expect(judged).toEqual([
      { speaker: "spk_ana", crosstalk: false },
      { speaker: "spk_ben", crosstalk: true },
    ]);
  });

  it("leaves unmeasured and null-measured portable items unflagged without crashing", async () => {
    globalThis.window = {
      location: { href: "http://127.0.0.1:8765/?meeting=unmeasured", protocol: "http:" },
    } as Window;
    serveOpus(
      buildPortableOpusFixture({
        manifest: {
          meeting: { durationMs: 2000 },
          speakers: [
            { id: "spk_ana", label: "Ana" },
            { id: "spk_ben", label: "Ben" },
          ],
        },
        rawTranscript: {
          items: [
            { speaker: "spk_ana", startMs: 0, endMs: 500, text: "hi" },
            {
              speaker: "spk_ben",
              startMs: 600,
              endMs: 1000,
              text: "yo",
              attributionGapDb: null,
              lowConfidenceSpeaker: null,
            },
          ],
        },
      }),
    );

    const artifact = await loadPortableArtifactFromAudioPath("./unmeasured.opus");

    const allWords = artifact.index.segments.flatMap((segment) => segment.words);
    expect(allWords).toHaveLength(2);
    for (const word of allWords) {
      expect(word.lowConfidenceSpeaker).toBeUndefined();
      expect(word.attributionGapDb).toBeUndefined();
    }
    const blocks = artifact.displayTranscript?.blocks ?? [];
    expect(blocks.length).toBeGreaterThan(0);
    for (const block of blocks) {
      expect(isLikelyCrosstalkTurn(canonicalWordsForBlock(artifact.index, block))).toBe(false);
    }
  });
});

describe("word-timing provenance (does the fallback repair apply?)", () => {
  const originalWindow = globalThis.window;
  const originalFetch = globalThis.fetch;

  afterEach(() => {
    globalThis.window = originalWindow;
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  function stubWindow() {
    globalThis.window = {
      location: { href: "http://127.0.0.1:8765/", protocol: "http:" },
    } as Window;
  }

  function servePortable(fixture: Parameters<typeof buildPortableOpusFixture>[0]) {
    const bytes = buildPortableOpusFixture(fixture);
    globalThis.fetch = vi.fn(async (input: string | URL) => {
      if (!String(input).endsWith(".opus")) {
        return { ok: false } as Response;
      }
      return {
        ok: true,
        status: 206,
        headers: new Headers({ "content-range": `bytes 0-${bytes.byteLength - 1}/${bytes.byteLength}` }),
        arrayBuffer: async () => bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength),
      } as Response;
    }) as typeof fetch;
  }

  const portableBaseManifest = {
    meeting: { durationMs: 5000 },
    integrity: { opusAudioSha256: OPUS_AUDIO_SHA256 },
    speakers: [{ id: "spk_1", label: "Alice" }],
  };
  const portableBaseTranscript = {
    items: [
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

  it("reads the marker off a portable manifest so the viewer stands the repair down", async () => {
    stubWindow();
    servePortable({
      manifest: {
        ...portableBaseManifest,
        provenance: {
          speechToText: { "raw-asr": { engine: "sherpa-onnx" } },
          wordTimings: { endsBoundedByAudio: true },
        },
      },
      rawTranscript: portableBaseTranscript,
    });

    const artifact = await loadPortableArtifactFromAudioPath("./bounded.opus");

    expect(artifact.wordEndsBoundedByAudio).toBe(true);
  });

  it("shows the reader that this artifact's word ends were measured", async () => {
    stubWindow();
    servePortable({
      manifest: {
        ...portableBaseManifest,
        provenance: { wordTimings: { endsBoundedByAudio: true } },
      },
      rawTranscript: portableBaseTranscript,
    });

    const artifact = await loadPortableArtifactFromAudioPath("./bounded.opus");
    const processing = artifact.metadata?.sections.find((section) => section.title === "Processing");

    expect(processing?.rows).toContainEqual({
      label: "Word timings",
      value: "Ends bounded by measured audio",
    });
  });

  it("leaves the repair running for a portable meeting with no marker", async () => {
    stubWindow();
    servePortable({
      manifest: {
        ...portableBaseManifest,
        provenance: { speechToText: { "raw-asr": { engine: "sherpa-onnx" } } },
      },
      rawTranscript: portableBaseTranscript,
    });

    const artifact = await loadPortableArtifactFromAudioPath("./unmarked.opus");
    const processing = artifact.metadata?.sections.find((section) => section.title === "Processing");

    expect(artifact.wordEndsBoundedByAudio).toBe(false);
    expect(processing?.rows.some((row) => row.label === "Word timings")).toBe(false);
  });

  it("leaves the repair running for a portable meeting with no provenance at all", async () => {
    stubWindow();
    servePortable({ manifest: portableBaseManifest, rawTranscript: portableBaseTranscript });

    expect((await loadPortableArtifactFromAudioPath("./ancient.opus")).wordEndsBoundedByAudio).toBe(false);
  });

  it("refuses anything but the literal boolean, so a stray string cannot disarm the repair", async () => {
    stubWindow();
    servePortable({
      manifest: {
        ...portableBaseManifest,
        provenance: { wordTimings: { endsBoundedByAudio: "true" } },
      },
      rawTranscript: portableBaseTranscript,
    });

    expect((await loadPortableArtifactFromAudioPath("./odd.opus")).wordEndsBoundedByAudio).toBe(false);
  });

  it("reads the marker off an artifact directory's manifest.json too", async () => {
    globalThis.window = {
      location: { href: "http://127.0.0.1:8765/?meeting=demo", protocol: "http:" },
    } as Window;
    globalThis.fetch = vi.fn(async (input: string | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === "HEAD") {
        return { ok: true } as Response;
      }
      if (url.endsWith("/transcript.words.v1.json")) {
        return { ok: true, json: async () => transcriptFixture } as Response;
      }
      if (url.endsWith("/manifest.json")) {
        return {
          ok: true,
          json: async () => ({ provenance: { wordTimings: { endsBoundedByAudio: true } } }),
        } as Response;
      }
      return { ok: false } as Response;
    }) as typeof fetch;

    const artifact = await loadArtifactFromDirectory("./meetings/demo");

    expect(artifact.wordEndsBoundedByAudio).toBe(true);
  });

  it("leaves the repair running for an artifact directory with no manifest", async () => {
    globalThis.window = {
      location: { href: "http://127.0.0.1:8765/?meeting=demo", protocol: "http:" },
    } as Window;
    globalThis.fetch = vi.fn(async (input: string | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === "HEAD") {
        return { ok: true } as Response;
      }
      if (url.endsWith("/transcript.words.v1.json")) {
        return { ok: true, json: async () => transcriptFixture } as Response;
      }
      return { ok: false } as Response;
    }) as typeof fetch;

    expect((await loadArtifactFromDirectory("./meetings/demo")).wordEndsBoundedByAudio).toBe(false);
  });
});

describe("the display pipeline MeetingView runs, end to end", () => {
  const originalWindow = globalThis.window;
  const originalFetch = globalThis.fetch;

  afterEach(() => {
    globalThis.window = originalWindow;
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  function servePortableMeeting(fixture: Parameters<typeof buildPortableOpusFixture>[0]) {
    globalThis.window = {
      location: { href: "http://127.0.0.1:8765/", protocol: "http:" },
    } as Window;
    const bytes = buildPortableOpusFixture(fixture);
    globalThis.fetch = vi.fn(async (input: string | URL) => {
      if (!String(input).endsWith(".opus")) {
        return { ok: false } as Response;
      }
      return {
        ok: true,
        status: 206,
        headers: new Headers({ "content-range": `bytes 0-${bytes.byteLength - 1}/${bytes.byteLength}` }),
        arrayBuffer: async () => bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength),
      } as Response;
    }) as typeof fetch;
  }

  /**
   * Exactly what MeetingView's reactive block does to a loaded artifact:
   * project each display block with the canonical words resolved onto it, then
   * reading order, then the effective timings.
   */
  function displaySegmentsFor(artifact: LoadedArtifact) {
    const blocks = (artifact.displayTranscript?.blocks ?? []).map((block) => ({
      ...block,
      words: canonicalWordsForBlock(artifact.index, block),
    }));
    return sortBlocksInReadingOrder(
      repairTurnFinalWordInflation(blocks, {
        endsBoundedByAudio: artifact.wordEndsBoundedByAudio,
      }),
    );
  }

  /**
   * Ten ordinary 240 ms words and one that the producer MEASURED at 1.44 s.
   * Against a 240 ms median the fallback budget is max(1000, 4 × 240) = 1000 ms,
   * so the display-time repair would clip "held." to 5400 — undoing, in the
   * viewer, the fix the producer just made.
   */
  const heldWordItems = [
    ...Array.from({ length: 10 }, (_, index) => ({
      id: `w_${index}`,
      speaker: "spk_1",
      text: `word${index}`,
      startMs: 2000 + index * 240,
      endMs: 2000 + (index + 1) * 240,
    })),
    { id: "w_last", speaker: "spk_1", text: "held.", startMs: 4400, endMs: 5840 },
  ];

  it("keeps a measured 1.44 s word intact all the way to the rendered spans", async () => {
    // The fp32 evidence. "held." runs 1.44 s against a 240 ms median, which is
    // four times the fallback budget's reference and would be clipped to 1 s by
    // the display-time repair — undoing, in the viewer, the fix the producer
    // just made.
    servePortableMeeting({
      manifest: {
        meeting: { durationMs: 6000 },
        integrity: { opusAudioSha256: OPUS_AUDIO_SHA256 },
        speakers: [{ id: "spk_1", label: "Alice" }],
        provenance: { wordTimings: { endsBoundedByAudio: true } },
      },
      rawTranscript: { items: heldWordItems },
    });

    const artifact = await loadPortableArtifactFromAudioPath("./measured-word-end.opus");
    const segments = displaySegmentsFor(artifact);

    expect(artifact.wordEndsBoundedByAudio).toBe(true);
    expect(segments[0]?.tokens.filter((token) => token.kind === "word").at(-1)).toMatchObject({
      text: "held",
      startMs: 4400,
      endMs: 5840,
    });
    expect(segments[0]?.words.at(-1)).toMatchObject({ text: "held.", startMs: 4400, endMs: 5840 });
    expect(segments[0]?.endMs).toBe(5840);
  });

  it("clips that same word when the artifact carries no marker", async () => {
    servePortableMeeting({
      manifest: {
        meeting: { durationMs: 6000 },
        integrity: { opusAudioSha256: OPUS_AUDIO_SHA256 },
        speakers: [{ id: "spk_1", label: "Alice" }],
      },
      rawTranscript: { items: heldWordItems },
    });

    const artifact = await loadPortableArtifactFromAudioPath("./unmarked-word-end.opus");
    const segments = displaySegmentsFor(artifact);

    expect(artifact.wordEndsBoundedByAudio).toBe(false);
    expect(segments[0]?.tokens.filter((token) => token.kind === "word").at(-1)).toMatchObject({
      text: "held",
      startMs: 4400,
      endMs: 5400,
    });
    expect(segments[0]?.words.at(-1)).toMatchObject({ text: "held.", startMs: 4400, endMs: 5400 });
    expect(segments[0]?.endMs).toBe(5400);
  });

  /** A published readable body may omit a display body. In that case the
   * viewer builds the display while preserving the readable paragraph and its
   * word-level overlap evidence.
   */
  it("keeps a rebuilt paragraph whole and marks what landed inside it", async () => {
    const hostWords = [
      { text: "So", startMs: 1000, endMs: 1700 },
      { text: "the", startMs: 1700, endMs: 2400 },
      { text: "installer", startMs: 2400, endMs: 3100 },
      { text: "is", startMs: 3100, endMs: 3800 },
      { text: "finished", startMs: 3800, endMs: 4500 },
      { text: "and", startMs: 4500, endMs: 5200 },
      { text: "the", startMs: 5200, endMs: 5900 },
      { text: "documentation", startMs: 5900, endMs: 6600 },
      { text: "went", startMs: 6600, endMs: 7300 },
      { text: "out.", startMs: 7300, endMs: 8000 },
    ];
    servePortableMeeting({
      manifest: {
        meeting: { durationMs: 9000 },
        integrity: { opusAudioSha256: OPUS_AUDIO_SHA256 },
        speakers: [
          { id: "spk_1", label: "Alice" },
          { id: "spk_2", label: "Bob" },
        ],
      },
      rawTranscript: {
        items: [
          ...hostWords.slice(0, 5).map((word, index) => ({
            id: `host_${index}`,
            speaker: "spk_1",
            ...word,
          })),
          { id: "aside_0", speaker: "spk_2", startMs: 4000, endMs: 4600, text: "Right." },
          ...hostWords.slice(5).map((word, index) => ({
            id: `host_${index + 5}`,
            speaker: "spk_1",
            ...word,
          })),
        ],
      },
      readableTranscript: {
        version: "transcript.readable.v1",
        speakers: [
          { id: "spk_1", label: "Alice" },
          { id: "spk_2", label: "Bob" },
        ],
        segments: [
          {
            id: "rseg_1",
            speaker: "spk_1",
            startMs: 1000,
            endMs: 8000,
            text: hostWords.map((word) => word.text).join(" "),
            sourceSegmentIds: hostWords.map((_, index) => `host_${index}`),
            words: hostWords,
          },
          {
            id: "rseg_2",
            speaker: "spk_2",
            startMs: 4000,
            endMs: 4600,
            text: "Right.",
            sourceSegmentIds: ["aside_0"],
            words: [{ text: "Right.", startMs: 4000, endMs: 4600 }],
          },
        ],
      },
    });

    const artifact = await loadPortableArtifactFromAudioPath("./readable-words.opus");
    const segments = displaySegmentsFor(artifact);
    const analysis = analyzeOverlap(segments);

    // The display builder used the published readable word timing.
    expect(segments[0]?.tokens[0]).toMatchObject({ text: "So", startMs: 1000, endMs: 1700 });
    // The paragraph is not cut, and no prose is redistributed by word count.
    expect(segments).toHaveLength(2);
    expect(segments[0]?.text).toBe("So the installer is finished and the documentation went out.");
    expect(segments.map((segment) => segment.id)).toEqual(["rseg_1", "rseg_2"]);
    // What the reader gets instead of the split: two paragraphs, each naming
    // the other speaker as having been on the recording at the same time.
    expect(analysis.get("rseg_2")?.containedIn).toBe("rseg_1");
    expect(buildTranscriptRows(segments).map((row) => [row.key, [...row.over]])).toEqual([
      ["rseg_1", ["Bob"]],
      ["rseg_2", ["Alice"]],
    ]);
  });
});

function encodeBodyForOpusTags(
  body: unknown,
  prefix: string,
): { tags: Record<string, string>; payloadRef: { prefix: string; chunkCount: number; sha256: string; rawBytes: number; gzipBytes: number; mime: string; encoding: string } } {
  const raw = Buffer.from(JSON.stringify(body), "utf8");
  const gzip = gzipSync(raw);
  const encoded = gzip
    .toString("base64")
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/g, "");
  const chunkSize = 4096;
  const tags: Record<string, string> = {};
  let chunkCount = 0;
  for (let offset = 0; offset < encoded.length; offset += chunkSize) {
    tags[`${prefix}${String(chunkCount).padStart(3, "0")}`] = encoded.slice(offset, offset + chunkSize);
    chunkCount += 1;
  }
  if (chunkCount === 0) {
    tags[`${prefix}000`] = "";
    chunkCount = 1;
  }
  return {
    tags,
    payloadRef: {
      prefix,
      chunkCount,
      sha256: createHash("sha256").update(raw).digest("hex"),
      rawBytes: raw.byteLength,
      gzipBytes: gzip.byteLength,
      mime: "application/vnd.cassini.transcript-words+json",
      encoding: "base64url+gzip+utf8json",
    },
  };
}

function buildPortableOpusFixture({
  manifest,
  rawTranscript,
  readableTranscript,
  displayTranscript,
  extraTags = {},
}: {
  manifest: object;
  rawTranscript?: unknown;
  readableTranscript?: unknown;
  displayTranscript?: unknown;
  extraTags?: Record<string, string>;
}): Uint8Array {
  const wire = structuredClone(manifest) as Record<string, any>;
  const bodyTags: Record<string, string> = {};
  const rawTranscriptId = "raw-asr";
  if (rawTranscript !== undefined) {
    const encoded = encodeBodyForOpusTags(rawTranscript, "CASSINI_TX_RAW_ASR_PAYLOAD_");
    Object.assign(bodyTags, encoded.tags);
    wire.transcripts = [{
      id: rawTranscriptId,
      role: "raw-asr",
      default: true,
      format: String((rawTranscript as any)?.format ?? (rawTranscript as any)?.version ?? "cassini.words.v1"),
      language: typeof (rawTranscript as any)?.language === "string" ? (rawTranscript as any).language : undefined,
      wordCount: Array.isArray((rawTranscript as any)?.items) ? (rawTranscript as any).items.length : 0,
      payloadRef: encoded.payloadRef,
    }];
  }

  const derivedEntries = Array.isArray(wire.readableTranscripts)
    ? [...wire.readableTranscripts]
    : [];
  for (const [body, id, role] of [
    [readableTranscript, "readable", "readable-cleanup"],
    [displayTranscript, "display", "display"],
  ] as const) {
    if (!body) {
      continue;
    }
    const prefix = `CASSINI_TX_${id.toUpperCase()}_PAYLOAD_`;
    const encoded = encodeBodyForOpusTags(body, prefix);
    Object.assign(bodyTags, encoded.tags);
    derivedEntries.push({
      id,
      role,
      default: true,
      format: String(body.format ?? body.version ?? `cassini.${role}.v1`),
      sourceTranscriptId: rawTranscriptId,
      payloadRef: encoded.payloadRef,
    });
  }
  if (derivedEntries.length > 0) {
    wire.readableTranscripts = derivedEntries;
  }

  wire.kind ??= "cassini-portable-meeting";
  wire.version ??= 1;
  wire.profile ??= "ogg-opus";
  wire.integrity = {
    matchPolicy: "exact-opus-audio-v1",
    opusAudioSha256: "a".repeat(64),
    ...((wire.integrity as object | undefined) ?? {}),
  };

  const rawJson = Buffer.from(JSON.stringify(wire), "utf8");
  const compressed = gzipSync(rawJson);
  const encoded = compressed
    .toString("base64")
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/g, "");
  const tags = {
    CASSINI_FORMAT: "org.cassini.portable-meeting/1",
    CASSINI_PROFILE: "ogg-opus",
    CASSINI_PAYLOAD_MIME: "application/vnd.cassini.portable-meeting+json",
    CASSINI_PAYLOAD_ENCODING: "base64url+gzip+utf8json",
    CASSINI_PAYLOAD_SCHEMA:
      "https://cassini-format.codemyriad.io/schema/cassini-portable-meeting-manifest-v1.schema.json",
    CASSINI_AUDIO_MATCH_POLICY: "exact-opus-audio-v1",
    CASSINI_AUDIO_OPUS_SHA256: String(
      (wire.integrity as { opusAudioSha256?: string }).opusAudioSha256,
    ),
    CASSINI_PAYLOAD_CHUNK_COUNT: "1",
    CASSINI_PAYLOAD_SHA256: createHash("sha256").update(rawJson).digest("hex"),
    CASSINI_PAYLOAD_RAW_BYTES: String(rawJson.byteLength),
    CASSINI_PAYLOAD_GZIP_BYTES: String(compressed.byteLength),
    CASSINI_PAYLOAD_000: encoded,
    ...bodyTags,
    ...extraTags,
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
