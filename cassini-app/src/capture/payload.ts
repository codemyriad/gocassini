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
import { transferCapture } from "./transfer";
import { forgetLegacyConsent, retireLegacyCaptureWorkers } from "./register";

import {
  SOURCE_CAPTURE_FORMAT,
  SOURCE_CAPTURE_PENDING_NAMES,
  captureDirName,
  roomTokenFromPath,
  type CaptureSidecar,
} from "./protocol";

const INITIAL_STATE_APP = "cassini_capture";
const INITIAL_STATE_KEY = "capture";
let immutableTransferEnabled = true;
let recordingIdentity: { room: string; id: string } | null = null;
let identityPending = false;
let identityEpoch = 0;
let sealingWorker: Worker | null = null;
const uploadRetries = new Map<string, { attempts: number; after: number }>();
let scanningBuffers = false;
let leftCall = false;

function deferBufferedUpload(dirName: string): void {
  const attempts = (uploadRetries.get(dirName)?.attempts ?? 0) + 1;
  uploadRetries.set(dirName, { attempts, after: Date.now() + Math.min(300_000, 5_000 * 2 ** Math.min(attempts, 6)) });
}

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

// PRESERVED_UPLOAD_STATUSES are refusals that must NOT count towards the
// attempt cap.
//
//   409 the server holds a different capture for this call and neither
//       contains the other, so it will not take this one and will not throw
//       the stored one away either
//
// The cap exists to stop a permanently-failing deployment re-offering a
// meeting-sized body forever, and giving up is the right end for that. It is
// the wrong end for this: the server refused precisely BECAUSE this buffer
// holds audio nothing else has, and counting those refusals would delete it on
// the fifth page load — the client destroying exactly what the server declined
// to destroy.
const PRESERVED_UPLOAD_STATUSES = new Set([409]);

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
    const body = (await response.json()) as { enabled?: unknown; uploadProtocol?: unknown };
    immutableTransferEnabled = body.uploadProtocol === 2;
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
  recordingId?: string;
  sessionId?: string;
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
// unloadStartedAtMs is when this document last began to unload.
//
// Which teardown runs first is Talk's business, not ours: it may close the
// publishing connection inside its own unload handler, and pc.close reaches
// endCall before pagehide ever fires. Without knowing, that ordering decided
// whether a reload sealed-and-uploaded or sealed-only — and a finalize that
// happened to complete wrote capture.json, which the next page will not resume.
// The listener is registered at install, before Talk's bundle loads, so it runs
// before Talk's own.
//
// A timestamp rather than a flag, because beforeunload is not a promise that
// the page is going: another handler can prompt and the participant can cancel,
// and a latched flag would then make every later leave seal without uploading —
// stranding a capture in a browser whose page never went anywhere. A real
// unload is over in milliseconds; a cancelled one leaves this behind and it
// expires.
let unloadStartedAtMs = 0;

// UNLOAD_WINDOW_MS is how long after beforeunload the document is still
// presumed to be leaving.
const UNLOAD_WINDOW_MS = 5_000;

function pageIsGoingAway(): boolean {
  return unloadStartedAtMs > 0 && Date.now() - unloadStartedAtMs < UNLOAD_WINDOW_MS;
}
let talkRecordingActive = false;
// talkRecordingRoom is the room talkRecordingActive is an answer ABOUT.
//
// Talk navigates between conversations without reloading, and both the socket
// event and the room request are answered per room; the flag they set was not.
// A participant leaving a recorded room for one that is not being recorded —
// the second room's sender arriving before the second room's status does —
// would have started a capture on the first room's answer.
let talkRecordingRoom: string | null = null;
// recordingStatusAnsweredFor distinguishes "Talk says this room is not being
// recorded" from "Talk has not answered about this room yet". Only the first is
// a reason to stop holding a buffered capture for adoption; treating the second
// as a no would upload every reloader's buffer a round trip before the answer
// that says to keep it arrives.
//
// It names a ROOM rather than being a flag, because Talk changes conversation
// without reloading: an answer about the room just left is not an answer about
// this one, and treating it as one left a page that failed its single bootstrap
// request in the new room believing the question had been settled.
function recordingStatusAnswered(roomToken: string | null): boolean {
  return roomToken !== null && talkRecordingRoom === roomToken;
}
let recordingStatusRevision = 0;
// recordingStatusFetchRoom is the room a status request is in flight FOR, or
// null when none is. Scoped for the same reason as the answer: a request still
// running for the conversation just left must not make the new one look like it
// has already been asked.
let recordingStatusFetchRoom: string | null = null;
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
// capturingRoom is the room capturingSender was seen in. Talk navigates between
// conversations without reloading, and the sender of the room being left can
// still be connected when the room being entered confirms its recording — so
// "a connected sender" and "this room is being recorded" can both be true of
// two different rooms at once, and capture would have started on the wrong
// microphone and filed it under the wrong conversation.
let capturingRoom: string | null = null;

