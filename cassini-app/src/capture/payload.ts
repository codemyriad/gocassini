// The payload the cassini_capture companion app loads on Nextcloud Talk's
// authenticated call page through LoadAdditionalScriptsEvent.
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
// Everything here is defensive. This code runs inside a live call: a throw in
// the wrong place degrades a real meeting for everyone in it. Every entry point
// is wrapped, and every failure mode ends in "do not capture", never in "break
// Talk".

import { loadState } from "@nextcloud/initial-state";
import { retireLegacyCaptureWorkers } from "./register";

import {
  SOURCE_CAPTURE_FORMAT,
  SOURCE_CAPTURE_PENDING_NAME,
  captureDirName,
  roomTokenFromPath,
  type CaptureSidecar,
} from "./protocol";

// CONSENT_STORAGE_KEY records the participant's answer. Capture never starts
// without an explicit opt-in: this records a meeting, and a recorder that turns
// itself on silently is not one we are willing to ship regardless of how much
// the transcript would improve.
const CONSENT_STORAGE_KEY = "cassini.sourceCapture.consent";

const INITIAL_STATE_APP = "cassini_capture";
const INITIAL_STATE_KEY = "capture";

export interface CaptureDeliveryConfig {
  enabled: boolean;
  proxyBase: string;
}

const DISABLED_DELIVERY: CaptureDeliveryConfig = { enabled: false, proxyBase: "" };

// normalizeCaptureDeliveryConfig is deliberately strict. Initial state is
// supplied by the companion app, but a missing/malformed value still has to
// fail closed: without a trusted proxy base neither the worker nor the
// revocation check has a safe destination.
export function normalizeCaptureDeliveryConfig(value: unknown): CaptureDeliveryConfig {
  if (!value || typeof value !== "object") {
    return DISABLED_DELIVERY;
  }
  const candidate = value as { enabled?: unknown; proxyBase?: unknown };
  if (candidate.enabled !== true || typeof candidate.proxyBase !== "string") {
    return DISABLED_DELIVERY;
  }
  const proxyBase = candidate.proxyBase.trim().replace(/\/+$/, "");
  if (!proxyBase.startsWith("/") || proxyBase.startsWith("//")) {
    return DISABLED_DELIVERY;
  }
  return { enabled: true, proxyBase };
}

export function captureDeliveryFromInitialState(): CaptureDeliveryConfig {
  try {
    return normalizeCaptureDeliveryConfig(
      loadState<unknown>(INITIAL_STATE_APP, INITIAL_STATE_KEY, DISABLED_DELIVERY),
    );
  } catch {
    return DISABLED_DELIVERY;
  }
}

let deliveryConfig: CaptureDeliveryConfig = DISABLED_DELIVERY;

// TIMESLICE_MS is how often MediaRecorder hands us a chunk to persist. Page
// teardown cannot wait for MediaRecorder's asynchronous final chunk, so this
// is also the crash/reload loss bound. Two seconds keeps OPFS traffic modest
// while making a reload a small seam rather than a missing stretch of speech.
const TIMESLICE_MS = 2_000;

// MUTE_POLL_MS samples Talk's mute state. MediaStreamTrack has no "enabled
// changed" event, so polling is the only way to observe it. The recording is
// already silent while muted; this only distinguishes deliberate silence from a
// capture failure in the sidecar.
const MUTE_POLL_MS = 250;

const AUDIO_BITS_PER_SECOND = 128_000;

// SERVER_CHECK_MS is how often a running capture re-asks the server whether
// collection is still permitted. Turning the administrator switch off cannot
// retract code already running in a call, so the client asks rather than the
// server having to reach it.
const SERVER_CHECK_MS = 30_000;

// SERVER_CHECK_TIMEOUT_MS bounds each of those requests. Without a deadline a
// hung fetch never settles, so the check does not fail closed at all and
// recording continues past the interval above while further polls pile up
// behind it.
const SERVER_CHECK_TIMEOUT_MS = 5_000;

// Talk's external signaling delivers recording changes immediately. The room
// status request is the bootstrap path (joining or reloading while a recording
// is already active) and the fallback for installations using internal
// signaling, where Talk does not forward recording events to the browser.
const RECORDING_STATUS_POLL_MS = 2_000;
const RECORDING_STATUS_FETCH_TIMEOUT_MS = 5_000;

