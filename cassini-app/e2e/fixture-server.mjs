// A minimal same-origin stand-in for a Nextcloud install running Talk.
//
// The source-capture chain is entirely browser-side — a companion-delivered
// script, an encoded transform reading outgoing RTP timestamps, OPFS, an
// upload — and none of that can be exercised by a unit
// test. The harness has no browser at all (its Talk publishers are pion Go
// clients), so this serves the smallest thing the chain needs to be real:
//
//   /                       a neutral same-origin page, outside any call
//   /call/<token>           the "Talk" call page
//   /apps/cassini_capture/js/capture-payload.js
//                           the companion app's ordinary script
//   /apps/spreed/js/talk-main.mjs
//                           a stub Talk bundle that publishes audio over a
//                           real RTCPeerConnection
//   <proxy>/ui/capture-*.js the real built bundles
//   <proxy>/operator/capture/upload
//                           collects what the payload uploads
//
// What this deliberately does NOT cover, so nobody mistakes a green run for
// more than it is: real Nextcloud, real Talk, real Janus, AppAPI's proxy and
// its header forwarding, and the operator's Go upload handler (that one has
// its own tests). The claim under test is the browser chain end to end.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const captureDist = join(here, "..", "dist", "capture");

export const PROXY_PREFIX = "/index.php/apps/app_api/proxy/gocassini";

function callPage(state, routeToken = "") {
  const initialState = Buffer.from(
    JSON.stringify({ enabled: state.captureEnabled, proxyBase: PROXY_PREFIX }),
  ).toString("base64");
  const companion = state.companionEnabled
    ? `<input type="hidden" id="initial-state-cassini_capture-capture" value="${initialState}">
<script src="/apps/cassini_capture/js/capture-payload.js"></script>`
    : "";
  return `<!doctype html>
<meta charset="utf-8">
<title>Stub Talk call</title>
<body>
<script>
  // Talk's own globals, as the payload expects to find them.
  // Shorten the payload's permission poll so a test can prove the runtime
  // shutdown rather than wait half a minute for it. The clamp in
  // serverCheckIntervalMS only lets this make the check stricter.
  window.__cassiniCaptureCheckMs = 700;
  window.__talkRouteToken = ${JSON.stringify(routeToken)};
  window.OC = {
    getRootPath: () => "",
    // A ?user= override so a test can model the thing browser storage really
    // does: it belongs to the origin, not to the signed-in session, so the next
    // person to sign in on this machine finds the previous one's buffer.
    getCurrentUser: () => ({ uid: new URLSearchParams(location.search).get("user") || "alice" }),
    requestToken: "stub-token",
  };
</script>
${companion}
<script type="module" src="/apps/spreed/js/talk-main.mjs"></script>
</body>`;
}

// The stub Talk bundle. It does what matters for capture: builds a real
// RTCPeerConnection with a real outgoing audio track, so the payload's
// RTCPeerConnection patch, sender discovery, MediaRecorder and encoded
// transform all run against genuine browser plumbing.
//
// The loopback peer models the network: a transform on the RECEIVING side
// drops a share of the encoded frames. That is what packet loss does to the
// audio the server ends up with, and it is the condition the whole feature
// exists for — so the test can assert that the loss is visible on the received
// side and absent from the captured side.
const TALK_BUNDLE = `
window.OCA = window.OCA || {};
if (window.__talkRouteToken) {
  history.pushState({}, "", "/call/" + window.__talkRouteToken);
}
// Talk exposes its external-signaling socket on window. The real socket carries
// this exact nested room message after the recording backend confirms each
// transition. EventTarget is enough for the fixture because Cassini is a
// passive message observer and must not replace or consume Talk's handler.
const signalingSocket = new EventTarget();
window.signalingSocket = signalingSocket;
window.__setRecordingStatus = (status, roomid = "testroom") => {
  signalingSocket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "event",
      event: {
        target: "room",
        type: "message",
        message: { roomid, data: { type: "recording", recording: { status } } },
      },
    }),
  }));
};
window.__capturePatchedBeforeTalk = window.RTCPeerConnection.__cassiniPatched === true;
// The device-preview state: Talk has the microphone open and shows it back to
// the participant, and there is no call. Nothing has been added to a peer
// connection, so no sender exists — which is the only thing the payload will
// ever record. A page in this state with the room's recording ACTIVE is the
// sharpest test of that: everything except the call itself is true.
window.__previewOnly = new URLSearchParams(location.search).get("preview") === "1";
// The state between the preview and the call: Talk has built the publishing
// peer connection and added the microphone to it, and nothing has connected.
// This is the participant who is loaded but not in the meeting — the one who
// says something they do not expect to be recorded.
window.__noNegotiate = new URLSearchParams(location.search).get("nonegotiate") === "1";
window.__talkReady = (async () => {
  const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
  window.__localTrack = stream.getAudioTracks()[0];
  if (window.__previewOnly) {
    return true;
  }
  const local = new RTCPeerConnection();
  const remote = new RTCPeerConnection();
  local.onicecandidate = (e) => e.candidate && remote.addIceCandidate(e.candidate);
  remote.onicecandidate = (e) => e.candidate && local.addIceCandidate(e.candidate);

  window.__received = { frames: 0, dropped: 0 };
  const lossRate = Number(new URLSearchParams(location.search).get("loss") || "0");

  remote.ontrack = () => {
    const receiver = remote.getReceivers().find((r) => r.track && r.track.kind === "audio");
    if (!receiver || !window.RTCRtpScriptTransform) return;
    const lossWorker = new Worker("${PROXY_PREFIX}/ui/loss-worker.js");
    lossWorker.onmessage = (event) => { window.__received = event.data; };
    receiver.transform = new RTCRtpScriptTransform(lossWorker, { lossRate });
  };

  const track = stream.getAudioTracks()[0];
  // Talk's own mute is TrackEnabler setting enabled=false on this very track;
  // the test drives it the same way.
  window.__localTrack = track;
  local.addTrack(track, stream);
  // The sender the payload attaches its encoded transform to. The tests read
  // its outbound RTP stats, because a transform left on it with nothing reading
  // it stops the packets and is a participant nobody can hear.
  window.__audioSender = local.getSenders().find((s) => s.track && s.track.kind === "audio");

  if (window.__noNegotiate) {
    // Deliberately no offer/answer: the sender exists, the track is live, and
    // the connection never leaves "new".
    window.__endCall = () => { local.close(); remote.close(); };
    return true;
  }
  const offer = await local.createOffer();
  await local.setLocalDescription(offer);
  await remote.setRemoteDescription(offer);
  const answer = await remote.createAnswer();
  await remote.setLocalDescription(answer);
  await local.setRemoteDescription(answer);

  // A device change mid-call, as Talk performs one.
  window.__replaceTrack = async () => {
    const fresh = await navigator.mediaDevices.getUserMedia({ audio: true });
    const sender = local.getSenders().find((s) => s.track && s.track.kind === "audio");
    await sender?.replaceTrack(fresh.getAudioTracks()[0]);
  };

  window.__endCall = () => { local.close(); remote.close(); };
  return true;
})();
`;

