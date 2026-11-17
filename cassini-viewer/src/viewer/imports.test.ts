import { describeImportSharing } from "./imports";
import { describe, expect, it } from "vitest";

import { formatImportDuration, importJobTerminal, parseImportPreview } from "./imports";
import { durationFormat } from "../i18n";

function fileOf(name: string, bytes: Uint8Array | string): File {
  const body = typeof bytes === "string" ? new TextEncoder().encode(bytes) : bytes;
  return new File([body], name);
}


// oggStream lays packets out as Ogg pages the way an encoder does: 255-byte
// lacing, at most 255 segments a page. The parser never checks the CRC.
function oggStream(packets: Uint8Array[]): Uint8Array {
  const pages: Uint8Array[] = [];
  for (const packet of packets) {
    const lacing: number[] = [];
    for (let left = packet.length; ; left -= 255) {
      lacing.push(Math.min(left, 255));
      if (left < 255) break;
    }
    let offset = 0;
    for (let i = 0; i < lacing.length; i += 255) {
      const segs = lacing.slice(i, i + 255);
      const size = segs.reduce((a, b) => a + b, 0);
      const page = new Uint8Array(27 + segs.length + size);
      page.set([0x4f, 0x67, 0x67, 0x53]);
      page[26] = segs.length;
      page.set(segs, 27);
      page.set(packet.subarray(offset, offset + size), 27 + segs.length);
      offset += size;
      pages.push(page);
    }
  }
  const out = new Uint8Array(pages.reduce((n, p) => n + p.length, 0));
  let at = 0;
  for (const page of pages) {
    out.set(page, at);
    at += page.length;
  }
  return out;
}

function opusTags(comments: string[]): Uint8Array {
  const enc = new TextEncoder();
  const parts = comments.map((c) => enc.encode(c));
  const vendor = enc.encode("test");
  const out = new Uint8Array(8 + 4 + vendor.length + 4 + parts.reduce((n, p) => n + 4 + p.length, 0));
  const view = new DataView(out.buffer);
  out.set(enc.encode("OpusTags"));
  view.setUint32(8, vendor.length, true);
  out.set(vendor, 12);
  let at = 12 + vendor.length;
  view.setUint32(at, parts.length, true);
  at += 4;
  for (const part of parts) {
    view.setUint32(at, part.length, true);
    out.set(part, at + 4);
    at += 4 + part.length;
  }
  return out;
}

const opusHead = new TextEncoder().encode("OpusHead\u0001\u0001\u0000\u0000\u0080\u00bb\u0000\u0000\u0000\u0000\u0000");

