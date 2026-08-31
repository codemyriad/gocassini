/// <reference lib="webworker" />
// The capture worker: RTP timing anchors and durable storage.
//
// It has two jobs, both of which have to happen off the page's main thread.
//
// 1. Timing. A WebRTC encoded transform can only run in a worker, by spec. We
//    attach one to the outgoing audio sender, so every encoded Opus frame
//    passes through here on its way to the network. We read its RTP timestamp
//    and hand the frame straight back, unmodified.
//
//    Those timestamps are the whole reason this design survives a bad uplink.
//    The RTP timestamp is the sender's own 48 kHz sample clock, and it is the
//    same number the recorder logs for every packet that reached it — which
//    pkg/core/timeline already maps onto the meeting timeline. Packet loss
//    destroys the audio the server received; it does not corrupt the timestamps
//    on the packets that did arrive, and a handful of those anywhere in the
//    call is enough to place the whole segment. Alignment therefore does not
//    depend on the damaged SFU audio being good enough to correlate against.
//
// 2. Storage. OPFS's createSyncAccessHandle is worker-only, and it is the right
//    place for this: quota is a share of free disk rather than localStorage's
//    few megabytes (an hour of Opus is ~14 MB before base64 even enters it),
//    and the file survives a tab close, a crash, and a reboot — which is what
//    makes "upload after the call" safe rather than a way to lose meetings.

import type { CaptureAnchor, CaptureSegment, CaptureSidecar } from "./protocol";
import { mergeMuteIntervals } from "./protocol";

declare const self: DedicatedWorkerGlobalScope & {
  onrtctransform: ((event: RTCTransformEvent) => void) | null;
};

// ANCHOR_EVERY_FRAMES samples one anchor per second of speech (Opus frames are
// 20 ms). An hour of talking is ~3600 anchors, a few hundred kB of JSON, and
// far more than placement needs — the redundancy is what makes the sidecar
// robust to the segments where the participant said nothing.
const ANCHOR_EVERY_FRAMES = 50;

interface RTCTransformEvent {
  transformer: {
    readable: ReadableStream<RTCEncodedAudioFrame>;
    writable: WritableStream<RTCEncodedAudioFrame>;
    options?: { sessionId?: string };
  };
}

interface RTCEncodedAudioFrame {
  timestamp?: number;
  getMetadata(): { synchronizationSource?: number; rtpTimestamp?: number };
}

// anchors accumulates for the whole call, not per segment.
//
// The transform is attached to the SENDER, which outlives the individual tracks
// a segment is cut on (replaceTrack during a device change restarts the
// recorder but not the encoder pipeline). Keeping one stream of wall-clocked
// anchors and slicing it by segment at finalize time is both simpler and more
// accurate than trying to notify the transform about page-side segment
// boundaries it cannot observe.
let anchors: CaptureAnchor[] = [];
let frameIndex = 0;
let lastSSRC = -1;

function recordAnchor(frame: RTCEncodedAudioFrame): void {
  const metadata = frame.getMetadata();
  const ssrc = metadata.synchronizationSource ?? -1;
  // Always anchor on an SSRC change: that is a re-negotiation, and it is also
  // where the recorder rotates to a new stream segment in its own artifact, so
  // it is exactly the seam both sides need a fresh reference point for.
  if (frameIndex % ANCHOR_EVERY_FRAMES === 0 || ssrc !== lastSSRC) {
    anchors.push({
      frameIndex,
      rtpTimestamp: metadata.rtpTimestamp ?? frame.timestamp ?? 0,
      ssrc,
      wallMs: Date.now(),
    });
    lastSSRC = ssrc;
  }
  frameIndex += 1;
}

// installTransformHandler wires the encoded-transform entry point. Split out
// from the assignment so the module can be imported by a unit test in Node,
// where there is no worker global to assign onto.
export function onTransform(event: RTCTransformEvent): void {
  const { readable, writable } = event.transformer;
  readable
    .pipeThrough(
      new TransformStream<RTCEncodedAudioFrame, RTCEncodedAudioFrame>({
        transform(frame, controller) {
          // Observe, never modify. This sits in the live send path of a call in
          // progress: anything thrown here degrades the participant's audio for
          // everyone, so the measurement is wrapped and the frame is forwarded
          // whatever happens.
          try {
            recordAnchor(frame);
          } catch {
            // A frame we failed to measure is a missing anchor, nothing more.
          }
          controller.enqueue(frame);
        },
      }),
    )
    .pipeTo(writable)
    .catch(() => {
      // The pipe rejects when the sender goes away at the end of the call.
    });
}