// Counts frames and drops a share of them, in the receive path.
const LOSS_WORKER = `
self.onrtctransform = (event) => {
  const { readable, writable, options } = event.transformer;
  const lossRate = options?.lossRate ?? 0;
  let seen = 0;
  let dropped = 0;
  readable.pipeThrough(new TransformStream({
    transform(frame, controller) {
      seen += 1;
      // Deterministic decimation rather than Math.random: a flaky assertion
      // about loss would be worse than no assertion.
      if (lossRate > 0 && seen % Math.round(1 / lossRate) === 0) {
        dropped += 1;
        self.postMessage({ frames: seen, dropped });
        return; // dropped, exactly as the network would
      }
      controller.enqueue(frame);
      self.postMessage({ frames: seen, dropped });
    },
  })).pipeTo(writable).catch(() => {});
};
`;

// The two ways the timing worker can be broken on the wire, both of them real:
// an ExApp restarted or upgraded mid-call answers 404 for a script the page
// already committed to, and a half-written or skewed bundle loads and then
// does nothing. The silent one is the dangerous shape, because nothing
// reports it: no error event, no rejected fetch, just an encoded transform on
// a live sender with no reader behind it.
const THROWING_CAPTURE_WORKER = `throw new Error("capture worker failed to evaluate");`;
const SILENT_CAPTURE_WORKER = `self.onmessage = () => {};`;

// A harmless stand-in for the abandoned bundle-rewriting worker. It exists
// only so the migration test can prove that the companion payload unregisters
// an already-installed copy by exact script URL.
const LEGACY_CAPTURE_WORKER = `self.addEventListener("fetch", () => {});`;

const NEUTRAL_PAGE = `<!doctype html>
<meta charset="utf-8">
<title>Stub Nextcloud page</title>`;

// parseMultipart pulls the sidecar and the segment sizes out of the upload.
// Minimal on purpose: the operator's Go handler is what parses this for real
// and has its own tests; here we only need to see what the browser sent.
function parseMultipart(body, contentType) {
  const boundaryMatch = /boundary=(?:"([^"]+)"|([^;]+))/i.exec(contentType);
  if (!boundaryMatch) return { sidecar: null, segments: [] };
  const boundary = Buffer.from(`--${boundaryMatch[1] ?? boundaryMatch[2]}`);
  const result = { sidecar: null, segments: [] };

  let index = body.indexOf(boundary);
  while (index !== -1) {
    const start = index + boundary.length;
    const next = body.indexOf(boundary, start);
    if (next === -1) break;
    const part = body.subarray(start, next);
    const headerEnd = part.indexOf("\r\n\r\n");
    if (headerEnd !== -1) {
      const headers = part.subarray(0, headerEnd).toString("utf8");
      // Trailing CRLF belongs to the delimiter, not the payload.
      const payload = part.subarray(headerEnd + 4, part.length - 2);
      const nameMatch = /name="([^"]+)"/.exec(headers);
      const name = nameMatch?.[1];
      const fileMatch = /filename="([^"]+)"/.exec(headers);
      if (name === "sidecar") {
        try {
          result.sidecar = JSON.parse(payload.toString("utf8"));
        } catch {
          result.sidecar = null;
        }
      } else if (fileMatch) {
        // A part is a segment because it carries a FILE name, exactly as the
        // operator decides it. The field name cannot be relied on: the AppAPI
        // proxy rebuilds the body through PHP, which collapses a repeated
        // field name to its last file and rewrites some characters.
        result.segments.push({ name: fileMatch[1], bytes: payload.length });
      }
    }
    index = next;
  }
  return result;
}

