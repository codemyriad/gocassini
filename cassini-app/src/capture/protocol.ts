// Shared contract for source-side audio capture.
//
// Cassini's recorder subscribes to each participant's stream through the SFU,
// so the audio it stores is whatever survived that participant's uplink. On a
// bad connection the words are simply gone, and no amount of server-side work
// brings them back. Source capture records the same signal one hop earlier —
// in the participant's browser, before Opus encoding and before the network —
// and uploads it after the call so the transcript can be built from audio the
// network never touched.
//
// This module holds the pieces the injected payload, capture worker and unit
// tests must all agree on. Everything here is pure: no DOM and no globals, so
// it runs in the worker and in vitest unchanged.

// SOURCE_CAPTURE_FORMAT identifies the sidecar schema. The operator rejects an
// upload whose sidecar does not carry exactly this string, so bumping it is
// how a breaking client change is fenced off from older servers.
export const SOURCE_CAPTURE_FORMAT = "org.cassini.source-capture/1";

// The worker refreshes this recovery sidecar as durable MediaRecorder chunks
// land. A normal stop replaces it with capture.json; after reload/crash the
// next Talk page can upload the completed prefix instead of losing it.
//
// There are TWO of them, written alternately, and that is not belt and braces.
// A checkpoint is truncate-then-write on one file, so a page that dies inside
// that window leaves an empty or half-written manifest — and since a page on
// its way out deliberately does not seal capture.json, that would be the only
// manifest in the directory. The next page would find nothing parseable and
// neither resume nor upload a capture that is sitting there complete. Writing
// the new generation into the slot the previous one is NOT in means a torn
// write can only ever damage the generation being written; the reader takes
// whichever slot parses with the later call end.
export const SOURCE_CAPTURE_PENDING_NAMES = [
  "capture.pending.json",
  "capture.pending.b.json",
] as const;

// SOURCE_CAPTURE_PENDING_NAME is the first slot, kept as a name because it is
// what a capture written by an older build has.
export const SOURCE_CAPTURE_PENDING_NAME = SOURCE_CAPTURE_PENDING_NAMES[0];

// TALK_CALL_PATH_SEGMENT is the path Nextcloud Talk serves a call under. Both
// URL shapes matter: pretty URLs give "<root>/call/<token>", and installs with
// the front controller in the path give "<root>/index.php/call/<token>".
export const TALK_CALL_PATH_SEGMENT = "call";

// A CaptureAnchor ties one outgoing encoded Opus frame to wall-clock time.
//
// rtpTimestamp is the participant's own 48 kHz audio sample clock. It is NOT
// the value the recorder logs for the same audio: Janus rewrites the timestamps
// it relays to each subscriber, so the two live in different spaces. What the
// pair is good for is the RATE — how fast this machine's sound card runs
// against its wall clock — which is the dominant drift in the system and which
// the server solves from these anchors rather than estimating.
//
// That part is immune to loss: these describe frames the client ENCODED, so
// whether each one reached the server is irrelevant. Placement's offset comes
// from wall clock instead; see docs/source-audio-capture.md.
export interface CaptureAnchor {
  // frameIndex counts encoded frames within the segment, from 0.
  frameIndex: number;
  // rtpTimestamp is the RTP timestamp of the frame (48 kHz units, wrapping).
  rtpTimestamp: number;
  // ssrc identifies the outgoing stream this frame belongs to. It changes when
  // the sender re-negotiates, which is also a seam in the recorder's rtplog.
  ssrc: number;
  // wallMs is Date.now() in the capture worker when the frame passed through.
  // Date.now rather than performance.now because the worker and the page have
  // different performance time origins, and the sidecar has to be reconcilable
  // across both realms.
  wallMs: number;
}

// A CaptureSegment is one continuous recording of one sender track. A call
// produces more than one when the track is replaced mid-call (device change,
// or Talk rebuilding its media pipeline), because a new track restarts the
// recorder's media clock.
export interface CaptureSegment {
  index: number;
  // audioName is the OPFS file name, and the multipart field name at upload.
  audioName: string;
  mimeType: string;
  startWallMs: number;
  stopWallMs: number;
  sampleRate: number | null;
  channelCount: number | null;
  // anchors is sampled, not exhaustive — one every anchorEveryFrames frames.
  anchors: CaptureAnchor[];
  // muteIntervals are [startWallMs, endWallMs] spans where the participant had
  // Talk's microphone muted. The audio is already silent there (a disabled
  // MediaStreamTrack delivers silence to every sink), so these are provenance,
  // not redaction: they let the server tell deliberate silence apart from a
  // capture failure.
  muteIntervals: Array<[number, number]>;
}

export interface CaptureSidecar {
  clockSamples?: import("./clock").CaptureClockSample[];
  recordingId?: string;
  sessionId?: string;
  format: typeof SOURCE_CAPTURE_FORMAT;
  roomToken: string;
  // participantId is the client's claim about who it is. The operator ignores
  // it in favour of the authenticated caller and only uses it to detect a
  // mismatch worth logging.
  participantId: string;
  callStartWallMs: number;
  callEndWallMs: number;
  userAgent: string;
  segments: CaptureSegment[];
}

// roomTokenFromPath extracts the Talk conversation token from a call URL,
// tolerating both the pretty and index.php shapes and any install subfolder.
// Returns null when the path is not a call page.
export function roomTokenFromPath(pathname: string): string | null {
  const match = new RegExp(`/${TALK_CALL_PATH_SEGMENT}/([^/?#]+)`).exec(pathname);
  if (!match) {
    return null;
  }
  const token = decodeURIComponent(match[1]).trim();
  return token === "" ? null : token;
}

// mergeMuteIntervals coalesces adjacent/overlapping mute spans produced by
// polling, so a long mute does not become hundreds of adjacent intervals.
export function mergeMuteIntervals(
  intervals: ReadonlyArray<readonly [number, number]>,
  gapToleranceMs = 500,
): Array<[number, number]> {
  const sorted = [...intervals]
    .filter(([start, end]) => Number.isFinite(start) && Number.isFinite(end) && end >= start)
    .sort((a, b) => a[0] - b[0]);
  const merged: Array<[number, number]> = [];
  for (const [start, end] of sorted) {
    const last = merged[merged.length - 1];
    if (last && start - last[1] <= gapToleranceMs) {
      last[1] = Math.max(last[1], end);
      continue;
    }
    merged.push([start, end]);
  }
  return merged;
}

// captureDirName is the per-call OPFS directory. Scoped by room and call start
// so two calls in the same room do not collide and an abandoned recording
// stays identifiable long after the tab that made it is gone.
export function captureDirName(roomToken: string, callStartWallMs: number): string {
  const safeToken = roomToken.replace(/[^A-Za-z0-9_-]/g, "");
  return `capture-${safeToken}-${callStartWallMs}`;
}