// serverCheckIntervalMS lets a test shorten the poll. It can only make the
// check MORE frequent, never less: a hostile value on the page cannot use this
// to keep a recorder alive, and anything that can set globals here could
// simply delete this code instead.
export function serverCheckIntervalMS(override: unknown): number {
  const requested = typeof override === "number" && override > 0 ? override : SERVER_CHECK_MS;
  return Math.min(SERVER_CHECK_MS, requested);
}

// CAPTURE_MIME_CANDIDATES is tried in order. WebM/Opus is what Chromium and
// Firefox record and what the server decodes most happily; Safari records only
// MP4, and offering it nothing rather than a container it cannot produce is the
// difference between "no capture" and "a throw mid-call". The chosen type
// travels in the sidecar so the server never has to guess at the container.
const CAPTURE_MIME_CANDIDATES = [
  "audio/webm;codecs=opus",
  "audio/webm",
  "audio/mp4;codecs=opus",
  "audio/mp4",
];

export function supportedCaptureMimeType(
  isSupported: (type: string) => boolean = (type) =>
    typeof MediaRecorder !== "undefined" && MediaRecorder.isTypeSupported(type),
): string | null {
  for (const candidate of CAPTURE_MIME_CANDIDATES) {
    if (isSupported(candidate)) {
      return candidate;
    }
  }
  return null;
}

// uploadURLFrom builds the operator's upload endpoint behind the AppAPI proxy.
// Exported pure for tests: getting this wrong silently uploads nowhere.
export function uploadURLFrom(proxyBase: string): string {
  return `${proxyBase.replace(/\/+$/, "")}/operator/capture/upload`;
}

// enabledURLFrom builds the operator's "may I still record?" endpoint.
export function enabledURLFrom(proxyBase: string): string {
  return `${proxyBase.replace(/\/+$/, "")}/operator/capture/enabled`;
}

const TALK_RECORDING_OFF = 0;
const TALK_RECORDING_VIDEO = 1;
const TALK_RECORDING_AUDIO = 2;
const TALK_RECORDING_VIDEO_STARTING = 3;
const TALK_RECORDING_AUDIO_STARTING = 4;
const TALK_RECORDING_FAILED = 5;

function isTalkRecordingStatus(value: unknown): value is number {
  return Number.isInteger(value) && Number(value) >= TALK_RECORDING_OFF && Number(value) <= TALK_RECORDING_FAILED;
}

// talkRecordingStatusFromSignalingData recognizes only the authoritative room
// message Talk itself consumes. It deliberately ignores chat messages, events
// for another room, malformed extension traffic, and the moderator's HTTP
// start/stop response (which is only a request, not confirmation).
export function talkRecordingStatusFromSignalingData(data: unknown, roomToken: string): number | null {
  let message: unknown = data;
  if (typeof message === "string") {
    try {
      message = JSON.parse(message);
    } catch {
      return null;
    }
  }
  if (!message || typeof message !== "object") {
    return null;
  }
  const frame = message as {
    type?: unknown;
    event?: {
      target?: unknown;
      type?: unknown;
      message?: {
        roomid?: unknown;
        data?: { type?: unknown; recording?: { status?: unknown } };
      };
    };
  };
  const roomMessage = frame.event?.message;
  const status = roomMessage?.data?.recording?.status;
  if (
    frame.type !== "event" ||
    frame.event?.target !== "room" ||
    frame.event?.type !== "message" ||
    roomMessage?.roomid !== roomToken ||
    roomMessage.data?.type !== "recording" ||
    !isTalkRecordingStatus(status)
  ) {
    return null;
  }
  return Number(status);
}

export function talkRoomStatusURL(roomToken: string, rootPath: string): string {
  return `${rootPath.replace(/\/+$/, "")}/ocs/v2.php/apps/spreed/api/v4/room/${encodeURIComponent(roomToken)}?format=json`;
}

