import { describe, expect, it } from "vitest";

import { formatImportDuration, importJobTerminal, parseImportPreview } from "./imports";

function fileOf(name: string, bytes: Uint8Array | string): File {
  const body = typeof bytes === "string" ? new TextEncoder().encode(bytes) : bytes;
  return new File([body], name);
}

describe("parseImportPreview", () => {
  it("refuses an empty file", async () => {
    await expect(parseImportPreview(fileOf("a.opus", new Uint8Array()))).rejects.toThrow(/empty/i);
  });

  it("refuses a file that is neither Ogg nor JSON, whatever its name says", async () => {
    await expect(parseImportPreview(fileOf("meeting.opus", "RIFF....WAVE"))).rejects.toThrow(
      /not a portable meeting/i,
    );
  });

  it("routes an OggS prefix to the portable parser, which fails closed without the format tag", async () => {
    const bytes = new TextEncoder().encode("OggS\u0000\u0002not-really-a-page");
    await expect(parseImportPreview(fileOf("renamed.json", bytes))).rejects.toThrow();
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
    expect(formatImportDuration(12 * 60_000)).toBe("12 min");
    expect(formatImportDuration(60 * 60_000)).toBe("1 h");
    expect(formatImportDuration(92 * 60_000)).toBe("1 h 32 min");
  });
});
