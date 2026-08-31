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
// This module holds the pieces the service worker, the injected payload, the
// capture worker and the unit tests must all agree on. Everything here is
// pure: no DOM, no globals, so it runs in a worker, in a service worker, and
// in vitest unchanged.

// SOURCE_CAPTURE_FORMAT identifies the sidecar schema. The operator rejects an
// upload whose sidecar does not carry exactly this string, so bumping it is
// how a breaking client change is fenced off from older servers.
export const SOURCE_CAPTURE_FORMAT = "org.cassini.source-capture/1";

// TALK_CALL_PATH_SEGMENT is the path Nextcloud Talk serves a call under. Both
// URL shapes matter: pretty URLs give "<root>/call/<token>", and installs with
// the front controller in the path give "<root>/index.php/call/<token>".
export const TALK_CALL_PATH_SEGMENT = "call";

// A CaptureAnchor ties one outgoing encoded Opus frame to wall-clock time.
//
// rtpTimestamp is the sender's own 48 kHz audio sample clock — the very number
// the recorder writes into its .rtplog for the packets that arrived, and which
// pkg/core/timeline already maps onto the meeting timeline. That is what makes
// placement independent of network quality: packet loss destroys the audio the
// server received, but the packets that DID arrive still carry exact
// timestamps, and a handful of them anywhere in the meeting is enough to
// anchor the whole segment.
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

// normalizeRootPath turns Nextcloud's root path into a "/…/" form with exactly
// one trailing slash. Nextcloud returns "" for an install at the domain root
// and "/nextcloud" for a subfolder install.
export function normalizeRootPath(rootPath: string | null | undefined): string {
  const trimmed = (rootPath ?? "").trim().replace(/\/+$/, "");
  if (trimmed === "") {
    return "/";
  }
  return (trimmed.startsWith("/") ? trimmed : "/" + trimmed) + "/";
}

// talkCallScopes lists the service-worker scopes that cover Talk's call pages.
//
// Two shapes, so two registrations: a service worker registration has exactly
// one scope. Both are deliberately NARROWER than the "/" scope Nextcloud's own
// Files app registers its preview service worker at (apps/files/src/services/
// ServiceWorker.js). Registering at "/" would REPLACE core's registration for
// every page; a narrower scope wins only on the pages it covers, so ours takes
// the call pages and core keeps everything else.
export function talkCallScopes(rootPath: string | null | undefined): string[] {
  const root = normalizeRootPath(rootPath);
  return [`${root}${TALK_CALL_PATH_SEGMENT}/`, `${root}index.php/${TALK_CALL_PATH_SEGMENT}/`];
}

// isTalkBundleURL reports whether a URL is one of Talk's own script bundles —
// the response the service worker appends the capture payload to.
//
// Matching the app's script directory rather than a pinned filename is
// deliberate: Talk renames its entry bundle between releases (talk-main.js ->
// talk-main.mjs, plus a build hash), and a pinned name would silently stop
// matching after an upgrade, disabling capture with no error anywhere.
export function isTalkBundleURL(rawURL: string): boolean {
  let pathname: string;
  try {
    pathname = new URL(rawURL).pathname;
  } catch {
    return false;
  }
  if (!/\/apps\/spreed\/js\//.test(pathname)) {
    return false;
  }
  const file = pathname.slice(pathname.lastIndexOf("/") + 1);
  return /^talk-main[.-]/.test(file) && /\.m?js$/.test(file);
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