export async function fetchTalkRecordingStatus(
  roomToken: string,
  rootPath: string,
  fetchImpl: typeof fetch = fetch,
  timeoutMS: number = RECORDING_STATUS_FETCH_TIMEOUT_MS,
): Promise<number | null> {
  const controller = new AbortController();
  let deadline: ReturnType<typeof setTimeout> | undefined;
  const expired = new Promise<never>((_, reject) => {
    deadline = setTimeout(() => {
      controller.abort();
      reject(new Error("Talk recording status timed out"));
    }, timeoutMS);
  });
  try {
    const response = await Promise.race([
      fetchImpl(talkRoomStatusURL(roomToken, rootPath), {
        credentials: "same-origin",
        cache: "no-store",
        headers: { "OCS-APIRequest": "true", Accept: "application/json" },
        signal: controller.signal,
      }),
      expired,
    ]);
    if (!response.ok) {
      return null;
    }
    const body = (await response.json()) as { ocs?: { data?: { callRecording?: unknown } } };
    const status = body.ocs?.data?.callRecording;
    return isTalkRecordingStatus(status) ? Number(status) : null;
  } catch {
    return null;
  } finally {
    if (deadline !== undefined) {
      clearTimeout(deadline);
    }
  }
}

// captureAllowedByServer asks the operator whether collection is permitted.
//
// Fails CLOSED. A server that cannot be reached, or answers anything other than
// an explicit yes, means no recording: the cost of a false no is a missing
// transcript improvement, and the cost of a false yes is collecting audio an
// administrator has switched off.
export async function captureAllowedByServer(
  proxyBase: string,
  fetchImpl: typeof fetch = fetch,
  timeoutMS: number = SERVER_CHECK_TIMEOUT_MS,
): Promise<boolean> {
  const controller = new AbortController();
  let deadline: ReturnType<typeof setTimeout> | undefined;
  // Raced, not merely aborted. Aborting signals a fetch that honours the
  // signal, but it does not settle the promise we are awaiting — so a
  // transport that ignores it would still hang here forever, and the check
  // would not fail closed at all. The abort is still issued so the abandoned
  // request does not stay open.
  const expired = new Promise<never>((_, reject) => {
    deadline = setTimeout(() => {
      controller.abort();
      reject(new Error("capture permission check timed out"));
    }, timeoutMS);
  });
  try {
    const response = await Promise.race([
      fetchImpl(enabledURLFrom(proxyBase), {
        credentials: "same-origin",
        cache: "no-store",
        signal: controller.signal,
      }),
      expired,
    ]);
    if (!response.ok) {
      return false;
    }
    const body = (await response.json()) as { enabled?: unknown };
    return body.enabled === true;
  } catch {
    // Aborted, offline, unparseable: all of them mean "do not record".
    return false;
  } finally {
    if (deadline !== undefined) {
      clearTimeout(deadline);
    }
  }
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
  // discarded marks a session whose consent was withdrawn mid-call. It is
  // terminal: re-granting during the same call does not resurrect audio
  // recorded while permission was absent, because the person who withdrew it
  // was not consenting to that stretch.
  discarded: boolean;
  // pendingChunks chains every ondataavailable hand-off. MediaRecorder emits
  // its final chunk asynchronously AFTER stop() and before onstop, and turning
  // a Blob into an ArrayBuffer is itself async — so sealing the sidecar without
  // awaiting this drops the end of the recording.
  pendingChunks: Promise<void>;
  // rotation serializes segment changes, and is deliberately a DIFFERENT chain
  // from pendingChunks. Rotation has to await the outstanding chunk hand-offs;
  // chaining it onto pendingChunks made that field refer to a promise
  // containing the very stopSegment call that awaits it, so the await could
  // never resolve and a mid-call device change hung capture for the rest of
  // the meeting.
  rotation: Promise<void>;
}

let state: CaptureState | null = null;
const publisherSenders = new Set<RTCRtpSender>();
// The transform has to be attached before Talk negotiates, but audio must not
// be recorded until Talk confirms that the official recording is active. This
// worker is therefore prepared early and remains inert: it creates no OPFS
// directory and retains no timing anchors until beginCapture activates it.
let preparedWorker: Worker | null = null;
let talkRecordingActive = false;
let recordingStatusRevision = 0;
let recordingStatusFetchInFlight = false;
let recordingStatusPoll: number | null = null;
let talkRoomToken: string | null = null;
// The connection whose sender we are recording. A Talk call has one publishing
// peer connection and many subscriber ones, and every subscriber closes
// routinely as people come and go — ending the capture on any of those closures
// truncated the recording mid-call, permanently, because endCall is idempotent
// and the first spurious close won.
let capturingConnection: RTCPeerConnection | null = null;
// The sender we are actually recording. A call can carry a second audio sender
// — shared system audio, for one — and letting any of them drive the session
// meant that sender's replaceTrack(null) stopped the microphone capture, or its
// replaceTrack rotated capture onto itself.
let capturingSender: RTCRtpSender | null = null;

