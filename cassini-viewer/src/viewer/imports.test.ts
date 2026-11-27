import { describeImportSharing } from "./imports";
import { describe, expect, it } from "vitest";

import { ImportFileError, formatImportDuration, formatImportFileLimit, importJobTerminal, importRunningStage, parseImportPreview, type ImportFileProblem } from "./imports";
import { durationFormat } from "../i18n";

function fileOf(name: string, bytes: Uint8Array | string): File {
  const body = typeof bytes === "string" ? new TextEncoder().encode(bytes) : bytes;
  return new File([body], name);
}

// An Ogg stream of two pages: an OpusHead, then the given second packet
// (OpusTags with these tags, unless raw bytes are given).
function oggOf(second: Record<string, string> | Uint8Array): Uint8Array {
  const encoder = new TextEncoder();
  const page = (packet: Uint8Array, sequence: number) => {
    const segments: number[] = [];
    let remaining = packet.length;
    for (; remaining >= 255; remaining -= 255) segments.push(255);
    segments.push(remaining);
    const out = new Uint8Array(27 + segments.length + packet.length);
    out.set(encoder.encode("OggS"), 0);
    new DataView(out.buffer).setUint32(18, sequence, true);
    out[26] = segments.length;
    out.set(segments, 27);
    out.set(packet, 27 + segments.length);
    return out;
  };
  const head = new Uint8Array(19);
  head.set(encoder.encode("OpusHead"), 0);
  let tags = second;
  if (!(second instanceof Uint8Array)) {
    const comments = Object.entries(second).map(([key, value]) => encoder.encode(`${key}=${value}`));
    const packet = new Uint8Array(16 + comments.reduce((sum, c) => sum + 4 + c.length, 0));
    packet.set(encoder.encode("OpusTags"), 0);
    const view = new DataView(packet.buffer);
    view.setUint32(12, comments.length, true);
    let offset = 16;
    for (const comment of comments) {
      view.setUint32(offset, comment.length, true);
      packet.set(comment, offset + 4);
      offset += 4 + comment.length;
    }
    tags = packet;
  }
  const pages = [page(head, 0), page(tags as Uint8Array, 1)];
  const out = new Uint8Array(pages[0].length + pages[1].length);
  out.set(pages[0], 0);
  out.set(pages[1], pages[0].length);
  return out;
}

async function refusal(file: File): Promise<ImportFileError> {
  try {
    await parseImportPreview(file);
  } catch (error) {
    expect(error).toBeInstanceOf(ImportFileError);
    return error as ImportFileError;
  }
  throw new Error("the file was accepted");
}

