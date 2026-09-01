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
//    These timestamps are the participant's own 48 kHz sample clock. They are
//    NOT what the recorder logs for the same audio — Janus rewrites the
//    timestamps it relays to each subscriber — so they cannot be matched to it
//    directly. What they give, paired with the wall-clock time recorded
//    alongside, is the RATE: how fast this machine's sound card runs against
//    its wall clock. That is the dominant drift in the system, and it is
//    immune to loss, because these describe frames the client ENCODED rather
//    than packets that arrived. See docs/source-audio-capture.md.
//
// 2. Storage. OPFS's createSyncAccessHandle is worker-only, and it is the right
//    place for this: quota is a share of free disk rather than localStorage's
//    few megabytes (an hour of Opus is ~14 MB before base64 even enters it),
//    and the file survives a tab close, a crash, and a reboot — which is what
//    makes "upload after the call" safe rather than a way to lose meetings.

import type { CaptureAnchor, CaptureSegment, CaptureSidecar } from "./protocol";
import { SOURCE_CAPTURE_PENDING_NAME, mergeMuteIntervals } from "./protocol";

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
// The transform is installed before Talk negotiates so it cannot be attached
// late, but it remains a pure pass-through until Talk confirms that its own
// recording is active. Thus no timing evidence, audio file, or OPFS directory
// is collected merely because somebody joined a call.
let timingActive = false;

function recordAnchor(frame: RTCEncodedAudioFrame): void {
  if (!timingActive) {
    return;
  }
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
  // failed marks a segment whose file could not be written completely — a full
  // disk, a revoked handle. Such a segment is dropped at finalize rather than
  // described in the sidecar: a manifest that promises audio the file does not
  // contain is worse than one segment fewer, because the server would place
  // and transcribe the truncation as if it were the meeting.
  failed: boolean;
}

let captureDir: FileSystemDirectoryHandle | null = null;
const segments = new Map<number, OpenSegment>();
let pendingDirName: string | null = null;
let pendingBase: Omit<CaptureSidecar, "segments" | "callEndWallMs"> | null = null;

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
  segments.set(meta.index, { handle, offset: 0, meta, stopWallMs: 0, muteIntervals: [], failed: false });
}

function recoverableSegments(now: number): CaptureSegment[] {
  return [...segments.values()]
    .filter((segment) => !segment.failed && segment.offset > 0)
    .sort((a, b) => a.meta.index - b.meta.index)
    .map((segment) => {
      const stopWallMs = segment.stopWallMs > 0 ? segment.stopWallMs : now;
      return {
        ...segment.meta,
        stopWallMs,
        anchors: anchorsWithin(anchors, segment.meta.startWallMs, stopWallMs),
        muteIntervals: mergeMuteIntervals(segment.muteIntervals),
      };
    });
}

// PENDING_SIDECAR_MIN_INTERVAL_MS bounds how often the recovery sidecar is
// rewritten. It is a full re-serialization of every segment through a fresh
// sync access handle, and chunks now arrive every two seconds, so writing one
// per chunk makes OPFS traffic grow with the length of the call for no gain:
// what a crash can lose is bounded by the chunk cadence either way, and the
// segment files themselves are already on disk. A segment closing forces a
// write regardless, so the sidecar is never stale about a sealed segment.
const PENDING_SIDECAR_MIN_INTERVAL_MS = 5_000;
let lastPendingWriteMs = 0;

// refreshPendingSidecar isolates the recovery sidecar from the recording. The
// sidecar exists to salvage a capture whose page died; failing to write it
// must never cost a segment whose own audio was written correctly, and must
// never surface as the fatal "error" that tears the worker down.
async function refreshPendingSidecar(force: boolean): Promise<void> {
  const now = Date.now();
  if (!force && now - lastPendingWriteMs < PENDING_SIDECAR_MIN_INTERVAL_MS) {
    return;
  }
  lastPendingWriteMs = now;
  try {
    await writePendingSidecar();
  } catch (error) {
    self.postMessage({ type: "pending-sidecar-failed", detail: String(error) });
  }
}

async function writePendingSidecar(): Promise<void> {
  if (!pendingBase || !pendingDirName) {
    return;
  }
  const now = Date.now();
  const built = recoverableSegments(now);
  if (built.length === 0) {
    return;
  }
  const dir = await ensureDir(pendingDirName);
  const fileHandle = await dir.getFileHandle(SOURCE_CAPTURE_PENDING_NAME, { create: true });
  const handle = await fileHandle.createSyncAccessHandle();
  const sidecar: CaptureSidecar = {
    ...pendingBase,
    callEndWallMs: Math.max(...built.map((segment) => segment.stopWallMs)),
    segments: built,
  };
  handle.truncate(0);
  handle.write(new TextEncoder().encode(JSON.stringify(sidecar)), { at: 0 });
  handle.flush();
  handle.close();
}

