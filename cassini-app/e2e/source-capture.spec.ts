import { expect, test } from "@playwright/test";
// @ts-expect-error - plain ESM fixture, no types needed
import { PROXY_PREFIX, startFixtureServer } from "./fixture-server.mjs";

// End-to-end browser coverage for source capture.
//
// Everything load-bearing here only exists in a browser: the companion's
// ordinary script loading before Talk, a WebRTC encoded transform reading outgoing
// RTP timestamps, OPFS, and an upload. The Go tests cover the arithmetic and
// the intake; this covers the chain that produces their input.
//
// The condition under test is the one the whole feature exists for: a lossy
// uplink. The stub call drops a share of the encoded frames on the RECEIVING
// side, which is what the network does to the audio the server ends up with,
// and the assertions check that the loss is real and that the captured copy
// does not have it.

let server: Awaited<ReturnType<typeof startFixtureServer>>;

test.beforeAll(async () => {
  server = await startFixtureServer();
});

test.afterAll(async () => {
  await server?.close();
});

test.beforeEach(() => {
  server.uploads.length = 0;
  server.state.captureEnabled = true;
  server.state.companionEnabled = true;
  server.state.recordingStatus = 0;
});

// enableCapture records the participant's opt-in on the same origin. The
// companion app, not this page, delivers code to Talk.
async function enableCapture(page: import("@playwright/test").Page) {
  await page.goto(`${server.origin}/`);
  await page.evaluate(() => localStorage.setItem("cassini.sourceCapture.consent", "granted"));
}

async function setOfficialRecording(
  page: import("@playwright/test").Page,
  status: 0 | 1 | 2 | 3 | 4 | 5,
  roomToken = "testroom",
) {
  server.state.recordingStatus = status;
  await page.evaluate(
    ({ nextStatus, room }) =>
      (window as never as { __setRecordingStatus: (status: number, room: string) => void })
        .__setRecordingStatus(nextStatus, room),
    { nextStatus: status, room: roomToken },
  );
}

test("the companion payload runs before Talk negotiates", async ({ page }) => {
  await enableCapture(page);
  await page.goto(`${server.origin}/call/testroom`);

  await page.waitForFunction(
    () => (window as never as { __talkReady?: unknown }).__talkReady !== undefined,
  );
  const patchedBeforeTalk = await page.evaluate(
    () => (window as never as { __capturePatchedBeforeTalk?: boolean }).__capturePatchedBeforeTalk,
  );
  expect(
    patchedBeforeTalk,
    "the capture payload ran too late to attach its encoded transform before negotiation",
  ).toBe(true);
});

test("joining a call does not record locally before Talk recording starts", async ({ page }) => {
  await enableCapture(page);
  await page.goto(`${server.origin}/call/testroom`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);
  await page.waitForTimeout(1200);

  const captures = await page.evaluate(async () => {
    const root = await navigator.storage.getDirectory();
    const names: string[] = [];
    for await (const [name] of root.entries()) {
      if (name.startsWith("capture-")) names.push(name);
    }
    return names;
  });
  expect(captures, "joining alone created local source-audio storage").toEqual([]);
  expect(server.uploads).toHaveLength(0);
});

test("starting states do not capture; confirmed active starts and confirmed off uploads", async ({ page }) => {
  await enableCapture(page);
  await page.goto(`${server.origin}/call/testroom`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);

  await setOfficialRecording(page, 4);
  await page.waitForTimeout(600);
  expect(server.uploads).toHaveLength(0);

  await setOfficialRecording(page, 2);
  await page.waitForTimeout(1400);
  await setOfficialRecording(page, 0);
  await expect.poll(() => server.uploads.length, { timeout: 20_000 }).toBe(1);

  // Talk's peer remains alive: the upload was driven by recording-off, not by
  // leaving the room or closing the page.
  expect(
    await page.evaluate(
      () => (window as never as { __localTrack: MediaStreamTrack }).__localTrack.readyState,
    ),
  ).toBe("live");
});

test("reloading during an active Talk recording resumes and preserves both sides", async ({ page }) => {
  await enableCapture(page);
  await page.goto(`${server.origin}/call/testroom`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);
  await setOfficialRecording(page, 2);
  await page.waitForTimeout(2600);

  // The room resource remains active across navigation. The outgoing page must
  // durably seal its interval, and the incoming page must bootstrap from this
  // status rather than waiting for another signaling transition that may never
  // arrive.
  await page.reload();
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);
  await page.waitForTimeout(2600);
  await setOfficialRecording(page, 0);

  await expect.poll(() => server.uploads.length, { timeout: 20_000 }).toBe(2);
  expect(server.uploads.every((upload) => upload.segments.some((segment) => segment.bytes > 1000))).toBe(true);
});