function workerURL(): string {
  return `${deliveryConfig.proxyBase}/ui/capture-worker.js`;
}

export function startSegment(session: CaptureState, sender: RTCRtpSender): void {
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
  // Stamped after start() returned, and the worker is told after that.
  //
  // This segment's window is what the server measures the uploaded file
  // against, and the file holds nothing from before the recorder was running.
  // Constructing the MediaRecorder and starting it is the page's own
  // bookkeeping, and a window that included it declared audio that was never
  // recorded.
  //
  // Taken here the stamp is a shade INSIDE the recording rather than outside
  // it: start() begins gathering data and returns, so the file's first sample
  // can predate this line by however long the return and this call take — one
  // statement, well under a millisecond. That direction is the one to watch,
  // because a window narrower than the audio is what would let a truncated
  // upload past the server's check, and it is why the stamp is not moved any
  // later than this. Against the tenth of the window that check allows, a
  // statement is nothing; the 0.4-1.7 s this replaces was not. See stopSegment
  // for the other end and for why nothing here is derived from the recording.
  //
  // Announcing the segment after start() also means a MediaRecorder that throws
  // on construction leaves no open file handle behind in the worker. The first
  // chunk cannot arrive for another TIMESLICE_MS and worker messages are applied
  // in the order they were posted, so the file is open long before it is
  // written to.
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
  // The instant the recorder is asked to stop, taken BEFORE asking, is the last
  // instant this segment's file can hold audio for. It is NOT the instant this
  // function finishes: the two awaits below are onstop and the whole outstanding
  // chunk hand-off chain, and stamping the segment's end after them declared
  // between 0.4 and 1.7 s of stopping as if it were recording. On a segment of
  // ten or twenty seconds that is more than the tenth the server allows between
  // what a segment declares and what it decodes to, so a departure tail was
  // refused although nothing had gone wrong — leaving the seconds around
  // somebody leaving a call as the ones least likely to come from their own
  // microphone.
  //
  // The awaits stay exactly where they are: they are what makes the FILE
  // complete, and posting segment-stop before them would have the worker close
  // the handle with the tail of the recording still in flight.
  //
  // Why not something closer to the audio itself. The obvious candidate is the
  // outgoing encoded frames the timing worker already sees, which would give the
  // instant the microphone last produced to within one 20 ms frame. It is the
  // wrong witness: those frames come from a different pipeline. Talk mutes with
  // `enabled = false`, and a disabled track delivers silence to every sink — the
  // recorder keeps writing it into the file while the sender may stop producing
  // frames for it, and Opus DTX and a renegotiation do the same. A window ending
  // at the last frame would then be NARROWER than the audio, which is a worse
  // failure than the one being fixed: an over-declared window costs a splice and
  // leaves the recorded track, while an under-declared one makes the decoded
  // fraction agree with a truncated upload and the check stops catching
  // anything.
  //
  // These two stamps are wrong in the same direction instead, and by an amount
  // that is a statement rather than a wait. start() returns after gathering has
  // begun and stop() is called after this line, so the window sits a shade
  // INSIDE the recording at both ends — sub-millisecond at each, against the
  // tenth of the window the server's check allows. It is worth naming because
  // that is the dangerous direction: a window narrower than the audio makes the
  // decoded fraction agree with a truncated upload. What makes it safe is the
  // size, and the fact that neither stamp is measured from the recording — a
  // chunk lost on the way to storage, a short write, or an upload cut off in
  // flight moves the file by seconds and the window by nothing. What left the
  // window is the stopping, which was never audio.
  let stopRequestedWallMs = Date.now();
  await new Promise<void>((resolve) => {
    recorder.onstop = () => resolve();
    try {
      stopRequestedWallMs = Date.now();
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
    stopWallMs: stopRequestedWallMs,
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
const storageWorkers = new WeakMap<Worker, Worker>();

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
    // Start both workers from the page: AppAPI worker responses can forbid
    // nested workers through their own CSP. A channel keeps disk work off
    // both the page and the outgoing-frame worker.
    const storage = new Worker(`${deliveryConfig.proxyBase}/ui/capture-storage-worker.js`);
    const channel = new MessageChannel();
    storage.postMessage({ type: "storage-port" }, [channel.port1]);
    worker.postMessage({ type: "storage-port" }, [channel.port2]);
    storage.onerror = () => abandonCapture(worker, "the storage worker failed to start");
    storageWorkers.set(worker, storage);
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
  storageWorkers.get(worker)?.terminate();
  storageWorkers.delete(worker);
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
  // The in-call condition, enforced continuously rather than only when a
  // recorder is constructed. A MediaRecorder holds its own MediaStream over the
  // track, so nothing about the sender leaving the call reaches it by itself:
  // the connection dropping, or Talk taking the sender off the connection
  // entirely, would leave it recording a participant the room can no longer
  // hear. This poll already runs, already has the sender and the session, and
  // is the one place that can notice. resumeSegment starts it again if the
  // participant comes back.
  if (session.recorder !== null && session.connection && !senderIsInTheCall(sender, session.connection)) {
    console.info("Cassini source capture: no longer sending to the call; recording paused");
    stopWithoutRestart(session);
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
    await markCaptureRevoked(opfsRoot, dirName);
    await discardCapture(opfsRoot, dirName);
    return;
  }
  const dir = await opfsRoot.getDirectoryHandle(dirName);
  if (sidecar.recordingId && sidecar.sessionId) {
    if (sidecar.participantId !== currentParticipantId()) throw new Error("capture belongs to another account");
    try {
      await transferCapture(deliveryConfig.proxyBase, sidecar,
        async (name) => (await dir.getFileHandle(name)).getFile(),
        () => serverAllowsCapture === true && !revokedDuringCall,
        (globalThis as { OC?: { requestToken?: string } }).OC?.requestToken ?? "");
    } catch (error) {
      deferBufferedUpload(dirName);
      throw error;
    }
    await opfsRoot.removeEntry(dirName, { recursive: true });
    uploadRetries.delete(dirName);
    console.info("Cassini source capture: upload accepted");
    return;
  }
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
    await markCaptureRevoked(opfsRoot, dirName);
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
  if (PRESERVED_UPLOAD_STATUSES.has(response.status)) {
    // Kept, and deliberately not counted. See PRESERVED_UPLOAD_STATUSES.
    console.warn(
      `Cassini source capture: the server holds a different capture for this call (${response.status}); ` +
        "keeping this one in browser storage",
    );
    throw new Error(`upload deferred: ${response.status}`);
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
  if (await captureIsRevoked(dir)) {
    // The administrator switched collection off during this capture. That is
    // terminal for the interval, and the marker is what makes it terminal even
    // if the delete that should have followed did not land.
    await root.removeEntry(dirName, { recursive: true }).catch(() => {});
    return null;
  }
  const read = async (name: string): Promise<CaptureSidecar | null> => {
    try {
      const file = await (await dir.getFileHandle(name)).getFile();
      const parsed: unknown = JSON.parse(await file.text());
      return isBufferedCaptureSidecar(parsed) ? parsed : null;
    } catch {
      // Absent, unreadable, or half-written.
      return null;
    }
  };
  const sealed = await read("capture.json");
  if (sealed !== null) {
    return { dirName, sidecar: sealed };
  }
  // Otherwise the newest recovery generation that parses. Both slots are tried
  // and the later call end wins: a checkpoint writes into the slot the previous
  // one is not in, so a page that died mid-write leaves one torn slot and one
  // whole older one, and taking the whole one costs a checkpoint interval
  // rather than the entire capture.
  let best: CaptureSidecar | null = null;
  for (const name of SOURCE_CAPTURE_PENDING_NAMES) {
    const candidate = await read(name);
    if (candidate !== null && (best === null || candidate.callEndWallMs > best.callEndWallMs)) {
      best = candidate;
    }
  }
  if (best === null) {
    return null;
  }
  return { dirName, sidecar: best };
}

// CAPTURE_REVOKED_MARKER names a capture the administrator switch turned off
// mid-interval.
//
// The switch being off has to mean nothing from that stretch is kept, and the
// deletion that enforces it can fail — a transient OPFS error, a handle that
// has not been released yet. Without a durable record of the refusal, that one
// failure is all it takes for the audio to be uploaded by a later page that
// finds the switch back on. The marker lives inside the capture directory, so
// it says nothing about anybody that the directory did not already say, and it
// is deleted with it.
const CAPTURE_REVOKED_MARKER = "capture.revoked";

async function captureIsRevoked(dir: FileSystemDirectoryHandle): Promise<boolean> {
  try {
    await dir.getFileHandle(CAPTURE_REVOKED_MARKER);
    return true;
  } catch {
    return false;
  }
}

// markCaptureRevoked records the refusal before attempting the deletion, so a
// deletion that fails still leaves the interval refused.
async function markCaptureRevoked(
  opfsRoot: FileSystemDirectoryHandle,
  dirName: string,
): Promise<void> {
  try {
    const dir = await opfsRoot.getDirectoryHandle(dirName);
    const handle = await dir.getFileHandle(CAPTURE_REVOKED_MARKER, { create: true });
    const writable = await (handle as unknown as { createWritable(): Promise<{ close(): Promise<void> }> })
      .createWritable();
    await writable.close();
  } catch {
    // The directory is already gone, or the marker cannot be written. The
    // deletion below is still attempted; this is the belt, not the braces.
  }
}

function currentParticipantId(): string {
  return (
    (globalThis as { OC?: { getCurrentUser?: () => { uid?: string } } }).OC?.getCurrentUser?.()?.uid ?? ""
  );
}

async function revokeBufferedRecording(recordingId: string): Promise<void> {
  const root = await navigator.storage.getDirectory();
  for await (const [name, handle] of (root as unknown as { entries(): AsyncIterable<[string, FileSystemHandle]> }).entries()) {
    if (handle.kind !== "directory" || !name.startsWith("capture-")) continue;
    const capture = await readSealedCapture(root, name).catch(() => null);
    if (capture?.sidecar.recordingId !== recordingId || !captureIsThisParticipants(capture)) continue;
    await markCaptureRevoked(root, name);
    await discardCapture(root, name);
  }
}

// captureIsThisParticipants reports whether a buffer was recorded by whoever is
// signed in now.
//
// Browser storage belongs to the ORIGIN, not to the session. On a shared
// machine the buffer a colleague's dead page left behind is still sitting there
// when the next person signs in, and this page must neither resume it nor
// upload it: the operator stamps the AUTHENTICATED caller as the owner, so
// offering somebody else's audio here files their voice under this
// participant's name, where it can be spliced onto this participant's track and
// read by the room as theirs.
//
// It is left alone instead. That is a buffer stranded in this browser until its
// own account next opens Talk on this machine, which is a cost paid in storage
// on their own disk — and the alternative is a person's speech published as
// somebody else's.
//
// A sidecar that names nobody is nobody's to offer either. It was tempting to
// let those through — an older build, or a page whose Talk globals were not
// ready, records an empty id, and stranding them costs their owner an upload.
// But the server stamps the authenticated caller regardless of what the sidecar
// says, so "we do not know whose this is" and "send it as this person's" cannot
// both be right, and the second publishes a person's speech under another name.
// Every capture this build writes carries an id, because OC.getCurrentUser is
// there on any Talk call page; what is refused here is the rare one that does
// not, and it is refused rather than misattributed.
function captureIsThisParticipants(sealed: SealedCapture): boolean {
  const claimed = (sealed.sidecar.participantId ?? "").trim();
  return claimed !== "" && claimed === currentParticipantId();
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

// Scan only idle buffers, under their Web Lock. A reload always writes a new
// directory; no file or manifest from the previous document is extended.
export async function settleBufferedCaptures(): Promise<number> {
  const room = roomTokenFromPath(location.pathname);
  if (scanningBuffers || serverAllowsCapture !== true || (talkRecordingActive && !leftCall) || (room !== null && !recordingStatusAnswered(room))) return 0;
  scanningBuffers = true;
  try {
    const root = await navigator.storage.getDirectory();
    const names: string[] = [];
    for await (const [name, handle] of (root as unknown as { entries(): AsyncIterable<[string, FileSystemHandle]> }).entries()) {
      if (handle.kind === "directory" && name.startsWith("capture-")) names.push(name);
    }
    let uploaded = 0;
    for (const name of names) {
      if (name === state?.dirName || (talkRecordingActive && !leftCall) || Date.now() < (uploadRetries.get(name)?.after ?? 0)) continue;
      const sent = await claimCaptureDir(name, async () => {
        if (name === state?.dirName || (talkRecordingActive && !leftCall)) return false;
        const sealed = await readSealedCapture(root, name);
        if (sealed === null || !captureIsThisParticipants(sealed)) return false;
        await uploadCapture(sealed.sidecar, name, false);
        return true;
      }, false);
      if (sent) uploaded++;
    }
    return uploaded;
  } finally { scanningBuffers = false; }
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
    leftCall = true;
    recordingIdentity = null;
    identityEpoch++;
    talkRecordingActive = false;
    const idleWorker = preparedWorker;
    capturingConnection = null;
    capturingSender = null;
    capturingRoom = null;
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
  sealingWorker = active.worker;
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
    if (active.discarded || serverAllowsCapture !== true) {
      // The administrator switched collection off during this call. That is
      // terminal for the recording, and the buffer must not outlive the page
      // to be uploaded by a later one that finds the switch back on. The marker
      // goes down first, so a delete that fails still leaves it refused.
      //
      // serverAllowsCapture as well as the session's own flag: a permission
      // answer that arrived after this teardown had already detached the
      // session from the global had nothing left to mark, and the switch being
      // off is the fact that matters either way.
      const opfsRoot = await navigator.storage.getDirectory();
      await markCaptureRevoked(opfsRoot, active.dirName);
      await discardCapture(opfsRoot, active.dirName);
    }
    active.releaseDirClaim();
    retireSessionWorker(active.worker);
    sealingWorker = null;
    return;
  }
  const base: Omit<CaptureSidecar, "segments"> = {
    recordingId: active.recordingId,
    sessionId: active.sessionId,
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
      active.worker.onmessage = null;
      sealingWorker = null;
      return;
    }
    if (event.data?.type !== "finalized") {
      return;
    }
    active.worker.onmessage = null;
    retireSessionWorker(active.worker);
    sealingWorker = null;
    // The worker is free as soon as storage is sealed. Upload latency must
    // not delay the next recording interval on the same live sender.
    if (talkRecordingActive && capturingSender && capturingConnection) {
      beginCapture(capturingSender, capturingConnection);
      active.releaseDirClaim();
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
        void settleBufferedCaptures().catch(() => {});
      });
  };
  active.worker.postMessage({ type: "finalize", dirName: active.dirName, base });
}

async function endCall(): Promise<void> {
  // "leave-it" once the document is on its way out, whoever noticed first.
  await finishCapture(true, pageIsGoingAway() ? "leave-it" : "upload");
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

// resumeSegment reopens a segment the in-call gate declined.
//
// rotateSegment closes the old recorder BEFORE it decides whether to open the
// next one, so a microphone replaced while the connection was down — an ICE
// restart, Talk rebuilding its media pipeline — left the session alive with no
// recorder. Nothing restarted it: beginCapture returns immediately while a
// session exists, so capture stayed dead for the rest of the meeting. That is a
// worse outcome than the one the gate exists to prevent.
//
// The index is already correct: rotateSegment's stopSegment advanced it before
// declining, so this opens the next segment rather than reopening the last.
// Serialized on the same rotation chain, and gated afresh, so a connection that
// comes back after the call is really over still starts nothing.
function resumeSegment(session: CaptureState, sender: RTCRtpSender, connection: RTCPeerConnection): void {
  session.rotation = session.rotation
    .then(() => {
      if (session.discarded || session.finished || session.recorder !== null) {
        return;
      }
      if (!talkRecordingActive || serverAllowsCapture !== true) {
        return;
      }
      // This session's OWN connection and room, not merely a connected sender
      // and a confirmed recording somewhere. Talk changes conversation without
      // reloading, so a new room's connection coming up would otherwise resume
      // the previous room's capture: a recorder on room B's microphone,
      // started before B confirmed anything, writing into A's directory.
      if (connection !== session.connection || talkRecordingRoom !== session.roomToken) {
        return;
      }
      if (!senderIsInTheCall(sender, connection)) {
        return;
      }
      startSegment(session, sender);
      console.info("Cassini source capture: back in the call; recording resumed");
    })
    .catch(() => {
      // A failed resume costs this segment, not the recording.
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
    const revoked = state;
    revoked.discarded = true;
    if (revoked.recordingId) void revokeBufferedRecording(revoked.recordingId).catch(() => {});
    stopWithoutRestart(revoked);
    // Marked now rather than at teardown. Everything from here on is refused
    // whatever happens to this page next, including a delete that fails and a
    // page that dies before it can try again.
    revoked.rotation = revoked.rotation
      .then(async () => {
        const opfsRoot = await navigator.storage.getDirectory();
        await markCaptureRevoked(opfsRoot, revoked.dirName);
      })
      .catch(() => {});
  }
  void settleBufferedCaptures().catch(() => {});
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
  if (connection.connectionState !== "connected" || sender.track?.readyState !== "live") {
    return false;
  }
  // And the sender must still BE one of this connection's senders. removeTrack
  // takes it off the connection without closing anything and without ending the
  // track, so a recorder holding its own MediaStream over that track would go
  // on recording a microphone the call is no longer carrying — the participant
  // audibly gone from the room and still being recorded.
  try {
    return connection.getSenders().includes(sender);
  } catch {
    // A closed connection can throw here. That is not in the call either.
    return false;
  }
}

function beginCapture(sender: RTCRtpSender, connection: RTCPeerConnection): void {
  if (state || sealingWorker !== null || !talkRecordingActive) {
    return;
  }
  if (!senderIsInTheCall(sender, connection)) {
    // Not in the call yet, or not any more. Every path that can change that —
    // the connection coming up, a track being replaced, Talk confirming its
    // recording — calls back here.
    return;
  }
  const roomToken = roomTokenFromPath(location.pathname);
  if (!roomToken) {
    return;
  }
  if (talkRecordingRoom !== roomToken) {
    // The active recording we know about is another room's. Talk navigates
    // between conversations without reloading, so arriving here with a stale
    // answer is ordinary, and starting on it would record a call nobody has
    // said is being recorded.
    return;
  }
  if (sender === capturingSender && capturingRoom !== null && capturingRoom !== roomToken) {
    // And the sender is the room being LEFT. Its connection can still be up
    // while the next room confirms its own recording; recording it here would
    // put one conversation's microphone into another's capture.
    return;
  }
  talkRoomToken = roomToken;
  // The administrator switch was injected before this script ran. Anything
  // other than an explicit yes means no recorder or OPFS directory is created.
  if (serverAllowsCapture !== true) {
    return;
  }
  if (immutableTransferEnabled && recordingIdentity?.room !== roomToken) {
    if (!identityPending) {
      identityPending = true;
      const epoch = identityEpoch;
      void fetch(deliveryConfig.proxyBase + "/operator/capture/recording?room=" + encodeURIComponent(roomToken),
        { credentials: "same-origin", cache: "no-store", signal: AbortSignal.timeout(5_000) })
        .then(async (response) => {
          if (!response.ok) return;
          const body = await response.json() as { recordingId?: unknown };
          if (epoch !== identityEpoch || !talkRecordingActive || talkRecordingRoom !== roomToken || typeof body.recordingId !== "string") return;
          recordingIdentity = { room: roomToken, id: body.recordingId };
          beginCapture(sender, connection);
        }).catch(() => {}).finally(() => { identityPending = false; });
    }
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
  const callStartWallMs = Date.now();
  let releaseDirClaim = (): void => {};
  const dirClaimed = new Promise<void>((resolve) => { releaseDirClaim = resolve; });
  const sessionId = immutableTransferEnabled ? crypto.randomUUID() : undefined;
  const session: CaptureState = {
    recordingId: immutableTransferEnabled ? recordingIdentity?.id : undefined,
    sessionId,
    releaseDirClaim,
    connection,
    roomToken,
    dirName: captureDirName(roomToken, callStartWallMs) + "-" + (sessionId ?? crypto.randomUUID()),
    callStartWallMs,
    worker,
    segmentIndex: 0,
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
      recordingId: session.recordingId,
      sessionId: session.sessionId,
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
  leftCall = false;
  const roomToken = roomTokenFromPath(location.pathname);
  if (roomToken) {
    talkRoomToken = roomToken;
    void refreshTalkRecordingStatus(roomToken);
  }
  // Taken over when there is no sender yet, and ALSO when the one we have
  // belongs to a conversation this page has left. Talk changes room without
  // reloading, so the old publishing connection can still be open when the new
  // one negotiates; holding on to it meant the new room's sender was ignored
  // here and never looked at again, and the participant spent the rest of that
  // recording uncaptured.
  if (capturingSender === null || (roomToken !== null && capturingRoom !== roomToken)) {
    capturingSender = sender;
    capturingConnection = connection;
    capturingRoom = roomToken;
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

function applyTalkRecordingStatus(status: number, roomToken: string | null): void {
  if (talkRecordingRoom !== roomToken) {
    recordingIdentity = null;
    identityEpoch++;
  }
  // Any of these is an answer about that room, including the ones that change
  // nothing.
  talkRecordingRoom = roomToken;
  if (status === TALK_RECORDING_VIDEO || status === TALK_RECORDING_AUDIO) {
    talkRecordingActive = true;
    if (state && roomToken !== null && state.roomToken !== roomToken) {
      // The confirmed recording is another room's, and this page has a capture
      // running for the one it just left. Talk changes conversation without
      // reloading and the old connection can outlive the change, so without
      // this the old recorder goes on writing into the old room's capture while
      // the only confirmation in hand is about a different call.
      console.info("Cassini source capture: moved to another conversation; sealing the previous capture");
      void finishCapture(false);
    }
    if (capturingSender && capturingConnection) {
      beginCapture(capturingSender, capturingConnection);
    }
    return;
  }
  if (status === TALK_RECORDING_VIDEO_STARTING || status === TALK_RECORDING_AUDIO_STARTING) {
    recordingIdentity = null;
    identityEpoch++;
    // A moderator requested recording, but Talk's backend has not confirmed it.
    // Starting here would collect audio from a recording that might fail.
    //
    // A capture already running is a different matter. This status describes a
    // recording that is BEGINNING, so the confirmed one this capture belongs to
    // has ended — the stop was missed, or the poll landed after both — and
    // going on collecting would be collecting outside any confirmed recording.
    // Seal it; the next confirmed ACTIVE starts a new one.
    talkRecordingActive = false;
    if (state) {
      console.info("Cassini source capture: a new recording is starting; sealing the previous one");
      void finishCapture(false);
    }
    return;
  }
  if (status === TALK_RECORDING_OFF || status === TALK_RECORDING_FAILED) {
    recordingIdentity = null;
    identityEpoch++;
    talkRecordingActive = false;
    if (state) {
      console.info("Cassini source capture: Talk recording stopped; sealing and uploading");
      void finishCapture(false);
    }
    void settleBufferedCaptures().catch(() => {});
  }
}

async function refreshTalkRecordingStatus(roomToken: string): Promise<void> {
  if (recordingStatusFetchRoom === roomToken) {
    return;
  }
  recordingStatusFetchRoom = roomToken;
  const revision = recordingStatusRevision;
  const rootPath =
    (globalThis as { OC?: { getRootPath?: () => string } }).OC?.getRootPath?.() ?? "";
  try {
    const status = await fetchTalkRecordingStatus(roomToken, rootPath);
    // A missed stop/start may look like ACTIVE in two consecutive polls.
    // Revalidate the server identity so the new interval cannot inherit the
    // previous recording's ID.
    if (immutableTransferEnabled && state?.recordingId &&
        (status === TALK_RECORDING_VIDEO || status === TALK_RECORDING_AUDIO)) {
      const response = await fetch(deliveryConfig.proxyBase + "/operator/capture/recording?room=" + encodeURIComponent(roomToken),
        { credentials: "same-origin", cache: "no-store", signal: AbortSignal.timeout(5_000) });
      if (response.ok) {
        const body = await response.json() as { recordingId?: string };
        if (revision === recordingStatusRevision && roomToken === roomTokenFromPath(location.pathname) &&
            body.recordingId && state?.recordingId && body.recordingId !== state.recordingId) {
          recordingIdentity = { room: roomToken, id: body.recordingId };
          identityEpoch++;
          void finishCapture(false);
        }
      }
    }
    // A signaling event received while this request was in flight is newer
    // than the response and wins. This prevents a slow bootstrap response of
    // OFF from tearing down a recording the socket just confirmed active.
    //
    // And the page must still be in the room this answer is about. A request
    // that stalled while Talk changed conversation would otherwise land
    // afterwards and be applied as the current truth — finishing the new
    // room's capture because the answer names a different room, and leaving a
    // hole until the poll starts it again.
    const currentRoom = roomTokenFromPath(location.pathname);
    if (status !== null && revision === recordingStatusRevision && roomToken === currentRoom) {
      applyTalkRecordingStatus(status, roomToken);
    }
  } catch {
    // Keep the known state; the next watchdog poll retries.
  } finally {
    if (recordingStatusFetchRoom === roomToken) {
      recordingStatusFetchRoom = null;
    }
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
    applyTalkRecordingStatus(status, roomToken);
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
    // External signaling is immediate. Poll as its missed-event watchdog while
    // recording, as the lifecycle source when no socket exists (Talk
    // installations using internal signaling), and — the case a reload lands
    // in — until Talk has answered at all.
    //
    // That last one is not a nicety. A page reloading into a recording that is
    // ALREADY active gets no signalling event, because nothing transitions; the
    // one bootstrap request is the only thing that would tell it. If that
    // request fails, and the socket is healthy so the watchdog stays off, the
    // page never learns it should be recording and the whole post-rejoin stint
    // is lost.
    if (
      !signalingSocketObserved ||
      talkRecordingActive ||
      !recordingStatusAnswered(talkRoomToken ?? roomTokenFromPath(location.pathname))
    ) {
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
      // The participant is now in the call. Two shapes reach here: a capture
      // that has not started, because beginCapture refused while the connection
      // was still negotiating; and one that is running with no recorder,
      // because a track was replaced while the connection was down and the
      // gate declined to open the next segment.
      if (state && !state.finished && !state.discarded && state.recorder === null) {
        resumeSegment(state, capturingSender, pc);
      } else {
        beginCapture(capturingSender, pc);
      }
    }
  });
  // removeTrack takes the sender off the connection without closing anything
  // and without ending the track, so nothing else here would notice: the
  // recorder holds its own MediaStream over that track and would go on
  // recording a participant the room can no longer hear. The mute poll catches
  // it within its interval; this catches it at the instant it happens.
  // removeTrack takes the sender off the connection without closing anything
  // and without ending the track, so nothing else here would notice: the
  // recorder holds its own MediaStream over that track and would go on
  // recording a participant the room can no longer hear.
  //
  // It closes the segment exactly the way replaceTrack(null) does, on the
  // rotation chain — the path stopSegment was written for, which waits for
  // MediaRecorder's final chunk before the worker closes the file. Stopping
  // the recorder synchronously here instead raced that chunk into a closed
  // handle and cost the whole segment. The mute poll is what bounds how much
  // audio can be recorded between the removal and the stop, and it is a
  // quarter second.
  const originalRemoveTrack = pc.removeTrack.bind(pc);
  pc.removeTrack = (sender: RTCRtpSender) => {
    try {
      if (state && sender === capturingSender) {
        stopWithoutRestart(state);
      }
    } catch {
      // Never let instrumentation break Talk's own removeTrack.
    }
    return originalRemoveTrack(sender);
  };
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
  void refreshCapturePermission(deliveryConfig.proxyBase);
  const globals = globalThis as unknown as { RTCPeerConnection: typeof RTCPeerConnection };
  const Original = globals.RTCPeerConnection;
  if (!Original || (Original as { __cassiniPatched?: boolean }).__cassiniPatched) {
    return;
  }
  void settleBufferedCaptures().catch(() => {});
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
  // beforeunload first, and registered here — before Talk's bundle loads, so
  // before Talk's own teardown — because Talk can close the publishing
  // connection from its unload handler and that reaches endCall before pagehide
  // fires. This is what makes a reload's outcome a decision rather than a race.
  window.addEventListener("beforeunload", () => {
    unloadStartedAtMs = Date.now();
  });
  window.addEventListener("pagehide", () => {
    unloadStartedAtMs = Date.now();
    void endPage();
  });
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