describe("parseImportPreview", () => {
  // #510: a person learns what is wrong in a sentence of their own language;
  // the parser's words stay as the detail.
  it.each<[string, File, ImportFileProblem, RegExp, RegExp | ""]>([
    ["an empty file", fileOf("a.opus", new Uint8Array()), "empty", /^This file is empty/, ""],
    ["a file the browser cannot read", { name: "moved.opus", size: 10, arrayBuffer: () => Promise.reject(new DOMException("gone", "NotReadableError")) } as unknown as File, "unreadable", /^This file couldn't be read/, /gone/],
    ["a Cassini transcription cut short", fileOf("half.json", JSON.stringify({ kind: "cassini-transcription", version: 1, source: {} }).slice(0, 40)), "damaged", /damaged or incomplete/, ""],
    // Never read: a stand-in that would fail if it were.
    ["a file over 4 GiB", { name: "huge.opus", size: 4 * 2 ** 30 + 1, arrayBuffer: () => Promise.reject(new Error("read")) } as unknown as File, "too_large", /^This file is larger than 4 GiB/, ""],
    ["neither Ogg nor JSON, whatever its name says", fileOf("meeting.opus", "RIFF....WAVE"), "not_cassini", /^Not a meeting file from Cassini/, /neither an Ogg\/Opus/],
    ["an ordinary Opus recording", fileOf("song.opus", oggOf({ TITLE: "A song" })), "not_cassini", /^Not a meeting file from Cassini/, /missing CASSINI_FORMAT/],
    ["an Ogg stream that is not Opus", fileOf("song.ogg", oggOf(new TextEncoder().encode("\u0003vorbis-comments"))), "not_cassini", /^Not a meeting file from Cassini/, /OpusTags/],
    ["a newer portable format", fileOf("m.opus", oggOf({ CASSINI_FORMAT: "org.cassini.portable-meeting/2" })), "newer", /^This file was made by a newer version of Cassini/, /CASSINI_FORMAT=org.cassini.portable-meeting\/2/],
    ["a Cassini file missing its other tags", fileOf("m.opus", oggOf({ CASSINI_FORMAT: "org.cassini.portable-meeting/1" })), "damaged", /^This file is damaged or incomplete/, /CASSINI_PROFILE/],
    ["a truncated Ogg stream", fileOf("renamed.json", new TextEncoder().encode("OggS\u0000\u0002not-really-a-page")), "damaged", /damaged or incomplete/, ""],
    ["another kind of JSON, under a BOM and an .opus name", fileOf("renamed.opus", "\ufeff" + JSON.stringify({ kind: "something-else", version: 1 })), "not_cassini", /^Not a meeting file from Cassini/, /unsupported transcription document/i],
    ["a newer transcription document", fileOf("m.json", JSON.stringify({ kind: "cassini-transcription", version: 2 })), "newer", /newer version of Cassini/, /version 2/],
    ["a transcription document with no meeting in it", fileOf("m.json", JSON.stringify({ kind: "cassini-transcription", version: 1 })), "damaged", /damaged or incomplete/, /unsupported transcription document/i],
  ])("refuses %s", async (_name, file, problem, message, detail) => {
    const error = await refusal(file);
    expect(error.problem).toBe(problem);
    expect(error.message).toMatch(message);
    if (detail) expect(error.detail).toMatch(detail);
  });
});

// #508: an administrator's lower limit is checked before the file is read,
// and the sentence names it.
describe("the file size limit", () => {
  it("refuses a file over a lower limit, naming it", async () => {
    const file = { name: "big.opus", size: 500 * 2 ** 20 + 1, arrayBuffer: () => Promise.reject(new Error("read")) } as unknown as File;
    const error = await parseImportPreview(file, 500 * 2 ** 20).then(
      () => { throw new Error("the file was accepted"); },
      (error: unknown) => error as ImportFileError,
    );
    expect(error.problem).toBe("too_large");
    expect(error.message).toMatch(/^This file is larger than 500 MiB/);
  });

  it("words a limit in GiB when it is whole, else in MiB", () => {
    expect(formatImportFileLimit(4 * 2 ** 30)).toBe("4 GiB");
    expect(formatImportFileLimit(2 ** 30)).toBe("1 GiB");
    expect(formatImportFileLimit(1536 * 2 ** 20)).toBe("1536 MiB");
    expect(formatImportFileLimit(2 ** 20)).toBe("1 MiB");
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
  // #484: who Nextcloud's limit held back is waiting, and who it refused is
  // named with its reason; neither counts as shared with yet.
  it("says who is still waiting for Nextcloud's limit and who was refused", () => {
    const got = describeImportSharing({
      createdBy: "alice",
      sharing: {
        share: true,
        audience: [{ type: "user", id: "bob", label: "Bob Lee" }, { type: "group", id: "design", label: "Design" }, { type: "user", id: "eve" }],
        waiting: [{ type: "user", id: "bob" }, { type: "group", id: "design" }],
        notShared: [{ type: "user", id: "eve", reason: "Sharing is only allowed with group members" }],
      },
    });
    expect(got.line).toBe("Shared with 1 recipient · 2 waiting · 1 not shared");
    expect(got.title.split("\n")).toEqual([
      "you",
      "Nextcloud lets Cassini create only 20 shares every 10 minutes. Cassini will share this meeting with Bob Lee, Design (group) as soon as it can; nobody needs to do anything.",
      "Nextcloud refused to share it with eve (Sharing is only allowed with group members).",
    ]);
  });
  it("counts each recipient once, the importer listed again included", () => {
    const got = describeImportSharing({
      createdBy: "alice",
      sharing: {
        share: true,
        audience: [{ type: "user", id: "alice" }, { type: "user", id: "bob" }, { type: "user", id: "bob" }],
        waiting: [{ type: "user", id: "alice" }, { type: "user", id: "bob" }],
      },
    });
    expect(got.line).toBe("Not shared yet · 2 waiting");
    expect(got.title).not.toMatch(/^you,/);
  });
  it("says a meeting nobody can open yet is not shared yet", () => {
    const got = describeImportSharing({ createdBy: "alice", sharing: { share: false, waiting: [{ type: "user", id: "alice" }] } });
    expect(got.line).toBe("Not shared yet · 1 waiting");
    expect(got.title).toContain("Cassini will share this meeting with you as soon as it can");
  });
  it("says so when the meeting is private", () => {
    expect(describeImportSharing({ sharing: { share: false } })).toEqual({ line: "Only you", title: "Only you can open this meeting." });
  });
});

describe("importRunningStage", () => {
  it("tells a queued import from one being worked on", () => {
    expect(importRunningStage({ state: "uploading" })).toBe("uploading");
    // Next in the person's own queue, or from a server that sends no position.
    expect(importRunningStage({ state: "queued", queuePosition: 0 })).toBe("waiting");
    expect(importRunningStage({ state: "queued" })).toBe("waiting");
    expect(importRunningStage({ state: "queued", queuePosition: 2 })).toBe("queued");
    expect(importRunningStage({ state: "fetching" })).toBe("fetching");
    expect(importRunningStage({ state: "validating" })).toBe("checking");
    for (const state of ["delivering", "sharing", "indexing"] as const) {
      expect(importRunningStage({ state })).toBe("adding");
    }
  });
});