async function appendChunk(index: number, buffer: ArrayBuffer): Promise<void> {
  const segment = segments.get(index);
  if (!segment) {
    return;
  }
  try {
    segment.handle.write(new Uint8Array(buffer), { at: segment.offset });
    segment.offset += buffer.byteLength;
  } catch (error) {
    segment.failed = true;
    self.postMessage({ type: "error", detail: `segment ${index}: ${String(error)}` });
    return;
  }
  await refreshPendingSidecar(false);
}

async function closeSegment(
  index: number,
  stopWallMs: number,
  muteIntervals: Array<[number, number]>,
): Promise<void> {
  const segment = segments.get(index);
  if (!segment) {
    return;
  }
  try {
    segment.handle.flush();
    segment.handle.close();
  } catch (error) {
    segment.failed = true;
    self.postMessage({ type: "error", detail: `segment ${index}: ${String(error)}` });
  }
  segment.stopWallMs = stopWallMs;
  segment.muteIntervals = muteIntervals;
  await refreshPendingSidecar(true);
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
  if (segments.size === 0) {
    // Nothing was ever opened — a capture denied or revoked before it started.
    // Creating the directory just to throw would leave an empty one behind on
    // the participant's disk, which is exactly what a denied capture must not
    // do.
    throw new Error("nothing was recorded");
  }
  const dir = await ensureDir(dirName);
  const built: CaptureSegment[] = [];
  for (const segment of [...segments.values()].sort((a, b) => a.meta.index - b.meta.index)) {
    if (segment.failed || segment.offset === 0) {
      // Never describe a segment whose bytes are not all there, and never
      // describe an empty one: the upload would be refused for a missing file,
      // taking the good segments with it.
      await dir.removeEntry(segment.meta.audioName).catch(() => {});
      continue;
    }
    built.push({
      ...segment.meta,
      stopWallMs: segment.stopWallMs,
      anchors: anchorsWithin(anchors, segment.meta.startWallMs, segment.stopWallMs),
      muteIntervals: mergeMuteIntervals(segment.muteIntervals),
    });
  }
  const sidecar: CaptureSidecar = { ...base, segments: built };
  if (built.length === 0) {
    throw new Error("no segment was written completely");
  }
  const fileHandle = await dir.getFileHandle("capture.json", { create: true });
  const handle = await fileHandle.createSyncAccessHandle();
  handle.truncate(0);
  handle.write(new TextEncoder().encode(JSON.stringify(sidecar)), { at: 0 });
  handle.flush();
  handle.close();
  await dir.removeEntry(SOURCE_CAPTURE_PENDING_NAME).catch(() => {});
  // The call can continue after the official recording stops. Release every
  // handle and reset interval-local evidence so this same pass-through worker
  // can serve a later recording interval without mixing their files or clocks.
  segments.clear();
  captureDir = null;
  anchors = [];
  frameIndex = 0;
  lastSSRC = -1;
  pendingDirName = null;
  pendingBase = null;
  lastPendingWriteMs = 0;
  return sidecar;
}

export async function onMessage(event: MessageEvent): Promise<void> {
  const message = event.data;
  try {
    switch (message?.type) {
      case "timing-active":
        timingActive = message.active === true;
        if (timingActive) {
          anchors = [];
          frameIndex = 0;
          lastSSRC = -1;
        }
        break;
      case "capture-start":
        pendingDirName = message.dirName;
        pendingBase = message.base;
        lastPendingWriteMs = 0;
        break;
      case "segment-start":
        await openSegment(message.dirName, message.meta);
        self.postMessage({ type: "segment-started", index: message.meta.index });
        break;
      case "chunk":
        await appendChunk(message.index, message.buffer);
        break;
      case "segment-stop":
        await closeSegment(message.index, message.stopWallMs, message.muteIntervals ?? []);
        self.postMessage({ type: "segment-stopped", index: message.index });
        break;
      case "finalize": {
        // A failure here reaches the page as "error", which is what releases
        // the worker: there will be no "finalized" to do it.
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
//
// Messages are serialized through one promise chain rather than dispatched
// concurrently. Opening a segment is async (getFileHandle, then
// createSyncAccessHandle) while appending a chunk is not, so a chunk that
// arrived while the open was still in flight would find no open segment and be
// dropped without a trace. Chaining costs nothing here — the writes are
// sequential anyway — and makes the message order the page sent the order the
// worker applies.
if (typeof self !== "undefined" && typeof self.postMessage === "function") {
  self.onrtctransform = onTransform;
  let queue: Promise<void> = Promise.resolve();
  self.onmessage = (event: MessageEvent) => {
    queue = queue.then(() => onMessage(event));
  };
}
