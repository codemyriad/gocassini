/// <reference lib="webworker" />
// Durable capture storage. Timing anchors arrive from timing-worker.ts;
// synchronous OPFS operations never run on the outgoing audio worker.

import type { CaptureAnchor, CaptureSegment, CaptureSidecar } from "./protocol";
import { SOURCE_CAPTURE_PENDING_NAMES, mergeMuteIntervals } from "./protocol";

declare const self: DedicatedWorkerGlobalScope;

// Timing arrives from a separate worker. No outgoing audio frame waits on
// this worker's synchronous disk operations.
let anchors: CaptureAnchor[] = [];
let timingActive = false;

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
  // preexisting marks a segment refused because its file ALREADY held audio.
  // It is failed in the sense that this interval wrote none of it, and it must
  // never be deleted with the others: those files are this worker's own
  // truncations, while this one is somebody else's complete recording, and
  // removing it would make the collision backstop destroy exactly what it
  // refused to overwrite.
  preexisting: boolean;
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
  // Never over audio that is already there.
  //
  // A resumed capture numbers itself past everything the directory holds, and
  // the page claims the directory so that a second page cannot be numbering
  // into it at the same time. Both of those can be wrong at once — a browser
  // without Web Locks has no claim to take, and a bug in either calculation
  // would land here — and what "wrong" costs at this line is a file full of the
  // participant's audio replaced by an empty one. So the truncate is
  // conditional, and a name that is already occupied fails the segment instead:
  // one segment fewer, rather than one destroyed.
  if (handle.getSize() > 0) {
    handle.close();
    self.postMessage({
      type: "error",
      detail: `segment ${meta.index}: ${meta.audioName} already holds audio; refusing to overwrite it`,
    });
    segments.set(meta.index, {
      handle,
      offset: 0,
      meta,
      stopWallMs: 0,
      muteIntervals: [],
      failed: true,
      preexisting: true,
    });
    return;
  }
  handle.truncate(0);
  segments.set(meta.index, {
    handle,
    offset: 0,
    meta,
    stopWallMs: 0,
    muteIntervals: [],
    failed: false,
    preexisting: false,
  });
}

function recoverableSegments(now: number): CaptureSegment[] {
  const live = [...segments.values()]
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
  return live.sort((a, b) => a.index - b.index);
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
// pendingSlot alternates between the two recovery-sidecar names, so a
// checkpoint always writes into the slot the previous one is not in. See
// SOURCE_CAPTURE_PENDING_NAMES: a page that dies mid-checkpoint can then only
// damage the generation being written, and the previous one is still whole.
let pendingSlot = 0;

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
  const slot = pendingSlot;
  const fileHandle = await dir.getFileHandle(SOURCE_CAPTURE_PENDING_NAMES[slot], { create: true });
  const handle = await fileHandle.createSyncAccessHandle();
  const sidecar: CaptureSidecar = {
    ...pendingBase,
    callEndWallMs: Math.max(...built.map((segment) => segment.stopWallMs)),
    segments: built,
  };
  const body = new TextEncoder().encode(JSON.stringify(sidecar));
  try {
    handle.truncate(0);
    const written = handle.write(body, { at: 0 });
    if (written !== body.byteLength) {
      // A short write leaves this slot torn. Say so rather than advance, so the
      // other slot stays the newest whole generation and this one is rewritten
      // next time.
      throw new Error(`recovery sidecar wrote ${written} of ${body.byteLength} bytes`);
    }
    handle.flush();
  } finally {
    // Always. A sync access handle holds an exclusive lock on its file, and this
    // worker outlives a failed checkpoint to keep the call's audio flowing, so
    // one leaked here would lock a file every later checkpoint needs.
    try {
      handle.close();
    } catch {
      // Already closed, or closing failed too. The write error above is the one
      // worth reporting.
    }
  }
  // Only a checkpoint that completed advances the slot.
  pendingSlot = (slot + 1) % SOURCE_CAPTURE_PENDING_NAMES.length;
}