test("leaving the room seals and uploads an active source capture", async ({ page }) => {
  await enableCapture(page);
  await page.goto(`${server.origin}/call/testroom`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);
  await setOfficialRecording(page, 2);
  await page.waitForTimeout(2200);

  await page.evaluate(() => (window as never as { __endCall: () => void }).__endCall());
  await expect.poll(() => server.uploads.length, { timeout: 20_000 }).toBe(1);
  expect(server.uploads[0].segments[0].bytes).toBeGreaterThan(1000);
});

test("a call started from Talk's index route is instrumented before SPA navigation", async ({ page }) => {
  await enableCapture(page);
  await page.goto(`${server.origin}/apps/spreed/`);
  await page.waitForFunction(
    () => (window as never as { __talkReady?: unknown }).__talkReady !== undefined,
  );
  expect(new URL(page.url()).pathname).toBe("/call/testroom");
  expect(
    await page.evaluate(
      () => (window as never as { __capturePatchedBeforeTalk?: boolean }).__capturePatchedBeforeTalk,
    ),
  ).toBe(true);
});

test("disabling the companion stops loading the payload without breaking Talk", async ({ page }) => {
  await enableCapture(page);
  server.state.companionEnabled = false;
  await page.goto(`${server.origin}/call/testroom`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);
  const patched = await page.evaluate(
    () => (window.RTCPeerConnection as unknown as { __cassiniPatched?: boolean }).__cassiniPatched === true,
  );
  expect(patched).toBe(false);
  await page.evaluate(() => (window as never as { __endCall: () => void }).__endCall());
  await page.waitForTimeout(1000);
  expect(server.uploads).toHaveLength(0);
});

test("the companion retires the abandoned capture worker without touching the call", async ({ page }) => {
  await page.goto(`${server.origin}/`);
  await page.evaluate(async (workerURL) => {
    const registration = await navigator.serviceWorker.register(workerURL, { scope: "/call/" });
    const worker = registration.installing ?? registration.waiting ?? registration.active;
    if (worker && worker.state !== "activated") {
      await new Promise<void>((resolve) => {
        worker.addEventListener("statechange", () => {
          if (worker.state === "activated") resolve();
        });
      });
    }
  }, `${server.origin}${PROXY_PREFIX}/ui/capture-sw.js`);

  await page.goto(`${server.origin}/call/testroom`);
  await page.waitForFunction(async () => {
    const registrations = await navigator.serviceWorker.getRegistrations();
    return registrations.every((registration) => {
      const worker = registration.active ?? registration.waiting ?? registration.installing;
      return !worker?.scriptURL.endsWith("/apps/app_api/proxy/gocassini/ui/capture-sw.js");
    });
  });
  expect(await page.title()).toBe("Stub Talk call");
  expect(
    await page.evaluate(
      () => (window as never as { __capturePatchedBeforeTalk?: boolean }).__capturePatchedBeforeTalk,
    ),
  ).toBe(true);
});

