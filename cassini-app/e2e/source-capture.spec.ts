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

// captureDirs lists the OPFS directories the payload has created, with the
// files in each. The directory NAME is what proves adoption: it carries the
// call start the capture identifies itself by, so a reload that produced a
// second name produced a second capture.
type CaptureDir = { name: string; files: string[]; segments: Record<string, number> };

async function captureDirs(
  page: import("@playwright/test").Page,
): Promise<CaptureDir[]> {
  return page.evaluate(async () => {
    const root = await navigator.storage.getDirectory();
    const dirs: CaptureDir[] = [];
    for await (const [name, handle] of (
      root as unknown as {
        entries(): AsyncIterable<[string, FileSystemHandle]>;
      }
    ).entries()) {
      if (handle.kind !== "directory" || !name.startsWith("capture-")) continue;
      const files: string[] = [];
      const segments: Record<string, number> = {};
      for await (const [child, childHandle] of (
        handle as unknown as { entries(): AsyncIterable<[string, FileSystemHandle]> }
      ).entries()) {
        files.push(child);
        if (/^segment-\d+\.webm$/.test(child) && childHandle.kind === "file") {
          try {
            segments[child] = (await (childHandle as FileSystemFileHandle).getFile()).size;
          } catch {
            // A live sync access handle can hide its own file's size.
          }
        }
      }
      dirs.push({ name, files: files.sort(), segments });
    }
    return dirs.sort((a, b) => a.name.localeCompare(b.name));
  });
}

// A reload mid-recording is the case this whole seam exists for: people reload
// when the connection is bad, and a bad connection is exactly when the
// recorder's own copy of them has holes in it.
//
// The page that goes away seals its buffer and deliberately does NOT upload —
// a request started during unload cannot finish, and whether it happened to
// land would decide whether the next page finds anything to resume. The page
// that comes back adopts that buffer as the leading segments of its own
// capture: same directory, same call start, segment numbering continuing. One
// capture, uploaded once when the recording stops, holding both sides of the
// reload.
test("a reload mid-recording is adopted into one capture holding both sides", async ({ page }) => {
  await page.goto(`${server.origin}/call/testroom`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);
  await setOfficialRecording(page, 2);
  await page.waitForTimeout(2600);
  // A microphone change first, so the reload has to resume a capture whose
  // newest segment may not be in the recovery sidecar yet — the sidecar is a
  // checkpoint, and a resumed capture that numbered from it alone opened, and
  // truncated, the file that segment had already been writing.
  await page.evaluate(() => (window as never as { __replaceTrack: () => Promise<void> }).__replaceTrack());
  await page.waitForTimeout(2600);

  const before = await captureDirs(page);
  expect(before, "the first page recorded nothing to resume").toHaveLength(1);
  const dirName = before[0].name;
  expect(
    Object.keys(before[0].segments).length,
    "the microphone change did not cut a second segment, so the reload proves less",
  ).toBeGreaterThanOrEqual(2);

  await page.reload();
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);
  // The pre-reload buffer must still be there while the new page records: an
  // upload started on the way out would have deleted it, and an upload started
  // on the way in would have filed the reload as a separate capture.
  expect(server.uploads, "the buffered capture was uploaded instead of resumed").toHaveLength(0);
  await page.waitForTimeout(3000);

  const during = await captureDirs(page);
  expect(
    during.map((dir) => dir.name),
    "the reload started a second capture instead of resuming the first",
  ).toEqual([dirName]);
  expect(
    Object.keys(during[0].segments).length,
    "the rejoined page did not add a segment of its own",
  ).toBeGreaterThan(Object.keys(before[0].segments).length);
  // Nothing recorded before the reload was reopened and overwritten.
  for (const [name, size] of Object.entries(before[0].segments)) {
    expect(
      during[0].segments[name],
      `${name} shrank across the reload: the resumed capture reused its index`,
    ).toBeGreaterThanOrEqual(size);
  }

  await setOfficialRecording(page, 0);
  await expect.poll(() => server.uploads.length, { timeout: 20_000 }).toBe(1);

  const upload = server.uploads[0];
  const sidecar = upload.sidecar!;
  expect(sidecar, "the upload carried no parseable sidecar").toBeTruthy();
  // The capture still identifies itself by the call it started in, before the
  // reload — which is what makes the server file it as one capture.
  expect(dirName).toContain(String(sidecar.callStartWallMs));
  // Both sides of the reload, contiguously numbered, every one of them real
  // audio the server received.
  expect(sidecar.segments.length).toBeGreaterThanOrEqual(3);
  expect(sidecar.segments.map((segment: { index: number }) => segment.index)).toEqual(
    sidecar.segments.map((_: unknown, index: number) => index),
  );
  expect(upload.segments.length).toBe(sidecar.segments.length);
  for (const segment of upload.segments) {
    expect(segment.bytes, `segment ${segment.name} arrived empty`).toBeGreaterThan(1000);
  }
  // Nothing is left behind once the merged capture is accepted.
  expect(await captureDirs(page)).toEqual([]);
});

