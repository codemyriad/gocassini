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
  server.state.captureWorker = "ok";
});

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
  await page.goto(`${server.origin}/call/testroom`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);
  await setOfficialRecording(page, 2);
  await page.waitForTimeout(2200);

  await page.evaluate(() => (window as never as { __endCall: () => void }).__endCall());
  await expect.poll(() => server.uploads.length, { timeout: 20_000 }).toBe(1);
  expect(server.uploads[0].segments[0].bytes).toBeGreaterThan(1000);
});

test("a call started from Talk's index route is instrumented before SPA navigation", async ({ page }) => {
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

// participantKeys reads everything the payload left in this browser except its
// own delivery bookkeeping. cassini.sourceCapture.uploadAttempts counts refusals
// per buffered capture so a permanently-failing deployment stops re-offering a
// meeting-sized body forever; it says nothing about a person. Anything else
// under this prefix would.
async function participantKeys(page: import("@playwright/test").Page): Promise<string[]> {
  return page.evaluate(() =>
    Object.keys(localStorage).filter(
      (key) => key.startsWith("cassini") && key !== "cassini.sourceCapture.uploadAttempts",
    ),
  );
}

// A browser that answered the opt-in an older build asked still holds that
// answer: nothing reads or writes the key any more, so without a deliberate
// delete it would simply sit there forever — a recorded answer to a question
// this build no longer asks, on a profile whose owner may never open Cassini
// again, and no such answer is kept. Every Talk page load clears it, and clears
// nothing else.
test("clears an older build's opt-in from a browser that still has one", async ({ page }) => {
  await page.goto(`${server.origin}/`);
  await page.evaluate(() => {
    localStorage.setItem("cassini.sourceCapture.consent", "granted");
    localStorage.setItem("cassini.sourceCapture.uploadAttempts", '{"capture-testroom-1":2}');
    localStorage.setItem("cassini.viewer.lastRoom", "testroom");
  });

  await page.goto(`${server.origin}/call/testroom`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);

  const stored = await page.evaluate(() => ({
    consent: localStorage.getItem("cassini.sourceCapture.consent"),
    attempts: localStorage.getItem("cassini.sourceCapture.uploadAttempts"),
    unrelated: localStorage.getItem("cassini.viewer.lastRoom"),
  }));
  expect(stored.consent, "an older build's opt-in survived the payload running").toBeNull();
  // Delivery bookkeeping is still live, and so is everything else on the origin.
  expect(stored.attempts).toBe('{"capture-testroom-1":2}');
  expect(stored.unrelated).toBe("testroom");
});

// Capture follows Talk's official recording. Every authenticated participant of
// a recorded call is captured, and nothing is asked of them or kept for them:
// telling the room it is being recorded is Talk's job, and a browser key of our
// own would be a second, weaker answer beside the one Talk already gives.
test("captures every participant of a recorded call with nothing stored in the browser", async ({ page }) => {
  await page.goto(`${server.origin}/call/testroom`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);

  expect(
    await participantKeys(page),
    "the payload stored something per participant before recording",
  ).toEqual([]);

  await setOfficialRecording(page, 2);
  await page.waitForTimeout(2500);
  await setOfficialRecording(page, 0);

  await expect.poll(() => server.uploads.length, { timeout: 20_000 }).toBe(1);
  expect(server.uploads[0].segments[0].bytes).toBeGreaterThan(1000);

  // An accepted upload leaves no residue either: nothing in this browser records
  // that this participant was captured.
  expect(
    await participantKeys(page),
    "the payload stored something per participant while capturing",
  ).toEqual([]);
});

test("the administrator switch stops a capture already running", async ({ page }) => {
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

  // Switch it back ON before hanging up, and wait for the client's own poll to
  // hear that, so the upload endpoint would happily accept and so would every
  // gate the payload keeps in front of it. The discard the switch already
  // triggered is then the only thing left that can stop this upload; anything
  // that arrives now is the client having failed to stop.
  server.state.captureEnabled = true;
  await page.waitForTimeout(1500);
  await setOfficialRecording(page, 0);
  await page.waitForTimeout(3000);

  expect(
    server.uploads.length,
    "the capture kept running after the administrator switched it off; the upload was accepted because the switch was back on",
  ).toBe(0);
});

// A broken timing worker must cost the capture and nothing else.
//
// This is the failure that would end a pilot. The worker is attached to the
// participant's outgoing audio as a WebRTC encoded transform, so every Opus
// frame they send passes through it, and it has to be attached before the call
// negotiates or it collects nothing at all. A worker that never runs therefore
// gets the frames and never reads them: the participant goes silent to the
// whole room while Talk still shows them unmuted and transmitting.
//
// The assertion that matters is audio, not tidiness. Measured on Chromium 151,
// `sender.transform = null` leaves the sender frozen at zero packets sent — it
// does that to a perfectly healthy transform too — so "the payload removed its
// transform" would be satisfied by a payload that left the participant mute.
// What these tests require is that the sender is still putting packets on the
// wire afterwards, and that nothing was collected.

// outboundPackets reads what the sender actually put on the wire. Talk's own
// mute is a disabled track, which still sends, so this measures the one thing a
// stalled transform destroys.
async function outboundPackets(page: import("@playwright/test").Page): Promise<number> {
  return await page.evaluate(async () => {
    const sender = (window as never as { __audioSender: RTCRtpSender }).__audioSender;
    let packets = 0;
    (await sender.getStats()).forEach((report: { type: string; packetsSent?: number }) => {
      if (report.type === "outbound-rtp") {
        packets = report.packetsSent ?? 0;
      }
    });
    return packets;
  });
}

async function expectStillAudible(page: import("@playwright/test").Page) {
  const before = await outboundPackets(page);
  await page.waitForTimeout(1_500);
  const after = await outboundPackets(page);
  expect(
    after - before,
    "the participant stopped sending audio: the room can no longer hear them",
  ).toBeGreaterThan(20);
}

async function joinWithBrokenWorker(page: import("@playwright/test").Page) {
  await page.goto(`${server.origin}/call/testroom`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);
}

test("a timing worker that 404s costs the capture, not the participant's audio", async ({ page }) => {
  // An ExApp restarted, upgraded, or rolled back mid-call answers 404 for a
  // script the page is about to depend on.
  server.state.captureWorker = "missing";
  await joinWithBrokenWorker(page);
  // Past the readiness deadline, so a worker that is never going to answer has
  // been given up on rather than merely not arrived yet.
  await page.waitForTimeout(4_000);
  await expectStillAudible(page);
  expect(server.uploads, "a capture was uploaded from a worker that never loaded").toHaveLength(0);
});

test("a timing worker that throws on load costs the capture, not the participant's audio", async ({
  page,
}) => {
  server.state.captureWorker = "throws";
  await joinWithBrokenWorker(page);
  await page.waitForTimeout(4_000);
  await expectStillAudible(page);
  expect(server.uploads).toHaveLength(0);
});

test("a timing worker that never reports ready gives the call its audio back", async ({ page }) => {
  // The dangerous shape: the script loads, so no error event fires anywhere,
  // and it simply is not the worker this payload expects. Version skew between
  // the companion app and the ExApp looks exactly like this, and it is the one
  // case where the transform really is attached to a worker that will never
  // read it — so this test watches the participant go mute and come back.
  server.state.captureWorker = "silent";
  await joinWithBrokenWorker(page);

  expect(
    await outboundPackets(page),
    "the fixture never stalled the sender, so this test proves nothing",
  ).toBe(0);

  await page.waitForTimeout(4_000);
  await expectStillAudible(page);
  expect(server.uploads).toHaveLength(0);
});