function workerURL(): string {
  return `${deliveryConfig.proxyBase}/ui/capture-worker.js`;
}

function startSegment(session: CaptureState, sender: RTCRtpSender): void {
  if (!sender.track) {
    return;
  }
  const track = sender.track;
  const settings = typeof track.getSettings === "function" ? track.getSettings() : {};
  const index = session.segmentIndex;
  const audioName = `segment-${index}.webm`;
  const mimeType = supportedCaptureMimeType();
  if (!mimeType) {
    // No container this browser will record. Safari has no audio/webm, so
    // assuming one would have produced a recorder that throws on construction
    // in the middle of somebody's call.
    return;
  }
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

function attachTimingTransform(worker: Worker, sender: RTCRtpSender): void {
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
    senderWithTransform.transform = new ScriptTransform(worker, { kind: "audio" });
  } catch {
    // Older shape or a sender that refuses a transform: anchors are optional.
  }
}

function pollMute(session: CaptureState, sender: RTCRtpSender): void {
  // The same tick that watches mute watches consent. Withdrawing it during a
  // call has to stop the recording then, not merely suppress the upload at the
  // end — the participant asked for the microphone to be let go.
  if (!session.discarded && !consentGranted(localStorage)) {
    session.discarded = true;
    stopWithoutRestart(session);
    return;
  }
  if (session.discarded) {
    return;
  }
  const enabled = sender.track?.enabled ?? true;
  if (!enabled && session.muteSince === null) {
    session.muteSince = Date.now();
  } else if (enabled && session.muteSince !== null) {
    session.muteIntervals.push([session.muteSince, Date.now()]);
    session.muteSince = null;
  }
}

// discardCapture deletes a buffered recording without uploading it.
async function discardCapture(opfsRoot: FileSystemDirectoryHandle, dirName: string): Promise<void> {
  await opfsRoot.removeEntry(dirName, { recursive: true }).catch(() => {});
}

async function uploadCapture(
  sidecar: CaptureSidecar,
  dirName: string,
  revokedDuringCall: boolean,
): Promise<void> {
  const opfsRoot = await navigator.storage.getDirectory();
  // Consent withdrawn at any point during the call is terminal for that call's
  // recording, whether or not it was granted again afterwards.
  if (revokedDuringCall || !consentGranted(localStorage)) {
    await discardCapture(opfsRoot, dirName);
    return;
  }
  const dir = await opfsRoot.getDirectoryHandle(dirName);
  const form = new FormData();
  form.append("sidecar", new Blob([JSON.stringify(sidecar)], { type: "application/json" }), "capture.json");
  for (const segment of sidecar.segments) {
    const fileHandle = await dir.getFileHandle(segment.audioName);
    form.append("segments", await fileHandle.getFile(), segment.audioName);
  }
  // Last check, immediately before the bytes leave: reading the segments back
  // out of OPFS above is several awaits long, and consent can be withdrawn in
  // that window.
  if (!consentGranted(localStorage)) {
    await discardCapture(opfsRoot, dirName);
    return;
  }
  const response = await fetch(uploadURLFrom(deliveryConfig.proxyBase), {
    method: "POST",
    body: form,
    credentials: "same-origin",
    headers: { requesttoken: (globalThis as { OC?: { requestToken?: string } }).OC?.requestToken ?? "" },
  });
  if (response.status === 403) {
    // Not transient: the administrator has switched collection off. Keeping the
    // buffer for a retry would mean holding audio this installation has said it
    // does not collect.
    await discardCapture(opfsRoot, dirName);
    return;
  }
  if (!response.ok) {
    // Leave OPFS untouched so the next Talk page load can retry. Losing the
    // recording to a transient 502 would defeat the point of buffering it.
    throw new Error(`upload failed: ${response.status}`);
  }
  await opfsRoot.removeEntry(dirName, { recursive: true });
  console.info("Cassini source capture: upload accepted");
}

function isBufferedCaptureSidecar(value: unknown): value is CaptureSidecar {
  if (!value || typeof value !== "object") {
    return false;
  }
  const sidecar = value as Partial<CaptureSidecar>;
  return (
    sidecar.format === SOURCE_CAPTURE_FORMAT &&
    typeof sidecar.roomToken === "string" &&
    typeof sidecar.callStartWallMs === "number" &&
    typeof sidecar.callEndWallMs === "number" &&
    Array.isArray(sidecar.segments) &&
    sidecar.segments.length > 0
  );
}