// The other half of R2's promise: a buffer whose recording is over is not held
// for a resumption that will never come. This is the existing retry path, and
// it has to still be the one a reload without a rejoin ends on.
test("a buffered capture whose recording is over uploads at the next page load", async ({ page }) => {
  await page.goto(`${server.origin}/call/testroom`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);
  await setOfficialRecording(page, 2);
  await page.waitForTimeout(2600);

  // Leave the page without leaving the room, which is what a navigation is.
  // The buffer is sealed here and nothing is sent.
  await page.goto(`${server.origin}/`);
  await page.waitForTimeout(600);
  expect(server.uploads, "a page on its way out started an upload it could not finish").toHaveLength(0);

  // Meanwhile the moderator stopped the recording. The participant's next Talk
  // page has nothing to resume the buffer into.
  server.state.recordingStatus = 0;
  await page.goto(`${server.origin}/call/testroom`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);

  await expect.poll(() => server.uploads.length, { timeout: 20_000 }).toBe(1);
  expect(server.uploads[0].segments[0].bytes).toBeGreaterThan(1000);
  expect(server.uploads[0].sidecar!.roomToken).toBe("testroom");
  await expect.poll(async () => (await captureDirs(page)).length, { timeout: 20_000 }).toBe(0);
});

// countRecorders installs a MediaRecorder that counts its own construction,
// before the payload runs — so a recorder built anywhere at all is caught, not
// only one these tests know how to look for.
async function countRecorders(page: import("@playwright/test").Page) {
  await page.addInitScript(() => {
    const Original = window.MediaRecorder;
    (window as never as { __recordersBuilt: number }).__recordersBuilt = 0;
    class Counted extends Original {
      constructor(stream: MediaStream, options?: MediaRecorderOptions) {
        (window as never as { __recordersBuilt: number }).__recordersBuilt += 1;
        super(stream, options);
      }
    }
    window.MediaRecorder = Counted as unknown as typeof MediaRecorder;
  });
}

async function expectNothingCollected(page: import("@playwright/test").Page, where: string) {
  expect(
    await page.evaluate(
      () => (window as never as { RTCPeerConnection: { __cassiniPatched?: boolean } }).RTCPeerConnection.__cassiniPatched,
    ),
    "the payload did not install, so this test proves nothing",
  ).toBe(true);
  // The microphone really is open: this is Talk holding it, not a page that
  // failed before it got that far.
  expect(
    await page.evaluate(
      () => (window as never as { __localTrack: MediaStreamTrack }).__localTrack.readyState,
    ),
  ).toBe("live");
  expect(
    await page.evaluate(() => (window as never as { __recordersBuilt: number }).__recordersBuilt),
    `a MediaRecorder was constructed ${where}`,
  ).toBe(0);
  expect(await captureDirs(page), `${where} created capture storage`).toEqual([]);
  expect(server.uploads).toHaveLength(0);
}

// Browser storage belongs to the origin, not to the signed-in session. On a
// shared machine the buffer a colleague's dead page left behind is still there
// when the next person signs in, and resuming it would splice their voice into
// this participant's capture and file it under this participant's name.
test("a buffered capture from another account is never resumed", async ({ page }) => {
  await page.goto(`${server.origin}/call/testroom`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);
  await setOfficialRecording(page, 2);
  await page.waitForTimeout(2600);
  const alice = await captureDirs(page);
  expect(alice, "alice recorded nothing to leave behind").toHaveLength(1);

  // Alice's page goes away mid-recording, leaving her buffer. Bob signs in on
  // the same browser and joins the same still-recording call.
  await page.goto(`${server.origin}/`);
  await page.waitForTimeout(600);
  expect(server.uploads).toHaveLength(0);

  await page.goto(`${server.origin}/call/testroom?user=bob`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);
  await page.waitForTimeout(3000);

  const dirs = await captureDirs(page);
  expect(
    dirs.map((dir) => dir.name),
    "bob resumed alice's buffered capture",
  ).not.toContain(alice[0].name);
  expect(dirs.length, "bob did not start a capture of his own").toBe(1);

  // Alice's buffer is not adopted, but it is not stranded either: it uploads on
  // the ordinary retry path, under the name her own page recorded it with.
  await expect.poll(() => server.uploads.length, { timeout: 20_000 }).toBe(1);
  expect(server.uploads[0].sidecar!.participantId).toBe("alice");
});

// R1, from the other side. Talk holds the microphone open in its device
// preview, and the room's recording is active — every condition the payload
// checks is true except the one that decides everything: there is no sender,
// because nothing has been added to a peer connection.
test("the device preview constructs no recorder and writes no storage", async ({ page }) => {
  await countRecorders(page);
  await page.goto(`${server.origin}/call/testroom?preview=1`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);

  // Talk's recording is confirmed active, in this very room.
  await setOfficialRecording(page, 2);
  await page.waitForTimeout(3000);
  await expectNothingCollected(page, "with the participant in the device preview");
});

// The sharper case, and the one somebody actually gets caught by: the page is
// loaded, Talk has built the publishing connection and put the microphone on
// it, the room is being recorded — and the participant is not in the call,
// because nothing has connected. Anything said here is said by somebody who
// does not believe they are in a meeting.
test("a sender on a connection that never came up constructs no recorder", async ({ page }) => {
  await countRecorders(page);
  await page.goto(`${server.origin}/call/testroom?nonegotiate=1`);
  await page.evaluate(() => (window as never as { __talkReady: Promise<boolean> }).__talkReady);

  // The sender is real and carries a live track; only the call is missing.
  expect(
    await page.evaluate(
      () => (window as never as { __audioSender: RTCRtpSender }).__audioSender.track?.readyState,
    ),
  ).toBe("live");
  expect(
    await page.evaluate(() => (window as never as { __audioSender: RTCRtpSender }) && true),
  ).toBe(true);

  await setOfficialRecording(page, 2);
  await page.waitForTimeout(3000);
  await expectNothingCollected(page, "with the publishing connection never connected");
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
// again, while docs/privacy.md tells administrators no such answer is kept.
// Every Talk page load clears it, and clears nothing else.
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
