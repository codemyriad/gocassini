/// <reference lib="webworker" />
// This worker stays on Talk's send path and never opens a file. The storage
// worker may stall or fail without holding an encoded frame hostage.
import type { CaptureAnchor } from "./protocol";

interface EncodedFrame {
  timestamp?: number;
  getMetadata(): { synchronizationSource?: number; rtpTimestamp?: number };
}
declare const self: DedicatedWorkerGlobalScope & {
  onrtctransform: ((event: { transformer: {
    readable: ReadableStream<EncodedFrame>;
    writable: WritableStream<EncodedFrame>;
  } }) => void) | null;
};

export function installTimingWorker(storage: MessagePort): void {
  let active = false;
  let frameIndex = 0;
  let lastSSRC = -1;
  storage.onmessage = (event: MessageEvent) => self.postMessage(event.data);
  self.onmessage = (event: MessageEvent) => {
    if (event.data?.type === "timing-active") {
      active = event.data.active === true;
      if (active) { frameIndex = 0; lastSSRC = -1; }
    }
    const buffer = event.data?.buffer;
    storage.postMessage(event.data, buffer instanceof ArrayBuffer ? [buffer] : []);
  };
  self.onrtctransform = (event) => {
    const { readable, writable } = event.transformer;
    void readable.pipeThrough(new TransformStream<EncodedFrame, EncodedFrame>({
      transform(frame, controller) {
        try {
          if (active) {
            const meta = frame.getMetadata();
            const ssrc = meta.synchronizationSource ?? -1;
            if (frameIndex % 50 === 0 || ssrc !== lastSSRC) {
              const anchor: CaptureAnchor = { frameIndex, ssrc,
                rtpTimestamp: meta.rtpTimestamp ?? frame.timestamp ?? 0, wallMs: Date.now() };
              storage.postMessage({ type: "anchor", anchor });
              lastSSRC = ssrc;
            }
            frameIndex++;
          }
        } catch { /* Measurement failure must not interrupt Talk. */ }
        controller.enqueue(frame);
      },
    })).pipeTo(writable).catch(() => {});
  };
}

if (typeof self !== "undefined" && typeof self.postMessage === "function") {
  self.onmessage = (event: MessageEvent) => {
    if (event.data?.type === "storage-port") installTimingWorker(event.ports[0]);
  };
}
