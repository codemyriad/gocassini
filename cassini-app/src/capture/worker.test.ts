// The worker owns the only copy of a capture that has not been uploaded yet,
// so the failure modes worth testing here are the ones that lose audio the
// browser already wrote: a recovery sidecar that cannot be written must not
// take a healthy segment down with it, and it must not be rewritten so often
// that a long call pays for it.
//
// worker.ts wires its globals only when `self.postMessage` exists, so stubbing
// the globals before importing it gives a real module under test rather than a
// reimplementation of one.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SOURCE_CAPTURE_FORMAT, SOURCE_CAPTURE_PENDING_NAME, type CaptureSidecar } from "./protocol";

interface FakeFile {
  bytes: Uint8Array;
  writes: number;
}

// FakeOPFS is a synchronous stand-in for the origin private file system: a
// single capture directory of files, each openable through a sync access
// handle. `failFile` makes exactly one file name refuse to open, which is how
// a full or revoked OPFS surfaces in practice.
class FakeOPFS {
  readonly files = new Map<string, FakeFile>();
  failFile: string | null = null;

  getDirectory = async (): Promise<unknown> => ({
    getDirectoryHandle: async () => this.directory(),
  });

  private directory(): unknown {
    return {
      getFileHandle: async (name: string) => {
        if (name === this.failFile) {
          throw new Error(`cannot open ${name}`);
        }
        const file = this.files.get(name) ?? { bytes: new Uint8Array(), writes: 0 };
        this.files.set(name, file);
        return {
          createSyncAccessHandle: async () => ({
            truncate: (length: number) => {
              file.bytes = file.bytes.slice(0, length);
            },
            write: (data: Uint8Array, options: { at: number }) => {
              const end = options.at + data.byteLength;
              const grown = new Uint8Array(Math.max(file.bytes.byteLength, end));
              grown.set(file.bytes);
              grown.set(data, options.at);
              file.bytes = grown;
              file.writes += 1;
            },
            flush: () => {},
            close: () => {},
          }),
        };
      },
      removeEntry: async (name: string) => {
        this.files.delete(name);
      },
    };
  }

  text(name: string): string {
    return new TextDecoder().decode(this.files.get(name)?.bytes ?? new Uint8Array());
  }
}

const DIR = "capture-room1-1000";

const base = {
  format: SOURCE_CAPTURE_FORMAT,
  roomToken: "room1",
  participantId: "alice",
  callStartWallMs: 1_000,
  userAgent: "test",
};

function segmentMeta(index: number, startWallMs: number) {
  return {
    index,
    audioName: `segment-${index}.webm`,
    mimeType: "audio/webm",
    startWallMs,
    sampleRate: 48_000,
    channelCount: 1,
  };
}

let opfs: FakeOPFS;
let posted: Array<Record<string, unknown>>;
let worker: typeof import("./worker");

async function send(message: Record<string, unknown>): Promise<void> {
  await worker.onMessage({ data: message } as MessageEvent);
}

async function startCapture(): Promise<void> {
  await send({ type: "capture-start", dirName: DIR, base });
  await send({ type: "segment-start", dirName: DIR, meta: segmentMeta(0, 1_000) });
}

function pendingSidecar(): CaptureSidecar {
  return JSON.parse(opfs.text(SOURCE_CAPTURE_PENDING_NAME)) as CaptureSidecar;
}

beforeEach(async () => {
  opfs = new FakeOPFS();
  posted = [];
  vi.stubGlobal("self", { postMessage: (message: Record<string, unknown>) => posted.push(message) });
  vi.stubGlobal("navigator", { storage: { getDirectory: opfs.getDirectory } });
  vi.useFakeTimers();
  vi.setSystemTime(1_000);
  vi.resetModules();
  worker = await import("./worker");
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("recovery sidecar", () => {
  it("describes the chunks already written, so a reload can upload the prefix", async () => {
    await startCapture();
    vi.setSystemTime(3_000);
    await send({ type: "chunk", index: 0, buffer: new Uint8Array([1, 2, 3]).buffer });
    vi.setSystemTime(20_000);
    await send({ type: "chunk", index: 0, buffer: new Uint8Array([4, 5, 6]).buffer });

    const sidecar = pendingSidecar();
    expect(sidecar.roomToken).toBe("room1");
    expect(sidecar.segments).toHaveLength(1);
    expect(sidecar.segments[0].audioName).toBe("segment-0.webm");
    expect(opfs.files.get("segment-0.webm")?.bytes.byteLength).toBe(6);
  });

  it("keeps a segment whose audio was written when the sidecar cannot be", async () => {
    await startCapture();
    opfs.failFile = SOURCE_CAPTURE_PENDING_NAME;
    vi.setSystemTime(20_000);
    await send({ type: "chunk", index: 0, buffer: new Uint8Array([1, 2, 3]).buffer });

    // The failure is reported, but never as the fatal "error" that tears the
    // worker down, and never by dropping the segment from the sealed sidecar.
    expect(posted.map((message) => message.type)).toEqual(["segment-started", "pending-sidecar-failed"]);

    opfs.failFile = null;
    await send({ type: "segment-stop", index: 0, stopWallMs: 21_000, muteIntervals: [] });
    await send({ type: "finalize", dirName: DIR, base: { ...base, callEndWallMs: 21_000 } });

    const finalized = posted.find((message) => message.type === "finalized");
    expect((finalized?.sidecar as CaptureSidecar).segments).toHaveLength(1);
  });

  it("throttles rewrites, and forces one when a segment is sealed", async () => {
    await startCapture();
    vi.setSystemTime(20_000);
    await send({ type: "chunk", index: 0, buffer: new Uint8Array([1]).buffer });
    const afterFirst = opfs.files.get(SOURCE_CAPTURE_PENDING_NAME)?.writes ?? 0;
    expect(afterFirst).toBe(1);

    // Chunks arrive every two seconds; the sidecar is a full re-serialization
    // of the whole capture, so it must not be rewritten for each one.
    for (let elapsed = 22_000; elapsed <= 24_000; elapsed += 2_000) {
      vi.setSystemTime(elapsed);
      await send({ type: "chunk", index: 0, buffer: new Uint8Array([1]).buffer });
    }
    expect(opfs.files.get(SOURCE_CAPTURE_PENDING_NAME)?.writes).toBe(afterFirst);

    // Sealing a segment is the point where the sidecar would otherwise be
    // stale about durable audio, so it ignores the throttle.
    vi.setSystemTime(24_500);
    await send({ type: "segment-stop", index: 0, stopWallMs: 24_500, muteIntervals: [] });
    expect(opfs.files.get(SOURCE_CAPTURE_PENDING_NAME)?.writes).toBe(afterFirst + 1);
    expect(pendingSidecar().segments[0].stopWallMs).toBe(24_500);
  });

  it("is removed once the capture is sealed, so it cannot be uploaded twice", async () => {
    await startCapture();
    vi.setSystemTime(20_000);
    await send({ type: "chunk", index: 0, buffer: new Uint8Array([1, 2]).buffer });
    expect(opfs.files.has(SOURCE_CAPTURE_PENDING_NAME)).toBe(true);

    await send({ type: "segment-stop", index: 0, stopWallMs: 21_000, muteIntervals: [] });
    await send({ type: "finalize", dirName: DIR, base: { ...base, callEndWallMs: 21_000 } });

    expect(opfs.files.has(SOURCE_CAPTURE_PENDING_NAME)).toBe(false);
    expect(opfs.files.has("capture.json")).toBe(true);
  });
});