// A navigation is allowed to cancel the network request: the completed
// capture remains in OPFS. Every new Talk page retries those sealed captures
// before recording anything else, which turns reload into two contiguous
// source sessions rather than a lost first half.
export async function retryBufferedCaptures(): Promise<number> {
  if (!consentGranted(localStorage) || serverAllowsCapture !== true) {
    return 0;
  }
  const root = await navigator.storage.getDirectory();
  let uploaded = 0;
  for await (const [dirName, handle] of root.entries()) {
    if (handle.kind !== "directory" || !dirName.startsWith("capture-")) {
      continue;
    }
    try {
      const dir = handle as FileSystemDirectoryHandle;
      let sidecarFile: File;
      try {
        sidecarFile = await (await dir.getFileHandle("capture.json")).getFile();
      } catch {
        sidecarFile = await (await dir.getFileHandle(SOURCE_CAPTURE_PENDING_NAME)).getFile();
      }
      const sidecar = JSON.parse(await sidecarFile.text()) as unknown;
      if (!isBufferedCaptureSidecar(sidecar)) {
        continue;
      }
      await uploadCapture(sidecar, dirName, false);
      uploaded += 1;
    } catch {
      // No sidecar means the previous page died before sealing. Leave the
      // directory untouched; periodic chunks may still be recoverable by a
      // future repair tool, while deleting it here would make that impossible.
    }
  }
  return uploaded;
}

// finishCapture seals the current official-recording interval and starts its
// upload. Talk's confirmed recording-off event calls this while the participant
// remains in the room; peer close and pagehide call it with callEnded=true as
// an idempotent fallback.
async function finishCapture(callEnded: boolean): Promise<void> {
  if (callEnded) {
    talkRecordingActive = false;
    const idleWorker = preparedWorker;
    preparedWorker = null;
    capturingConnection = null;
    capturingSender = null;
    idleWorker?.terminate();
  }
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
  active.worker.postMessage({ type: "timing-active", active: false });
  // Let any rotation still in flight finish before closing the final segment,
  // so a device change during the last seconds of a call does not race the
  // teardown.
  await active.rotation.catch(() => {});
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
    if (event.data?.type === "error") {
      // The worker could not seal anything — nothing was recorded, or a write
      // failed. There will be no "finalized" to finish this interval.
      active.worker.terminate();
      return;
    }
    if (event.data?.type !== "finalized") {
      return;
    }
    void uploadCapture(event.data.sidecar as CaptureSidecar, event.data.dirName as string, active.discarded)
      .catch(() => {
        // The OPFS buffer is deliberately left in place so a later page load
        // can retry; nothing is lost to a transient upload failure.
        console.warn("Cassini source capture: upload deferred; buffered audio remains in browser storage");
      })
      .finally(() => {
        if (callEnded || capturingConnection === null) {
          active.worker.terminate();
          return;
        }
        // The official recording stopped but the call continues. Keep the
        // transform's pass-through worker alive so stopping Cassini cannot
        // interrupt Talk's outgoing audio. The worker reset after finalize and
        // can be reused if recording starts again.
        if (preparedWorker === null) {
          preparedWorker = active.worker;
        }
      });
  };
  active.worker.postMessage({ type: "finalize", dirName: active.dirName, base });
}

async function endCall(): Promise<void> {
  await finishCapture(true);
}

// stopWithoutRestart closes the current segment and leaves the recorder idle.
// Used for replaceTrack(null), which detaches the microphone: the old track is
// gone, so continuing to record it would keep writing whatever that detached
// source still produces.
export function stopWithoutRestart(session: CaptureState): void {
  session.rotation = session.rotation.then(() => stopSegment(session)).catch(() => {});
}

// rotateSegment closes the current segment and opens the next, serialized on
// the session's rotation chain.
//
// The chain is `rotation`, never `pendingChunks`. stopSegment awaits
// pendingChunks to catch the recorder's final chunk; chaining rotation onto
// that same field made it refer to a promise containing the very stopSegment
// call that awaits it, so the await could never resolve and one mid-call
// device change hung capture for the rest of the meeting.
//
// Not awaited by the caller: Talk's own replaceTrack must not wait on our
// bookkeeping.
export function rotateSegment(session: CaptureState, sender: RTCRtpSender): void {
  session.rotation = session.rotation
    .then(() => stopSegment(session))
    .then(() => {
      // Re-checked HERE, not only when the rotation was queued. A session whose
      // consent was withdrawn must not start recording again because the
      // participant happened to change microphone afterwards; the upload was
      // already blocked, but collection has to stop too.
      if (session.discarded || session.finished) {
        return;
      }
      startSegment(session, sender);
    })
    .catch(() => {
      // A failed rotation costs this segment, not the recording.
    });
}

