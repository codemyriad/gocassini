import { expect, test } from "@playwright/test";
// @ts-expect-error - plain ESM fixture, no types needed
import { startFixtureServer } from "./fixture-server.mjs";

// End-to-end browser coverage for source capture.
//
// Everything load-bearing here only exists in a browser: a service worker
// rewriting another app's bundle, a WebRTC encoded transform reading outgoing
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
  server.state.bundleIsNotTalk = false;
});

// enableCapture visits the Cassini page, records consent and waits for the
// service worker to be in control — the same sequence a user performs by
// opting in, minus the UI that does not exist yet.
async function enableCapture(page: import("@playwright/test").Page) {
  await page.goto(`${server.origin}/`);
  await page.evaluate(() => localStorage.setItem("cassini.sourceCapture.consent", "granted"));
  await page.waitForFunction(() => (window as never as { __swScope?: string }).__swScope !== undefined);
}

test("the service worker appends the capture payload to Talk's bundle", async ({ page }) => {
  await enableCapture(page);

  // Asserted through the real path — the page loading its <script> — rather
  // than by fetching the URL. The worker only rewrites requests whose
  // destination is "script", so a fetch() would (correctly) get the original
  // and prove nothing about what Talk actually evaluates.
  await page.goto(`${server.origin}/call/testroom`);

  // Talk's own code ran: the payload is appended, so it cannot have replaced it.
  await page.waitForFunction(
    () => (window as never as { __talkReady?: unknown }).__talkReady !== undefined,
  );
  // And ours ran after it.
  const patched = await page.evaluate(
    () => (window.RTCPeerConnection as unknown as { __cassiniPatched?: boolean }).__cassiniPatched === true,
  );
  expect(patched, "the capture payload did not run on the call page").toBe(true);
});

test("a response that is not Talk's bundle is passed through untouched", async ({ page }) => {
  await enableCapture(page);
  await page.goto(`${server.origin}/call/testroom`);

  // A login page or a proxy notice served at the bundle's URL satisfies the
  // path pattern but is not a script to append to. Driven from the server, not
  // with page.route: the service worker issues this request, and page-level
  // interception never sees it.
  server.state.bundleIsNotTalk = true;
  await page.goto(`${server.origin}/call/testroom?v=2`);
  await page.waitForTimeout(1000);

  // Nothing of ours was welded onto it, so nothing of ours ran either.
  const patched = await page.evaluate(
    () => (window.RTCPeerConnection as unknown as { __cassiniPatched?: boolean }).__cassiniPatched === true,
  );
  expect(patched, "the payload was appended to something that was not Talk's script").toBe(false);
  const talkReady = await page.evaluate(() => (window as never as { __talkReady?: unknown }).__talkReady);
  expect(talkReady, "the stub served HTML, so Talk cannot have initialised").toBeUndefined();
});

test("captures the participant's own audio through a lossy uplink and uploads it", async ({ page }) => {
  await enableCapture(page);

  // 20% of the encoded frames never reach the far side.
  await page.goto(`${server.origin}/call/testroom?loss=0.2`);
  await page.waitForFunction(() => (window as never as { __talkReady?: Promise<boolean> }).__talkReady !== undefined);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);

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

  await page.evaluate(() => (window as never as { __endCall: () => void }).__endCall());
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

test("captures nothing without an explicit opt-in", async ({ page }) => {
  // The service worker still has to be registered for the payload to arrive at
  // all, so register it and then withdraw consent — the state a user is in
  // after turning the feature off.
  await enableCapture(page);
  await page.evaluate(() => localStorage.removeItem("cassini.sourceCapture.consent"));

  await page.goto(`${server.origin}/call/testroom`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);
  await page.waitForTimeout(2000);
  await page.evaluate(() => (window as never as { __endCall: () => void }).__endCall());
  await page.waitForTimeout(2000);

  expect(server.uploads.length, "audio was uploaded without consent").toBe(0);
});
