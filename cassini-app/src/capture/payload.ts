// The payload the service worker appends to Nextcloud Talk's bundle.
//
// It records the participant's own microphone at the source — before Opus
// encoding and before the network — so the transcript can later be rebuilt from
// audio no uplink ever damaged. It records the track Talk is SENDING, not a
// microphone stream of its own, which matters for two independent reasons:
//
//   Mute is honoured by construction. Talk mutes with TrackEnabler
//   (src/utils/media/pipeline/TrackEnabler.js), which sets `enabled = false` on
//   the very track feeding the peer connection and re-forces that state if
//   anything changes it. A disabled MediaStreamTrack delivers silence to every
//   sink, this recorder included. There is no code path here that can produce a
//   hot mic, and none that could be added: the enforcement is Talk's, upstream
//   of us, and it actively fights attempts to flip it back.
//
//   It is the same signal the SFU encoded, one step earlier — post noise
//   suppression, pre-Opus. So the uploaded audio and the recorder's copy differ
//   only by the encode and the network, which is what makes verifying an upload
//   against the server's own recording meaningful.
//
// Everything here is defensive. This code runs inside a live call, appended to
// another application's bundle: a throw in the wrong place degrades a real
// meeting for everyone in it. Every entry point is wrapped, and every failure
// mode ends in "do not capture", never in "break Talk".

import {
  SOURCE_CAPTURE_FORMAT,
  captureDirName,
  roomTokenFromPath,
  type CaptureSidecar,
} from "./protocol";

// CONSENT_STORAGE_KEY records the participant's answer. Capture never starts
// without an explicit opt-in: this records a meeting, and a recorder that turns
// itself on silently is not one we are willing to ship regardless of how much
// the transcript would improve.
const CONSENT_STORAGE_KEY = "cassini.sourceCapture.consent";

// TIMESLICE_MS is how often MediaRecorder hands us a chunk to persist. Ten
// seconds bounds what an unclean tab kill can lose while keeping the message
// and OPFS write rate negligible.
const TIMESLICE_MS = 10_000;

// MUTE_POLL_MS samples Talk's mute state. MediaStreamTrack has no "enabled
// changed" event, so polling is the only way to observe it. The recording is
// already silent while muted; this only distinguishes deliberate silence from a
// capture failure in the sidecar.
const MUTE_POLL_MS = 250;

const AUDIO_BITS_PER_SECOND = 128_000;

// uploadURLFrom builds the operator's upload endpoint behind the AppAPI proxy.
// Exported pure for tests: getting this wrong silently uploads nowhere.
export function uploadURLFrom(rootPath: string): string {
  const root = (rootPath ?? "").replace(/\/+$/, "");
  return `${root}/index.php/apps/app_api/proxy/gocassini/operator/capture/upload`;
}

// pickAudioSender returns the sender carrying the participant's microphone.
// With an SFU there is exactly one publishing peer connection; the subscriber
// connections have receivers only, so a sender with a live audio track
// identifies the publisher without needing to understand Talk's signalling.
export function pickAudioSender(
  senders: ReadonlyArray<{ track?: { kind?: string; readyState?: string } | null }>,
): number {
  return senders.findIndex(
    (sender) => sender.track?.kind === "audio" && sender.track?.readyState === "live",
  );
}

export function consentGranted(storage: Pick<Storage, "getItem">): boolean {
  try {
    return storage.getItem(CONSENT_STORAGE_KEY) === "granted";
  } catch {
    // Private mode, disabled storage: absence of a recorded yes is a no.
    return false;
  }
}

export interface CaptureState {
  roomToken: string;
  dirName: string;
  callStartWallMs: number;
  worker: Worker;
  segmentIndex: number;
  recorder: MediaRecorder | null;
  muteIntervals: Array<[number, number]>;
  muteSince: number | null;
  mutePoll: number | null;
  segmentStartWallMs: number;
  finished: boolean;
  // pendingChunks chains every ondataavailable hand-off. MediaRecorder emits
  // its final chunk asynchronously AFTER stop() and before onstop, and turning
  // a Blob into an ArrayBuffer is itself async — so sealing the sidecar without
  // awaiting this drops the end of the recording.
  pendingChunks: Promise<void>;
}