// serverAllowsCapture starts from the value injected by the companion app and
// is refreshed on a timer.
//
// It is resolved BEFORE a call starts rather than when one does, and that
// ordering is forced by the platform: an encoded transform has to be attached
// to a sender before negotiation, so there is no room for a round trip inside
// addTrack. Asking there attached the transform a round trip late and produced
// a recording with no timing anchors at all.
//
let serverAllowsCapture = false;

// refreshCapturePermission updates the cache and stops an active capture the
// moment the answer turns to no, so the switch reaches a call in progress.
async function refreshCapturePermission(proxyBase: string): Promise<void> {
  const allowed = await captureAllowedByServer(proxyBase);
  serverAllowsCapture = allowed;
  if (!allowed && state && !state.discarded) {
    state.discarded = true;
    stopWithoutRestart(state);
  }
}

function beginCapture(sender: RTCRtpSender, connection: RTCPeerConnection): void {
  if (state || !talkRecordingActive) {
    return;
  }
  const roomToken = roomTokenFromPath(location.pathname);
  if (!roomToken || !consentGranted(localStorage)) {
    return;
  }
  talkRoomToken = roomToken;
  // The administrator switch was injected before this script ran. Anything
  // other than an explicit yes means no recorder or OPFS directory is created.
  if (serverAllowsCapture !== true) {
    return;
  }
  const worker = preparedWorker ?? new Worker(workerURL());
  if (preparedWorker === null) {
    // A second recording interval can start after the first worker finalized.
    // Reattaching may be unavailable because the sender still owns the first
    // transform; source audio remains usable and simply carries no RTP-rate
    // anchors in that uncommon shape.
    attachTimingTransform(worker, sender);
  }
  preparedWorker = null;
  const callStartWallMs = Date.now();
  const session: CaptureState = {
    roomToken,
    dirName: captureDirName(roomToken, callStartWallMs),
    callStartWallMs,
    worker,
    segmentIndex: 0,
    recorder: null,
    muteIntervals: [],
    muteSince: null,
    mutePoll: null,
    segmentStartWallMs: callStartWallMs,
    finished: false,
    discarded: false,
    pendingChunks: Promise.resolve(),
    rotation: Promise.resolve(),
  };
  state = session;
  capturingSender = sender;
  capturingConnection = connection;
  worker.postMessage({
    type: "capture-start",
    dirName: session.dirName,
    base: {
      format: SOURCE_CAPTURE_FORMAT,
      roomToken: session.roomToken,
      participantId:
        (globalThis as { OC?: { getCurrentUser?: () => { uid?: string } } }).OC?.getCurrentUser?.()
          ?.uid ?? "",
      callStartWallMs: session.callStartWallMs,
      userAgent: navigator.userAgent,
    },
  });
  worker.postMessage({ type: "timing-active", active: true });
  startSegment(session, sender);
  console.info("Cassini source capture: Talk recording active; local source recording started");
  session.mutePoll = setInterval(() => pollMute(session, sender), MUTE_POLL_MS) as unknown as number;
}

function watchSender(sender: RTCRtpSender, connection: RTCPeerConnection): void {
  if (publisherSenders.has(sender)) {
    return;
  }
  publisherSenders.add(sender);
  const roomToken = roomTokenFromPath(location.pathname);
  if (roomToken) {
    talkRoomToken = roomToken;
    void refreshTalkRecordingStatus(roomToken);
  }
  if (capturingSender === null) {
    capturingSender = sender;
    capturingConnection = connection;
    if (serverAllowsCapture && consentGranted(localStorage)) {
      preparedWorker = new Worker(workerURL());
      attachTimingTransform(preparedWorker, sender);
    }
    beginCapture(sender, connection);
  }
  // A replaced track (device switch, or Talk rebuilding its media pipeline)
  // restarts the recorder's media clock, so it has to become a new segment.
  const originalReplace = sender.replaceTrack.bind(sender);
  sender.replaceTrack = async (track: MediaStreamTrack | null) => {
    const result = await originalReplace(track);
    const session = state;
    // Only the sender we are recording drives the session. Another audio
    // sender's track changes are none of our business.
    if (sender === capturingSender) {
      if (session && track && track.kind === "audio") {
        rotateSegment(session, sender);
      } else if (session && track === null) {
        // The microphone was detached. Close the segment rather than keep
        // recording a track Talk is no longer sending.
        stopWithoutRestart(session);
      } else if (!session && track && track.kind === "audio") {
        beginCapture(sender, connection);
      }
    }
    return result;
  };
}

