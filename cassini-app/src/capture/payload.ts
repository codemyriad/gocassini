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
// Three conditions gate collection, and this file has no fourth: the
// administrator switch, which the server answers for and re-answers every
// thirty seconds; Talk's own confirmed recording, which says the ROOM is being
// recorded; and this participant actually being in the call, which is the
// publishing connection reaching "connected" with a live track and is the only
// one of the three that says anything about this browser. Somebody whose page
// has loaded and whose microphone Talk has opened, but who has not joined, is
// not recorded. Cassini stores nothing per participant and
// asks them nothing. Telling a room that it is being recorded is Talk's job —
// its recording indicator and its recording-consent setting — so there is no
// browser key here to read, and adding one would put a second, weaker answer
// beside the one Talk already gives everybody in the call.
//
// Everything here is defensive. This code runs inside a live call: a throw in
// the wrong place degrades a real meeting for everyone in it. Every entry point
// is wrapped, and every failure mode ends in "do not capture", never in "break
// Talk".

import { loadState } from "@nextcloud/initial-state";
import { forgetLegacyConsent, retireLegacyCaptureWorkers } from "./register";

import {
  SOURCE_CAPTURE_FORMAT,
  SOURCE_CAPTURE_PENDING_NAME,
  captureDirName,
  roomTokenFromPath,
  type CaptureSidecar,
} from "./protocol";

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
// Statuses that mean "this capture will never be accepted". Everything else,
// 4xx included, leaves the buffer in place for the next Talk page load.
//   400 the sidecar or a segment name is wrong, i.e. a client bug
//   403 collection is off, or the uploader was not in that room
//   413 the body is over the server's cap
//   415 the body was not the multipart the server expects
//   422 the sidecar contradicts itself
const TERMINAL_UPLOAD_STATUSES = new Set([400, 403, 413, 415, 422]);

// MAX_UPLOAD_ATTEMPTS caps how many times one capture is offered to the server.
//
// The terminal list above is an allowlist, so anything outside it keeps the
// buffer — which is right for a 502 and wrong forever. A deployment that
// answers 404 or 410 to this route (a companion left enabled against an ExApp
// that is gone, a route removed by an upgrade) would otherwise have every
// affected participant re-uploading a meeting-sized body on every Talk page
// load, permanently, with no backoff. A capture that has been refused this
// many times is not going to be accepted.
const MAX_UPLOAD_ATTEMPTS = 5;
const ATTEMPTS_STORAGE_KEY = "cassini.sourceCapture.uploadAttempts";