let state: CaptureState | null = null;
const publisherSenders = new Set<RTCRtpSender>();

function workerURL(): string {
  // The payload is served next to the worker under the ExApp's /ui/ prefix, and
  // the service worker rewrote Talk's bundle rather than ours, so the payload's
  // own URL is not discoverable from document.currentScript here. Derive it
  // from the same proxy path the upload uses.
  const root = (globalThis as { OC?: { getRootPath?: () => string } }).OC?.getRootPath?.() ?? "";
  return `${root.replace(/\/+$/, "")}/index.php/apps/app_api/proxy/gocassini/ui/capture-worker.js`;
}

function startSegment(session: CaptureState, sender: RTCRtpSender): void {
  if (!sender.track) {
    return;
  }
  const track = sender.track;
  const settings = typeof track.getSettings === "function" ? track.getSettings() : {};
  const index = session.segmentIndex;
  const audioName = `segment-${index}.webm`;
  const mimeType = MediaRecorder.isTypeSupported("audio/webm;codecs=opus")
    ? "audio/webm;codecs=opus"
    : "audio/webm";
  session.segmentStartWallMs = Date.now();
  session.worker.postMessage({
    type: "segment-start",
    dirName: session.dirName,
    meta: {
      index,
      audioName,
      mimeType,
      startWallMs: session.segmentStartWallMs,
      sampleRate: settings.sampleRate ?? null,
      channelCount: settings.channelCount ?? null,
    },
  });

  const recorder = new MediaRecorder(new MediaStream([track]), {
    mimeType,
    audioBitsPerSecond: AUDIO_BITS_PER_SECOND,
  });
  recorder.ondataavailable = (event) => {
    if (event.data.size === 0) {
      return;
    }
    // Chain rather than fire-and-forget, and close over `session` rather than
    // reading the module global: by the time the last chunk arrives the call
    // has ended and the global is already cleared.
    session.pendingChunks = session.pendingChunks.then(async () => {
      const buffer = await event.data.arrayBuffer();
      session.worker.postMessage({ type: "chunk", index, buffer }, [buffer]);
    });
  };
  recorder.start(TIMESLICE_MS);
  session.recorder = recorder;
}

// stopSegment closes the current segment and resolves once the recorder has
// really stopped and every chunk it produced has been handed to the worker.
//
// The ordering matters and is not obvious: stop() makes MediaRecorder emit a
// final dataavailable and only then onstop, and each chunk hand-off is itself
// async. Posting segment-stop before those land would have the worker close the
// file handle with the tail of the recording still in flight.
export async function stopSegment(session: CaptureState): Promise<void> {
  const recorder = session.recorder;
  if (!recorder) {
    return;
  }
  const index = session.segmentIndex;
  const muteIntervals = session.muteIntervals;
  session.recorder = null;
  await new Promise<void>((resolve) => {
    recorder.onstop = () => resolve();
    try {
      recorder.stop();
    } catch {
      // Already inactive: nothing more will arrive, so do not wait for an
      // onstop that will never fire.
      resolve();
    }
  });
  await session.pendingChunks;
  session.worker.postMessage({
    type: "segment-stop",
    index,
    stopWallMs: Date.now(),
    muteIntervals,
  });
  session.segmentIndex += 1;
  session.muteIntervals = [];
}

// RTCRtpScriptTransform is not in every lib.dom we build against, and the sender
// property it is assigned to is typed differently across them. Model both
// loosely and keep the looseness contained to this function.
type ScriptTransformCtor = new (worker: Worker, options: unknown) => object;

