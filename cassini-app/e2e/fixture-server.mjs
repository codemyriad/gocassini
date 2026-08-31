// A minimal same-origin stand-in for a Nextcloud install running Talk.
//
// The source-capture chain is entirely browser-side — a service worker
// rewriting another app's bundle, an encoded transform reading outgoing RTP
// timestamps, OPFS, an upload — and none of that can be exercised by a unit
// test. The harness has no browser at all (its Talk publishers are pion Go
// clients), so this serves the smallest thing the chain needs to be real:
//
//   /                       the Cassini page: registers the service worker
//   /call/<token>           the "Talk" call page
//   /apps/spreed/js/talk-main.mjs
//                           a stub Talk bundle that publishes audio over a
//                           real RTCPeerConnection, which the service worker
//                           must find and append the payload to
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

const CALL_PAGE = `<!doctype html>
<meta charset="utf-8">
<title>Stub Talk call</title>
<body>
<script>
  // Talk's own globals, as the payload expects to find them.
  window.OC = {
    getRootPath: () => "",
    getCurrentUser: () => ({ uid: "alice" }),
    requestToken: "stub-token",
  };
</script>
<script type="module" src="/apps/spreed/js/talk-main.mjs"></script>
</body>`;

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
// Talk's bundle references OCA; the service worker uses that as its last check
// that what it is about to append to really is Talk's script.
window.OCA = window.OCA || {};
window.__talkReady = (async () => {
  const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
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

const CASSINI_PAGE = `<!doctype html>
<meta charset="utf-8">
<title>Stub Cassini page</title>
<body>
<script type="module">
  try {
    const reg = await navigator.serviceWorker.register("${PROXY_PREFIX}/ui/capture-sw.js", { scope: "/call/" });
    await reg.update().catch(() => {});
    window.__swScope = reg.scope;
  } catch (error) {
    window.__swError = String(error);
  }
</script>
</body>`;

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
      if (name === "sidecar") {
        try {
          result.sidecar = JSON.parse(payload.toString("utf8"));
        } catch {
          result.sidecar = null;
        }
      } else if (name === "segments") {
        const fileMatch = /filename="([^"]+)"/.exec(headers);
        result.segments.push({ name: fileMatch?.[1] ?? "", bytes: payload.length });
      }
    }
    index = next;
  }
  return result;
}

export async function startFixtureServer() {
  const uploads = [];
  // Lets a test serve something OTHER than Talk's script at the bundle's URL —
  // a login page, a proxy notice — which the worker must pass through
  // untouched rather than weld a payload onto. Server state rather than a
  // Playwright route because the worker, not the page, issues this request.
  const state = { bundleIsNotTalk: false, captureEnabled: true };
  const server = createServer(async (req, res) => {
    const url = new URL(req.url, "http://127.0.0.1");
    const path = url.pathname;

    if (path === "/") {
      res.writeHead(200, { "content-type": "text/html" });
      return res.end(CASSINI_PAGE);
    }
    if (path.startsWith("/call/")) {
      res.writeHead(200, { "content-type": "text/html" });
      return res.end(CALL_PAGE);
    }
    if (path === "/apps/spreed/js/talk-main.mjs") {
      if (state.bundleIsNotTalk) {
        res.writeHead(200, { "content-type": "text/html" });
        return res.end("<html><body>Please log in</body></html>");
      }
      res.writeHead(200, { "content-type": "text/javascript" });
      return res.end(TALK_BUNDLE);
    }
    if (path === `${PROXY_PREFIX}/ui/loss-worker.js`) {
      res.writeHead(200, { "content-type": "text/javascript" });
      return res.end(LOSS_WORKER);
    }
    if (path.startsWith(`${PROXY_PREFIX}/ui/`)) {
      const name = path.slice(`${PROXY_PREFIX}/ui/`.length);
      try {
        const body = await readFile(join(captureDist, name));
        const headers = { "content-type": "text/javascript" };
        // The header the whole delivery mechanism depends on. In production
        // AppAPI's proxy forwards what the operator sets; here the fixture
        // stands in for both.
        if (name === "capture-sw.js") headers["service-worker-allowed"] = "/";
        res.writeHead(200, headers);
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