function applyTalkRecordingStatus(status: number): void {
  if (status === TALK_RECORDING_VIDEO || status === TALK_RECORDING_AUDIO) {
    talkRecordingActive = true;
    if (capturingSender && capturingConnection) {
      beginCapture(capturingSender, capturingConnection);
    }
    return;
  }
  if (status === TALK_RECORDING_VIDEO_STARTING || status === TALK_RECORDING_AUDIO_STARTING) {
    // A moderator requested recording, but Talk's backend has not confirmed it.
    // Starting here would collect audio from a recording that might fail.
    return;
  }
  if (status === TALK_RECORDING_OFF || status === TALK_RECORDING_FAILED) {
    talkRecordingActive = false;
    if (state) {
      console.info("Cassini source capture: Talk recording stopped; sealing and uploading");
      void finishCapture(false);
    }
  }
}

async function refreshTalkRecordingStatus(roomToken: string): Promise<void> {
  if (recordingStatusFetchInFlight) {
    return;
  }
  recordingStatusFetchInFlight = true;
  const revision = recordingStatusRevision;
  const rootPath =
    (globalThis as { OC?: { getRootPath?: () => string } }).OC?.getRootPath?.() ?? "";
  try {
    const status = await fetchTalkRecordingStatus(roomToken, rootPath);
    // A signaling event received while this request was in flight is newer
    // than the response and wins. This prevents a slow bootstrap response of
    // OFF from tearing down a recording the socket just confirmed active.
    if (status !== null && revision === recordingStatusRevision) {
      applyTalkRecordingStatus(status);
    }
  } finally {
    recordingStatusFetchInFlight = false;
  }
}

type SignalingSocketLike = {
  addEventListener(type: string, listener: (event: MessageEvent) => void): void;
};

const watchedSignalingSockets = new WeakSet<object>();
let signalingSocketObserved = false;

function watchSignalingSocket(socket: unknown): void {
  if (
    !socket ||
    typeof socket !== "object" ||
    typeof (socket as Partial<SignalingSocketLike>).addEventListener !== "function" ||
    watchedSignalingSockets.has(socket)
  ) {
    return;
  }
  watchedSignalingSockets.add(socket);
  signalingSocketObserved = true;
  (socket as SignalingSocketLike).addEventListener("message", (event: MessageEvent) => {
    const roomToken = talkRoomToken ?? roomTokenFromPath(location.pathname);
    if (!roomToken) {
      return;
    }
    talkRoomToken = roomToken;
    const status = talkRecordingStatusFromSignalingData(event.data, roomToken);
    if (status === null) {
      return;
    }
    recordingStatusRevision += 1;
    applyTalkRecordingStatus(status);
  });
  // Reconnecting can span a transition. The room resource gives us the
  // current state without guessing which events were missed.
  const roomToken = talkRoomToken ?? roomTokenFromPath(location.pathname);
  if (roomToken) {
    talkRoomToken = roomToken;
    void refreshTalkRecordingStatus(roomToken);
  }
}

function installSignalingSocketObserver(): void {
  const globals = globalThis as unknown as Record<string, unknown>;
  const descriptor = Object.getOwnPropertyDescriptor(globals, "signalingSocket");
  if (descriptor && descriptor.configurable === false) {
    watchSignalingSocket(globals.signalingSocket);
    return;
  }
  let current = globals.signalingSocket;
  Object.defineProperty(globals, "signalingSocket", {
    configurable: true,
    enumerable: descriptor?.enumerable ?? true,
    get: () => current,
    set: (value: unknown) => {
      current = value;
      watchSignalingSocket(value);
    },
  });
  watchSignalingSocket(current);
}

