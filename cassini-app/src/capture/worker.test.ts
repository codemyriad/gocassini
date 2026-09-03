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

import { SOURCE_CAPTURE_FORMAT, SOURCE_CAPTURE_PENDING_NAMES, type CaptureSidecar } from "./protocol";

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
  failFileB: string | null = null;

  getDirectory = async (): Promise<unknown> => ({
    getDirectoryHandle: async () => this.directory(),
  });

  private directory(): unknown {
    return {
      getFileHandle: async (name: string) => {
        if (name === this.failFile || name === this.failFileB) {
          throw new Error(`cannot open ${name}`);
        }
        const file = this.files.get(name) ?? { bytes: new Uint8Array(), writes: 0 };
        this.files.set(name, file);
        return {
          createSyncAccessHandle: async () => ({
            // The worker refuses to open a segment name that already holds
            // audio, so the fake has to be able to say that it does.
            getSize: () => file.bytes.byteLength,
            truncate: (length: number) => {
              file.bytes = file.bytes.slice(0, length);
            },
            // The real write returns how many bytes it wrote, and the worker
            // checks it: a short write leaves the slot torn and must not be
            // reported as a completed checkpoint.
            write: (data: Uint8Array, options: { at: number }) => {
              const end = options.at + data.byteLength;
              const grown = new Uint8Array(Math.max(file.bytes.byteLength, end));
              grown.set(file.bytes);
              grown.set(data, options.at);
              file.bytes = grown;
              file.writes += 1;
              return data.byteLength;
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

// The newest recovery generation, chosen the way the payload's reader chooses
// it: checkpoints alternate between two slots so a torn write can only damage
// the one being written, and whichever parses with the later call end is the
// one a reloading page would use.
function pendingSidecar(): CaptureSidecar {
  let best: CaptureSidecar | null = null;
  for (const name of SOURCE_CAPTURE_PENDING_NAMES) {
    try {
      const parsed = JSON.parse(opfs.text(name)) as CaptureSidecar;
      if (best === null || parsed.callEndWallMs > best.callEndWallMs) {
        best = parsed;
      }
    } catch {
      // Absent or torn.
    }
  }
  if (best === null) {
    throw new Error("no recovery sidecar was written");
  }
  return best;
}

// pendingWrites counts checkpoints across both slots.
function pendingWrites(): number {
  return SOURCE_CAPTURE_PENDING_NAMES.reduce(
    (total, name) => total + (opfs.files.get(name)?.writes ?? 0),
    0,
  );
}

function hasPendingSidecar(): boolean {
  return SOURCE_CAPTURE_PENDING_NAMES.some((name) => opfs.files.has(name));
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
    // Both slots, so the checkpoint has nowhere to succeed.
    opfs.failFile = SOURCE_CAPTURE_PENDING_NAMES[0];
    opfs.failFileB = SOURCE_CAPTURE_PENDING_NAMES[1];
    vi.setSystemTime(20_000);
    await send({ type: "chunk", index: 0, buffer: new Uint8Array([1, 2, 3]).buffer });

    // "ready" is the worker's first act on startup: it is how the page learns
    // that the worker behind the transform it has already attached to the
    // participant's live sender is real. The sidecar failure after it is
    // reported, but never as the fatal "error" that tears the worker down, and
    // never by dropping the segment from the sealed sidecar.
    expect(posted.map((message) => message.type)).toEqual([
      "ready",
      "segment-started",
      "pending-sidecar-failed",
    ]);

    opfs.failFile = null;
    opfs.failFileB = null;
    await send({ type: "segment-stop", index: 0, stopWallMs: 21_000, muteIntervals: [] });
    await send({ type: "finalize", dirName: DIR, base: { ...base, callEndWallMs: 21_000 } });

    const finalized = posted.find((message) => message.type === "finalized");
    expect((finalized?.sidecar as CaptureSidecar).segments).toHaveLength(1);
  });

  it("throttles rewrites, and forces one when a segment is sealed", async () => {
    await startCapture();
    vi.setSystemTime(20_000);
    await send({ type: "chunk", index: 0, buffer: new Uint8Array([1]).buffer });
    const afterFirst = pendingWrites();
    expect(afterFirst).toBe(1);

    // Chunks arrive every two seconds; the sidecar is a full re-serialization
    // of the whole capture, so it must not be rewritten for each one.
    for (let elapsed = 22_000; elapsed <= 24_000; elapsed += 2_000) {
      vi.setSystemTime(elapsed);
      await send({ type: "chunk", index: 0, buffer: new Uint8Array([1]).buffer });
    }
    expect(pendingWrites()).toBe(afterFirst);

    // Sealing a segment is the point where the sidecar would otherwise be
    // stale about durable audio, so it ignores the throttle.
    vi.setSystemTime(24_500);
    await send({ type: "segment-stop", index: 0, stopWallMs: 24_500, muteIntervals: [] });
    expect(pendingWrites()).toBe(afterFirst + 1);
    expect(pendingSidecar().segments[0].stopWallMs).toBe(24_500);
  });

  it("is removed once the capture is sealed, so it cannot be uploaded twice", async () => {
    await startCapture();
    vi.setSystemTime(20_000);
    await send({ type: "chunk", index: 0, buffer: new Uint8Array([1, 2]).buffer });
    expect(hasPendingSidecar()).toBe(true);

    await send({ type: "segment-stop", index: 0, stopWallMs: 21_000, muteIntervals: [] });
    await send({ type: "finalize", dirName: DIR, base: { ...base, callEndWallMs: 21_000 } });

    expect(hasPendingSidecar()).toBe(false);
    expect(opfs.files.has("capture.json")).toBe(true);
  });
});