function attachTimingTransform(session: CaptureState, sender: RTCRtpSender): void {
  const ScriptTransform = (globalThis as { RTCRtpScriptTransform?: ScriptTransformCtor })
    .RTCRtpScriptTransform;
  const senderWithTransform = sender as unknown as { transform?: object | null };
  if (!ScriptTransform) {
    // Without encoded transforms there are no RTP anchors, so the server falls
    // back to wall-clock plus correlation against whatever intact SFU audio it
    // has. Degraded placement, still a usable upload.
    return;
  }
  if (senderWithTransform.transform) {
    // Talk's end-to-end encryption already owns the single transform slot on
    // this sender (src/utils/e2ee/JitsiE2EEContext.js). Taking it would break
    // the call's encryption; capture continues without anchors.
    return;
  }
  try {
    senderWithTransform.transform = new ScriptTransform(session.worker, { kind: "audio" });
  } catch {
    // Older shape or a sender that refuses a transform: anchors are optional.
  }
}

function pollMute(session: CaptureState, sender: RTCRtpSender): void {
  const enabled = sender.track?.enabled ?? true;
  if (!enabled && session.muteSince === null) {
    session.muteSince = Date.now();
  } else if (enabled && session.muteSince !== null) {
    session.muteIntervals.push([session.muteSince, Date.now()]);
    session.muteSince = null;
  }
}

async function uploadCapture(sidecar: CaptureSidecar, dirName: string): Promise<void> {
  const root = (globalThis as { OC?: { getRootPath?: () => string } }).OC?.getRootPath?.() ?? "";
  const opfsRoot = await navigator.storage.getDirectory();
  const dir = await opfsRoot.getDirectoryHandle(dirName);
  const form = new FormData();
  form.append("sidecar", new Blob([JSON.stringify(sidecar)], { type: "application/json" }), "capture.json");
  for (const segment of sidecar.segments) {
    const fileHandle = await dir.getFileHandle(segment.audioName);
    form.append("segments", await fileHandle.getFile(), segment.audioName);
  }
  const response = await fetch(uploadURLFrom(root), {
    method: "POST",
    body: form,
    credentials: "same-origin",
    headers: { requesttoken: (globalThis as { OC?: { requestToken?: string } }).OC?.requestToken ?? "" },
  });
  if (!response.ok) {
    // Leave OPFS untouched so the next Talk page load can retry. Losing the
    // recording to a transient 502 would defeat the point of buffering it.
    throw new Error(`upload failed: ${response.status}`);
  }
  await opfsRoot.removeEntry(dirName, { recursive: true });
}

// endCall seals the recording and starts the upload. It is idempotent: several
// things legitimately signal the end of a call (the publishing peer connection
// closing, its state going to closed/failed, the page going away) and they can
// all fire for the same call.
async function endCall(): Promise<void> {
  if (!state || state.finished) {
    return;
  }
  state.finished = true;
  const active = state;
  // Cleared up front so a second signal cannot start a parallel teardown, while
  // everything below works from `active` — the async tail of a recording
  // outlives the global by design.
  state = null;
  if (active.mutePoll !== null) {
    clearInterval(active.mutePoll);
  }
  await stopSegment(active);
  const base: Omit<CaptureSidecar, "segments"> = {
    format: SOURCE_CAPTURE_FORMAT,
    roomToken: active.roomToken,
    participantId:
      (globalThis as { OC?: { getCurrentUser?: () => { uid?: string } } }).OC?.getCurrentUser?.()?.uid ?? "",
    callStartWallMs: active.callStartWallMs,
    callEndWallMs: Date.now(),
    userAgent: navigator.userAgent,
  };
  // stopSegment has already awaited the recorder's final chunk, so the worker's
  // view of the segment is complete before the sidecar is sealed.
  active.worker.onmessage = (event: MessageEvent) => {
    if (event.data?.type === "finalized") {
      void uploadCapture(event.data.sidecar as CaptureSidecar, event.data.dirName as string).catch(
        () => {
          // The OPFS buffer is deliberately left in place so a later page load
          // can retry; nothing is lost to a transient upload failure.
        },
      );
    }
  };
  active.worker.postMessage({ type: "finalize", dirName: active.dirName, base });
}