// --- OPFS storage -----------------------------------------------------------

interface OpenSegment {
  handle: FileSystemSyncAccessHandle;
  offset: number;
  meta: Omit<CaptureSegment, "anchors" | "muteIntervals" | "stopWallMs">;
  stopWallMs: number;
  muteIntervals: Array<[number, number]>;
}

let captureDir: FileSystemDirectoryHandle | null = null;
const segments = new Map<number, OpenSegment>();

async function ensureDir(name: string): Promise<FileSystemDirectoryHandle> {
  if (captureDir) {
    return captureDir;
  }
  const root = await navigator.storage.getDirectory();
  captureDir = await root.getDirectoryHandle(name, { create: true });
  return captureDir;
}

async function openSegment(dirName: string, meta: OpenSegment["meta"]): Promise<void> {
  const dir = await ensureDir(dirName);
  const fileHandle = await dir.getFileHandle(meta.audioName, { create: true });
  const handle = await fileHandle.createSyncAccessHandle();
  handle.truncate(0);
  segments.set(meta.index, { handle, offset: 0, meta, stopWallMs: 0, muteIntervals: [] });
}

function appendChunk(index: number, buffer: ArrayBuffer): void {
  const segment = segments.get(index);
  if (!segment) {
    return;
  }
  segment.handle.write(new Uint8Array(buffer), { at: segment.offset });
  segment.offset += buffer.byteLength;
}

function closeSegment(index: number, stopWallMs: number, muteIntervals: Array<[number, number]>): void {
  const segment = segments.get(index);
  if (!segment) {
    return;
  }
  segment.handle.flush();
  segment.handle.close();
  segment.stopWallMs = stopWallMs;
  segment.muteIntervals = muteIntervals;
}

// anchorsWithin slices the call-wide anchor stream to one segment's wall-clock
// span. A segment with no anchors is not an error: the participant may simply
// not have spoken, or the browser may not support encoded transforms, and the
// server falls back to correlating against whatever intact SFU audio exists.
export function anchorsWithin(
  all: readonly CaptureAnchor[],
  startWallMs: number,
  stopWallMs: number,
): CaptureAnchor[] {
  return all.filter((anchor) => anchor.wallMs >= startWallMs && anchor.wallMs <= stopWallMs);
}

async function finalize(dirName: string, base: Omit<CaptureSidecar, "segments">): Promise<CaptureSidecar> {
  const dir = await ensureDir(dirName);
  const built: CaptureSegment[] = [];
  for (const segment of [...segments.values()].sort((a, b) => a.meta.index - b.meta.index)) {
    built.push({
      ...segment.meta,
      stopWallMs: segment.stopWallMs,
      anchors: anchorsWithin(anchors, segment.meta.startWallMs, segment.stopWallMs),
      muteIntervals: mergeMuteIntervals(segment.muteIntervals),
    });
  }
  const sidecar: CaptureSidecar = { ...base, segments: built };
  const fileHandle = await dir.getFileHandle("capture.json", { create: true });
  const handle = await fileHandle.createSyncAccessHandle();
  handle.truncate(0);
  handle.write(new TextEncoder().encode(JSON.stringify(sidecar)), { at: 0 });
  handle.flush();
  handle.close();
  return sidecar;
}

export async function onMessage(event: MessageEvent): Promise<void> {
  const message = event.data;
  try {
    switch (message?.type) {
      case "segment-start":
        await openSegment(message.dirName, message.meta);
        self.postMessage({ type: "segment-started", index: message.meta.index });
        break;
      case "chunk":
        appendChunk(message.index, message.buffer);
        break;
      case "segment-stop":
        closeSegment(message.index, message.stopWallMs, message.muteIntervals ?? []);
        self.postMessage({ type: "segment-stopped", index: message.index });
        break;
      case "finalize": {
        const sidecar = await finalize(message.dirName, message.base);
        self.postMessage({ type: "finalized", dirName: message.dirName, sidecar });
        break;
      }
      default:
        break;
    }
  } catch (error) {
    self.postMessage({ type: "error", detail: String(error) });
  }
}

// Wire the worker globals only in a real worker. Importing this module in a
// unit test must not throw on a missing `self`.
if (typeof self !== "undefined" && typeof self.postMessage === "function") {
  self.onrtctransform = onTransform;
  self.onmessage = (event: MessageEvent) => {
    void onMessage(event);
  };
}