async function appendChunk(index: number, buffer: ArrayBuffer): Promise<void> {
  const segment = segments.get(index);
  if (!segment) {
    return;
  }
  // A segment with no bytes yet is not in the recovery sidecar — there is
  // nothing to recover — so the write that gives it its first bytes is the one
  // that makes it describable, and it is forced past the throttle.
  //
  // Without that, a segment opened by a mid-call microphone change stayed
  // unnamed for up to the throttle interval while its file grew on disk. A page
  // that reloaded in that window handed the next page a manifest describing one
  // fewer segment than the directory held, and the resumed capture numbered its
  // own first segment over the one already there. One extra sidecar write per
  // segment, not per chunk.
  const firstBytes = segment.offset === 0;
  try {
    const written = segment.handle.write(new Uint8Array(buffer), { at: segment.offset });
    if (written !== buffer.byteLength) {
      // The audio the sidecars assert is durable. A short write under quota or
      // storage pressure does not throw, and advancing the offset by what was
      // ASKED for rather than what landed leaves a hole in the file while every
      // manifest goes on describing it as whole — a capture that looks intact
      // and is not, which is the one shape the recorder cannot detect either.
      throw new Error(`wrote ${written} of ${buffer.byteLength} bytes`);
    }
    segment.offset += written;
  } catch (error) {
    segment.failed = true;
    self.postMessage({ type: "error", detail: `segment ${index}: ${String(error)}` });
    return;
  }
  await refreshPendingSidecar(firstBytes);
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
  if (!segment.preexisting) {
    // A segment refused because its file already held audio never opened a
    // handle this interval owns: it was closed at the refusal, and flushing it
    // only raises an error about a state that is correct.
    try {
      segment.handle.flush();
    } catch (error) {
      segment.failed = true;
      self.postMessage({ type: "error", detail: `segment ${index}: ${String(error)}` });
    }
  }
  try {
    // Closed even when the flush threw. A sync access handle holds an
    // exclusive lock on its file, and this worker now survives a failed
    // finalize to keep the call's audio flowing — so a handle leaked here is
    // not collected with the worker any more. It outlives the recording and
    // locks a file the next interval may need to write.
    segment.handle.close();
  } catch {
    // Already closed, or closing failed too. Either way there is nothing
    // further to do with it, and the flush error above is the one worth
    // reporting.
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
      //
      // The file goes with it — except when it was already there. Those bytes
      // are another interval's complete recording, which this one declined to
      // overwrite; deleting it here would make the refusal worse than the
      // overwrite it prevented.
      if (!segment.preexisting) {
        await dir.removeEntry(segment.meta.audioName).catch(() => {});
      }
      continue;
    }
    built.push({
      ...segment.meta,
      stopWallMs: segment.stopWallMs,
      anchors: anchorsWithin(anchors, segment.meta.startWallMs, segment.stopWallMs),
      muteIntervals: mergeMuteIntervals(segment.muteIntervals),
    });
  }
  built.sort((a, b) => a.index - b.index);
  const sidecar: CaptureSidecar = { ...base, segments: built };
  if (built.length === 0) {
    throw new Error("no segment was written completely");
  }
  const fileHandle = await dir.getFileHandle("capture.json", { create: true });
  const handle = await fileHandle.createSyncAccessHandle();
  const body = new TextEncoder().encode(JSON.stringify(sidecar));
  try {
    handle.truncate(0);
    const written = handle.write(body, { at: 0 });
    if (written !== body.byteLength) {
      // Checked here for the same reason the checkpoint checks it, and with
      // more at stake: the recovery slots are removed on the strength of this
      // manifest, so a short write that went unnoticed would leave the
      // directory with a truncated capture.json and no generation to fall back
      // on — segment files intact and nothing able to describe them.
      throw new Error(`capture sidecar wrote ${written} of ${body.byteLength} bytes`);
    }
    handle.flush();
  } finally {
    try {
      handle.close();
    } catch {
      // Nothing further to do with it.
    }
  }
  // Both recovery slots, and only now that capture.json is verifiably the
  // manifest.
  for (const name of SOURCE_CAPTURE_PENDING_NAMES) {
    await dir.removeEntry(name).catch(() => {});
  }
  return sidecar;
}

// resetRecordingInterval releases every handle and clears the evidence that
// belongs to one recording interval. The call can continue after the official
// recording stops, so this same pass-through worker may serve a later interval
// and must not mix their files or clocks.
function resetRecordingInterval(): void {
  // Release anything closeSegment never reached — an interval abandoned
  // mid-recording leaves its handles open, and this worker outlives the
  // interval now.
  for (const segment of segments.values()) {
    try {
      segment.handle.close();
    } catch {
      // Already closed. The point is only that none stays locked.
    }
  }
  segments.clear();
  captureDir = null;
  anchors = [];
  pendingDirName = null;
  pendingBase = null;
  lastPendingWriteMs = 0;
  pendingSlot = 0;
}

export async function onMessage(event: MessageEvent): Promise<void> {
  const message = event.data;
  try {
    switch (message?.type) {
      case "timing-active":
        timingActive = message.active === true;
        if (timingActive) {
          anchors = [];
        }
        break;
      case "anchor":
        if (timingActive) anchors.push(message.anchor as CaptureAnchor);
        break;
      case "capture-start": {
        pendingDirName = message.dirName;
        pendingBase = message.base;
        lastPendingWriteMs = 0;
        break;
      }
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
        // The timing worker forwards completion or failure to the page while
        // continuing to forward Talk's outgoing audio.
        try {
          const sidecar = await finalize(message.dirName, message.base);
          self.postMessage({ type: "finalized", dirName: message.dirName, sidecar });
        } finally {
          // Reset whether or not the seal worked. A failed finalize ends the
          // interval just as much as a successful one, and anything left
          // behind would be written into the NEXT recording: ensureDir caches
          // the directory handle and segment indices restart at zero.
          resetRecordingInterval();
        }
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
  let queue: Promise<void> = Promise.resolve();
  self.onmessage = (event: MessageEvent) => {
    queue = queue.then(() => onMessage(event));
  };
  // Report for duty, and only once everything above is wired. The page cannot
  // wait for this before attaching its encoded transform — a transform
  // attached after the call negotiates collects nothing — so it attaches first
  // and uses this message to learn that the worker it already committed to is
  // real. Silence here means the participant's outgoing Opus frames are being
  // routed into a worker that will never read them, which the page has a short
  // deadline to notice and undo. Posting this before the handlers were wired
  // would answer for a worker that still drops the frames it is handed.
  self.postMessage({ type: "ready" });
}