function beginCapture(sender: RTCRtpSender): void {
  if (state) {
    return;
  }
  const roomToken = roomTokenFromPath(location.pathname);
  if (!roomToken || !consentGranted(localStorage)) {
    return;
  }
  const callStartWallMs = Date.now();
  const session: CaptureState = {
    roomToken,
    dirName: captureDirName(roomToken, callStartWallMs),
    callStartWallMs,
    worker: new Worker(workerURL()),
    segmentIndex: 0,
    recorder: null,
    muteIntervals: [],
    muteSince: null,
    mutePoll: null,
    segmentStartWallMs: callStartWallMs,
    finished: false,
    pendingChunks: Promise.resolve(),
  };
  state = session;
  attachTimingTransform(session, sender);
  startSegment(session, sender);
  session.mutePoll = setInterval(() => pollMute(session, sender), MUTE_POLL_MS) as unknown as number;
}

function watchSender(sender: RTCRtpSender): void {
  if (publisherSenders.has(sender)) {
    return;
  }
  publisherSenders.add(sender);
  beginCapture(sender);
  // A replaced track (device switch, or Talk rebuilding its media pipeline)
  // restarts the recorder's media clock, so it has to become a new segment.
  const originalReplace = sender.replaceTrack.bind(sender);
  sender.replaceTrack = async (track: MediaStreamTrack | null) => {
    const result = await originalReplace(track);
    const session = state;
    if (session && track && track.kind === "audio") {
      // A replaced track restarts the recorder's media clock, so it has to
      // become a new segment. Talk's own call must not wait on our bookkeeping,
      // hence the un-awaited chain — but it IS chained, so the new segment
      // cannot start writing before the old one is closed.
      session.pendingChunks = session.pendingChunks
        .then(() => stopSegment(session))
        .then(() => startSegment(session, sender));
    }
    return result;
  };
}

function instrument(pc: RTCPeerConnection): void {
  const originalAddTrack = pc.addTrack.bind(pc);
  pc.addTrack = (track: MediaStreamTrack, ...streams: MediaStream[]) => {
    const sender = originalAddTrack(track, ...streams);
    if (track.kind === "audio") {
      try {
        watchSender(sender);
      } catch {
        // Never let instrumentation break Talk's own addTrack.
      }
    }
    return sender;
  };
  pc.addEventListener("connectionstatechange", () => {
    if (pc.connectionState === "closed" || pc.connectionState === "failed") {
      const index = pickAudioSender(pc.getSenders());
      if (index >= 0 || publisherSenders.size > 0) {
        void endCall();
      }
    }
  });
  const originalClose = pc.close.bind(pc);
  pc.close = () => {
    try {
      if (pickAudioSender(pc.getSenders()) >= 0) {
        void endCall();
      }
    } catch {
      // Fall through to the real close regardless.
    }
    originalClose();
  };
}

// install patches the RTCPeerConnection constructor. Talk resolves it from the
// global at call time — the bundle body we are appended to only defines
// modules — so patching now still catches every connection the call creates.
export function install(): void {
  const globals = globalThis as unknown as { RTCPeerConnection: typeof RTCPeerConnection };
  const Original = globals.RTCPeerConnection as unknown as new (
    ...args: unknown[]
  ) => RTCPeerConnection;
  if (!Original || (Original as { __cassiniPatched?: boolean }).__cassiniPatched) {
    return;
  }
  const Patched = function (this: unknown, ...args: unknown[]) {
    const pc = new Original(...args);
    try {
      instrument(pc);
    } catch {
      // An uninstrumented connection is a missing recording, not a broken call.
    }
    return pc;
  } as unknown as typeof RTCPeerConnection;
  Patched.prototype = Original.prototype;
  (Patched as { __cassiniPatched?: boolean }).__cassiniPatched = true;
  globals.RTCPeerConnection = Patched;
  // A page going away mid-upload keeps its OPFS buffer, so the worst case is a
  // retry rather than a lost recording.
  window.addEventListener("pagehide", () => void endCall());
}

if (typeof window !== "undefined" && roomTokenFromPath(location.pathname)) {
  try {
    install();
  } catch {
    // Talk loads normally whatever happens here.
  }
}