function readUploadAttempts(): Record<string, number> {
  try {
    const raw = localStorage.getItem(ATTEMPTS_STORAGE_KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : {};
    return parsed && typeof parsed === "object" ? (parsed as Record<string, number>) : {};
  } catch {
    return {};
  }
}

function writeUploadAttempts(attempts: Record<string, number>): void {
  try {
    localStorage.setItem(ATTEMPTS_STORAGE_KEY, JSON.stringify(attempts));
  } catch {
    // Storage full or blocked. The cap is a safety net, not a correctness
    // requirement, so losing the count is survivable.
  }
}

// noteUploadAttempt records one refusal and reports whether this capture has
// run out of attempts.
function noteUploadAttempt(dirName: string): boolean {
  const attempts = readUploadAttempts();
  const next = (attempts[dirName] ?? 0) + 1;
  attempts[dirName] = next;
  writeUploadAttempts(attempts);
  return next >= MAX_UPLOAD_ATTEMPTS;
}

function clearUploadAttempts(dirName: string): void {
  const attempts = readUploadAttempts();
  if (dirName in attempts) {
    delete attempts[dirName];
    writeUploadAttempts(attempts);
  }
}



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

export interface CaptureState {
  roomToken: string;
  dirName: string;
  // connection is the publishing peer connection this capture belongs to. It is
  // kept so the in-call gate can be re-checked whenever a segment is about to
  // start, not only when the capture began.
  connection: RTCPeerConnection | null;
  callStartWallMs: number;
  worker: Worker;
  segmentIndex: number;
  recorder: MediaRecorder | null;
  muteIntervals: Array<[number, number]>;
  muteSince: number | null;
  mutePoll: number | null;
  segmentStartWallMs: number;
  finished: boolean;
  // discarded marks a session the administrator switch turned off mid-call. It
  // is terminal: switching collection back on during the same call does not
  // resurrect audio recorded in between, because a switch that is off has to
  // mean nothing from that stretch is kept, not merely nothing further is
  // recorded.
  discarded: boolean;
  // pendingChunks chains every ondataavailable hand-off. MediaRecorder emits
  // its final chunk asynchronously AFTER stop() and before onstop, and turning
  // a Blob into an ArrayBuffer is itself async — so sealing the sidecar without
  // awaiting this drops the end of the recording.
  pendingChunks: Promise<void>;
  // releaseDirClaim drops this page's exclusive claim on the capture directory.
  // Called once the capture is finished with — after its upload settles, or
  // immediately when the page is going away — never before, because another
  // Talk page's scan must not reach the directory while it is being written or
  // sent.
  releaseDirClaim: () => void;
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
// worker is therefore started early and remains inert: it creates no OPFS
// directory and retains no timing anchors until beginCapture activates it.
// Early also buys the readiness deadline its head start, so a worker that will
// never run is usually caught before the call has a frame to send.
let preparedWorker: Worker | null = null;
// captureAbandoned is terminal for this page. A worker that could not start is
// not going to start on the next attempt, and every attempt is another
// transform offered to a sender carrying a live call.
let captureAbandoned = false;
let talkRecordingActive = false;
// recordingStatusAnswered distinguishes "Talk says this room is not being
// recorded" from "Talk has not answered yet". Only the first is a reason to
// stop holding a buffered capture for adoption; treating the second as a no
// would upload every reloader's buffer a round trip before the answer that
// says to keep it arrives.
let recordingStatusAnswered = false;
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
  // A live track, not merely a present one. An ended track delivers nothing and
  // a recorder on it is a recorder on something Talk is no longer sending.
  if (!sender.track || sender.track.readyState !== "live") {
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

// attachedTransform records the one transform this payload attached, and what
// it is attached to. There is at most one: it belongs to the publishing sender,
// and the worker behind it is either idle or the running session's.
let attachedTransform: {
  worker: Worker;
  sender: RTCRtpSender;
  connection: RTCPeerConnection;
} | null = null;
let readyDeadline: number | null = null;
let readyDeadlineWorker: Worker | null = null;

// PASS_THROUGH_WORKER_SOURCE is the transform that does nothing at all: it
// hands every encoded frame straight back. It is inline, and that is the whole
// point of it — it needs no network, no build artifact and no version
// agreement, so it is the one worker that cannot fail the way the capture
// worker just did. See restoreOutgoingAudio.
const PASS_THROUGH_WORKER_SOURCE =
  "self.onrtctransform=(e)=>{e.transformer.readable.pipeTo(e.transformer.writable).catch(()=>{})};";

// WORKER_READY_TIMEOUT_MS bounds the wait for a worker to report for duty.
//
// The participant is mute for as long as this runs, because the transform is
// already attached to their sender by then and a worker that never started is
// not reading it — so this is a silence budget, not a patience budget. It is
// measured from the worker's creation at install, which is a whole Talk
// bootstrap before the call negotiates and starts sending, so in practice a
// healthy worker has answered long before any frame is at stake and a broken
// one is caught before one is either. Three seconds is well past the point
// where a few kilobytes served from the same origin is merely slow.
const WORKER_READY_TIMEOUT_MS = 3_000;

// prepareTimingWorker starts the worker and watches it prove it is alive.
//
// Three failures leave a worker on the page that will never read a frame: the
// script 404s (an ExApp restarted or upgraded mid-call), it throws while it
// evaluates, or it loads and does nothing (a skewed build). onerror reports the
// second and, in most browsers, the first. The deadline covers all three,
// because a 404 fires no error event in some browsers and a silent script fires
// none anywhere.
function prepareTimingWorker(): void {
  if (preparedWorker !== null || captureAbandoned) {
    return;
  }
  let worker: Worker;
  try {
    worker = new Worker(workerURL());
  } catch {
    // A constructor that throws — a blocked URL, a policy that forbids the
    // worker — will throw again on the next call. Stop asking.
    captureAbandoned = true;
    return;
  }
  preparedWorker = worker;
  worker.onerror = () => abandonCapture(worker, "the timing worker failed to start");
  worker.onmessageerror = () =>
    abandonCapture(worker, "the timing worker sent an unreadable message");
  // addEventListener rather than onmessage: finishCapture assigns its own
  // onmessage to await the sealed sidecar, and would otherwise assign over the
  // readiness signal.
  worker.addEventListener("message", (event: MessageEvent) => {
    if (event.data?.type === "ready") {
      clearReadyDeadline(worker);
    }
  });
  readyDeadlineWorker = worker;
  readyDeadline = setTimeout(
    () => abandonCapture(worker, "the timing worker never reported ready"),
    WORKER_READY_TIMEOUT_MS,
  ) as unknown as number;
}

function clearReadyDeadline(worker: Worker): void {
  if (readyDeadlineWorker !== worker || readyDeadline === null) {
    return;
  }
  clearTimeout(readyDeadline);
  readyDeadline = null;
  readyDeadlineWorker = null;
}

// attachedSenderCanStillSend reports whether the transform we attached is on a
// sender that can send again. Only a closed connection is a definite no:
// "failed" can be recovered by an ICE restart, and a worker stopped in that
// window takes the participant's audio with it.
function attachedSenderCanStillSend(): boolean {
  return attachedTransform !== null && attachedTransform.connection.connectionState !== "closed";
}

// restoreOutgoingAudio is what a failed capture owes the call.
//
// The transform is attached before negotiation and cannot wait for the worker
// to prove itself: a transform attached after the sender has negotiated is
// ignored by the platform and collects nothing, which is why the attach is
// eager in the first place. So by the time a worker turns out to be broken, the
// participant's every encoded frame is already being routed into it, and
// nobody in the room can hear them.
//
// Replacing the transform is the only exit. Measured on Chromium 151.0.7922.34:
// `sender.transform = null` does NOT release the sender — it freezes at zero
// packets sent, and so does a HEALTHY transform detached the same way, and
// neither a replaceTrack nor waiting brings it back. Assigning a working
// transform over the failed one restores the flow immediately. So the sender
// gets a pass-through built from an inline string: no fetch, no build artifact,
// nothing that can be missing or skewed the way the capture worker just was.
function restoreOutgoingAudio(): void {
  const attached = attachedTransform;
  if (attached === null) {
    return;
  }
  if (attached.connection.connectionState === "closed") {
    // Nothing left to rescue, and nothing left to protect either.
    attachedTransform = null;
    return;
  }
  // attachedTransform is cleared ONLY once the sender is someone else's
  // problem. It is what stops releaseTimingWorker terminating a worker whose
  // transform is still on a live sender, and terminating is the one move with
  // no way back: the sender stays frozen and no later attempt can reach it. So
  // every path that fails to substitute leaves it set, deliberately, and the
  // broken worker keeps running as the only thing still draining frames.
  const ScriptTransform = (globalThis as { RTCRtpScriptTransform?: ScriptTransformCtor })
    .RTCRtpScriptTransform;
  if (!ScriptTransform) {
    return;
  }
  try {
    const source = URL.createObjectURL(
      new Blob([PASS_THROUGH_WORKER_SOURCE], { type: "text/javascript" }),
    );
    // The URL is deliberately not revoked. Revoking races the worker's own
    // fetch of it, and one blob URL on a page whose capture has just failed is
    // not worth that risk.
    const passThrough = new Worker(source);
    (attached.sender as unknown as { transform?: object | null }).transform = new ScriptTransform(
      passThrough,
      { kind: "audio" },
    );
    attachedTransform = null;
  } catch {
    // A policy that forbids a blob worker leaves the participant where the
    // broken capture worker left them. Nothing else here can reach that sender.
  }
}

// releaseTimingWorker is the only place in this file that stops a worker.
//
// Terminating one whose transform is still on a sender that can send makes the
// participant INAUDIBLE to the whole room for the rest of the call: their
// encoded frames are routed into a worker that no longer reads them, Talk goes
// on showing them as unmuted and transmitting, and only a page reload ends it.
// A worker whose transform is live is therefore left running as the pure
// pass-through it already is, and only one that nothing depends on is stopped.
// Callers that must stop a live one call restoreOutgoingAudio first, which
// hands the sender a transform that works. Safe to call twice.
function releaseTimingWorker(worker: Worker | null): void {
  if (worker === null) {
    return;
  }
  if (attachedTransform?.worker === worker) {
    if (attachedSenderCanStillSend()) {
      return;
    }
    attachedTransform = null;
  }
  if (preparedWorker === worker) {
    preparedWorker = null;
  }
  clearReadyDeadline(worker);
  worker.terminate();
}

// retireSessionWorker gives a finished session's worker back to the idle slot,
// or stops it when nothing depends on it any more. Which of the two happens is
// not this function's choice: while its transform is on a sender that can still
// send, the worker IS part of Talk's outgoing audio path.
function retireSessionWorker(worker: Worker): void {
  releaseTimingWorker(worker);
  if (preparedWorker === null && attachedTransform?.worker === worker) {
    preparedWorker = worker;
  }
}

// abandonCapture gives up on collecting for the rest of this page's life, and
// gives the call back its audio first. The worker is the storage as well as the
// timing, so one that cannot start is not a degraded capture but no capture at
// all — and handing a live sender another transform backed by the same broken
// script is the one trade this feature must never make. Audio already buffered
// in OPFS is left alone: a later page load still uploads it.
function abandonCapture(worker: Worker, reason: string): void {
  captureAbandoned = true;
  if (attachedTransform?.worker === worker) {
    restoreOutgoingAudio();
  }
  const failed = state?.worker === worker ? state : null;
  if (failed) {
    state = null;
    failed.finished = true;
    // The buffer outlives this page's capture and a later page has to be able
    // to settle it. A claim left behind would make it look like somebody is
    // still recording into it for as long as this page is open.
    failed.releaseDirClaim();
    if (failed.mutePoll !== null) {
      clearInterval(failed.mutePoll);
    }
    try {
      failed.recorder?.stop();
    } catch {
      // Already inactive. Stopping it touches only the recorder; the track it
      // reads stays Talk's, live and unchanged.
    }
    failed.recorder = null;
  }
  releaseTimingWorker(worker);
  console.warn(`Cassini source capture: ${reason}; capture abandoned for this page`);
}

// attachTimingTransform installs the encoded transform, and does it eagerly:
// synchronously inside Talk's addTrack, before the connection negotiates.
//
// That ordering is forced by the platform, not chosen. A transform attached
// after the sender has negotiated is ignored — measured on Chromium
// 151.0.7922.34, a first transform attached a moment too late receives no
// frames at all while the audio flows past it — so a capture that waits for its
// worker to prove itself is a capture with no timing anchors, ever. The price
// of attaching before the worker has answered is that a broken worker takes the
// participant's audio with it until the readiness deadline above notices and
// restoreOutgoingAudio gives the sender a transform that works.
function attachTimingTransform(
  worker: Worker,
  sender: RTCRtpSender,
  connection: RTCPeerConnection,
): void {
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
    // this sender (src/utils/e2ee/JitsiE2EEContext.js), or it is ours from an
    // earlier recording interval. Taking it would break the call's encryption;
    // capture continues without anchors.
    return;
  }
  try {
    senderWithTransform.transform = new ScriptTransform(worker, { kind: "audio" });
    attachedTransform = { worker, sender, connection };
  } catch {
    // Older shape or a sender that refuses a transform: anchors are optional.
  }
}

function pollMute(session: CaptureState, sender: RTCRtpSender): void {
  // A session the administrator switch discarded keeps its poll running until
  // teardown, but it has no segment left to attribute mute to: recording it
  // would append intervals to a recording that is never going to be uploaded.
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
// discardCapture removes a capture and reports whether it is actually gone.
//
// It still never throws — a failed delete must not break the caller's flow —
// but callers that key state on the capture being gone (the upload attempt
// counter) need to know, or a failed delete silently resets that state and the
// capture is offered again forever.
async function discardCapture(
  opfsRoot: FileSystemDirectoryHandle,
  dirName: string,
): Promise<boolean> {
  try {
    await opfsRoot.removeEntry(dirName, { recursive: true });
    return true;
  } catch {
    return false;
  }
}

async function uploadCapture(
  sidecar: CaptureSidecar,
  dirName: string,
  revokedDuringCall: boolean,
): Promise<void> {
  const opfsRoot = await navigator.storage.getDirectory();
  // The administrator switch turned off at any point during the call is
  // terminal for that call's recording, whether or not it was switched back on
  // afterwards.
  if (revokedDuringCall || serverAllowsCapture !== true) {
    await discardCapture(opfsRoot, dirName);
    return;
  }
  const dir = await opfsRoot.getDirectoryHandle(dirName);
  const form = new FormData();
  form.append("sidecar", new Blob([JSON.stringify(sidecar)], { type: "application/json" }), "capture.json");
  // One DISTINCT field name per segment, and nothing but ASCII letters,
  // digits and an underscore in it.
  //
  // Every segment used to go under the repeated name "segments". Go's
  // multipart reader accepts that, but Nextcloud's AppAPI proxy rebuilds the
  // body from PHP's $_POST/$_FILES rather than streaming it, and PHP keeps
  // only the last file for a repeated field name — so a two-segment upload
  // arrived as one and was refused. Segments are cut whenever Talk replaces
  // the sender's track, i.e. on any microphone change during a recorded call,
  // so this was not an edge case. PHP also rewrites "." and other characters
  // in field names, which is why the name is deliberately dull.
  //
  // The server identifies a segment by its FILE name, which the sidecar
  // already refers to; these field names exist only so the proxy keeps every
  // part.
  for (const [index, segment] of sidecar.segments.entries()) {
    const fileHandle = await dir.getFileHandle(segment.audioName);
    form.append(`segment_${index}`, await fileHandle.getFile(), segment.audioName);
  }
  // Last check, immediately before the bytes leave: reading the segments back
  // out of OPFS above is several awaits long, and the thirty-second poll can
  // land the administrator's off in that window.
  if (serverAllowsCapture !== true) {
    await discardCapture(opfsRoot, dirName);
    return;
  }
  const response = await fetch(uploadURLFrom(deliveryConfig.proxyBase), {
    method: "POST",
    body: form,
    credentials: "same-origin",
    headers: { requesttoken: (globalThis as { OC?: { requestToken?: string } }).OC?.requestToken ?? "" },
  });
  if (TERMINAL_UPLOAD_STATUSES.has(response.status)) {
    // The server judged THIS capture, and retrying an identical body cannot
    // change the answer. Keeping it would mean re-uploading a meeting-sized
    // body on every Talk page load, forever, with no backoff — and disabling
    // the companion, the recommended way to back the feature out, also removes
    // the only code that would ever clean it up.
    //
    // The list is deliberately an allowlist rather than "any 4xx". A 4xx can
    // also describe the delivery rather than the capture — a truncated body, a
    // rate limit, an expired session — and discarding an intact recording
    // because a proxy cut the request short would be the feature losing audio
    // it was built to save.
    //
    // The cost is still honest: a recording is discarded and the participant is
    // not told, which is why the rejection has to stay legible in the console.
    console.warn(
      `Cassini source capture: upload rejected (${response.status}); discarding this recording`,
    );
    if (await discardCapture(opfsRoot, dirName)) {
      clearUploadAttempts(dirName);
    }
    return;
  }
  if (!response.ok) {
    // The server did not decide, it failed. Leave OPFS untouched so the next
    // Talk page load can retry: losing a recording to a transient 502 would
    // defeat the point of buffering it. But give up eventually, or a
    // permanently-failing deployment turns every participant into a forever
    // re-uploader.
    if (noteUploadAttempt(dirName)) {
      console.warn(
        `Cassini source capture: giving up after ${MAX_UPLOAD_ATTEMPTS} attempts ` +
          `(last status ${response.status}); discarding this recording`,
      );
      if (await discardCapture(opfsRoot, dirName)) {
        clearUploadAttempts(dirName);
      }
      return;
    }
    throw new Error(`upload failed: ${response.status}`);
  }
  // Cleared only after the capture is actually gone. Clearing first would
  // reset the count on a deletion that failed, and the accepted capture would
  // then be re-uploaded on every page load with a counter that never advances.
  await opfsRoot.removeEntry(dirName, { recursive: true });
  clearUploadAttempts(dirName);
  console.info("Cassini source capture: upload accepted");
}

// A sealed capture found in OPFS at page load: everything a previous page
// buffered, and the manifest describing it.
interface SealedCapture {
  dirName: string;
  sidecar: CaptureSidecar;
  // release drops the exclusive claim this page took on the directory when it
  // decided to hold the capture. It is set only on a held capture, and it is
  // either handed to the capture that adopts it or called when the hold ends.
  release?: () => void;
  // highestSegmentOnDisk is the largest segment index the directory actually
  // holds a file for, which is not always the largest one the manifest names.
  // The recovery sidecar is a checkpoint, so a segment can have bytes on disk
  // before it has an entry — and a resumed capture that numbered from the
  // manifest alone would then open, and truncate, a file already full of the
  // participant's audio.
  highestSegmentOnDisk: number;
  // interrupted is true when the only manifest in the directory was the
  // worker's recovery sidecar, i.e. the page that recorded it died mid-interval
  // rather than finishing one. It is the sharpest fact available about whether
  // a buffer belongs to a recording that is still running: a page whose
  // recording STOPPED seals capture.json before it uploads, so a directory
  // holding one describes an interval that is already over whatever Talk says
  // about the room now.
  interrupted: boolean;
}

// adoptable is the sealed capture this page may CONTINUE rather than upload.
//
// A reload during a recorded call is the case this exists for, and it is the
// case the feature matters most in: people reload when the connection is bad,
// which is exactly when the recorder's copy of them is full of holes. The
// previous page left a buffer behind; uploading it here would push a
// meeting-sized body up the same bad uplink the participant is still trying to
// talk over, and would file the two halves of one recording as two captures.
// So it is held instead, and the capture this page starts adopts it as its
// leading segments — the reload becoming a segment boundary exactly like a
// mid-call microphone change.
//
// Held, never abandoned: every path out of holding either adopts it or uploads
// it. See releaseAdoptableCapture.
let adoptable: SealedCapture | null = null;
let adoptDeadline: number | null = null;

// settlingBufferedCaptures is true from install until the page has decided what
// to do with the buffers already in this browser.
//
// A capture must not start before that decision. The decision is what turns a
// reload into ONE capture, and it reads only local storage — a directory
// listing and a few small manifests — while a capture start waits on Talk
// loading its bundle, opening the microphone and negotiating. So in practice
// the decision is long since made; but "in practice" is how the reload filed
// two captures on a machine where the page was fast and the disk was not.
let settlingBufferedCaptures = false;
let deferredCaptureStart: { sender: RTCRtpSender; connection: RTCPeerConnection } | null = null;

// SETTLE_DEADLINE_MS bounds that wait. A capture start is the participant's
// audio; a storage layer that never answers must cost the adoption, not the
// recording.
const SETTLE_DEADLINE_MS = 2_000;

function finishSettling(): void {
  if (!settlingBufferedCaptures) {
    return;
  }
  settlingBufferedCaptures = false;
  const deferred = deferredCaptureStart;
  deferredCaptureStart = null;
  if (deferred !== null) {
    beginCapture(deferred.sender, deferred.connection);
  }
}

// ADOPT_MAX_AGE_MS bounds how stale a sealed capture may be and still be
// treated as this recording's own first half.
//
// The page cannot ask Talk which recording a buffer belongs to; there is no
// recording id in any of this. What it has is "the same room, the same
// participant, a page that died mid-interval, and a recording active now" —
// which a buffer from a DIFFERENT recording minutes earlier in the same room
// would also satisfy. This is measured at page load, so what it has to cover is
// the dead time of a reload rather than a whole rejoin, and a minute is far
// more than that. Getting it wrong is not silent corruption — segments carry
// their own wall-clock windows, so a wrongly adopted one is placed before this
// recording began and falls outside it — but it does withhold that audio until
// the current recording stops, which is worth not doing.
const ADOPT_MAX_AGE_MS = 60_000;

// ADOPT_DECISION_TIMEOUT_MS is how long a held capture waits for this page to
// start recording before it is uploaded after all.
//
// Holding is only ever an optimisation; uploading is always correct. So the
// deadline is generous — a participant whose microphone permission prompt is
// still open has not failed to rejoin — and its expiry costs nothing but the
// upload happening mid-call, which is what every previous build did.
const ADOPT_DECISION_TIMEOUT_MS = 60_000;

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

// readSealedCapture reads one buffered capture's manifest.
//
// capture.json first, then the recovery sidecar the worker refreshes as chunks
// land. Both are tried on CONTENT rather than on existence: a page that died
// while sealing can leave a capture.json truncated mid-JSON, and preferring a
// file that merely exists would then discard a capture whose recovery sidecar
// was perfectly good. A directory with neither is left alone — the previous
// page died before the first checkpoint, and a future repair tool has more to
// work with than an empty slot.
async function readSealedCapture(
  root: FileSystemDirectoryHandle,
  dirName: string,
): Promise<SealedCapture | null> {
  const dir = await root.getDirectoryHandle(dirName);
  let highestSegmentOnDisk = -1;
  for await (const [name] of (
    dir as unknown as { entries(): AsyncIterable<[string, FileSystemHandle]> }
  ).entries()) {
    const match = /^segment-(\d+)\.webm$/.exec(name);
    if (match) {
      highestSegmentOnDisk = Math.max(highestSegmentOnDisk, Number(match[1]));
    }
  }
  for (const name of ["capture.json", SOURCE_CAPTURE_PENDING_NAME]) {
    try {
      const file = await (await dir.getFileHandle(name)).getFile();
      const parsed: unknown = JSON.parse(await file.text());
      if (isBufferedCaptureSidecar(parsed)) {
        return {
          dirName,
          sidecar: parsed,
          highestSegmentOnDisk,
          interrupted: name === SOURCE_CAPTURE_PENDING_NAME,
        };
      }
    } catch {
      // Absent, unreadable, or half-written. Try the other one.
    }
  }
  return null;
}

// nextSegmentIndex is the first index a resumed capture may use.
//
// One past the highest the directory contains, counting BOTH the manifest and
// the files on disk, and never a count. The manifest can have holes, because
// the worker drops a segment whose file was not written completely; and it can
// under-report, because the recovery sidecar is a checkpoint and a segment can
// have bytes before it has an entry. Either way, reusing an index opens the
// file that index already names, truncates it, and destroys audio the
// participant had already recorded.
function nextSegmentIndex(sealed: SealedCapture): number {
  let highest = sealed.highestSegmentOnDisk;
  for (const segment of sealed.sidecar.segments) {
    if (Number.isInteger(segment.index) && segment.index > highest) {
      highest = segment.index;
    }
  }
  return highest + 1;
}

// captureIsAdoptable decides whether a sealed capture is this recording's own
// first half rather than a leftover to upload.
function captureIsAdoptable(sealed: SealedCapture, roomToken: string | null): boolean {
  if (roomToken === null || sealed.sidecar.roomToken !== roomToken) {
    // Another room's buffer has nothing to do with the call this page is on.
    return false;
  }
  // And another PERSON's buffer has nothing to do with whoever is signed in
  // now. Browser storage belongs to the origin, not to the session: on a shared
  // machine the buffer a colleague's dead page left behind is still sitting
  // there when the next person signs in, and adopting it would splice their
  // voice into this participant's capture and file it under this participant's
  // authenticated name. The sidecar's own claim about who recorded it is not
  // trustworthy for authorisation — the server decides that — but it is exactly
  // the right thing to compare against here, because both sides are this
  // browser's own record of who was signed in.
  const currentUser =
    (globalThis as { OC?: { getCurrentUser?: () => { uid?: string } } }).OC?.getCurrentUser?.()?.uid ?? "";
  if ((sealed.sidecar.participantId ?? "") !== currentUser) {
    return false;
  }
  if (captureAbandoned) {
    // This page will never record, so it will never adopt anything either.
    return false;
  }
  if (recordingStatusAnswered && !talkRecordingActive) {
    // Talk has already told us this room is not being recorded. Whatever the
    // buffer belongs to is over, so it uploads now — the retry path, unchanged.
    return false;
  }
  if (!sealed.interrupted) {
    // A sealed capture.json is a recording interval that ENDED — the page was
    // alive, Talk confirmed the stop, and the manifest was written before the
    // upload it then failed to deliver. Resuming one would splice a finished
    // recording onto the front of whichever recording is running now, and would
    // hold its audio in the browser while that earlier meeting's build ran
    // without it. Only a buffer whose page died mid-interval is resumable.
    return false;
  }
  const age = Date.now() - sealed.sidecar.callEndWallMs;
  return sealed.sidecar.callEndWallMs > 0 && age >= 0 && age <= ADOPT_MAX_AGE_MS;
}

// CAPTURE_LOCK_WAIT_MS is how long the page-load scan waits for another page's
// claim on a capture directory to clear.
//
// A same-tab reload releases the previous document's claim as that document is
// destroyed, which is milliseconds; a second Talk tab that is genuinely
// recording holds it for the whole meeting. So a short wait tells the two
// apart, and timing out means "leave it alone", never "take it".
const CAPTURE_LOCK_WAIT_MS = 3_000;

type LockManagerLike = {
  request(
    name: string,
    options: { mode: string; signal?: AbortSignal },
    callback: () => Promise<void>,
  ): Promise<void>;
};

// captureLocks returns the Web Locks manager, or null where there is none —
// an insecure context, or a browser without it. Without locks this file behaves
// exactly as it did before them.
function captureLocks(): LockManagerLike | null {
  const locks = (navigator as unknown as { locks?: LockManagerLike }).locks;
  if (!locks || typeof locks.request !== "function") {
    return null;
  }
  if (typeof AbortSignal === "undefined" || typeof AbortSignal.timeout !== "function") {
    return null;
  }
  return locks;
}

function captureLockName(dirName: string): string {
  return `cassini.sourceCapture.${dirName}`;
}

// claimCaptureDir runs `work` while this page exclusively claims one capture
// directory, and returns `busy` instead when another page in this browser holds
// it.
//
// Two Talk pages in one browser is ordinary — a second tab on the same
// conversation, opened while the first is in a recorded call. Without this the
// second page reads the first's LIVE recovery sidecar, which is
// indistinguishable from a sealed one, and either resumes the directory a
// recorder is writing into or uploads it and DELETES it mid-call. A claim held
// for as long as the recording runs, and released the instant its document is
// destroyed, is the one thing that separates "this buffer is finished with" from
// "this buffer is in use".
async function claimCaptureDir<T>(dirName: string, work: () => Promise<T>, busy: T): Promise<T> {
  const locks = captureLocks();
  if (locks === null) {
    return work();
  }
  let result = busy;
  try {
    await locks.request(
      captureLockName(dirName),
      { mode: "exclusive", signal: AbortSignal.timeout(CAPTURE_LOCK_WAIT_MS) },
      async () => {
        result = await work();
      },
    );
  } catch {
    return busy;
  }
  return result;
}

// claimCaptureDirUntilReleased takes a claim and hands back the function that
// drops it, or null when another page in this browser holds the directory.
//
// The difference from claimCaptureDir is who decides when the claim ends. A
// buffer held for adoption is held across a decision this page has not made yet
// — it waits for the participant to rejoin — so the claim has to span the wait,
// the adoption, the recording that follows and its upload. Releasing it after
// the READ, and taking a fresh one when the capture eventually starts, left the
// directory unclaimed for the whole of that wait: two pages loading at once
// could each hold the same buffer, each compute the same next segment index,
// and each open the same file.
function claimCaptureDirUntilReleased(dirName: string): Promise<(() => void) | null> {
  const locks = captureLocks();
  if (locks === null) {
    // Nothing to claim and nothing to release. Browsers without Web Locks get
    // the behaviour they had before them.
    return Promise.resolve(() => {});
  }
  return new Promise((resolve) => {
    let granted = false;
    void locks
      .request(
        captureLockName(dirName),
        { mode: "exclusive", signal: AbortSignal.timeout(CAPTURE_LOCK_WAIT_MS) },
        () =>
          new Promise<void>((release) => {
            granted = true;
            resolve(() => release());
          }),
      )
      .catch(() => {
        if (!granted) {
          resolve(null);
        }
      });
  });
}

// holdCaptureDir claims a directory for as long as this page records into it.
// Fire-and-forget by necessity — beginCapture is synchronous, because it runs
// inside Talk's addTrack — so the claim lands a tick later. The window that
// leaves is another page's scan arriving in that same tick, which the sealed
// state below would in any case refuse to resume.
function holdCaptureDir(dirName: string, until: Promise<void>): void {
  const locks = captureLocks();
  if (locks === null) {
    return;
  }
  void locks
    .request(captureLockName(dirName), { mode: "exclusive" }, () => until)
    .catch(() => {
      // A claim we could not take costs the protection, not the recording.
    });
}

// captureFilesArePresent checks that every segment a manifest names is still on
// disk.
//
// Only adoption asks. Uploading a manifest whose file has vanished fails and
// leaves the buffer for a later retry, which is survivable; ADOPTING one is
// not, because the missing file would be carried into the merged sidecar and
// make the whole capture — the audio recorded after the reload included —
// unuploadable for as long as it exists.
async function captureFilesArePresent(
  root: FileSystemDirectoryHandle,
  sealed: SealedCapture,
): Promise<boolean> {
  try {
    const dir = await root.getDirectoryHandle(sealed.dirName);
    for (const segment of sealed.sidecar.segments) {
      await dir.getFileHandle(segment.audioName);
    }
    return true;
  } catch {
    return false;
  }
}

// releaseAdoptableCapture uploads a held capture, because this page is not
// going to continue it: Talk says the recording is over, or nothing started
// recording before the deadline.
function releaseAdoptableCapture(): void {
  const held = adoptable;
  adoptable = null;
  clearAdoptDeadline();
  if (held === null) {
    return;
  }
  void uploadCapture(held.sidecar, held.dirName, false)
    .catch(() => {
      // Left in OPFS for the next Talk page load, exactly as any other deferred
      // upload is.
      console.warn("Cassini source capture: upload deferred; buffered audio remains in browser storage");
    })
    .finally(() => {
      // Only once the bytes are gone or given up on. The claim is what stops
      // another page in this browser touching the directory, and the upload is
      // the last thing that touches it.
      held.release?.();
    });
}

function clearAdoptDeadline(): void {
  if (adoptDeadline !== null) {
    clearTimeout(adoptDeadline);
    adoptDeadline = null;
  }
}

// settleBufferedCaptures decides what happens to every capture left in this
// browser's storage, and is the only thing that reads that storage at page
// load.
//
// A navigation is allowed to cancel an upload, and a reload is not even asked
// to attempt one, so completed captures routinely outlive the page that made
// them. Each is either uploaded now or held for this page's own capture to
// adopt; nothing is left undecided.
export async function settleBufferedCaptures(): Promise<number> {
  if (serverAllowsCapture !== true) {
    return 0;
  }
  const root = await navigator.storage.getDirectory();
  // The listing is snapshotted BEFORE anything is uploaded, and that ordering
  // is load-bearing. Uploading is slow, this page starts its own capture in
  // the same seconds, and a live capture directory enumerated by a scan still
  // in progress would be uploaded half-written and then DELETED out from under
  // the worker recording into it. A snapshot cannot see a directory that did
  // not exist when the page loaded.
  const names: string[] = [];
  for await (const [dirName, handle] of root.entries()) {
    if (handle.kind === "directory" && dirName.startsWith("capture-")) {
      names.push(dirName);
    }
  }
  // Every manifest is read before anything is decided, and read WITHOUT a
  // claim. Reading is local, fast and harmless — even of a directory another
  // page is recording into — and it has to finish before the decision, because
  // which capture is held cannot depend on the order OPFS happened to list them
  // in. Waiting on a lock here would also put a network-scale wait in front of
  // a capture start, which is the one thing this scan must never do.
  const sealed: SealedCapture[] = [];
  for (const dirName of names) {
    // Belt and braces against the snapshot above: never touch the directory a
    // capture is recording into, or one already held for adoption.
    if (state?.dirName === dirName || adoptable?.dirName === dirName) {
      continue;
    }
    try {
      const found = await readSealedCapture(root, dirName);
      if (found !== null) {
        sealed.push(found);
      }
    } catch {
      // Absent or unreadable. A directory with no usable manifest is left
      // alone: the page that made it died before its first checkpoint, and a
      // future repair tool has more to work with than an empty slot.
    }
  }
  const roomToken = roomTokenFromPath(location.pathname);
  // Exactly one capture is ever held, and it is the FRESHEST one this room can
  // resume. An older buffer for the same room belongs to an earlier recording,
  // not to the front of this one.
  if (adoptable === null && state === null) {
    const candidates = sealed
      .filter((candidate) => captureIsAdoptable(candidate, roomToken))
      .sort((a, b) => b.sidecar.callEndWallMs - a.sidecar.callEndWallMs);
    for (const candidate of candidates) {
      // The claim is taken HERE and held until the capture that adopts it is
      // uploaded, or until the hold ends. A directory another page holds is
      // not this page's to resume — that page is recording into it — and it is
      // left entirely alone rather than tried again below.
      //
      // This is the one wait that can outlast the settle deadline, and only
      // when another page really is holding the directory. A capture then
      // starts fresh instead of resuming, which is the right answer for that
      // case anyway: there is nothing here for this page to continue. On a
      // reload the previous document is already gone, so the claim is granted
      // at once.
      const release = await claimCaptureDirUntilReleased(candidate.dirName);
      if (release === null) {
        continue;
      }
      if (!(await captureFilesArePresent(root, candidate))) {
        // A manifest naming a file that is gone is not resumable: the missing
        // file would be carried into the merged sidecar and make the whole
        // capture unuploadable. Let the loop below offer it instead, where a
        // failure costs only this capture.
        release();
        continue;
      }
      adoptable = { ...candidate, release };
      clearAdoptDeadline();
      adoptDeadline = setTimeout(() => {
        console.info("Cassini source capture: nothing resumed the buffered capture; uploading it");
        releaseAdoptableCapture();
      }, ADOPT_DECISION_TIMEOUT_MS) as unknown as number;
      break;
    }
  }
  // Remembered rather than re-read from `adoptable` below. Each upload is a
  // network round trip, and beginCapture can adopt the held capture during one
  // of them — which clears `adoptable` and would let a later iteration upload
  // the directory a recorder is now writing into, and then DELETE it on
  // success. The name is decided once, here.
  const heldDirName = adoptable?.dirName ?? null;
  // Everything a capture start has to wait for is decided by here; the uploads
  // below are network work that a recorder must not be held behind.
  finishSettling();
  let uploaded = 0;
  for (const candidate of sealed) {
    // state is re-read every iteration for the same reason from the other
    // side: whichever way the race falls, a live capture's directory is never
    // uploaded out from under it.
    if (candidate.dirName === heldDirName || candidate.dirName === state?.dirName) {
      continue;
    }
    try {
      const sent = await claimCaptureDir(
        candidate.dirName,
        async () => {
          await uploadCapture(candidate.sidecar, candidate.dirName, false);
          return true;
        },
        false,
      );
      if (sent) {
        uploaded += 1;
      }
    } catch {
      // Left in place so a later page load can retry; nothing is lost to a
      // transient upload failure.
    }
  }
  return uploaded;
}

// finishCapture ends the current recording interval. Talk's confirmed
// recording-off event calls it while the participant remains in the room; peer
// close and page teardown call it with callEnded=true as an idempotent
// fallback. `disposition` says what happens to the buffer afterwards:
//
//   "upload"     seal the manifest and send it — a recording that stopped, or a
//                participant who left the room while the page lives on.
//   "leave-it"   stop the recorder, close the segment so the recovery sidecar
//                describes every byte on disk, and do NOTHING else. This is the
//                page teardown path, and each of the three omissions is
//                deliberate. An upload started during unload cannot finish, and
//                whether it happened to land would decide whether the next page
//                found anything to resume. Sealing capture.json cannot finish
//                either, and a HALF-finished seal is worse than none: it
//                replaces the recovery sidecar with an older manifest, which a
//                later page would then prefer — and a second reload would reuse
//                a segment index the first one had already written, truncating
//                that file.
async function finishCapture(callEnded: boolean, disposition: "upload" | "leave-it" = "upload"): Promise<void> {
  if (callEnded) {
    talkRecordingActive = false;
    const idleWorker = preparedWorker;
    capturingConnection = null;
    capturingSender = null;
    // Stops the worker only if its transform is not on a sender that can still
    // send. pagehide reaches here with the connection still up, and a page on
    // its way out has nothing to gain from stopping it a moment early.
    releaseTimingWorker(idleWorker);
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
  if (disposition === "leave-it") {
    if (active.discarded) {
      // The administrator switched collection off during this call. That is
      // terminal for the recording, and the buffer must not outlive the page
      // to be uploaded by a later one that finds the switch back on.
      const opfsRoot = await navigator.storage.getDirectory();
      await discardCapture(opfsRoot, active.dirName);
    }
    active.releaseDirClaim();
    retireSessionWorker(active.worker);
    return;
  }
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
      // failed. There will be no "finalized" to finish this interval, so the
      // worker is retired here instead. Retired, not terminated: a full disk
      // during a call that is still running must not cost the participant
      // their audio, and the worker has reset its own interval state and can
      // serve the next one.
      active.releaseDirClaim();
      retireSessionWorker(active.worker);
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
        // The official recording may have stopped while the call continues.
        // Keeping the transform's pass-through worker alive is then the whole
        // point: stopping Cassini must not interrupt Talk's outgoing audio.
        // The worker reset after finalize and can be reused if recording
        // starts again.
        active.releaseDirClaim();
        retireSessionWorker(active.worker);
      });
  };
  active.worker.postMessage({ type: "finalize", dirName: active.dirName, base });
}

async function endCall(): Promise<void> {
  await finishCapture(true);
}

// endPage is the teardown a navigation gets: close the segment so the recovery
// sidecar describes every byte on disk, and leave everything else to whichever
// page loads next. See finishCapture's "leave-it" disposition for why it
// neither seals nor uploads.
async function endPage(): Promise<void> {
  await finishCapture(true, "leave-it");
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
      // Re-checked HERE, not only when the rotation was queued. A session the
      // administrator switch discarded must not start recording again because
      // the participant happened to change microphone afterwards; the upload
      // was already blocked, but collection has to stop too.
      if (session.discarded || session.finished) {
        return;
      }
      // And the in-call condition, because a rotation opens a NEW recorder and
      // every one of them has to answer for itself. `discarded` and `finished`
      // above already carry the other two — the administrator switch marks the
      // session discarded, and Talk's recording stopping finishes it — but
      // nothing marked the session when the participant simply stopped being in
      // the call, so a microphone replaced after the connection went away would
      // open a recorder on a page that is no longer in the meeting.
      if (session.connection && !senderIsInTheCall(sender, session.connection)) {
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
  if (allowed) {
    return;
  }
  if (state && !state.discarded) {
    state.discarded = true;
    stopWithoutRestart(state);
  }
  // A buffer merely held for adoption is just as much this call's audio, and
  // the switch being off has to mean nothing from that stretch is kept. This
  // deletes it rather than uploading it: releaseAdoptableCapture goes through
  // uploadCapture, which discards outright while collection is off.
  releaseAdoptableCapture();
}

// senderIsInTheCall is the whole of "this participant is in the call".
//
// Talk's confirmed recording says the ROOM is being recorded; it says nothing
// about whether this browser is in it. The publishing connection reaching
// `connected` is what does, and it is the only signal here that separates
// somebody who is in the meeting from somebody whose page has loaded, whose
// microphone Talk has opened, and who has not joined — the state where a
// participant reasonably believes nothing they say is being recorded.
//
// So the transform still attaches inside addTrack, because the platform ignores
// one attached later, and it stays a pass-through; the RECORDER waits. The cost
// is the negotiation's worth of audio at the very start of a call, which is
// before anybody is talking, and instrument()'s connectionstatechange starts
// the capture the moment the connection comes up.
function senderIsInTheCall(sender: RTCRtpSender, connection: RTCPeerConnection): boolean {
  return connection.connectionState === "connected" && sender.track?.readyState === "live";
}

function beginCapture(sender: RTCRtpSender, connection: RTCPeerConnection): void {
  if (state || !talkRecordingActive) {
    return;
  }
  if (!senderIsInTheCall(sender, connection)) {
    // Not in the call yet, or not any more. Every path that can change that —
    // the connection coming up, a track being replaced, Talk confirming its
    // recording — calls back here.
    return;
  }
  if (settlingBufferedCaptures) {
    // The page has not finished deciding what happens to the buffers already
    // in this browser. Starting now would file a reload as a second capture
    // purely because the storage read had not landed yet. finishSettling
    // calls back here.
    deferredCaptureStart = { sender, connection };
    return;
  }
  const roomToken = roomTokenFromPath(location.pathname);
  if (!roomToken) {
    return;
  }
  talkRoomToken = roomToken;
  // The administrator switch was injected before this script ran. Anything
  // other than an explicit yes means no recorder or OPFS directory is created.
  if (serverAllowsCapture !== true) {
    return;
  }
  // Usually a no-op: install prepared a worker before Talk loaded. It is not
  // one when an earlier worker was retired, so ask for one rather than assume.
  prepareTimingWorker();
  const worker = preparedWorker;
  if (worker === null) {
    // The worker is the storage as well as the timing. Without one there is
    // nothing to record into.
    return;
  }
  preparedWorker = null;
  // Usually a no-op: watchSender attached the transform inside addTrack. This
  // covers a session that begins on a sender which had no worker to attach to
  // then — the platform ignores a transform attached this late, so that capture
  // simply carries no anchors.
  attachTimingTransform(worker, sender, connection);
  // Adoption. A buffered capture held for this room becomes the leading
  // segments of the one starting now, rather than a second capture of the same
  // recording: same directory, same call start, segment numbering continuing
  // where the previous page stopped. The reload is then a segment boundary,
  // which is a seam this pipeline already understands — it is what a mid-call
  // microphone change produces — and the gap between the two is a stretch the
  // participant genuinely was not in the call for.
  //
  // Consumed unconditionally, whether or not it is used, so that nothing can
  // hold a buffer past the capture it was waiting for.
  const inherited = adoptable;
  adoptable = null;
  clearAdoptDeadline();
  const adopted =
    inherited !== null && inherited.sidecar.roomToken === roomToken ? inherited : null;
  if (inherited !== null && adopted === null) {
    // Held for a room this capture is not for. Upload it rather than drop it,
    // and give the directory back once those bytes are gone.
    void uploadCapture(inherited.sidecar, inherited.dirName, false)
      .catch(() => {})
      .finally(() => inherited.release?.());
  }
  const callStartWallMs = adopted?.sidecar.callStartWallMs ?? Date.now();
  // An adopted capture already comes with this page's claim on its directory,
  // taken when the buffer was held. It is handed over rather than re-taken:
  // asking for the same lock again would queue behind the one this page is
  // still holding and never be granted.
  let releaseDirClaim = adopted?.release ?? ((): void => {});
  let dirClaimed: Promise<void> | null = null;
  if (adopted === null) {
    dirClaimed = new Promise<void>((resolve) => {
      releaseDirClaim = resolve;
    });
  }
  const session: CaptureState = {
    releaseDirClaim,
    connection,
    roomToken,
    dirName: adopted?.dirName ?? captureDirName(roomToken, callStartWallMs),
    callStartWallMs,
    worker,
    segmentIndex: adopted === null ? 0 : nextSegmentIndex(adopted),
    recorder: null,
    muteIntervals: [],
    muteSince: null,
    mutePoll: null,
    segmentStartWallMs: Date.now(),
    finished: false,
    discarded: false,
    pendingChunks: Promise.resolve(),
    rotation: Promise.resolve(),
  };
  state = session;
  capturingSender = sender;
  capturingConnection = connection;
  if (dirClaimed !== null) {
    holdCaptureDir(session.dirName, dirClaimed);
  }
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
    // The segments the previous page left in this directory. Their files are
    // already there; the worker carries them into every sidecar it writes from
    // here on, so a second reload inherits both stints rather than only the
    // last.
    adopted: adopted?.sidecar.segments ?? [],
  });
  worker.postMessage({ type: "timing-active", active: true });
  startSegment(session, sender);
  console.info(
    adopted === null
      ? "Cassini source capture: Talk recording active; local source recording started"
      : `Cassini source capture: resuming the buffered capture of this recording (${adopted.sidecar.segments.length} segment(s) carried over)`,
  );
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
    if (serverAllowsCapture) {
      prepareTimingWorker();
      if (preparedWorker !== null) {
        attachTimingTransform(preparedWorker, sender, connection);
      }
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
  // Any of these is an answer, including the ones that change nothing.
  recordingStatusAnswered = true;
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
    // Nothing left to adopt a buffered capture into: the recording it belongs
    // to is over. This is the retry path a reload without a rejoin ends on.
    releaseAdoptableCapture();
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
      return;
    }
    if (pc.connectionState === "connected" && capturingSender) {
      // The participant is now in the call. If Talk's recording was already
      // active, this is where capture starts: beginCapture refused while the
      // connection was still negotiating.
      beginCapture(capturingSender, pc);
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
  // Before the enabled check, deliberately. An installation that has since
  // switched capture off must still forget an answer an older build recorded,
  // and this is the one function every Talk call page reaches.
  forgetLegacyConsent();
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
  settlingBufferedCaptures = true;
  const settleDeadline = setTimeout(finishSettling, SETTLE_DEADLINE_MS);
  void settleBufferedCaptures()
    .catch(() => {})
    .finally(() => {
      clearTimeout(settleDeadline);
      finishSettling();
    });
  installTalkRecordingLifecycle();
  // Here rather than at addTrack, because the readiness deadline runs from this
  // line and a participant is mute for whatever is left of it. Talk still has
  // to load its bundle, mount, and ask for the microphone before the first
  // addTrack — far longer than a few kilobytes of worker takes to start — so a
  // broken worker is normally caught before the call has negotiated, and the
  // deadline never costs anybody a spoken word.
  prepareTimingWorker();
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
  window.addEventListener("pagehide", () => void endPage());
}

if (typeof window !== "undefined") {
  try {
    // A user can reach Talk before revisiting Cassini after the migration.
    // Retire the abandoned bundle-rewriting worker here as well; this does not
    // delay installation or negotiation.
    void retireLegacyCaptureWorkers(navigator.serviceWorker).catch(() => {});
    // Also here, not only in install: with the switch off the initial state
    // says disabled and install is never called, and that browser has just as
    // much of an old answer to forget as one on an installation still running
    // the feature.
    forgetLegacyConsent();
    const config = captureDeliveryFromInitialState();
    if (config.enabled) {
      install(config);
    }
  } catch {
    // Talk loads normally whatever happens here.
  }
}