test("captures the participant's own audio through a lossy uplink and uploads it", async ({ page }) => {
  await enableCapture(page);

  // 20% of the encoded frames never reach the far side.
  await page.goto(`${server.origin}/call/testroom?loss=0.2`);
  await page.waitForFunction(() => (window as never as { __talkReady?: Promise<boolean> }).__talkReady !== undefined);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);
  await setOfficialRecording(page, 2);

  // Talk long enough for several encoded frames and at least one recorder
  // chunk boundary, with a spell of mute in the middle.
  await page.waitForTimeout(2500);
  await page.evaluate(() => {
    (window as never as { __localTrack: MediaStreamTrack }).__localTrack.enabled = false;
  });
  await page.waitForTimeout(1000);
  await page.evaluate(() => {
    (window as never as { __localTrack: MediaStreamTrack }).__localTrack.enabled = true;
  });
  await page.waitForTimeout(1500);

  // The loss has to be real, or the rest of this test proves nothing.
  const received = await page.evaluate(
    () => (window as never as { __received?: { frames: number; dropped: number } }).__received,
  );
  expect(received, "no frames reached the receiving side at all").toBeTruthy();
  expect(received!.frames).toBeGreaterThan(50);
  expect(received!.dropped, "the simulated uplink dropped nothing").toBeGreaterThan(5);

  await setOfficialRecording(page, 0);
  await expect.poll(() => server.uploads.length, { timeout: 20_000 }).toBe(1);

  const upload = server.uploads[0];
  expect(upload.sidecar, "the upload carried no parseable sidecar").toBeTruthy();
  const sidecar = upload.sidecar!;

  expect(sidecar.format).toBe("org.cassini.source-capture/1");
  expect(sidecar.roomToken).toBe("testroom");
  expect(sidecar.segments.length).toBeGreaterThan(0);

  // Audio actually reached the server.
  expect(upload.segments.length).toBe(sidecar.segments.length);
  expect(upload.segments[0].bytes).toBeGreaterThan(1000);

  const segment = sidecar.segments[0];
  // The anchors are the timing evidence. They must exist, be plentiful despite
  // the loss, and advance monotonically on the sender's sample clock.
  expect(segment.anchors.length, "no RTP anchors were recorded").toBeGreaterThan(2);
  const timestamps = segment.anchors.map((anchor: { rtpTimestamp: number }) => anchor.rtpTimestamp);
  for (let i = 1; i < timestamps.length; i += 1) {
    expect(timestamps[i]).toBeGreaterThan(timestamps[i - 1]);
  }
  // 20% loss on the wire; the anchors describe frames the client ENCODED, so
  // they are unaffected by it. Sampled one in fifty, ~5 s of audio yields well
  // over the handful a placement needs.
  expect(segment.anchors.length).toBeGreaterThanOrEqual(3);

  // The mute spell was observed and travelled with the recording.
  expect(segment.muteIntervals.length, "the mute was not recorded").toBeGreaterThan(0);
  const [muteStart, muteEnd] = segment.muteIntervals[0];
  expect(muteEnd - muteStart).toBeGreaterThan(500);
});

test("consent withdrawn mid-call discards that call's recording", async ({ page }) => {
  await enableCapture(page);
  await page.goto(`${server.origin}/call/testroom`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);
  await setOfficialRecording(page, 2);
  await page.waitForTimeout(1500);

  // Turn capture off while the call is still running, then back on before it
  // ends. The audio recorded in between was captured without permission, and
  // re-granting must not resurrect it.
  await page.evaluate(() => localStorage.removeItem("cassini.sourceCapture.consent"));
  await page.waitForTimeout(1000);
  await page.evaluate(() => localStorage.setItem("cassini.sourceCapture.consent", "granted"));
  await page.waitForTimeout(500);

  // And a device change afterwards must not restart collection either: the
  // upload was already blocked, but a revoked session has to stop recording.
  await page.evaluate(async () => {
    const win = window as never as { __replaceTrack?: () => Promise<void> };
    await win.__replaceTrack?.();
  });
  await page.waitForTimeout(800);

  await setOfficialRecording(page, 0);
  await page.waitForTimeout(3000);

  expect(server.uploads.length, "a recording made after consent was withdrawn was uploaded").toBe(0);
});

test("the administrator switch stops a capture already running", async ({ page }) => {
  await enableCapture(page);
  await page.goto(`${server.origin}/call/testroom`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);
  await setOfficialRecording(page, 2);
  await page.waitForTimeout(1500);

  // Turning the server-side switch off has to reach a call in progress. The
  // companion cannot retract a script already executing, so the client asks.
  server.state.captureEnabled = false;
  // Long enough for at least one poll at the shortened interval the stub page
  // sets. Without this wait the test would pass on the upload endpoint's 403
  // instead, proving nothing about the client.
  await page.waitForTimeout(2500);

  // Switch it back ON before hanging up, so the upload endpoint would happily
  // accept. Anything that arrives now is the client having failed to stop.
  server.state.captureEnabled = true;
  await setOfficialRecording(page, 0);
  await page.waitForTimeout(3000);

  expect(
    server.uploads.length,
    "the capture kept running after the administrator switched it off; the upload was accepted because the switch was back on",
  ).toBe(0);
});

test("captures nothing without an explicit opt-in", async ({ page }) => {
  await enableCapture(page);
  await page.evaluate(() => localStorage.removeItem("cassini.sourceCapture.consent"));

  await page.goto(`${server.origin}/call/testroom`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);
  await setOfficialRecording(page, 2);
  await page.waitForTimeout(2000);
  await setOfficialRecording(page, 0);
  await page.waitForTimeout(2000);

  expect(server.uploads.length, "audio was uploaded without consent").toBe(0);
});