describe("parseImportPreview", () => {
  it("refuses an empty file", async () => {
    await expect(parseImportPreview(fileOf("a.opus", new Uint8Array()))).rejects.toThrow(/empty/i);
  });

  it("refuses a file that is neither Ogg, JSON nor plausibly audio", async () => {
    await expect(parseImportPreview(fileOf("notes.txt", "RIFF....WAVE"))).rejects.toThrow(
      /not a portable meeting/i,
    );
  });

  it("takes a raw audio recording as a meeting source and names it from the file", async () => {
    const preview = await parseImportPreview(fileOf("Board offsite.m4a", "not decoded in the browser"));
    expect(preview.format).toBe("audio");
    expect(preview.title).toBe("Board offsite");
    expect(preview.hasAudio).toBe(true);
  });

  it("routes an OggS prefix to the portable parser, which fails closed without the format tag", async () => {
    const bytes = new TextEncoder().encode("OggS\u0000\u0002not-really-a-page");
    await expect(parseImportPreview(fileOf("renamed.json", bytes))).rejects.toThrow();
  });

  it("takes an Ogg recording without Cassini tags as audio", async () => {
    const bytes = new TextEncoder().encode("OggS\u0000\u0002OpusHead....OpusTags plain recorder");
    const preview = await parseImportPreview(fileOf("Standup.opus", bytes));
    expect(preview.format).toBe("audio");
    expect(preview.title).toBe("Standup");
  });

  it("refuses a damaged portable meeting instead of re-transcribing its audio", async () => {
    const bytes = new TextEncoder().encode("OggS\u0000\u0002OpusHead....OpusTags CASSINI_FORMAT=org.cassini.portable-meeting/9");
    await expect(parseImportPreview(fileOf("Standup.opus", bytes))).rejects.toThrow();
  });

  it("finds Cassini tags that come after more than a MiB of cover art", async () => {
    const art = "METADATA_BLOCK_PICTURE=" + "A".repeat(2 << 20);
    const stream = oggStream([opusHead, opusTags([art, "CASSINI_FORMAT=org.cassini.portable-meeting/1"])]);
    await expect(parseImportPreview(fileOf("Standup.opus", stream))).rejects.toThrow();
  });

  it("takes a recording whose comment values mention CASSINI_ as audio", async () => {
    const stream = oggStream([opusHead, opusTags(["COMMENT=exported CASSINI_FORMAT by hand"])]);
    const preview = await parseImportPreview(fileOf("Standup.opus", stream));
    expect(preview.format).toBe("audio");
  });

  it("recognises an audio file from its head, without reading it whole", async () => {
    class HugeFile extends File {
      override arrayBuffer(): Promise<ArrayBuffer> {
        throw new Error("read the whole file");
      }
      override slice(start?: number, end?: number): Blob {
        return new Blob([new Uint8Array(Math.min(end ?? 0, 1 << 20) - (start ?? 0))]);
      }
    }
    const preview = await parseImportPreview(new HugeFile([], "Offsite.wav", { type: "audio/wav" }));
    expect(preview.format).toBe("audio");
    await expect(parseImportPreview(new HugeFile([], "Video.mp4", { type: "video/mp4" }))).rejects.toThrow(
      /not a portable meeting/i,
    );
  });

  it("routes JSON content to the transcription parser even under a BOM and an .opus name", async () => {
    // Content decides the parser; the transcription reader then refuses the
    // wrong kind with its own sentence.
    const body = "﻿" + JSON.stringify({ kind: "something-else", version: 1 });
    await expect(parseImportPreview(fileOf("renamed.opus", body))).rejects.toThrow(
      /unsupported transcription document/i,
    );
  });
});

describe("importJobTerminal", () => {
  it("stops polling for settled or staged jobs", () => {
    expect(importJobTerminal("done")).toBe(true);
    expect(importJobTerminal("staged")).toBe(true);
    expect(importJobTerminal("upload_failed")).toBe(true);
    expect(importJobTerminal("uploading")).toBe(false);
    expect(importJobTerminal("failed")).toBe(true);
    expect(importJobTerminal("canceled")).toBe(true);
    for (const state of ["queued", "fetching", "validating", "delivering", "sharing", "indexing"] as const) {
      expect(importJobTerminal(state)).toBe(false);
    }
  });
});

describe("formatImportDuration", () => {
  it("reads as people say durations", () => {
    expect(formatImportDuration(0)).toBe("");
    expect(formatImportDuration(20_000)).toBe("under a minute");
    expect(formatImportDuration(12 * 60_000)).toBe(durationFormat({ hours: 0, minutes: 12 }));
    expect(formatImportDuration(60 * 60_000)).toBe(durationFormat({ hours: 1 }));
    expect(formatImportDuration(92 * 60_000)).toBe(durationFormat({ hours: 1, minutes: 32 }));
    // In the locale's units, the same in every runtime.
    expect(formatImportDuration(92 * 60_000)).toBe("1 hr 32 mins");
  });
});

describe("describeImportSharing", () => {
  it("names the importer and every recipient, kinds marked", () => {
    const got = describeImportSharing({
      sharing: {
        share: true,
        audience: [
          { type: "user", id: "bob", label: "Bob Lee" },
          { type: "group", id: "design", label: "Design" },
          { type: "circle", id: "c1", label: "Core" },
          { type: "user", id: "nolabel" },
        ],
      },
    });
    expect(got.line).toBe("Shared with 5 recipients");
    expect(got.title).toBe("you, Bob Lee, Design (group), Core (team), nolabel");
  });
  it("says so when the meeting is private", () => {
    expect(describeImportSharing({ sharing: { share: false } })).toEqual({ line: "Only you", title: "Only you can open this meeting." });
  });
});