export async function startFixtureServer() {
  const uploads = [];
  // captureWorker decides what the proxy serves for the timing worker:
  // "ok" the real built bundle, "missing" a 404, "throws" a script that dies
  // on load, "silent" one that loads and never signals anything.
  const state = {
    captureEnabled: true,
    companionEnabled: true,
    recordingStatus: 0,
    captureWorker: "ok",
  };
  const server = createServer(async (req, res) => {
    const url = new URL(req.url, "http://127.0.0.1");
    const path = url.pathname;

    if (path === "/") {
      res.writeHead(200, { "content-type": "text/html" });
      return res.end(NEUTRAL_PAGE);
    }
    if (path.startsWith("/call/")) {
      res.writeHead(200, { "content-type": "text/html" });
      return res.end(callPage(state));
    }
    if (path === "/apps/spreed/" || path === "/index.php/apps/spreed/") {
      res.writeHead(200, { "content-type": "text/html" });
      return res.end(callPage(state, "testroom"));
    }
    if (path === "/apps/spreed/js/talk-main.mjs") {
      res.writeHead(200, { "content-type": "text/javascript" });
      return res.end(TALK_BUNDLE);
    }
    if (path === "/apps/cassini_capture/js/capture-payload.js") {
      try {
        const body = await readFile(join(captureDist, "capture-payload.js"));
        res.writeHead(200, { "content-type": "text/javascript" });
        return res.end(body);
      } catch {
        res.writeHead(404);
        return res.end("capture companion payload was not built");
      }
    }
    if (path === `${PROXY_PREFIX}/ui/loss-worker.js`) {
      res.writeHead(200, { "content-type": "text/javascript" });
      return res.end(LOSS_WORKER);
    }
    if (path === `${PROXY_PREFIX}/ui/capture-worker.js` && state.captureWorker !== "ok") {
      if (state.captureWorker === "missing") {
        res.writeHead(404);
        return res.end("no capture worker on this installation");
      }
      res.writeHead(200, { "content-type": "text/javascript" });
      return res.end(
        state.captureWorker === "throws" ? THROWING_CAPTURE_WORKER : SILENT_CAPTURE_WORKER,
      );
    }
    if (path === `${PROXY_PREFIX}/ui/capture-sw.js`) {
      res.writeHead(200, {
        "content-type": "text/javascript",
        "service-worker-allowed": "/",
      });
      return res.end(LEGACY_CAPTURE_WORKER);
    }
    if (path.startsWith(`${PROXY_PREFIX}/ui/`)) {
      const name = path.slice(`${PROXY_PREFIX}/ui/`.length);
      try {
        const body = await readFile(join(captureDist, name));
        res.writeHead(200, { "content-type": "text/javascript" });
        return res.end(body);
      } catch {
        res.writeHead(404);
        return res.end("no such capture asset");
      }
    }
    if (path === `${PROXY_PREFIX}/operator/capture/enabled`) {
      // The administrator switch, as a running capture sees it. A test can flip
      // it mid-call to prove the boundary reaches clients already recording.
      res.writeHead(200, { "content-type": "application/json", "cache-control": "no-store" });
      return res.end(JSON.stringify({ enabled: state.captureEnabled }));
    }
    if (path === "/ocs/v2.php/apps/spreed/api/v4/room/testroom") {
      res.writeHead(200, { "content-type": "application/json", "cache-control": "no-store" });
      return res.end(JSON.stringify({ ocs: { data: { callRecording: state.recordingStatus } } }));
    }
    if (path === `${PROXY_PREFIX}/operator/capture/upload` && req.method === "POST") {
      if (!state.captureEnabled) {
        res.writeHead(403);
        return res.end("source capture is not enabled on this installation");
      }
      const chunks = [];
      for await (const chunk of req) chunks.push(chunk);
      uploads.push(parseMultipart(Buffer.concat(chunks), req.headers["content-type"] ?? ""));
      res.writeHead(202, { "content-type": "application/json" });
      return res.end(JSON.stringify({ status: "accepted" }));
    }
    res.writeHead(404);
    res.end("not found");
  });

  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address();
  return {
    origin: `http://127.0.0.1:${port}`,
    uploads,
    state,
    close: () => new Promise((resolve) => server.close(resolve)),
  };
}