function installTalkRecordingLifecycle(): void {
  installSignalingSocketObserver();
  const initialRoomToken = roomTokenFromPath(location.pathname);
  if (initialRoomToken) {
    talkRoomToken = initialRoomToken;
    void refreshTalkRecordingStatus(initialRoomToken);
  }
  recordingStatusPoll = setInterval(() => {
    // External signaling is immediate. Poll only as its missed-event watchdog
    // while recording, or as the lifecycle source when no socket exists (Talk
    // installations using internal signaling).
    if (!signalingSocketObserved || talkRecordingActive) {
      const roomToken = talkRoomToken ?? roomTokenFromPath(location.pathname);
      if (roomToken) {
        talkRoomToken = roomToken;
        void refreshTalkRecordingStatus(roomToken);
      }
    }
  }, RECORDING_STATUS_POLL_MS) as unknown as number;
}

function instrument(pc: RTCPeerConnection): void {
  const originalAddTrack = pc.addTrack.bind(pc);
  pc.addTrack = (track: MediaStreamTrack, ...streams: MediaStream[]) => {
    const sender = originalAddTrack(track, ...streams);
    if (track.kind === "audio") {
      try {
        watchSender(sender, pc);
      } catch {
        // Never let instrumentation break Talk's own addTrack.
      }
    }
    return sender;
  };
  // ONLY the publishing connection ends the capture. Subscriber connections
  // close constantly as participants come and go, and treating any of those as
  // the end of the call truncated the recording.
  pc.addEventListener("connectionstatechange", () => {
    if (pc !== capturingConnection) {
      return;
    }
    if (pc.connectionState === "closed" || pc.connectionState === "failed") {
      void endCall();
    }
  });
  const originalClose = pc.close.bind(pc);
  pc.close = () => {
    try {
      if (pc === capturingConnection) {
        void endCall();
      }
    } catch {
      // Fall through to the real close regardless.
    }
    originalClose();
  };
}

// install patches the RTCPeerConnection constructor. The companion loads this
// ordinary script before Talk's bundle, so every connection the call creates
// resolves the patched constructor.
export function install(config: CaptureDeliveryConfig = captureDeliveryFromInitialState()): void {
  const normalized = normalizeCaptureDeliveryConfig(config);
  if (!normalized.enabled) {
    return;
  }
  deliveryConfig = normalized;
  serverAllowsCapture = true;
  const globals = globalThis as unknown as { RTCPeerConnection: typeof RTCPeerConnection };
  const Original = globals.RTCPeerConnection;
  if (!Original || (Original as { __cassiniPatched?: boolean }).__cassiniPatched) {
    return;
  }
  void retryBufferedCaptures().catch(() => {});
  installTalkRecordingLifecycle();
  // A Proxy rather than a wrapper function. A wrapper loses new.target (so
  // `class Mine extends RTCPeerConnection` builds the wrong prototype), drops
  // static members such as generateCertificate, and gives Talk a constructor
  // that is not the one it thinks it has. The construct trap changes exactly
  // one thing: it instruments the connection on the way out.
  const Patched = new Proxy(Original, {
    construct(target, args, newTarget) {
      const pc = Reflect.construct(target, args, newTarget) as RTCPeerConnection;
      try {
        instrument(pc);
      } catch {
        // An uninstrumented connection is a missing recording, not a broken call.
      }
      return pc;
    },
  });
  (Patched as { __cassiniPatched?: boolean }).__cassiniPatched = true;
  globals.RTCPeerConnection = Patched;
  // Initial state resolves the administrator switch before this ordinary
  // script runs, leaving the synchronous addTrack path free to attach the
  // encoded transform before negotiation. Polling remains for revocation
  // during an already-running call.
  setInterval(
    () => void refreshCapturePermission(deliveryConfig.proxyBase),
    serverCheckIntervalMS((globalThis as { __cassiniCaptureCheckMs?: unknown }).__cassiniCaptureCheckMs),
  );
  // A page going away mid-upload keeps its OPFS buffer, so the worst case is a
  // retry rather than a lost recording.
  window.addEventListener("pagehide", () => void endCall());
}

if (typeof window !== "undefined") {
  try {
    // A user can reach Talk before revisiting Cassini after the migration.
    // Retire the abandoned bundle-rewriting worker here as well; this does not
    // delay installation or negotiation.
    void retireLegacyCaptureWorkers(navigator.serviceWorker).catch(() => {});
    const config = captureDeliveryFromInitialState();
    if (config.enabled) {
      install(config);
    }
  } catch {
    // Talk loads normally whatever happens here.
  }
}
